// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// textPreviewResp is the shape of the render-text preview endpoint's JSON.
type textPreviewResp struct {
	Text  string `json:"text"`
	Error string `json:"error"`
}

func doTextPreview(t *testing.T, h *gatewayHarness, body any) (int, textPreviewResp) {
	t.Helper()
	rw := h.do(t, "POST", "/api/v1/tools/render-text/preview", body)
	var pr textPreviewResp
	if rw.Body.Len() > 0 {
		_ = json.Unmarshal(rw.Body.Bytes(), &pr)
	}
	return rw.Code, pr
}

// TestRenderTextPreview_TableFromRows: a per-row template + prefix/suffix
// renders a table over sample rows — the HTML-table preset's shape.
func TestRenderTextPreview_TableFromRows(t *testing.T) {
	h := newGatewayHarness(t)
	code, pr := doTextPreview(t, h, map[string]any{
		"template":  `'<tr><td>' + string(row["rank"]) + '</td><td>' + row["model"] + '</td></tr>'`,
		"separator": "",
		"prefix":    "<table>",
		"suffix":    "</table>",
		"rows": []map[string]any{
			{"rank": 1, "model": "A"},
			{"rank": 2, "model": "B"},
		},
	})
	if code != http.StatusOK {
		t.Fatalf("want 200, got %d", code)
	}
	if pr.Error != "" {
		t.Fatalf("unexpected error: %s", pr.Error)
	}
	want := "<table><tr><td>1</td><td>A</td></tr><tr><td>2</td><td>B</td></tr></table>"
	if pr.Text != want {
		t.Fatalf("got %q, want %q", pr.Text, want)
	}
}

// TestRenderTextPreview_SeparatorDefaultsToNewline: when separator is omitted
// the lines join with a newline (matching the drop's default).
func TestRenderTextPreview_SeparatorDefaultsToNewline(t *testing.T) {
	h := newGatewayHarness(t)
	code, pr := doTextPreview(t, h, map[string]any{
		"template": `'• ' + row["name"]`,
		"rows":     []map[string]any{{"name": "one"}, {"name": "two"}},
	})
	if code != http.StatusOK || pr.Error != "" {
		t.Fatalf("want 200 no-error, got code=%d err=%q", code, pr.Error)
	}
	if pr.Text != "• one\n• two" {
		t.Fatalf("got %q, want newline-joined bullets", pr.Text)
	}
}

// TestRenderTextPreview_BadCELIsInline: a malformed CEL template comes back as
// a 200 with a labelled error, not an HTTP error.
func TestRenderTextPreview_BadCELIsInline(t *testing.T) {
	h := newGatewayHarness(t)
	code, pr := doTextPreview(t, h, map[string]any{
		"template": `row["name" +`, // unbalanced
		"rows":     []map[string]any{{"name": "x"}},
	})
	if code != http.StatusOK {
		t.Fatalf("CEL error should still be 200, got %d", code)
	}
	if pr.Error == "" || pr.Text != "" {
		t.Fatalf("want inline error and no text, got err=%q text=%q", pr.Error, pr.Text)
	}
	if !strings.Contains(pr.Error, "template:") {
		t.Errorf("parse error should be labelled 'template:', got %q", pr.Error)
	}
}

// TestRenderTextPreview_EmptyRowsUsesEmptyParam: zero rows yields the `empty`
// fallback verbatim, not the prefix/suffix.
func TestRenderTextPreview_EmptyRowsUsesEmptyParam(t *testing.T) {
	h := newGatewayHarness(t)
	code, pr := doTextPreview(t, h, map[string]any{
		"template": `'• ' + row["name"]`,
		"prefix":   "PFX",
		"empty":    "Nothing today.",
		"rows":     []map[string]any{},
	})
	if code != http.StatusOK || pr.Error != "" {
		t.Fatalf("want 200 no-error, got code=%d err=%q", code, pr.Error)
	}
	if pr.Text != "Nothing today." {
		t.Fatalf("got %q, want the empty fallback", pr.Text)
	}
}

// TestRenderTextPreview_NoRendererIsEmpty: before a preset is applied (no
// template, no column) the preview is a clean empty string, not an error.
func TestRenderTextPreview_NoRendererIsEmpty(t *testing.T) {
	h := newGatewayHarness(t)
	code, pr := doTextPreview(t, h, map[string]any{
		"rows": []map[string]any{{"name": "x"}},
	})
	if code != http.StatusOK || pr.Error != "" || pr.Text != "" {
		t.Fatalf("want 200 empty, got code=%d err=%q text=%q", code, pr.Error, pr.Text)
	}
}

// TestRenderTextPreview_RequiresAuth: the endpoint is authenticated.
func TestRenderTextPreview_RequiresAuth(t *testing.T) {
	h := newGatewayHarness(t)
	b, _ := json.Marshal(map[string]any{"template": "x"})
	req := httptest.NewRequest("POST", "/api/v1/tools/render-text/preview", bytes.NewBuffer(b))
	req.Header.Set("Content-Type", "application/json")
	// deliberately NO Authorization header
	rw := httptest.NewRecorder()
	ServeForTest(h.gw, rw, req)
	if rw.Code == http.StatusOK {
		t.Fatalf("preview without auth should not be 200, got %d", rw.Code)
	}
}
