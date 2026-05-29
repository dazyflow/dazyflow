package daemon

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"git.sr.ht/~klahr/hazy-flow/auth"
	"git.sr.ht/~klahr/hazy-flow/core"
)

// Google sign-in — per-org SSO using the org's own OAuth client id +
// secret (stored in OrgAuth). Flow:
//
//   GET  /api/v1/auth/google/start?tenant=<org>    → 302 to Google
//   GET  /api/v1/auth/google/callback?code=…       → exchange + issue session
//
// The state token binds the in-flight flow to a target org and expires
// after a short window. We don't reuse the integrations OAuth registry
// here because that one stores per-tenant tokens for connecting Gmail /
// Slack / etc. — this flow is for *signing in to Hazy Flow itself*.

const (
	googleAuthURL     = "https://accounts.google.com/o/oauth2/v2/auth"
	googleTokenURL    = "https://oauth2.googleapis.com/token"
	googleUserinfoURL = "https://www.googleapis.com/oauth2/v3/userinfo"
	googleScopes      = "openid email profile"
)

// googleSignInStates pairs a random state value with the tenant the
// user is signing into. Module-scoped because the lifecycle is shorter
// than the gateway's; expired entries get reaped on each new mint.
var googleSignInStates = struct {
	mu    sync.Mutex
	items map[string]googleSignInState
}{items: map[string]googleSignInState{}}

type googleSignInState struct {
	Tenant   string
	Created  time.Time
	ReturnTo string
	// Test marks an admin-initiated "Test sign-in" round-trip. When
	// true, errors after state-consume redirect to ReturnTo with a
	// ?test_error=<code> query param instead of dumping JSON, so the
	// admin SSO page can render a friendly diagnosis. Real (member-
	// initiated) sign-in failures keep the existing JSON behavior.
	Test bool
}

const googleSignInStateTTL = 10 * time.Minute

func mintGoogleState(tenant, returnTo string, test bool) (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	s := hex.EncodeToString(b)
	googleSignInStates.mu.Lock()
	defer googleSignInStates.mu.Unlock()
	now := time.Now()
	for k, v := range googleSignInStates.items {
		if now.Sub(v.Created) > googleSignInStateTTL {
			delete(googleSignInStates.items, k)
		}
	}
	googleSignInStates.items[s] = googleSignInState{
		Tenant:   tenant,
		Created:  now,
		ReturnTo: returnTo,
		Test:     test,
	}
	return s, nil
}

func consumeGoogleState(state string) (googleSignInState, bool) {
	googleSignInStates.mu.Lock()
	defer googleSignInStates.mu.Unlock()
	v, ok := googleSignInStates.items[state]
	if !ok {
		return googleSignInState{}, false
	}
	delete(googleSignInStates.items, state)
	if time.Since(v.Created) > googleSignInStateTTL {
		return googleSignInState{}, false
	}
	return v, true
}

// googleSignInStart is unauthenticated by design — the user is signing
// IN, so we can't expect a credential yet. Required query: tenant.
// Optional: return_to (a path the callback redirects to on success;
// defaults to "/").
func (h *HTTPGateway) googleSignInStart(rw http.ResponseWriter, r *http.Request) {
	if h.OrgAuth == nil {
		writeJSONError(rw, http.StatusNotImplemented, "org SSO not configured")
		return
	}
	tenant := r.URL.Query().Get("tenant")
	if tenant == "" {
		writeJSONError(rw, http.StatusBadRequest, "tenant query param required")
		return
	}
	cfg, err := h.OrgAuth.GetOrgAuth(r.Context(), tenant)
	if err != nil || !cfg.GoogleEnabled() {
		writeJSONError(rw, http.StatusNotFound, "Google sign-in isn't configured for that organization")
		return
	}
	returnTo := r.URL.Query().Get("return_to")
	if returnTo == "" || !strings.HasPrefix(returnTo, "/") {
		returnTo = "/"
	}
	test := r.URL.Query().Get("test") == "1"
	state, err := mintGoogleState(tenant, returnTo, test)
	if err != nil {
		writeJSONError(rw, http.StatusInternalServerError, err.Error())
		return
	}
	q := url.Values{}
	q.Set("client_id", cfg.GoogleClientID)
	q.Set("redirect_uri", h.googleRedirectURI())
	q.Set("response_type", "code")
	q.Set("scope", googleScopes)
	q.Set("state", state)
	q.Set("access_type", "online")
	q.Set("prompt", "select_account")
	if cfg.GoogleWorkspaceDomain != "" {
		// hd= hints Google to pre-select the workspace account if the
		// user has more than one signed-in. We re-verify the hd claim
		// after exchange.
		q.Set("hd", cfg.GoogleWorkspaceDomain)
	}
	http.Redirect(rw, r, googleAuthURL+"?"+q.Encode(), http.StatusFound)
}

