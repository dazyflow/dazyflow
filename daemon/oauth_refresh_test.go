// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"testing"
	"time"

	"git.sr.ht/~klahr/dazyflow/core"
)

// refreshHarness wires an encrypted store + registry + fake provider,
// the shared setup for the refresh-on-expiry tests.
func refreshHarness(t *testing.T) (*OAuthRegistry, *fakeProvider) {
	t.Helper()
	es, err := NewEncryptedSecrets(make([]byte, 32), NewMemSecretsStore())
	if err != nil {
		t.Fatalf("secrets: %v", err)
	}
	fp := newFakeProvider(t)
	reg := NewOAuthRegistry("https://example.test", es)
	reg.Register(fp.provider())
	reg.HTTPClient = fp.server.Client()
	return reg, fp
}

func seedToken(t *testing.T, reg *OAuthRegistry, tok *StoredOAuthToken) {
	t.Helper()
	if _, err := reg.store(t.Context(), "acme", "test", "main", tok); err != nil {
		t.Fatalf("seed token: %v", err)
	}
}

func getToken(t *testing.T, reg *OAuthRegistry) *StoredOAuthToken {
	t.Helper()
	tok, err := reg.GetOAuthToken(core.WithTenant(t.Context(), "acme"), "test", "main")
	if err != nil {
		t.Fatalf("GetOAuthToken: %v", err)
	}
	return tok
}

// An expired access token with a refresh_token is transparently
// refreshed: the caller gets a fresh access token, the provider is hit
// with grant_type=refresh_token, and the new token is persisted.
func TestOAuth_GetToken_RefreshesExpired(t *testing.T) {
	reg, fp := refreshHarness(t)
	past := time.Now().UTC().Add(-time.Minute)
	seedToken(t, reg, &StoredOAuthToken{
		AccessToken:  "old-access",
		RefreshToken: "the-refresh",
		ExpiresAt:    &past,
		ObtainedAt:   past,
	})
	// Google-style refresh response: a new access token, no new
	// refresh_token (the old one stays valid).
	fp.tokenBody = `{"access_token":"new-access","token_type":"Bearer","expires_in":3600}`
	fp.lastFormBody = nil

	got := getToken(t, reg)
	if got.AccessToken != "new-access" {
		t.Errorf("access_token = %q, want new-access", got.AccessToken)
	}
	if got.RefreshToken != "the-refresh" {
		t.Errorf("refresh_token = %q, want the old one carried over", got.RefreshToken)
	}
	if fp.lastFormBody.Get("grant_type") != "refresh_token" {
		t.Errorf("grant_type = %q, want refresh_token", fp.lastFormBody.Get("grant_type"))
	}
	if fp.lastFormBody.Get("refresh_token") != "the-refresh" {
		t.Errorf("sent refresh_token = %q", fp.lastFormBody.Get("refresh_token"))
	}

	// The refreshed token is persisted: a second read (now valid for an
	// hour) returns it without another provider call.
	fp.lastFormBody = nil
	again := getToken(t, reg)
	if again.AccessToken != "new-access" {
		t.Errorf("persisted access_token = %q", again.AccessToken)
	}
	if fp.lastFormBody != nil {
		t.Errorf("a still-valid token should not refresh; provider was hit with %v", fp.lastFormBody)
	}
}

// A token comfortably within its lifetime is returned untouched.
func TestOAuth_GetToken_ValidNotRefreshed(t *testing.T) {
	reg, fp := refreshHarness(t)
	future := time.Now().UTC().Add(time.Hour)
	seedToken(t, reg, &StoredOAuthToken{
		AccessToken:  "still-valid",
		RefreshToken: "the-refresh",
		ExpiresAt:    &future,
		ObtainedAt:   time.Now().UTC(),
	})
	fp.lastFormBody = nil

	got := getToken(t, reg)
	if got.AccessToken != "still-valid" {
		t.Errorf("access_token = %q, want still-valid", got.AccessToken)
	}
	if fp.lastFormBody != nil {
		t.Errorf("valid token should not hit the provider; got %v", fp.lastFormBody)
	}
}

// Expired but with no refresh_token: nothing to refresh, so the stored
// (expired) token comes back as-is and the provider isn't called.
func TestOAuth_GetToken_ExpiredNoRefreshToken(t *testing.T) {
	reg, fp := refreshHarness(t)
	past := time.Now().UTC().Add(-time.Minute)
	seedToken(t, reg, &StoredOAuthToken{
		AccessToken: "old-access",
		ExpiresAt:   &past,
		ObtainedAt:  past,
	})
	fp.lastFormBody = nil

	got := getToken(t, reg)
	if got.AccessToken != "old-access" {
		t.Errorf("access_token = %q, want old-access (unchanged)", got.AccessToken)
	}
	if fp.lastFormBody != nil {
		t.Errorf("no refresh_token means no provider call; got %v", fp.lastFormBody)
	}
}

// When the refresh call fails, GetOAuthToken falls back to the stored
// token (best-effort) rather than hard-failing the lookup.
func TestOAuth_GetToken_RefreshFailureFallsBackToStored(t *testing.T) {
	reg, fp := refreshHarness(t)
	past := time.Now().UTC().Add(-time.Minute)
	seedToken(t, reg, &StoredOAuthToken{
		AccessToken:  "old-access",
		RefreshToken: "the-refresh",
		ExpiresAt:    &past,
		ObtainedAt:   past,
	})
	fp.tokenStatus = 400
	fp.tokenBody = `{"error":"invalid_grant"}`

	got := getToken(t, reg) // must not error
	if got.AccessToken != "old-access" {
		t.Errorf("access_token = %q, want the stored old-access on refresh failure", got.AccessToken)
	}
}

