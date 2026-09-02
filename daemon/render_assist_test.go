// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"encoding/json"
	"net/http"
	"testing"
)

type assistResp struct {
	Template    string `json:"template"`
	Error       string `json:"error"`
	NeedConnect bool   `json:"need_connect"`
}

func doAssist(t *testing.T, h *gatewayHarness, body any) (int, assistResp) {
	t.Helper()
	rw := h.do(t, "POST", "/api/v1/tools/render-template/assist", body)
	var ar assistResp
	if rw.Body.Len() > 0 {
		_ = json.Unmarshal(rw.Body.Bytes(), &ar)
	}
	return rw.Code, ar
}

// TestRenderAssist_NoProviderNeedsConnect: with no secret store / no
// connected LLM, the endpoint asks the user to connect one (200 +
// need_connect), rather than erroring out — the UI turns this into a link.
func TestRenderAssist_NoProviderNeedsConnect(t *testing.T) {
	t.Parallel()
	h := newGatewayHarness(t) // harness has no EncryptedSecrets configured
	code, ar := doAssist(t, h, map[string]any{
		"description": "a welcome email", "fields": []string{"name"},
	})
	if code != http.StatusOK {
		t.Fatalf("want 200, got %d", code)
	}
	if !ar.NeedConnect || ar.Template != "" {
		t.Fatalf("want need_connect with no template, got %+v", ar)
	}
}

// TestRenderAssist_EmptyDescription is a 400 (nothing to generate from).
func TestRenderAssist_EmptyDescription(t *testing.T) {
	t.Parallel()
	h := newGatewayHarness(t)
	code, _ := doAssist(t, h, map[string]any{"description": "   "})
	if code != http.StatusBadRequest {
		t.Fatalf("empty description: want 400, got %d", code)
	}
}

// TestStripCodeFences covers the helper that removes the ```html … ```
// wrapper models often add despite instructions.
func TestStripCodeFences(t *testing.T) {
	t.Parallel()
	cases := []struct{ in, want string }{
		{"<h1>Hi</h1>", "<h1>Hi</h1>"},
		{"```html\n<h1>Hi</h1>\n```", "<h1>Hi</h1>"},
		{"```\n<p>x</p>\n```", "<p>x</p>"},
		{"  <p>trim me</p>  ", "<p>trim me</p>"},
	}
	for _, c := range cases {
		if got := stripCodeFences(c.in); got != c.want {
			t.Errorf("stripCodeFences(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestRenderAssist_DecodeError(t *testing.T) {
	t.Parallel()
	h := newGatewayHarness(t)
	req := newRawReq(t, h, "POST", "/api/v1/tools/render-template/assist", "{not json")
	rw := serveRaw(h, req)
	if rw.Code != http.StatusBadRequest {
		t.Fatalf("assist bad json = %d (%s), want 400", rw.Code, rw.Body.String())
	}
}
