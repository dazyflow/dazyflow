package daemon

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"git.sr.ht/~klahr/dazyflow/auth"
	"git.sr.ht/~klahr/dazyflow/core"
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
// Slack / etc. — this flow is for *signing in to Dazyflow itself*.

const (
	googleAuthURL     = "https://accounts.google.com/o/oauth2/v2/auth"
	googleTokenURL    = "https://oauth2.googleapis.com/token"
	googleUserinfoURL = "https://www.googleapis.com/oauth2/v3/userinfo"
	googleScopes      = "openid email profile"
	// googleOIDCIssuer is the issuer Google asserts in its ID tokens. The
	// scheme-less form "accounts.google.com" also appears in the wild, but
	// go-oidc discovery uses the canonical https form; we accept both when
	// checking the iss claim below.
	googleOIDCIssuer = "https://accounts.google.com"
)

// googleVerifierCache memoizes one OIDC verifier per client_id. Each
// verifier does OIDC discovery + JWKS fetch once and then refreshes keys in
// the background, so we must not rebuild one per callback. Keyed by
// client_id because the audience check is client-id-specific.
var googleVerifierCache = struct {
	mu sync.Mutex
	m  map[string]auth.IDTokenVerifier
}{m: map[string]auth.IDTokenVerifier{}}

// googleIDTokenVerifier returns a cached (or freshly built) OIDC verifier
// that validates a Google ID token's signature against Google's JWKS and
// asserts iss=accounts.google.com, aud=clientID, and exp. Reuses the
// repo's NewOIDCVerifier (Google is standard OIDC).
func googleIDTokenVerifier(ctx context.Context, clientID string) (auth.IDTokenVerifier, error) {
	googleVerifierCache.mu.Lock()
	defer googleVerifierCache.mu.Unlock()
	if v, ok := googleVerifierCache.m[clientID]; ok {
		return v, nil
	}
	v, err := auth.NewOIDCVerifier(ctx, auth.OIDCConfig{
		Issuer:   googleOIDCIssuer,
		Audience: clientID,
	})
	if err != nil {
		return nil, err
	}
	googleVerifierCache.m[clientID] = v
	return v, nil
}

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
	// Host is the host the browser used to start the flow (e.g.
	// "acme.dazyflow.app"). Captured only when WildcardDomain is set and
	// the host is the apex or one of its subdomains; empty otherwise.
	// Google always redirects back to the apex callback (the single
	// registered redirect_uri), so when Host names a different host the
	// callback hands the session off to it via a one-time token rather
	// than setting a cookie on the apex (Option B: host-only cookies).
	Host string
	// Test marks an admin-initiated "Test sign-in" round-trip. When
	// true, errors after state-consume redirect to ReturnTo with a
	// ?test_error=<code> query param instead of dumping JSON, so the
	// admin SSO page can render a friendly diagnosis. Real (member-
	// initiated) sign-in failures keep the existing JSON behavior.
	Test bool
}

const googleSignInStateTTL = 10 * time.Minute

