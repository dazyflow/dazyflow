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

	"github.com/dazyflow/dazyflow/auth"
	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/internal/emailtmpl"
)

// newEmailAdminHarness is newSecretsHarness with the token swapped for an
// organization:admin one — managing email templates is admin-only, and the
// admin role also carries secret:read so the same token drives reads. Returns
// the harness plus the original (secret:write-only, non-admin) editor token so
// a test can assert the admin gate.
func newEmailAdminHarness(t *testing.T) (*gatewayHarness, string) {
	t.Helper()
	h := newSecretsHarness(t)
	editorTok := h.token
	_, adminTok, err := auth.IssueAPIKey(h.ks, t.Context(), "tmpl-admin", "t", "ws", "admin@t", []core.Role{core.TeamRoleAdmin()}, nil)
	if err != nil {
		t.Fatalf("issue admin token: %v", err)
	}
	h.token = adminTok
	return h, editorTok
}

func putTemplateBody(displayName, html string) []byte {
	b, _ := json.Marshal(map[string]any{"name": displayName, "html": html})
	return b
}

type templateListResp struct {
	Templates []struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		HTML     string `json:"html"`
		Builtin  bool   `json:"builtin"`
		ReadOnly bool   `json:"readOnly"`
	} `json:"templates"`
}

func listTemplates(t *testing.T, h *gatewayHarness) templateListResp {
	t.Helper()
	rw := h.do(t, "GET", "/api/v1/email-templates", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("GET status=%d body=%s", rw.Code, rw.Body.String())
	}
	var resp templateListResp
	if err := json.Unmarshal(rw.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	return resp
}

func TestEmailTemplates_CRUDRoundTrip(t *testing.T) {
	h, _ := newEmailAdminHarness(t)
	const html = `<div>{{.Body}}</div>`
	if rw := h.do(t, "PUT", "/api/v1/email-templates/welcome",
		json.RawMessage(putTemplateBody("Welcome email", html))); rw.Code != http.StatusNoContent {
		t.Fatalf("PUT status=%d body=%s", rw.Code, rw.Body.String())
	}

	resp := listTemplates(t, h)
	var got *struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		HTML     string `json:"html"`
		Builtin  bool   `json:"builtin"`
		ReadOnly bool   `json:"readOnly"`
	}
	for i := range resp.Templates {
		if resp.Templates[i].ID == "welcome" {
			got = &resp.Templates[i]
		}
	}
	if got == nil {
		t.Fatalf("welcome not in list: %+v", resp.Templates)
	}
	if got.Name != "Welcome email" || got.HTML != html || got.Builtin || got.ReadOnly {
		t.Errorf("template view = %+v", *got)
	}

	// Delete, then it's gone.
	if rw := h.do(t, "DELETE", "/api/v1/email-templates/welcome", nil); rw.Code != http.StatusNoContent {
		t.Fatalf("DELETE status=%d", rw.Code)
	}
	for _, tpl := range listTemplates(t, h).Templates {
		if tpl.ID == "welcome" {
			t.Errorf("welcome should be gone: %+v", tpl)
		}
	}
}

func TestEmailTemplates_BuiltinsAlwaysListedAndReadOnly(t *testing.T) {
	h, _ := newEmailAdminHarness(t)
	resp := listTemplates(t, h)
	builtins := emailtmpl.BuiltinTemplates()
	if len(resp.Templates) < len(builtins) {
		t.Fatalf("expected at least %d built-ins, got %d", len(builtins), len(resp.Templates))
	}
	seen := map[string]bool{}
	for _, tpl := range resp.Templates {
		if tpl.Builtin {
			if !tpl.ReadOnly {
				t.Errorf("built-in %q must be read-only", tpl.ID)
			}
			seen[tpl.ID] = true
		}
	}
	for _, b := range builtins {
		if !seen[b.ID] {
			t.Errorf("built-in %q missing from list", b.ID)
		}
	}
}

