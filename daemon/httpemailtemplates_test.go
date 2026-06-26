package daemon

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"git.sr.ht/~klahr/dazyflow/internal/emailtmpl"
)

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
	h := newSecretsHarness(t)
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
	h := newSecretsHarness(t)
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
	h := newSecretsHarness(t)
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

func TestEmailTemplates_DeleteBuiltinRejected(t *testing.T) {
	h := newSecretsHarness(t)
	rw := h.do(t, "DELETE", "/api/v1/email-templates/builtin:plain", nil)
	if rw.Code != http.StatusConflict {
		t.Errorf("delete built-in: status=%d, want 409", rw.Code)
	}
}

func TestEmailTemplates_HiddenFromSecretsListing(t *testing.T) {
	h := newSecretsHarness(t)
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
