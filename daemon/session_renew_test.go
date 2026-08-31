// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/dazyflow/dazyflow/auth"
	"github.com/dazyflow/dazyflow/core"
)

// sessionHarness wires a session store + the SessionAuthenticator into the
// chain, then issues a session whose ExpiresAt/CreatedAt the caller can
// position relative to the idle window to drive renewal.
func sessionHarness(t *testing.T) (*gatewayHarness, auth.SessionStore) {
	t.Helper()
	h := newGatewayHarness(t)
	store := auth.NewMemSessionStore()
	h.gw.Sessions = store
	h.gw.SessionTTL = 7 * 24 * time.Hour
	h.gw.MaxSessionAge = 30 * 24 * time.Hour
	h.svc.Auth = auth.Chain{
		&auth.APIKeyAuthenticator{Store: h.ks},
		&auth.SessionAuthenticator{Store: store},
	}
	return h, store
}

func issueSessionAt(t *testing.T, store auth.SessionStore, created, expires time.Time) string {
	t.Helper()
	user := auth.User{
		Subject:   "alice",
		Tenant:    "t",
		Workspace: "ws",
		Roles:     []core.Role{{Name: "editor", Permissions: []core.Permission{core.PermGraphRun}}},
	}
	_, token, err := auth.IssueSession(t.Context(), store, user, time.Hour)
	if err != nil {
		t.Fatalf("issue session: %v", err)
	}
	// Reposition the stored record's timestamps to drive the renewal
	// threshold deterministically (IssueSession always uses now).
	key := auth.SessionLookupKey(token)
	sess, err := store.GetSession(t.Context(), key)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	sess.CreatedAt = created
	sess.ExpiresAt = expires
	if err := store.PutSession(t.Context(), sess); err != nil {
		t.Fatalf("put session: %v", err)
	}
	return token
}

func cookieGet(t *testing.T, h *gatewayHarness, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("GET", "/api/v1/me", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	rw := httptest.NewRecorder()
	ServeForTest(h.gw, rw, req)
	return rw
}

func renewalCookie(rw *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range rw.Result().Cookies() {
		if c.Name == sessionCookieName {
			return c
		}
	}
	return nil
}

// TestSessionRenewal_SlidesInSecondHalf: a cookie request made past the
// idle midpoint renews the session — the server-side ExpiresAt advances and
// a fresh Set-Cookie carries the new expiry, so an active user never hits
// the boundary.
func TestSessionRenewal_SlidesInSecondHalf(t *testing.T) {
	h, store := sessionHarness(t)
	now := time.Now()
	// Deep in the second half of the 7d window, well within the 30d cap.
	token := issueSessionAt(t, store, now.Add(-6*24*time.Hour), now.Add(time.Hour))

	rw := cookieGet(t, h, token)
	if rw.Code != http.StatusOK {
		t.Fatalf("whoami: code=%d body=%s", rw.Code, rw.Body.String())
	}

	c := renewalCookie(rw)
	if c == nil {
		t.Fatalf("expected a refreshed session cookie, got none")
	}
	if !c.Expires.After(now.Add(6 * 24 * time.Hour)) {
		t.Fatalf("cookie expiry %v not slid forward ~7d", c.Expires)
	}

	sess, _ := store.GetSession(t.Context(), auth.SessionLookupKey(token))
	if !sess.ExpiresAt.After(now.Add(6 * 24 * time.Hour)) {
		t.Fatalf("stored expiry %v not slid forward", sess.ExpiresAt)
	}
}

// TestSessionRenewal_NoWriteInFirstHalf: a freshly issued session (first
// half of its window) must not be rewritten or re-cookied on every request.
func TestSessionRenewal_NoWriteInFirstHalf(t *testing.T) {
	h, store := sessionHarness(t)
	now := time.Now()
	// Issued moments ago: nearly the full 7d remains → first half.
	token := issueSessionAt(t, store, now, now.Add(7*24*time.Hour))
	before, _ := store.GetSession(t.Context(), auth.SessionLookupKey(token))

	rw := cookieGet(t, h, token)
	if rw.Code != http.StatusOK {
		t.Fatalf("whoami: code=%d", rw.Code)
	}
	if c := renewalCookie(rw); c != nil {
		t.Fatalf("did not expect a renewal cookie in first half, got expiry %v", c.Expires)
	}
	after, _ := store.GetSession(t.Context(), auth.SessionLookupKey(token))
	if !after.ExpiresAt.Equal(before.ExpiresAt) {
		t.Fatalf("expiry changed without renewal: %v -> %v", before.ExpiresAt, after.ExpiresAt)
	}
}

// TestSessionRenewal_CappedAtMaxAge: a continuously-used session past its
// renewal threshold but near the absolute cap is clamped to CreatedAt+MaxAge,
// never beyond — a leaked token can't be kept alive forever by use.
func TestSessionRenewal_CappedAtMaxAge(t *testing.T) {
	h, store := sessionHarness(t)
	now := time.Now()
	// Created ~30d ago so the cap sits just 2h out; current expiry is in
	// the second half so a renewal fires, but is clamped to the cap rather
	// than sliding the full 7d.
	created := now.Add(-30*24*time.Hour + 2*time.Hour)
	token := issueSessionAt(t, store, created, now.Add(30*time.Minute))
	cap := created.Add(30 * 24 * time.Hour)

	rw := cookieGet(t, h, token)
	if rw.Code != http.StatusOK {
		t.Fatalf("whoami: code=%d", rw.Code)
	}
	sess, _ := store.GetSession(t.Context(), auth.SessionLookupKey(token))
	if sess.ExpiresAt.After(cap) {
		t.Fatalf("expiry %v exceeded cap %v", sess.ExpiresAt, cap)
	}
	if !sess.ExpiresAt.Equal(cap) {
		t.Fatalf("expiry %v not clamped to cap %v", sess.ExpiresAt, cap)
	}
}

// TestSessionRenewal_BearerNotCookied: a session token presented as a
// bearer header (not a cookie) authenticates but gets no Set-Cookie — only
// browser cookie sessions need their Expires refreshed.
func TestSessionRenewal_BearerNotCookied(t *testing.T) {
	h, store := sessionHarness(t)
	now := time.Now()
	token := issueSessionAt(t, store, now.Add(-6*24*time.Hour), now.Add(time.Hour))

	req := httptest.NewRequest("GET", "/api/v1/me", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rw := httptest.NewRecorder()
	ServeForTest(h.gw, rw, req)
	if rw.Code != http.StatusOK {
		t.Fatalf("whoami: code=%d body=%s", rw.Code, rw.Body.String())
	}
	if c := renewalCookie(rw); c != nil {
		t.Fatalf("did not expect a Set-Cookie for bearer auth, got %v", c)
	}
}
