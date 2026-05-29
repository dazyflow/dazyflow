package daemon

import "testing"

// The JSON an authored google.integration.ts reduces to (see
// engine/jsdrop/sdk/examples/google.integration.ts).
const googleIntegrationJSON = `{
  "id": "google", "version": "1.0.0", "label": "Google",
  "summary": "Connect Google accounts for Gmail, Sheets, Drive, and Calendar.",
  "icon": "google", "brandLogo": "/brands/google.svg",
  "docsUrl": "https://console.cloud.google.com/apis/credentials",
  "auth": {
    "kind": "oauth2",
    "authorizeUrl": "https://accounts.google.com/o/oauth2/v2/auth",
    "tokenUrl": "https://oauth2.googleapis.com/token",
    "usePKCE": true, "refreshable": true, "clientAuth": "body",
    "authorizeParams": { "access_type": "offline", "prompt": "consent" },
    "scopes": [
      "https://www.googleapis.com/auth/gmail.send",
      "https://www.googleapis.com/auth/spreadsheets"
    ]
  },
  "setup": [
    { "key": "client_id", "label": "OAuth client ID", "type": "text", "required": true },
    { "key": "client_secret", "label": "OAuth client secret", "type": "secret", "required": true },
    { "key": "redirect_uri", "label": "Authorized redirect URI", "type": "display",
      "value": "{publicBaseUrl}/api/v1/oauth/{id}/callback" }
  ]
}`

// acmeIntegrationJSON is a fictional third-party integration whose id is NOT a
// reserved built-in. The install-pipeline tests use it because they exercise
// pipeline mechanics (gating, persistence, uninstall) for an unsigned,
// community-tier integration — the common third-party case. Reserved built-in
// ids (google, slack, …) can only be claimed by a signed manifest; that rule
// has its own test (TestInstaller_ReservedIDRequiresTrust).
const acmeIntegrationJSON = `{
  "id": "acme", "version": "1.0.0", "label": "Acme CRM",
  "summary": "Connect an Acme CRM account.",
  "auth": {
    "kind": "oauth2",
    "authorizeUrl": "https://auth.acme.test/authorize",
    "tokenUrl": "https://auth.acme.test/token",
    "usePKCE": true, "refreshable": true, "clientAuth": "body",
    "scopes": ["crm.read", "crm.write"]
  },
  "setup": [
    { "key": "client_id", "label": "OAuth client ID", "type": "text", "required": true },
    { "key": "client_secret", "label": "OAuth client secret", "type": "secret", "required": true },
    { "key": "redirect_uri", "label": "Authorized redirect URI", "type": "display",
      "value": "{publicBaseUrl}/api/v1/oauth/{id}/callback" }
  ]
}`

func TestLoadIntegrationManifest_Google(t *testing.T) {
	m, err := LoadIntegrationManifest([]byte(googleIntegrationJSON))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if m.ID != "google" || m.Version != "1.0.0" {
		t.Fatalf("header wrong: id=%q version=%q", m.ID, m.Version)
	}
	if m.Auth.Kind != "oauth2" {
		t.Fatalf("auth.kind = %q", m.Auth.Kind)
	}
	if m.Auth.AuthorizeParams["access_type"] != "offline" {
		t.Errorf("authorizeParams not parsed: %v", m.Auth.AuthorizeParams)
	}
	if len(m.Setup) != 3 || m.Setup[1].Type != "secret" {
		t.Errorf("setup fields wrong: %+v", m.Setup)
	}
}

func TestLoadIntegrationManifest_Validation(t *testing.T) {
	bad := map[string]string{
		"no id":      `{"version":"1.0.0","summary":"x","auth":{"kind":"oauth2"}}`,
		"no version": `{"id":"x","summary":"x","auth":{"kind":"oauth2"}}`,
		"no summary": `{"id":"x","version":"1.0.0","auth":{"kind":"oauth2"}}`,
		"bad kind":   `{"id":"x","version":"1.0.0","summary":"x","auth":{"kind":"telepathy"}}`,
	}
	for name, j := range bad {
		if _, err := LoadIntegrationManifest([]byte(j)); err == nil {
			t.Errorf("%s: expected validation error", name)
		}
	}
}

func TestRegisterFromManifest_Google(t *testing.T) {
	m, err := LoadIntegrationManifest([]byte(googleIntegrationJSON))
	if err != nil {
		t.Fatal(err)
	}

	reg := NewOAuthRegistry("https://app.example.com", nil)
	if err := RegisterFromManifest(reg, m, "cid", "csecret"); err != nil {
		t.Fatalf("register: %v", err)
	}

	p, ok := reg.Provider("google")
	if !ok {
		t.Fatalf("google not registered; providers=%v", reg.Providers())
	}
	if p.AuthorizeURL != "https://accounts.google.com/o/oauth2/v2/auth" {
		t.Errorf("AuthorizeURL = %q", p.AuthorizeURL)
	}
	if p.TokenURL != "https://oauth2.googleapis.com/token" {
		t.Errorf("TokenURL = %q", p.TokenURL)
	}
	if p.AuthorizeExtras["access_type"] != "offline" || p.AuthorizeExtras["prompt"] != "consent" {
		t.Errorf("AuthorizeExtras lost in mapping: %v", p.AuthorizeExtras)
	}
	if len(p.Scopes) != 2 {
		t.Errorf("Scopes = %v", p.Scopes)
	}
	if p.ClientID != "cid" || p.ClientSecret != "csecret" {
		t.Error("creds not threaded onto the provider")
	}
}

func TestRegisterFromManifest_Rejects(t *testing.T) {
	oauth, _ := LoadIntegrationManifest([]byte(googleIntegrationJSON))
	if err := RegisterFromManifest(NewOAuthRegistry("https://x", nil), oauth, "", ""); err == nil {
		t.Error("expected error registering with empty credentials")
	}

	secretKind, err := LoadIntegrationManifest([]byte(`{"id":"stripe","version":"1.0.0","summary":"x","auth":{"kind":"secret"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := RegisterFromManifest(NewOAuthRegistry("https://x", nil), secretKind, "a", "b"); err == nil {
		t.Error("expected error registering a non-oauth2 integration as an OAuth provider")
	}

	// A non-https authorize/token URL leaks the auth code / client_secret over
	// the wire and must be refused.
	httpURL, err := LoadIntegrationManifest([]byte(`{"id":"acme","version":"1.0.0","summary":"x","auth":{"kind":"oauth2","authorizeUrl":"http://auth.acme.test/a","tokenUrl":"https://auth.acme.test/t"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := RegisterFromManifest(NewOAuthRegistry("https://x", nil), httpURL, "c", "s"); err == nil {
		t.Error("expected error registering an oauth2 provider with an http:// authorizeUrl")
	}
}
