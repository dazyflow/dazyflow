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

// previewResp is the shape of the preview endpoint's JSON.
type previewResp struct {
	HTML  string `json:"html"`
	Error string `json:"error"`
}

func doPreview(t *testing.T, h *gatewayHarness, body any) (int, previewResp) {
	t.Helper()
	rw := h.do(t, "POST", "/api/v1/tools/render-template/preview", body)
	var pr previewResp
	if rw.Body.Len() > 0 {
		_ = json.Unmarshal(rw.Body.Bytes(), &pr)
	}
	return rw.Code, pr
}

// TestRenderPreview_RendersAndEscapes: the endpoint renders merge fields and
// auto-escapes untrusted data — same engine as the drop.
func TestRenderPreview_RendersAndEscapes(t *testing.T) {
	t.Parallel()
	h := newGatewayHarness(t)
	code, pr := doPreview(t, h, map[string]any{
		"template": "<h1>Hi {{.name}}</h1>",
		"data":     map[string]any{"name": "<script>x</script>"},
	})
	if code != http.StatusOK {
		t.Fatalf("want 200, got %d", code)
	}
	if pr.Error != "" {
		t.Fatalf("unexpected error: %s", pr.Error)
	}
	if strings.Contains(pr.HTML, "<script>") || !strings.Contains(pr.HTML, "&lt;script&gt;") {
		t.Fatalf("data not escaped in preview: %q", pr.HTML)
	}
}

// TestRenderPreview_ErrorsAreInline: a bad template comes back as a 200 with
// an error field (so the editor shows it inline), not an HTTP error.
func TestRenderPreview_ErrorsAreInline(t *testing.T) {
	t.Parallel()
	h := newGatewayHarness(t)
	code, pr := doPreview(t, h, map[string]any{"template": "{{.unclosed", "data": map[string]any{}})
	if code != http.StatusOK {
		t.Fatalf("template error should still be 200, got %d", code)
	}
	if pr.Error == "" || pr.HTML != "" {
		t.Fatalf("want inline error and no html, got err=%q html=%q", pr.Error, pr.HTML)
	}
	if !strings.Contains(pr.Error, "template:") {
		t.Errorf("parse error should be labelled 'template:', got %q", pr.Error)
	}
}

// TestRenderPreview_EmptyTemplateNoData: an empty template with no data is a
// clean empty render (the UI state before the user types), not an error.
func TestRenderPreview_EmptyTemplateNoData(t *testing.T) {
	t.Parallel()
	h := newGatewayHarness(t)
	code, pr := doPreview(t, h, map[string]any{"template": ""})
	if code != http.StatusOK || pr.Error != "" || pr.HTML != "" {
		t.Fatalf("empty template: want 200 empty, got code=%d err=%q html=%q", code, pr.Error, pr.HTML)
	}
}

// TestRenderPreview_RequiresAuth: the endpoint is authenticated.
func TestRenderPreview_RequiresAuth(t *testing.T) {
	t.Parallel()
	h := newGatewayHarness(t)
	b, _ := json.Marshal(map[string]any{"template": "x"})
	req := httptest.NewRequest("POST", "/api/v1/tools/render-template/preview", bytes.NewBuffer(b))
	req.Header.Set("Content-Type", "application/json")
	// deliberately NO Authorization header
	rw := httptest.NewRecorder()
	ServeForTest(h.gw, rw, req)
	if rw.Code == http.StatusOK {
		t.Fatalf("preview without auth should not be 200, got %d", rw.Code)
	}
}