func mintGoogleState(tenant, returnTo, host string, test bool) (string, error) {
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
		Host:     host,
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
	if !safeReturnPath(returnTo) {
		returnTo = "/"
	}
	test := r.URL.Query().Get("test") == "1"
	// Remember which host started the flow so the apex callback can hand
	// the session back to the right org subdomain. Only trust the host
	// when it's the apex or a subdomain of the configured WildcardDomain;
	// otherwise leave it empty and the callback sets the cookie inline as
	// before (the non-wildcard path).
	startHost := h.signInStartHost(r)
	state, err := mintGoogleState(tenant, returnTo, startHost, test)
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
func (h *HTTPGateway) redirectTestError(rw http.ResponseWriter, r *http.Request, st googleSignInState, code string) {
	target := st.ReturnTo
	if !safeReturnPath(target) {
		target = "/admin/sso"
	}
	http.Redirect(rw, r, h.signInRedirectURL(r, st, appendQuery(target, "test_error", code)), http.StatusFound)
}

// safeReturnPath reports whether p is a safe same-origin redirect
// target: a rooted path ("/foo") that can't be coerced into an
// off-origin URL. We reject "//host" (protocol-relative) and "/\host"
// (which some browsers normalize to a host) so a crafted return_to can't
// turn the sign-in flow into an open redirect.
func safeReturnPath(p string) bool {
	return strings.HasPrefix(p, "/") &&
		!strings.HasPrefix(p, "//") &&
		!strings.HasPrefix(p, "/\\")
}

// signInStartHost returns the host the browser used to begin a sign-in
// flow, but only when per-org subdomains are enabled and the host is the
// wildcard apex or one of its subdomains. Empty means "don't track a
// host" — the callback then behaves exactly as the pre-subdomain code
// (set the cookie inline on whatever host the callback ran on).
func (h *HTTPGateway) signInStartHost(r *http.Request) string {
	if h.WildcardDomain == "" {
		return ""
	}
	bare := strings.ToLower(bareHost(r.Host))
	if bare == h.WildcardDomain || hostIsSubdomainOf(bare, h.WildcardDomain) {
		return r.Host
	}
	return ""
}

// signInRedirectURL turns a same-origin path into an absolute URL on
// st.Host when the flow started on a different host than the one now
// handling the request (the apex callback bouncing back to an org
// subdomain). When the hosts match, or no host was tracked, it returns
// the path unchanged so the redirect stays relative.
func (h *HTTPGateway) signInRedirectURL(r *http.Request, st googleSignInState, pathQuery string) string {
	if st.Host == "" || sameHost(st.Host, r.Host) {
		return pathQuery
	}
	scheme := "https"
	if !h.requestIsHTTPS(r) {
		scheme = "http"
	}
	return scheme + "://" + st.Host + pathQuery
}

// bareHost strips an optional :port from a Host header value.
func bareHost(h string) string {
	if host, _, err := net.SplitHostPort(h); err == nil {
		return host
	}
	return h
}

// sameHost compares two Host values ignoring port and case.
func sameHost(a, b string) bool {
	return strings.EqualFold(bareHost(a), bareHost(b))
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
		h.signInError(rw, r, st, "denied", http.StatusBadRequest, "google: "+errStr)
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
		h.signInError(rw, r, st, "not_configured", http.StatusBadRequest,
			"Google sign-in is no longer configured for that organization")
		return
	}
	tok, idToken, info, err := exchangeGoogleCode(r.Context(), cfg, code, h.googleRedirectURI())
	if err != nil {
		h.signInError(rw, r, st, classifyGoogleError(err), http.StatusBadGateway, err.Error())
		return
	}
	_ = tok // access token isn't stored — sign-in is one-shot
	// Verify the ID token's signature, issuer, audience, and expiry against
	// Google's JWKS. Without this, identity rested entirely on the userinfo
	// response, which the signed token must corroborate before we trust it.
	verifiedEmail, reason, status, msg := h.verifyGoogleIDToken(r.Context(), cfg, idToken, info)
	if reason != "" {
		h.signInError(rw, r, st, reason, status, msg)
		return
	}
	email := verifiedEmail
	if reason, status, msg := validateGoogleClaims(info, email, cfg); reason != "" {
		h.signInError(rw, r, st, reason, status, msg)
		return
	}
	// An admin Test sign-in only verifies the Google side: the code
	// exchange succeeded (so client_id/secret are right), the redirect URI
	// matched, and the returned email is verified (and in-domain if
	// restricted). That's the whole contract the success banner claims.
	// Stop here — a test must not mint a real user, auto-create a
	// membership, or issue a session/cookie as a side effect (the last
	// would also clobber the admin's current session).
	if st.Test {
		target := st.ReturnTo
		if target == "" {
			target = "/"
		}
		http.Redirect(rw, r, appendQuery(target, "test", "ok"), http.StatusFound)
		return
	}
	// Resolve the user (creating one on first SSO sign-in) and the org they
	// land in, then issue a session against that resolved active org.
	user, isNew, ok := h.resolveSignInUser(rw, r, email, st)
	if !ok {
		return
	}
	activeTenant, activeWorkspace, activeRoles, reason, status, msg := h.resolveActiveOrg(r, cfg, user, isNew, email, st)
	if reason != "" {
		h.signInError(rw, r, st, reason, status, msg)
		return
	}
	sessUser := user
	sessUser.Tenant = activeTenant
	sessUser.Workspace = activeWorkspace
	sessUser.Roles = activeRoles
	// Same moderation lockout as the password/TOTP legs.
	if _, locked := h.signInLockout(r.Context(), sessUser); locked {
		h.signInError(rw, r, st, "suspended", http.StatusForbidden, "your account or organization has been suspended")
		return
	}
	sess, token, err := auth.IssueSession(r.Context(), h.Sessions, h.elevatePlatformAdmin(r.Context(), sessUser), h.sessionTTL())
	if err != nil {
		writeJSONError(rw, http.StatusInternalServerError, fmt.Sprintf("issue session: %v", err))
		return
	}
	h.auditAuth(r.Context(), r, sess.Tenant, sess.Subject, "auth.signin", "method=google")
	h.completeSignIn(rw, r, st, sess, token)
}

