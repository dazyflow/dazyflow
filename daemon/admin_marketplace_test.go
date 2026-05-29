package daemon

import (
	"encoding/json"
	"strings"
	"testing"

)

func newMarketplaceHarness(t *testing.T) *gatewayHarness {
	t.Helper()
	h := newGatewayHarness(t)
	es, err := NewEncryptedSecrets(make([]byte, 32), NewMemSecretsStore())
	if err != nil {
		t.Fatalf("NewEncryptedSecrets: %v", err)
	}
	h.gw.EncryptedSecrets = es
	h.gw.OAuth = NewOAuthRegistry("https://app.example.test", es)
	h.gw.svc.PublicBaseURL = "https://app.example.test"
	h.gw.Installer = NewInstaller(h.gw.OAuth, testScriptedCatalog(t), es, nil)
	return h
}

// Install is platform-admin only — editors and tenant admins are rejected.
func TestMarketplace_RequiresPlatformAdmin(t *testing.T) {
	h := newMarketplaceHarness(t)
	if rw := h.do(t, "GET", "/api/v1/admin/marketplace/integrations", nil); rw.Code != 403 {
		t.Errorf("editor should be 403; got %d", rw.Code)
	}
	if rw := h.adminDo(t, "GET", "/api/v1/admin/marketplace/integrations", nil); rw.Code != 403 {
		t.Errorf("tenant admin should be 403; got %d", rw.Code)
	}
	if rw := h.platformDo(t, "GET", "/api/v1/admin/marketplace/integrations", nil); rw.Code != 200 {
		t.Errorf("platform admin should be 200; got %d body=%s", rw.Code, rw.Body.String())
	}
}

// Preview validates a manifest and returns the setup form with display fields
// templated for this deployment (the redirect URI filled in).
func TestMarketplace_PreviewRendersSetupForm(t *testing.T) {
	h := newMarketplaceHarness(t)
	rw := h.platformDo(t, "POST", "/api/v1/admin/marketplace/integrations/preview",
		map[string]any{"manifest": json.RawMessage(googleIntegrationJSON)})
	if rw.Code != 200 {
		t.Fatalf("preview code = %d body=%s", rw.Code, rw.Body.String())
	}
	var out integrationPreview
	if err := json.NewDecoder(rw.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.ID != "google" || out.AuthKind != "oauth2" {
		t.Errorf("preview header wrong: %+v", out)
	}
	var redirect string
	for _, f := range out.Setup {
		if f.Key == "redirect_uri" {
			redirect = f.Value
		}
	}
	if redirect != "https://app.example.test/api/v1/oauth/google/callback" {
		t.Errorf("redirect_uri not templated: %q", redirect)
	}
}

// The full flow over HTTP: a drop is gated until its integration is installed,
// and installing the integration registers + persists the provider.
func TestMarketplace_InstallIntegrationThenGatedDrop(t *testing.T) {
	h := newMarketplaceHarness(t)

	dropBody := map[string]any{"name": "acme.ts", "source": acmeDropSrc}
	ackedDropBody := map[string]any{"name": "acme.ts", "source": acmeDropSrc, "acknowledged": true}

	// 0. Without capability consent → 400, regardless of prerequisites.
	if rw := h.platformDo(t, "POST", "/api/v1/admin/marketplace/drops", dropBody); rw.Code != 400 {
		t.Fatalf("unacknowledged drop install should be 400; got %d body=%s", rw.Code, rw.Body.String())
	}

	// 0b. Preview surfaces the requested capabilities for the consent screen.
	rwp := h.platformDo(t, "POST", "/api/v1/admin/marketplace/drops/preview", dropBody)
	if rwp.Code != 200 {
		t.Fatalf("drop preview code = %d body=%s", rwp.Code, rwp.Body.String())
	}
	var cap dropCapabilitySummary
	if err := json.Unmarshal(rwp.Body.Bytes(), &cap); err != nil {
		t.Fatalf("decode capability summary: %v", err)
	}
	if !cap.Sandboxed {
		t.Error("installed drop should be reported as sandboxed")
	}
	if len(cap.OAuth) != 1 || cap.OAuth[0] != "acme" {
		t.Errorf("preview should list the acme OAuth requirement, got %#v", cap.OAuth)
	}

	// 1. Acknowledged, but the acme integration isn't installed → 409.
	if rw := h.platformDo(t, "POST", "/api/v1/admin/marketplace/drops", ackedDropBody); rw.Code != 409 {
		t.Fatalf("gated drop install should be 409; got %d body=%s", rw.Code, rw.Body.String())
	}

	// 2. Install the acme integration.
	rw := h.platformDo(t, "POST", "/api/v1/admin/marketplace/integrations", map[string]any{
		"manifest":    json.RawMessage(acmeIntegrationJSON),
		"credentials": map[string]string{"client_id": "cid", "client_secret": "sec"},
	})
	if rw.Code != 200 {
		t.Fatalf("install integration code = %d body=%s", rw.Code, rw.Body.String())
	}
	if _, ok := h.gw.OAuth.Provider("acme"); !ok {
		t.Error("provider not registered after install")
	}
	if c, err := loadProviderCreds(t.Context(), h.gw.EncryptedSecrets, "acme"); err != nil || c == nil || c.ClientID != "cid" {
		t.Errorf("creds not persisted: (%+v, %v)", c, err)
	}

	// 3. Now the (acknowledged) drop installs.
	if rw := h.platformDo(t, "POST", "/api/v1/admin/marketplace/drops", ackedDropBody); rw.Code != 200 {
		t.Fatalf("drop install after integration should be 200; got %d body=%s", rw.Code, rw.Body.String())
	}

	// 4. It shows up in the installed-drops listing.
	rw = h.platformDo(t, "GET", "/api/v1/admin/marketplace/drops", nil)
	if rw.Code != 200 || !strings.Contains(rw.Body.String(), "acme_sync") {
		t.Errorf("installed drops listing missing acme_sync: %d %s", rw.Code, rw.Body.String())
	}
}

// Install an integration straight from a git repo@tag over HTTP.
func TestMarketplace_InstallIntegrationFromGit(t *testing.T) {
	h := newMarketplaceHarness(t)
	repo := makeRepo(t, map[string]string{"integration.json": acmeIntegrationJSON}, "v1.0.0")
	rw := h.platformDo(t, "POST", "/api/v1/admin/marketplace/integrations/from-git", map[string]any{
		"repo":        repo,
		"ref":         "v1.0.0",
		"credentials": map[string]string{"client_id": "c", "client_secret": "s"},
	})
	if rw.Code != 200 {
		t.Fatalf("from-git install: %d %s", rw.Code, rw.Body.String())
	}
	if _, ok := h.gw.OAuth.Provider("acme"); !ok {
		t.Error("provider not registered via from-git install")
	}
}
