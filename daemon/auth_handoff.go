package daemon

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"sync"
	"time"
)

// One-time sign-in handoff tokens bridge an OAuth/SSO sign-in that
// completed on the apex callback to the org subdomain where the session
// cookie has to live (Option B: host-only cookies, no shared
// parent-domain cookie). The apex callback issues the real session,
// stashes its token here under a single-use code, and 302s the browser
// to "<subdomain>/api/v1/auth/handoff?ot=<code>", which this handler
// turns into a host-only Set-Cookie on the subdomain origin.
//
// The code is single-use and short-lived: it's consumed (deleted) the
// first time it's presented and rejected after handoffTTL. It travels in
// a URL exactly like an OAuth authorization code does, and over the same
// TLS hop; the brief lifetime + single use keep that exposure bounded.
//
// Module-scoped (like googleSignInStates) so its lifecycle is
// independent of the gateway. This shares the SSO flow's existing
// single-instance assumption: the apex callback and the subdomain
// handoff must land on the same dzd process (all *.<domain> hosts route
// to one upstream), which is the case for the supported single-node /
// sticky-routed deployments.
var handoffStore = struct {
	mu    sync.Mutex
	items map[string]handoffEntry
}{items: map[string]handoffEntry{}}

type handoffEntry struct {
	Token     string    // the session token to install as the cookie
	ExpiresAt time.Time // session expiry — becomes the cookie's Expires
	Created   time.Time
}

// handoffTTL bounds how long a one-time code is valid. The redirect from
// the apex callback to the subdomain is immediate, so this only has to
// cover the round-trip plus clock skew; keep it short.
const handoffTTL = 2 * time.Minute

func mintHandoff(token string, expiresAt time.Time) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	code := hex.EncodeToString(b)
	handoffStore.mu.Lock()
	defer handoffStore.mu.Unlock()
	now := time.Now()
	for k, v := range handoffStore.items {
		if now.Sub(v.Created) > handoffTTL {
			delete(handoffStore.items, k)
		}
	}
	handoffStore.items[code] = handoffEntry{
		Token:     token,
		ExpiresAt: expiresAt,
		Created:   now,
	}
	return code, nil
}

func consumeHandoff(code string) (handoffEntry, bool) {
	handoffStore.mu.Lock()
	defer handoffStore.mu.Unlock()
	v, ok := handoffStore.items[code]
	if !ok {
		return handoffEntry{}, false
	}
	delete(handoffStore.items, code)
	if time.Since(v.Created) > handoffTTL {
		return handoffEntry{}, false
	}
	return v, true
}

// authHandoff runs on the org subdomain. It consumes the one-time code
// minted by the apex SSO callback, sets the host-only session cookie on
// this subdomain, and forwards the user to their original destination.
// On a missing/expired/replayed code it routes back to the sign-in page
// rather than erroring, so a stale link (e.g. a back-button replay)
// degrades to "sign in again" instead of a dead end.
func (h *HTTPGateway) authHandoff(rw http.ResponseWriter, r *http.Request) {
	returnTo := r.URL.Query().Get("return_to")
	if !safeReturnPath(returnTo) {
		returnTo = "/"
	}
	code := r.URL.Query().Get("ot")
	entry, ok := consumeHandoff(code)
	if !ok {
		http.Redirect(rw, r, "/signin?error=handoff_expired", http.StatusFound)
		return
	}
	h.setSessionCookie(rw, r, entry.Token, entry.ExpiresAt)
	http.Redirect(rw, r, returnTo, http.StatusFound)
}