// signInError ends a Google sign-in attempt. An admin "Test" attempt (st.Test)
// routes to the friendly test-error page keyed by testReason (which the admin
// UI polls for); a real sign-in gets a JSON error with the given HTTP status.
// When state was never consumed, st is the zero value (st.Test == false), so
// early errors take the JSON path.
func (h *HTTPGateway) signInError(rw http.ResponseWriter, r *http.Request, st googleSignInState, testReason string, status int, msg string) {
	if st.Test {
		h.redirectTestError(rw, r, st, testReason)
		return
	}
	writeJSONError(rw, status, msg)
}

// verifyGoogleIDToken cryptographically verifies the ID token returned by
// the token exchange: signature against Google's JWKS, iss == Google,
// aud == the org's configured client_id, and exp not passed (all enforced
// by NewOIDCVerifier). It then derives the authoritative email from the
// signed token and requires it to match the userinfo response, so a tampered
// userinfo body can't substitute a different identity. Returns the verified
// (lower-cased) email, or a (reason, status, msg) triple on failure.
func (h *HTTPGateway) verifyGoogleIDToken(ctx context.Context, cfg auth.OrgAuthConfig, idToken string, info googleUserInfo) (email, reason string, status int, msg string) {
	if idToken == "" {
		return "", "no_id_token", http.StatusBadGateway, "google didn't return an id_token"
	}
	verifier, err := googleIDTokenVerifier(ctx, cfg.GoogleClientID)
	if err != nil {
		return "", "verifier_init_failed", http.StatusBadGateway,
			"could not initialize Google token verification: " + err.Error()
	}
	claims, err := verifier.Verify(ctx, idToken)
	if err != nil {
		return "", "id_token_invalid", http.StatusForbidden,
			"google id_token failed verification: " + err.Error()
	}
	// Pull the email out of the signed claims (go-oidc surfaces extra
	// claims via Extras). This is the trusted identity; userinfo is only a
	// corroborating display source.
	signedEmail := ""
	if claims.Extras != nil {
		if e, ok := claims.Extras["email"].(string); ok {
			signedEmail = strings.ToLower(strings.TrimSpace(e))
		}
	}
	if signedEmail == "" {
		return "", "no_email", http.StatusBadGateway, "google id_token carried no email claim"
	}
	uiEmail := strings.ToLower(strings.TrimSpace(info.Email))
	if uiEmail != "" && uiEmail != signedEmail {
		return "", "email_mismatch", http.StatusForbidden,
			"google id_token email does not match the userinfo email"
	}
	return signedEmail, "", 0, ""
}