func (h *HTTPGateway) googleRedirectURI() string {
	base := strings.TrimRight(h.svc.PublicBaseURL, "/")
	return base + "/api/v1/auth/google/callback"
}

// classifyGoogleError maps a token-exchange error string into a short
// code the SSO admin page knows how to render. We substring-match on
// Google's OAuth error codes (which are stable across their API) since
// the wrapping error chain hides the structured fields.
func classifyGoogleError(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "invalid_client"):
		return "invalid_client"
	case strings.Contains(msg, "redirect_uri_mismatch"):
		return "redirect_uri_mismatch"
	case strings.Contains(msg, "invalid_grant"):
		return "invalid_grant"
	case strings.Contains(msg, "unauthorized_client"):
		return "unauthorized_client"
	default:
		return "exchange_failed"
	}
}

// appendQuery adds key=value to a URL string, picking ? or & as needed.
// We don't round-trip through net/url because ReturnTo is a path+query
// (not a full URL) — net/url.Parse handles it but the formatting churn
// isn't worth it for one append.
func appendQuery(base, key, val string) string {
	sep := "?"
	if strings.Contains(base, "?") {
		sep = "&"
	}
	return base + sep + url.QueryEscape(key) + "=" + url.QueryEscape(val)
}

// redirectTestError sends the admin back to the SSO settings page with
// a ?test_error=<code> query param so the page can render a friendly
// banner. Used only when st.Test is true; production sign-in failures
// fall through to writeJSONError as before.
func redirectTestError(rw http.ResponseWriter, r *http.Request, st googleSignInState, code string) {
	target := st.ReturnTo
	if target == "" || !strings.HasPrefix(target, "/") {
		target = "/admin/sso"
	}
	http.Redirect(rw, r, appendQuery(target, "test_error", code), http.StatusFound)
}