func TestEmailTemplates_RejectsBadInput(t *testing.T) {
	h, _ := newEmailAdminHarness(t)
	// Empty HTML.
	if rw := h.do(t, "PUT", "/api/v1/email-templates/x",
		json.RawMessage(putTemplateBody("X", "  "))); rw.Code != http.StatusBadRequest {
		t.Errorf("empty html: status=%d, want 400", rw.Code)
	}
	// HTML without the {{.Body}} placeholder.
	if rw := h.do(t, "PUT", "/api/v1/email-templates/x",
		json.RawMessage(putTemplateBody("X", "<div>no body</div>"))); rw.Code != http.StatusBadRequest {
		t.Errorf("missing placeholder: status=%d, want 400", rw.Code)
	}
	// Colon in the name (would collide with the builtin: namespace).
	if rw := h.do(t, "PUT", "/api/v1/email-templates/builtin:x",
		json.RawMessage(putTemplateBody("X", "<div>{{.Body}}</div>"))); rw.Code != http.StatusBadRequest {
		t.Errorf("colon name: status=%d, want 400", rw.Code)
	}
}

// doAsToken sends a request with an explicit bearer token (h.do is fixed to
// h.token) and returns the status code.
func doAsToken(t *testing.T, h *gatewayHarness, token, method, path string, body []byte) int {
	t.Helper()
	var br *bytes.Buffer
	if body != nil {
		br = bytes.NewBuffer(body)
	} else {
		br = bytes.NewBuffer(nil)
	}
	req := httptest.NewRequest(method, path, br)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	rw := httptest.NewRecorder()
	ServeForTest(h.gw, rw, req)
	return rw.Code
}

func TestEmailTemplates_WriteRequiresAdmin(t *testing.T) {
	h, editorTok := newEmailAdminHarness(t)
	// A non-admin editor (secret:write but not organization:admin) can LIST,
	// but cannot create or delete.
	if code := doAsToken(t, h, editorTok, "GET", "/api/v1/email-templates", nil); code != http.StatusOK {
		t.Errorf("editor list status=%d, want 200", code)
	}
	if code := doAsToken(t, h, editorTok, "PUT", "/api/v1/email-templates/welcome",
		putTemplateBody("Welcome", "<div>{{.Body}}</div>")); code != http.StatusForbidden {
		t.Errorf("editor PUT status=%d, want 403", code)
	}
	if code := doAsToken(t, h, editorTok, "DELETE", "/api/v1/email-templates/welcome", nil); code != http.StatusForbidden {
		t.Errorf("editor DELETE status=%d, want 403", code)
	}
	// The admin token (h.token) can write.
	if rw := h.do(t, "PUT", "/api/v1/email-templates/welcome",
		json.RawMessage(putTemplateBody("Welcome", "<div>{{.Body}}</div>"))); rw.Code != http.StatusNoContent {
		t.Errorf("admin PUT status=%d, want 204", rw.Code)
	}
}

func TestEmailTemplates_DeleteBuiltinRejected(t *testing.T) {
	h, _ := newEmailAdminHarness(t)
	rw := h.do(t, "DELETE", "/api/v1/email-templates/builtin:plain", nil)
	if rw.Code != http.StatusConflict {
		t.Errorf("delete built-in: status=%d, want 409", rw.Code)
	}
}

func TestEmailTemplates_HiddenFromSecretsListing(t *testing.T) {
	h, _ := newEmailAdminHarness(t)
	h.do(t, "PUT", "/api/v1/email-templates/welcome",
		json.RawMessage(putTemplateBody("Welcome", "<div>{{.Body}}</div>")))
	rw := h.do(t, "GET", "/api/v1/secrets", nil)
	var resp struct {
		Secrets []string `json:"secrets"`
	}
	_ = json.Unmarshal(rw.Body.Bytes(), &resp)
	for _, n := range resp.Secrets {
		if strings.HasPrefix(n, "emailtmpl.") || n == "welcome" {
			t.Errorf("template leaked into secrets listing: %v", resp.Secrets)
		}
	}
}