// validateGoogleClaims enforces the three sign-in preconditions on Google's
// returned profile: a non-empty email, a verified email, and (when the org
// restricts to a Workspace domain) a matching hd= claim. It returns the
// test-error reason, HTTP status, and user-facing message for the first
// failure, or ("", 0, "") when the claims pass.
func validateGoogleClaims(info googleUserInfo, email string, cfg auth.OrgAuthConfig) (reason string, status int, msg string) {
	if email == "" {
		return "no_email", http.StatusBadGateway, "google didn't return an email"
	}
	if !info.EmailVerified {
		return "not_verified", http.StatusForbidden, "google didn't verify this email"
	}
	if cfg.GoogleWorkspaceDomain != "" && !strings.EqualFold(info.HD, cfg.GoogleWorkspaceDomain) {
		return "domain_mismatch", http.StatusForbidden,
			fmt.Sprintf("only %s users can sign into this organization via Google", cfg.GoogleWorkspaceDomain)
	}
	return "", 0, ""
}

// resolveSignInUser looks up the user by email, creating a User record (and
// seeding a default org profile) on first SSO sign-in. ok is false when a
// create failed and an error response has already been written — the caller
// must then return without further output.
func (h *HTTPGateway) resolveSignInUser(rw http.ResponseWriter, r *http.Request, email string, st googleSignInState) (user auth.User, isNew, ok bool) {
	user, err := h.Users.GetByEmail(r.Context(), email)
	isNew = err != nil
	if !isNew {
		return user, false, true
	}
	user = auth.User{
		Email:     email,
		Subject:   email,
		Tenant:    st.Tenant,
		Workspace: "main",
		Roles:     defaultSignupRoles(),
		CreatedAt: time.Now().UTC(),
	}
	if err := h.Users.PutUser(r.Context(), user); err != nil {
		writeJSONError(rw, http.StatusInternalServerError, fmt.Sprintf("create user: %v", err))
		return auth.User{}, false, false
	}
	// Seed a sensible org display name for the brand-new home org — SSO
	// sign-ins typically come from a Workspace domain, so the default
	// ("Acme" from acme.com) lands close to what the owner would have picked.
	if h.Profiles != nil {
		if name := auth.DefaultOrgDisplayName(email); name != "" {
			_ = h.Profiles.PutOrgProfile(r.Context(), auth.OrgProfile{
				Tenant:      user.Tenant,
				DisplayName: name,
				UpdatedAt:   time.Now().UTC(),
			})
		}
	}
	return user, true, true
}

