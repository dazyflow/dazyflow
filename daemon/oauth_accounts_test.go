package daemon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"git.sr.ht/~klahr/hazyflow/auth"
	"git.sr.ht/~klahr/hazyflow/core"
)

// The /oauth/google/accounts endpoint reports, per connected account, which
// services its stored grant covers — the data behind the /admin/google page.
// It's org-admin only, and coverage is derived from the same scope groups the
// connect path uses (scopeSubsetForIntegration).
func TestOAuthListAccounts_CoverageAndGate(t *testing.T) {
	h := newGoogleOAuthHarness(t)
	es := h.gw.EncryptedSecrets

	// "support" has Forms + Sheets scopes but no Gmail; "ops" has only Gmail.
	formsSheets := `{"access_token":"x","scope":"https://www.googleapis.com/auth/forms.responses.readonly https://www.googleapis.com/auth/forms.body.readonly https://www.googleapis.com/auth/drive.metadata.readonly https://www.googleapis.com/auth/spreadsheets https://www.googleapis.com/auth/drive.readonly"}`
	if err := es.Put(t.Context(), "t", secretNameFor("google", "support"), formsSheets); err != nil {
		t.Fatalf("seed support: %v", err)
	}
	gmailOnly := `{"access_token":"y","scope":"https://www.googleapis.com/auth/gmail.send https://www.googleapis.com/auth/gmail.readonly"}`
	if err := es.Put(t.Context(), "t", secretNameFor("google", "ops"), gmailOnly); err != nil {
		t.Fatalf("seed ops: %v", err)
	}

	// Non-admin (secret:write only) → 403.
	swRole := core.Role{Name: "editor", Permissions: []core.Permission{core.PermSecretWrite}}
	_, swTok, err := auth.IssueAPIKey(h.ks, t.Context(), "k-sw", "t", "ws", "u@t", []core.Role{swRole}, nil)
	if err != nil {
		t.Fatalf("issue secret:write key: %v", err)
	}
	req := httptest.NewRequest("GET", "/api/v1/oauth/google/accounts", nil)
	req.Header.Set("Authorization", "Bearer "+swTok)
	rw := httptest.NewRecorder()
	ServeForTest(h.gw, rw, req)
	if rw.Code != http.StatusForbidden {
		t.Fatalf("non-admin should be 403; got %d body=%s", rw.Code, rw.Body.String())
	}

	// Org-admin → 200 with per-account coverage.
	rw = h.adminDo(t, "GET", "/api/v1/oauth/google/accounts", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("admin should be 200; got %d body=%s", rw.Code, rw.Body.String())
	}
	var resp struct {
		Provider string   `json:"provider"`
		Services []string `json:"services"`
		Accounts []struct {
			Account  string          `json:"account"`
			Coverage map[string]bool `json:"coverage"`
		} `json:"accounts"`
	}
	if err := json.Unmarshal(rw.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Provider != "google" {
		t.Errorf("provider = %q, want google", resp.Provider)
	}
	// Services are the sorted scope-group keys.
	wantServices := []string{"Gmail", "Google Calendar", "Google Drive", "Google Forms", "Google Sheets"}
	if len(resp.Services) != len(wantServices) {
		t.Fatalf("services = %v, want %v", resp.Services, wantServices)
	}
	for i, s := range wantServices {
		if resp.Services[i] != s {
			t.Errorf("services[%d] = %q, want %q", i, resp.Services[i], s)
		}
	}
	cov := map[string]map[string]bool{}
	for _, a := range resp.Accounts {
		cov[a.Account] = a.Coverage
	}
	if c := cov["support"]; c == nil || !c["Google Forms"] || !c["Google Sheets"] || c["Gmail"] {
		t.Errorf("support coverage = %v, want Forms+Sheets only", c)
	}
	if c := cov["ops"]; c == nil || !c["Gmail"] || c["Google Forms"] || c["Google Sheets"] {
		t.Errorf("ops coverage = %v, want Gmail only", c)
	}
}