// googleSignInCallback handles the redirect back from Google. We
// exchange the code for an id_token + access_token, fetch userinfo,
// verify hd= when the org requires it, look up the user (creating
// them if needed; their tenant will be the org doing SSO if they're
// brand new), and issue a session.
func (h *HTTPGateway) googleSignInCallback(rw http.ResponseWriter, r *http.Request) {
	if h.OrgAuth == nil || h.Users == nil || h.Sessions == nil {
		writeJSONError(rw, http.StatusNotImplemented, "google sign-in not configured")
		return
	}
	// Consume state up-front so that even early errors (e.g. user
	// declined consent on Google, which arrives as ?error=access_denied
	// alongside the state) can route to the friendly test-error page.
	state := r.URL.Query().Get("state")
	var st googleSignInState
	hasState := false
	if state != "" {
		st, hasState = consumeGoogleState(state)
	}
	if errStr := r.URL.Query().Get("error"); errStr != "" {
		if hasState && st.Test {
			redirectTestError(rw, r, st, "denied")
			return
		}
		writeJSONError(rw, http.StatusBadRequest, "google: "+errStr)
		return
	}
	code := r.URL.Query().Get("code")
	if !hasState {
		writeJSONError(rw, http.StatusBadRequest, "invalid or expired state")
		return
	}
	if code == "" {
		writeJSONError(rw, http.StatusBadRequest, "missing code")
		return
	}
	cfg, err := h.OrgAuth.GetOrgAuth(r.Context(), st.Tenant)
	if err != nil || !cfg.GoogleEnabled() {
		if st.Test {
			redirectTestError(rw, r, st, "not_configured")
			return
		}
		writeJSONError(rw, http.StatusBadRequest, "Google sign-in is no longer configured for that organization")
		return
	}
	tok, info, err := exchangeGoogleCode(r.Context(), cfg, code, h.googleRedirectURI())
	if err != nil {
		if st.Test {
			redirectTestError(rw, r, st, classifyGoogleError(err))
			return
		}
		writeJSONError(rw, http.StatusBadGateway, err.Error())
		return
	}
	_ = tok // access token isn't stored — sign-in is one-shot
	email := strings.ToLower(strings.TrimSpace(info.Email))
	if email == "" {
		if st.Test {
			redirectTestError(rw, r, st, "no_email")
			return
		}
		writeJSONError(rw, http.StatusBadGateway, "google didn't return an email")
		return
	}
	if !info.EmailVerified {
		if st.Test {
			redirectTestError(rw, r, st, "not_verified")
			return
		}
		writeJSONError(rw, http.StatusForbidden, "google didn't verify this email")
		return
	}
	if cfg.GoogleWorkspaceDomain != "" {
		if !strings.EqualFold(info.HD, cfg.GoogleWorkspaceDomain) {
			if st.Test {
				redirectTestError(rw, r, st, "domain_mismatch")
				return
			}
			writeJSONError(rw, http.StatusForbidden,
				fmt.Sprintf("only %s users can sign into this organization via Google", cfg.GoogleWorkspaceDomain))
			return
		}
	}
	// Resolve which org the user lands in:
	//   - If they already have a User record, use their home tenant.
	//   - If they have a Membership in this org, switch their session to it.
	//   - If neither, create a Membership in this org and use it as the
	//     active tenant. Their User record gets created with this org as
	//     their home so the next sign-in is consistent.
	user, err := h.Users.GetByEmail(r.Context(), email)
	isNew := err != nil
	if isNew {
		user = auth.User{
			Email:     email,
			Subject:   email,
			Tenant:    st.Tenant,
			Workspace: "main",
			Roles:     defaultSignupRoles(),
			CreatedAt: time.Now().UTC(),
		}
		if err := h.Users.PutUser(r.Context(), user); err != nil {
			if st.Test {
				redirectTestError(rw, r, st, "internal")
				return
			}
			writeJSONError(rw, http.StatusInternalServerError, fmt.Sprintf("create user: %v", err))
			return
		}
		// Seed a sensible org display name for the brand-new home org —
		// SSO sign-ins typically come from a Workspace domain, so the
		// default ("Acme" from acme.com) lands close to what the owner
		// would have picked.
		if h.Profiles != nil {
			if name := auth.DefaultOrgDisplayName(email); name != "" {
				_ = h.Profiles.PutOrgProfile(r.Context(), auth.OrgProfile{
					Tenant:      user.Tenant,
					DisplayName: name,
					UpdatedAt:   time.Now().UTC(),
				})
			}
		}
	}
	activeTenant := user.Tenant
	activeWorkspace := user.Workspace
	activeRoles := user.Roles
	if !isNew && user.Tenant != st.Tenant && h.Memberships != nil {
		if m, err := h.Memberships.GetMembership(r.Context(), email, st.Tenant); err == nil {
			activeTenant = m.Tenant
			activeWorkspace = m.Workspace
			activeRoles = m.Roles
		} else {
			// Auto-create a membership: the workspace admin who turned
			// SSO on for the domain has implicitly authorized everyone
			// in that domain to join. We give the basic editor role.
			m = auth.Membership{
				UserEmail: email,
				Tenant:    st.Tenant,
				Workspace: "main",
				Roles:     defaultSignupRoles(),
				CreatedAt: time.Now().UTC(),
			}
			if err := h.Memberships.PutMembership(r.Context(), m); err == nil {
				activeTenant = m.Tenant
				activeWorkspace = m.Workspace
				activeRoles = m.Roles
			}
		}
	}
	// Issue session against the resolved active org.
	ttl := h.SessionTTL
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	sessUser := user
	sessUser.Tenant = activeTenant
	sessUser.Workspace = activeWorkspace
	sessUser.Roles = activeRoles
	sess, token, err := auth.IssueSession(r.Context(), h.Sessions, sessUser, ttl)
	if err != nil {
		if st.Test {
			redirectTestError(rw, r, st, "internal")
			return
		}
		writeJSONError(rw, http.StatusInternalServerError, fmt.Sprintf("issue session: %v", err))
		return
	}
	http.SetCookie(rw, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		Expires:  sess.ExpiresAt,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   h.requestIsHTTPS(r),
	})
	// Land the user wherever the sign-in started — defaults to /.
	// For an admin Test sign-in, decorate with ?test=ok so the SSO
	// page can render a success banner (the page strips the param
	// after surfacing it, so a refresh doesn't keep claiming success).
	target := st.ReturnTo
	if target == "" {
		target = "/"
	}
	if st.Test {
		target = appendQuery(target, "test", "ok")
	}
	http.Redirect(rw, r, target, http.StatusFound)
}