func TestEmailTemplates_Preview(t *testing.T) {
	h := newSecretsHarness(t)
	body, _ := json.Marshal(map[string]any{"html": `<section>{{.Body}}</section>`})
	rw := h.do(t, "POST", "/api/v1/email-templates/preview", json.RawMessage(body))
	if rw.Code != http.StatusOK {
		t.Fatalf("preview status=%d body=%s", rw.Code, rw.Body.String())
	}
	var resp struct {
		HTML string `json:"html"`
	}
	_ = json.Unmarshal(rw.Body.Bytes(), &resp)
	if !strings.Contains(resp.HTML, "<section>") || !strings.Contains(resp.HTML, "preview") {
		t.Errorf("preview did not render sample body: %q", resp.HTML)
	}
}

func TestEmailTemplates_PreviewByID(t *testing.T) {
	h, _ := newEmailAdminHarness(t)
	h.do(t, "PUT", "/api/v1/email-templates/welcome",
		json.RawMessage(putTemplateBody("Welcome", `<main>{{.Body}}</main>`)))

	preview := func(payload map[string]any) (int, string) {
		b, _ := json.Marshal(payload)
		rw := h.do(t, "POST", "/api/v1/email-templates/preview", json.RawMessage(b))
		var resp struct {
			HTML string `json:"html"`
		}
		_ = json.Unmarshal(rw.Body.Bytes(), &resp)
		return rw.Code, resp.HTML
	}

	// Resolve a saved template by id and wrap the REAL body (no sample fallback).
	code, html := preview(map[string]any{"id": "welcome", "body": "<p>real</p>"})
	if code != http.StatusOK {
		t.Fatalf("preview by id status=%d", code)
	}
	if !strings.Contains(html, "<main>") || !strings.Contains(html, "<p>real</p>") {
		t.Errorf("preview by id did not wrap the real body: %q", html)
	}
	if strings.Contains(html, "preview of your email template") {
		t.Error("real body should not fall back to the sample body")
	}

	// A built-in id resolves too.
	if code, _ := preview(map[string]any{"id": "builtin:plain", "body": "<p>x</p>"}); code != http.StatusOK {
		t.Errorf("preview builtin status=%d, want 200", code)
	}
	// Unknown id → 404.
	if code, _ := preview(map[string]any{"id": "nope", "body": "x"}); code != http.StatusNotFound {
		t.Errorf("preview unknown id status=%d, want 404", code)
	}
	// No template + empty body still renders the body bare.
	if code, html := preview(map[string]any{"body": "<p>bare</p>"}); code != http.StatusOK || !strings.Contains(html, "<p>bare</p>") {
		t.Errorf("bare preview status=%d html=%q", code, html)
	}
}

// emailTemplateGate not-configured branch (no EncryptedSecrets) across the
// list / put / delete / preview / send-test endpoints.

func TestEmailTemplates_NotConfigured(t *testing.T) {
	h := newGatewayHarness(t) // no EncryptedSecrets
	cases := []struct {
		method, path string
	}{
		{"GET", "/api/v1/email-templates"},
		{"PUT", "/api/v1/email-templates/welcome"},
		{"DELETE", "/api/v1/email-templates/welcome"},
		{"POST", "/api/v1/email-templates/preview"},
		{"POST", "/api/v1/email-templates/send-test"},
	}
	for _, c := range cases {
		rw := h.adminDo(t, c.method, c.path, map[string]any{})
		if rw.Code != http.StatusNotImplemented {
			t.Errorf("%s %s = %d (%s), want 501", c.method, c.path, rw.Code, rw.Body.String())
		}
	}
}

func TestEmailTemplates_ListForbiddenWithoutReadPerm(t *testing.T) {
	h := newGatewayHarness(t)
	h.gw.EncryptedSecrets = testEncryptedSecrets(t)
	// Default editor token lacks secret:read.
	rw := h.do(t, "GET", "/api/v1/email-templates", nil)
	if rw.Code != http.StatusForbidden {
		t.Fatalf("list templates no perm = %d (%s), want 403", rw.Code, rw.Body.String())
	}
}