// A refresh the provider definitively rejects means the GRANT is dead — the
// user revoked access, changed their password, or it simply expired. That is
// the only moment the daemon knows, and it is what lets the Apps page offer
// "reconnect this account" instead of showing it as healthy while every run
// comes back 401.
func TestOAuth_DeadGrantIsRecordedForReconnect(t *testing.T) {
	reg, fp := refreshHarness(t)
	past := time.Now().UTC().Add(-time.Minute)
	seedToken(t, reg, &StoredOAuthToken{
		AccessToken:  "old-access",
		RefreshToken: "revoked",
		ExpiresAt:    &past,
		ObtainedAt:   past,
	})
	ctx := core.WithTenant(t.Context(), "acme")

	// Storing a token clears any previous marker, so we start clean.
	if got := reg.ReconnectNeeded(ctx, "acme", "test", []string{"main"}); len(got) != 0 {
		t.Fatalf("a freshly stored token should not need reconnecting: %v", got)
	}

	fp.tokenStatus = 400
	fp.tokenBody = `{"error":"invalid_grant","error_description":"Token has been expired or revoked."}`
	// The lookup still hands back the stored token — the API call is what
	// surfaces the authoritative 401 — but the account is now flagged.
	if _, err := reg.GetOAuthToken(ctx, "test", "main"); err != nil {
		t.Fatalf("GetOAuthToken should degrade, not fail: %v", err)
	}
	dead := reg.ReconnectNeeded(ctx, "acme", "test", []string{"main"})
	if len(dead) != 1 || dead[0] != "main" {
		t.Fatalf("the dead account was not flagged: %v", dead)
	}
	// Another account of the same provider is unaffected.
	if got := reg.ReconnectNeeded(ctx, "acme", "test", []string{"other"}); len(got) != 0 {
		t.Errorf("an unrelated account was flagged: %v", got)
	}

	// Reconnecting is the fix: storing a working token forgets the flag.
	fp.tokenStatus = 200
	if _, err := reg.store(ctx, "acme", "test", "main", &StoredOAuthToken{
		AccessToken: "fresh", RefreshToken: "good", ObtainedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("store: %v", err)
	}
	if got := reg.ReconnectNeeded(ctx, "acme", "test", []string{"main"}); len(got) != 0 {
		t.Fatalf("reconnecting should clear the flag, still: %v", got)
	}
}

// A refresh that WORKS clears a previous flag too — the account healed
// itself (a transient provider outage, say) and shouldn't keep nagging.
func TestOAuth_SuccessfulRefreshClearsTheFlag(t *testing.T) {
	reg, fp := refreshHarness(t)
	past := time.Now().UTC().Add(-time.Minute)
	seedToken(t, reg, &StoredOAuthToken{
		AccessToken: "old", RefreshToken: "r", ExpiresAt: &past, ObtainedAt: past,
	})
	ctx := core.WithTenant(t.Context(), "acme")

	fp.tokenStatus = 500
	fp.tokenBody = `{"error":"server_error"}`
	_, _ = reg.GetOAuthToken(ctx, "test", "main")
	if got := reg.ReconnectNeeded(ctx, "acme", "test", []string{"main"}); len(got) != 1 {
		t.Fatalf("a failed refresh should flag the account: %v", got)
	}

	fp.tokenStatus = 200
	fp.tokenBody = `{"access_token":"new","token_type":"Bearer","expires_in":3600}`
	if _, err := reg.GetOAuthToken(ctx, "test", "main"); err != nil {
		t.Fatalf("GetOAuthToken: %v", err)
	}
	if got := reg.ReconnectNeeded(ctx, "acme", "test", []string{"main"}); len(got) != 0 {
		t.Errorf("a working refresh should clear the flag: %v", got)
	}
}

// The Apps page must not have to wait for a scheduled run to discover a dead
// grant — that is exactly when someone is standing on the page wondering why
// their flow broke. Listing accounts refreshes any whose token has already
// expired, which is the same work the next run would do.
func TestOAuth_RefreshStaleAccounts_FindsDeadGrantWithoutARun(t *testing.T) {
	reg, fp := refreshHarness(t)
	past := time.Now().UTC().Add(-time.Minute)
	seedToken(t, reg, &StoredOAuthToken{
		AccessToken: "old", RefreshToken: "revoked", ExpiresAt: &past, ObtainedAt: past,
	})
	ctx := core.WithTenant(t.Context(), "acme")

	fp.tokenStatus = 400
	fp.tokenBody = `{"error":"invalid_grant"}`
	reg.RefreshStaleAccounts(ctx, "acme", "test", []string{"main"})

	if got := reg.ReconnectNeeded(ctx, "acme", "test", []string{"main"}); len(got) != 1 {
		t.Fatalf("listing should have discovered the dead grant, got %v", got)
	}
}

// An account whose token is still valid costs nothing: no refresh call, and
// nothing to report.
func TestOAuth_RefreshStaleAccounts_LeavesHealthyTokensAlone(t *testing.T) {
	reg, fp := refreshHarness(t)
	future := time.Now().UTC().Add(time.Hour)
	seedToken(t, reg, &StoredOAuthToken{
		AccessToken: "good", RefreshToken: "r", ExpiresAt: &future, ObtainedAt: time.Now().UTC(),
	})
	ctx := core.WithTenant(t.Context(), "acme")

	fp.lastFormBody = nil
	reg.RefreshStaleAccounts(ctx, "acme", "test", []string{"main"})
	if fp.lastFormBody != nil {
		t.Errorf("a valid token should not be refreshed on a page load: %v", fp.lastFormBody)
	}
	if got := reg.ReconnectNeeded(ctx, "acme", "test", []string{"main"}); len(got) != 0 {
		t.Errorf("a healthy account was flagged: %v", got)
	}
}