// googleUserInfo is the subset of Google's userinfo endpoint we care
// about. The hd= claim ("hosted domain") identifies Google Workspace
// accounts; personal Gmail accounts return empty.
type googleUserInfo struct {
	Email         string `json:"email"`
	EmailVerified bool   `json:"email_verified"`
	HD            string `json:"hd"`
	Name          string `json:"name"`
	Sub           string `json:"sub"`
}

// exchangeGoogleCode is the standard OAuth code → token round trip,
// followed by a userinfo lookup. Returns the access token + verified
// user info.
func exchangeGoogleCode(ctx context.Context, cfg auth.OrgAuthConfig, code, redirectURI string) (string, googleUserInfo, error) {
	form := url.Values{}
	form.Set("client_id", cfg.GoogleClientID)
	form.Set("client_secret", cfg.GoogleClientSecret)
	form.Set("code", code)
	form.Set("redirect_uri", redirectURI)
	form.Set("grant_type", "authorization_code")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, googleTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", googleUserInfo{}, fmt.Errorf("build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", googleUserInfo{}, fmt.Errorf("token request: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", googleUserInfo{}, fmt.Errorf("token exchange %d: %s", resp.StatusCode, body)
	}
	var tok struct {
		AccessToken string `json:"access_token"`
		IDToken     string `json:"id_token"`
		TokenType   string `json:"token_type"`
	}
	if err := json.Unmarshal(body, &tok); err != nil {
		return "", googleUserInfo{}, fmt.Errorf("parse token response: %w", err)
	}
	if tok.AccessToken == "" {
		return "", googleUserInfo{}, fmt.Errorf("no access_token in response")
	}
	// Fetch userinfo with the access token (less brittle than parsing
	// id_token in-house; Google's userinfo endpoint is the canonical
	// claim set).
	uiReq, err := http.NewRequestWithContext(ctx, http.MethodGet, googleUserinfoURL, nil)
	if err != nil {
		return "", googleUserInfo{}, fmt.Errorf("build userinfo request: %w", err)
	}
	uiReq.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	uiResp, err := client.Do(uiReq)
	if err != nil {
		return "", googleUserInfo{}, fmt.Errorf("userinfo request: %w", err)
	}
	defer uiResp.Body.Close()
	uiBody, _ := io.ReadAll(io.LimitReader(uiResp.Body, 64*1024))
	if uiResp.StatusCode < 200 || uiResp.StatusCode >= 300 {
		return "", googleUserInfo{}, fmt.Errorf("userinfo %d: %s", uiResp.StatusCode, uiBody)
	}
	var info googleUserInfo
	if err := json.Unmarshal(uiBody, &info); err != nil {
		return "", googleUserInfo{}, fmt.Errorf("parse userinfo: %w", err)
	}
	return tok.AccessToken, info, nil
}

// Compile-time check that defaultSignupRoles still exists where this
// file expects it (avoids breakage from an unrelated rename).
var _ = func() []core.Role { return defaultSignupRoles() }