// resolveActiveOrg picks the (tenant, workspace, roles) the session lands in.
// An existing user signing into a different org than their home tenant uses
// an existing Membership in that org if they have one; otherwise we may
// auto-create one — but ONLY when the org restricts SSO to a Workspace
// domain the user matches (the admin who set the domain has authorized
// everyone in it), OR the user has a pending invitation to the org. Without
// either signal, auto-enrolling any Google account that happens to know the
// org's tenant id is far too broad, so we reject with a clear message.
// Returns a non-empty reason/status/msg when the sign-in must be refused.
func (h *HTTPGateway) resolveActiveOrg(r *http.Request, cfg auth.OrgAuthConfig, user auth.User, isNew bool, email string, st googleSignInState) (tenant, workspace string, roles []core.Role, reason string, status int, msg string) {
	if isNew || user.Tenant == st.Tenant || h.Memberships == nil {
		return user.Tenant, user.Workspace, user.Roles, "", 0, ""
	}
	if m, err := h.Memberships.GetMembership(r.Context(), email, st.Tenant); err == nil {
		return m.Tenant, m.Workspace, m.Roles, "", 0, ""
	}
	// No existing membership: only auto-enroll when there's an authorizing
	// signal. A matching Workspace-domain restriction means the admin opted
	// the whole domain in; a pending invitation is an explicit per-user grant.
	//
	// The roles granted depend on WHICH signal authorized the join — and
	// must NOT be defaultSignupRoles(), which carries tenant_owner /
	// PermOrganizationAdmin (correct for a brand-new home org, a privilege
	// escalation when joining an EXISTING org).
	domainAuthorized := cfg.GoogleWorkspaceDomain != "" &&
		strings.EqualFold(cfg.GoogleWorkspaceDomain, emailDomain(email))
	inv, hasInvite := h.pendingInvitation(r.Context(), email, st.Tenant)
	if !domainAuthorized && !hasInvite {
		return "", "", nil, "not_invited", http.StatusForbidden,
			"your account isn't a member of this organization — ask an admin to invite you"
	}
	// Seat gate: an SSO join consumes a seat just like accepting an invitation,
	// so enforce the org's member cap here too — otherwise a Workspace-domain
	// org could grow past max_members through SSO, bypassing acceptInvitation's
	// gate. Free-tier only; pro/comped/trial resolve to no cap.
	if exceeded, limit := h.seatQuotaExceeded(r.Context(), st.Tenant); exceeded {
		return "", "", nil, "org_full", http.StatusPaymentRequired,
			fmt.Sprintf("this organization has reached its %d-member limit — ask an admin to upgrade", limit)
	}
	// Domain-authorized joiners get a minimal default role (editor — no org
	// administration); an invitation carries its own scoped roles, which we
	// honor exactly (mirrors acceptInvitation in orgs.go).
	workspace = "main"
	roles = []core.Role{core.TeamRoleEditor()}
	if hasInvite {
		workspace = inv.Workspace
		roles = inv.Roles
	}
	m := auth.Membership{
		UserEmail: email,
		Tenant:    st.Tenant,
		Workspace: workspace,
		Roles:     roles,
		InvitedBy: inv.InvitedBy,
		CreatedAt: time.Now().UTC(),
	}
	if err := h.Memberships.PutMembership(r.Context(), m); err != nil {
		// Don't silently fall through to the user's home org — that would
		// drop them into the wrong tenant with their home-org roles. Fail loud.
		return "", "", nil, "membership_create_failed", http.StatusInternalServerError,
			fmt.Sprintf("could not enroll you in this organization: %v", err)
	}
	// An invitation is single-use: mark it accepted so it can't be reused
	// (best-effort — the membership already exists; mirrors acceptInvitation).
	if hasInvite && h.Invitations != nil {
		_ = h.Invitations.MarkAccepted(r.Context(), inv.Token, m.CreatedAt)
	}
	return m.Tenant, m.Workspace, m.Roles, "", 0, ""
}

// emailDomain returns the lower-cased domain part of an email, or "" when
// the address has no "@".
func emailDomain(email string) string {
	at := strings.LastIndex(email, "@")
	if at < 0 || at == len(email)-1 {
		return ""
	}
	return strings.ToLower(email[at+1:])
}

// pendingInvitation returns the still-acceptable invitation addressed to
// email in tenant, and ok=true when one exists. The returned invitation's
// scoped Roles/Workspace are what the joiner gets — never the broad
// signup defaults. Best-effort: a missing/erroring store reads as "no
// invite" (ok=false), which (absent a domain match) correctly blocks
// auto-enroll.
func (h *HTTPGateway) pendingInvitation(ctx context.Context, email, tenant string) (inv auth.Invitation, ok bool) {
	if h.Invitations == nil {
		return auth.Invitation{}, false
	}
	invs, err := h.Invitations.ListByTenant(ctx, tenant)
	if err != nil {
		return auth.Invitation{}, false
	}
	now := time.Now().UTC()
	for _, candidate := range invs {
		if strings.EqualFold(strings.TrimSpace(candidate.Email), strings.TrimSpace(email)) && candidate.IsPending(now) {
			return candidate, true
		}
	}
	return auth.Invitation{}, false
}

