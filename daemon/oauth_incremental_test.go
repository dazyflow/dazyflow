// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// fullGoogleScopes mirrors the provider's complete scope set.
var fullGoogleScopes = []string{
	"https://www.googleapis.com/auth/gmail.send",
	"https://www.googleapis.com/auth/spreadsheets",
	"https://www.googleapis.com/auth/gmail.readonly",
	"https://www.googleapis.com/auth/drive.readonly",
	"https://www.googleapis.com/auth/forms.responses.readonly",
	"https://www.googleapis.com/auth/forms.body.readonly",
}

func newGoogleOAuthHarness(t *testing.T) *gatewayHarness {
	t.Helper()
	h := newSecretsHarness(t) // token has secret:write; EncryptedSecrets wired
	h.gw.OAuth = NewOAuthRegistry("https://example.test", h.gw.EncryptedSecrets)
	h.gw.OAuth.Register(OAuthProvider{
		Name:         "google",
		AuthorizeURL: "https://accounts.google.com/o/oauth2/v2/auth",
		TokenURL:     "https://oauth2.googleapis.com/token",
		Scopes:       fullGoogleScopes,
		ClientID:     "cid.apps.googleusercontent.com",
		ClientSecret: "GOCSPX-secret",
		AuthorizeExtras: map[string]string{
			"access_type":            "offline",
			"prompt":                 "consent",
			"include_granted_scopes": "true",
		},
	})
	return h
}

// authorizeScopes drives GET /oauth/google/authorize and returns the space-
// split scope set + the full query of the consent URL it 302s to.
func authorizeScopes(t *testing.T, h *gatewayHarness, query string) (map[string]bool, url.Values) {
	t.Helper()
	// Connecting Google now requires organization:admin (org-shared credential).
	rw := h.adminDo(t, "GET", "/api/v1/oauth/google/authorize?"+query, nil)
	if rw.Code != http.StatusFound {
		t.Fatalf("authorize status=%d body=%s", rw.Code, rw.Body.String())
	}
	loc, err := url.Parse(rw.Header().Get("Location"))
	if err != nil {
		t.Fatalf("bad Location: %v", err)
	}
	q := loc.Query()
	set := map[string]bool{}
	for _, s := range strings.Fields(q.Get("scope")) {
		set[s] = true
	}
	return set, q
}

func TestScopeSubsetForIntegration(t *testing.T) {
	cases := map[string][]string{
		"Gmail":         {"https://www.googleapis.com/auth/gmail.send", "https://www.googleapis.com/auth/gmail.readonly"},
		"Google Sheets": {"https://www.googleapis.com/auth/spreadsheets", "https://www.googleapis.com/auth/drive.readonly"},
		"Google Forms":  {"https://www.googleapis.com/auth/forms.responses.readonly", "https://www.googleapis.com/auth/forms.body.readonly", "https://www.googleapis.com/auth/drive.metadata.readonly"},
	}
	for integ, want := range cases {
		got := scopeSubsetForIntegration("google", integ)
		if strings.Join(got, " ") != strings.Join(want, " ") {
			t.Errorf("%q -> %v, want %v", integ, got, want)
		}
	}
	if scopeSubsetForIntegration("google", "Unknown") != nil {
		t.Error("unknown integration should be nil (full set)")
	}
	if scopeSubsetForIntegration("slack", "Gmail") != nil {
		t.Error("non-google provider should be nil")
	}
}

func TestAuthorize_IncrementalScopesPerIntegration(t *testing.T) {
	h := newGoogleOAuthHarness(t)

	// Connecting for Sheets requests ONLY the sheets scopes — no gmail/forms.
	scopes, q := authorizeScopes(t, h, "integration=Google+Sheets&return_to=/apps")
	if !scopes["https://www.googleapis.com/auth/spreadsheets"] || !scopes["https://www.googleapis.com/auth/drive.readonly"] {
		t.Errorf("missing sheets scopes: %v", scopes)
	}
	if scopes["https://www.googleapis.com/auth/gmail.send"] || scopes["https://www.googleapis.com/auth/forms.body.readonly"] {
		t.Errorf("sheets connect leaked gmail/forms scopes: %v", scopes)
	}
	// include_granted_scopes must ride along so the grant merges.
	if q.Get("include_granted_scopes") != "true" {
		t.Errorf("include_granted_scopes = %q, want true", q.Get("include_granted_scopes"))
	}

	// Gmail connect → only gmail scopes.
	gmail, _ := authorizeScopes(t, h, "integration=Gmail")
	if !gmail["https://www.googleapis.com/auth/gmail.send"] || gmail["https://www.googleapis.com/auth/spreadsheets"] {
		t.Errorf("gmail connect scopes wrong: %v", gmail)
	}
}

func TestAuthorize_NoIntegrationRequestsFullSet(t *testing.T) {
	h := newGoogleOAuthHarness(t)
	// Omitting integration falls back to the provider's full scope list.
	scopes, _ := authorizeScopes(t, h, "return_to=/apps")
	for _, s := range fullGoogleScopes {
		if !scopes[s] {
			t.Errorf("full-set fallback missing %q (got %v)", s, scopes)
		}
	}
	// An unknown integration also falls back to the full set.
	unknown, _ := authorizeScopes(t, h, "integration=Mystery")
	if !unknown["https://www.googleapis.com/auth/gmail.send"] {
		t.Errorf("unknown integration should fall back to full set: %v", unknown)
	}
}