// completeSignIn delivers the freshly-issued session to the browser and lands
// the user at their return path. When the sign-in started on a different host
// than this apex callback (per-org subdomains), it stashes the session under a
// single-use handoff token and bounces to that host's /auth/handoff so the
// cookie is scoped to one subdomain; otherwise it sets the cookie inline.
func (h *HTTPGateway) completeSignIn(rw http.ResponseWriter, r *http.Request, st googleSignInState, sess auth.Session, token string) {
	target := st.ReturnTo
	if !safeReturnPath(target) {
		target = "/"
	}
	if st.Host != "" && !sameHost(st.Host, r.Host) {
		code, err := mintHandoff(token, sess.ExpiresAt)
		if err != nil {
			writeJSONError(rw, http.StatusInternalServerError, fmt.Sprintf("sign-in handoff: %v", err))
			return
		}
		scheme := "https"
		if !h.requestIsHTTPS(r) {
			scheme = "http"
		}
		dest := scheme + "://" + st.Host + "/api/v1/auth/handoff?ot=" + url.QueryEscape(code) +
			"&return_to=" + url.QueryEscape(target)
		http.Redirect(rw, r, dest, http.StatusFound)
		return
	}
	// Same host (apex sign-in, or the subdomains feature is off): set the
	// session cookie inline and land the user where the flow started.
	h.setSessionCookie(rw, r, token, sess.ExpiresAt)
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
// followed by a userinfo lookup. Returns the access token, the raw
// id_token (a signed JWT the caller must verify before trusting), and the
// userinfo profile.
func exchangeGoogleCode(ctx context.Context, cfg auth.OrgAuthConfig, code, redirectURI string) (string, string, googleUserInfo, error) {
	form := url.Values{}
	form.Set("client_id", cfg.GoogleClientID)
	form.Set("client_secret", cfg.GoogleClientSecret)
	form.Set("code", code)
	form.Set("redirect_uri", redirectURI)
	form.Set("grant_type", "authorization_code")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, googleTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", "", googleUserInfo{}, fmt.Errorf("build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", "", googleUserInfo{}, fmt.Errorf("token request: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", "", googleUserInfo{}, fmt.Errorf("token exchange %d: %s", resp.StatusCode, body)
	}
	var tok struct {
		AccessToken string `json:"access_token"`
		IDToken     string `json:"id_token"`
		TokenType   string `json:"token_type"`
	}
	if err := json.Unmarshal(body, &tok); err != nil {
		return "", "", googleUserInfo{}, fmt.Errorf("parse token response: %w", err)
	}
	if tok.AccessToken == "" {
		return "", "", googleUserInfo{}, fmt.Errorf("no access_token in response")
	}
	// Fetch userinfo with the access token (less brittle than parsing
	// id_token in-house; Google's userinfo endpoint is the canonical
	// claim set).
	uiReq, err := http.NewRequestWithContext(ctx, http.MethodGet, googleUserinfoURL, nil)
	if err != nil {
		return "", "", googleUserInfo{}, fmt.Errorf("build userinfo request: %w", err)
	}
	uiReq.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	uiResp, err := client.Do(uiReq)
	if err != nil {
		return "", "", googleUserInfo{}, fmt.Errorf("userinfo request: %w", err)
	}
	defer uiResp.Body.Close()
	uiBody, _ := io.ReadAll(io.LimitReader(uiResp.Body, 64*1024))
	if uiResp.StatusCode < 200 || uiResp.StatusCode >= 300 {
		return "", "", googleUserInfo{}, fmt.Errorf("userinfo %d: %s", uiResp.StatusCode, uiBody)
	}
	var info googleUserInfo
	if err := json.Unmarshal(uiBody, &info); err != nil {
		return "", "", googleUserInfo{}, fmt.Errorf("parse userinfo: %w", err)
	}
	return tok.AccessToken, tok.IDToken, info, nil
}

// Compile-time check that defaultSignupRoles still exists where this
// file expects it (avoids breakage from an unrelated rename).
var _ = func() []core.Role { return defaultSignupRoles() }
