// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
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

	"github.com/dazyflow/dazyflow/auth"
	"github.com/dazyflow/dazyflow/core"
)

// Per-org SSO using the org's own OAuth client, not a deployment-wide one.

const (
	googleAuthURL    = "https://accounts.google.com/o/oauth2/v2/auth"
	googleScopes     = "openid email profile"
	googleOIDCIssuer = "https://accounts.google.com"
)

var (
	googleTokenURL    = "https://oauth2.googleapis.com/token"
	googleUserinfoURL = "https://www.googleapis.com/oauth2/v3/userinfo"
)

// One verifier per client_id: each does discovery and a JWKS fetch.
var googleVerifierCache = struct {
	mu    sync.Mutex
	m     map[string]auth.IDTokenVerifier
	order []string
}{m: map[string]auth.IDTokenVerifier{}}

const googleVerifierCacheMax = 256

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
	googleVerifierCache.order = append(googleVerifierCache.order, clientID)
	for len(googleVerifierCache.order) > googleVerifierCacheMax {
		oldest := googleVerifierCache.order[0]
		googleVerifierCache.order = googleVerifierCache.order[1:]
		delete(googleVerifierCache.m, oldest)
	}
	return v, nil
}

type googleSignInState struct {
	Tenant   string
	Created  time.Time
	ReturnTo string
	// The host the BROWSER used, so the apex callback can hand back to it.
	Host string
	Test bool
	// Ties the flow to the browser that started it: the state token alone only
	// proves SOME browser started one, which is not the same browser.
	Binding string
}

const googleSignInCookie = "dz_signin_state"

func (h *authAPI) setGoogleSignInCookie(rw http.ResponseWriter, binding, startHost string) {
	c := &http.Cookie{
		Name:     googleSignInCookie,
		Value:    binding,
		Path:     "/api/v1/auth/google",
		MaxAge:   int(googleSignInStateTTL / time.Second),
		HttpOnly: true,
		Secure:   strings.HasPrefix(h.svc.PublicBaseURL, "https"),
		SameSite: http.SameSiteLaxMode,
	}
	c.Domain = h.signInCookieDomain(startHost)
	http.SetCookie(rw, c)
}

func (h *authAPI) signInCookieDomain(startHost string) string {
	if h.WildcardDomain == "" || startHost == "" {
		return ""
	}
	// Compared against the HOST, not the full URL: a path would never match.
	if sameHost(startHost, publicBaseHost(h.svc.PublicBaseURL)) {
		return ""
	}
	return h.WildcardDomain
}

func publicBaseHost(base string) string {
	base = strings.TrimSpace(base)
	if base == "" {
		return ""
	}
	if u, err := url.Parse(base); err == nil && u.Host != "" {
		return u.Host
	}
	return base
}

func (h *authAPI) clearGoogleSignInCookie(rw http.ResponseWriter, startHost string) {
	http.SetCookie(rw, &http.Cookie{
		Name:     googleSignInCookie,
		Value:    "",
		Path:     "/api/v1/auth/google",
		Domain:   h.signInCookieDomain(startHost),
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   strings.HasPrefix(h.svc.PublicBaseURL, "https"),
		SameSite: http.SameSiteLaxMode,
	})
}

func signInBindingOK(r *http.Request, st googleSignInState) bool {
	if st.Binding == "" {
		return false
	}
	c, err := r.Cookie(googleSignInCookie)
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(c.Value), []byte(st.Binding)) == 1
}

const googleSignInStateTTL = 10 * time.Minute

func (h *authAPI) mintGoogleState(ctx context.Context, tenant, returnTo, host, binding string, test bool) (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	state := hex.EncodeToString(b)
	st := googleSignInState{
		Tenant:   tenant,
		Created:  time.Now(),
		ReturnTo: returnTo,
		Host:     host,
		Test:     test,
		Binding:  binding,
	}
	if err := putEphemeral(ctx, h.Ephemeral, auth.EphemeralGoogleSignIn, state, st, time.Now().Add(googleSignInStateTTL)); err != nil {
		return "", err
	}
	return state, nil
}

func (h *authAPI) consumeGoogleState(ctx context.Context, state string) (googleSignInState, bool) {
	return consumeEphemeral[googleSignInState](ctx, h.Ephemeral, auth.EphemeralGoogleSignIn, state)
}

// Unauthenticated by design: the user is signing in.
func (h *authAPI) googleSignInStart(rw http.ResponseWriter, r *http.Request) {
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
	// So the apex callback can hand back to the subdomain that started it.
	startHost := h.signInStartHost(r)
	// Recorded on BOTH sides, so the callback can compare them.
	binding, err := newOAuthBinding()
	if err != nil {
		writeJSONError(rw, http.StatusInternalServerError, err.Error())
		return
	}
	state, err := h.mintGoogleState(r.Context(), tenant, returnTo, startHost, binding, test)
	if err != nil {
		writeJSONError(rw, http.StatusInternalServerError, err.Error())
		return
	}
	h.setGoogleSignInCookie(rw, binding, startHost)
	q := url.Values{}
	q.Set("client_id", cfg.GoogleClientID)
	q.Set("redirect_uri", h.googleRedirectURI())
	q.Set("response_type", "code")
	q.Set("scope", googleScopes)
	q.Set("state", state)
	q.Set("access_type", "online")
	q.Set("prompt", "select_account")
	if cfg.GoogleWorkspaceDomain != "" {
		q.Set("hd", cfg.GoogleWorkspaceDomain)
	}
	http.Redirect(rw, r, googleAuthURL+"?"+q.Encode(), http.StatusFound)
}

func (h *authAPI) googleRedirectURI() string {
	base := strings.TrimRight(h.svc.PublicBaseURL, "/")
	return base + "/api/v1/auth/google/callback"
}

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

func appendQuery(base, key, val string) string {
	sep := "?"
	if strings.Contains(base, "?") {
		sep = "&"
	}
	return base + sep + url.QueryEscape(key) + "=" + url.QueryEscape(val)
}

func (h *authAPI) redirectTestError(rw http.ResponseWriter, r *http.Request, st googleSignInState, code string) {
	target := st.ReturnTo
	if !safeReturnPath(target) {
		target = "/admin/sso"
	}
	http.Redirect(rw, r, h.signInRedirectURL(r, st, appendQuery(target, "test_error", code)), http.StatusFound)
}

// Same-origin path only: an absolute URL here would be an open redirect.
func safeReturnPath(p string) bool {
	return strings.HasPrefix(p, "/") &&
		!strings.HasPrefix(p, "//") &&
		!strings.HasPrefix(p, "/\\")
}

func (h *authAPI) signInStartHost(r *http.Request) string {
	if h.WildcardDomain == "" {
		return ""
	}
	bare := strings.ToLower(bareHost(r.Host))
	if bare == h.WildcardDomain || hostIsSubdomainOf(bare, h.WildcardDomain) {
		return r.Host
	}
	return ""
}

func (h *authAPI) signInRedirectURL(r *http.Request, st googleSignInState, pathQuery string) string {
	if st.Host == "" || sameHost(st.Host, r.Host) {
		return pathQuery
	}
	scheme := "https"
	if !h.requestIsHTTPS(r) {
		scheme = "http"
	}
	return scheme + "://" + st.Host + pathQuery
}

func bareHost(h string) string {
	if host, _, err := net.SplitHostPort(h); err == nil {
		return host
	}
	return h
}

func sameHost(a, b string) bool {
	return strings.EqualFold(bareHost(a), bareHost(b))
}

func (h *authAPI) googleSignInCallback(rw http.ResponseWriter, r *http.Request) {
	if h.OrgAuth == nil || h.Users == nil || h.Sessions == nil {
		writeJSONError(rw, http.StatusNotImplemented, "google sign-in not configured")
		return
	}
	state := r.URL.Query().Get("state")
	var st googleSignInState
	hasState := false
	if state != "" {
		st, hasState = h.consumeGoogleState(r.Context(), state)
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
	// The state token proves SOME browser started a flow, not this one.
	if !signInBindingOK(r, st) {
		h.clearGoogleSignInCookie(rw, st.Host)
		h.signInError(rw, r, st, "state_mismatch", http.StatusBadRequest,
			"This sign-in was started in a different browser or has expired. Please sign in again.")
		return
	}
	h.clearGoogleSignInCookie(rw, st.Host)
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
	vc, reason, status, msg := h.verifyGoogleIDToken(r.Context(), cfg, idToken, info)
	if reason != "" {
		h.signInError(rw, r, st, reason, status, msg)
		return
	}
	email := vc.Email
	if reason, status, msg := validateGoogleClaims(vc, cfg); reason != "" {
		h.signInError(rw, r, st, reason, status, msg)
		return
	}
	// Verifies the Google side only; it must never issue a session.
	if st.Test {
		target := st.ReturnTo
		if target == "" {
			target = "/"
		}
		http.Redirect(rw, r, appendQuery(target, "test", "ok"), http.StatusFound)
		return
	}
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
	if _, locked := h.signInLockout(r.Context(), sessUser); locked {
		h.signInError(rw, r, st, "suspended", http.StatusForbidden, "your account or organization has been suspended")
		return
	}
	// A verified Google identity is NOT sufficient on its own: the account must also
	// be a member of the org whose client id was used.
	if sessUser.TOTPEnabled && h.totpConfigured() {
		challenge, cerr := auth.IssueTOTPChallengeWithOrg(
			r.Context(), h.TOTPChallenges, sessUser.Email, activeTenant, activeWorkspace, activeRoles)
		if cerr != nil {
			h.signInError(rw, r, st, "totp", http.StatusInternalServerError, fmt.Sprintf("issue challenge: %v", cerr))
			return
		}
		h.auditAuth(r.Context(), r, activeTenant, sessUser.Email, "auth.mfa_challenge", "method=google")
		h.redirectToTOTP(rw, r, st, challenge)
		return
	}
	sess, token, err := auth.IssueSession(r.Context(), h.Sessions, h.elevateSessionRoles(r.Context(), sessUser), h.sessionTTL())
	if err != nil {
		writeJSONError(rw, http.StatusInternalServerError, fmt.Sprintf("issue session: %v", err))
		return
	}
	h.auditAuth(r.Context(), r, sess.Tenant, sess.Subject, "auth.signin", "method=google")
	h.completeSignIn(rw, r, st, sess, token)
}

func (h *authAPI) redirectToTOTP(rw http.ResponseWriter, r *http.Request, st googleSignInState, challenge string) {
	target := st.ReturnTo
	if !safeReturnPath(target) {
		target = "/"
	}
	dest := "/signin?totp_challenge=" + url.QueryEscape(challenge) + "&return_to=" + url.QueryEscape(target)
	if st.Host != "" && !sameHost(st.Host, r.Host) {
		scheme := "https"
		if !h.requestIsHTTPS(r) {
			scheme = "http"
		}
		dest = scheme + "://" + st.Host + dest
	}
	http.Redirect(rw, r, dest, http.StatusFound)
}

func (h *authAPI) signInError(rw http.ResponseWriter, r *http.Request, st googleSignInState, testReason string, status int, msg string) {
	if st.Test {
		h.redirectTestError(rw, r, st, testReason)
		return
	}
	writeJSONError(rw, status, msg)
}

// Verifies signature, issuer and audience; a decoded token is not a verified one.
func (h *authAPI) verifyGoogleIDToken(ctx context.Context, cfg auth.OrgAuthConfig, idToken string, info googleUserInfo) (claimsOut verifiedGoogleClaims, reason string, status int, msg string) {
	if idToken == "" {
		return verifiedGoogleClaims{}, "no_id_token", http.StatusBadGateway, "google didn't return an id_token"
	}
	verifier, err := googleIDTokenVerifier(ctx, cfg.GoogleClientID)
	if err != nil {
		return verifiedGoogleClaims{}, "verifier_init_failed", http.StatusBadGateway,
			"could not initialize Google token verification: " + err.Error()
	}
	claims, err := verifier.Verify(ctx, idToken)
	if err != nil {
		return verifiedGoogleClaims{}, "id_token_invalid", http.StatusForbidden,
			"google id_token failed verification: " + err.Error()
	}
	vc := verifiedGoogleClaims{}
	if claims.Extras != nil {
		if e, ok := claims.Extras["email"].(string); ok {
			vc.Email = strings.ToLower(strings.TrimSpace(e))
		}
		vc.EmailVerified = googleClaimBool(claims.Extras["email_verified"])
		if hd, ok := claims.Extras["hd"].(string); ok {
			vc.HD = strings.TrimSpace(hd)
		}
	}
	if vc.Email == "" {
		return verifiedGoogleClaims{}, "no_email", http.StatusBadGateway, "google id_token carried no email claim"
	}
	uiEmail := strings.ToLower(strings.TrimSpace(info.Email))
	if uiEmail != "" && uiEmail != vc.Email {
		return verifiedGoogleClaims{}, "email_mismatch", http.StatusForbidden,
			"google id_token email does not match the userinfo email"
	}
	return vc, "", 0, ""
}

type verifiedGoogleClaims struct {
	Email         string
	EmailVerified bool
	HD            string // Google Workspace "hosted domain"
}

func googleClaimBool(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		return strings.EqualFold(strings.TrimSpace(t), "true")
	default:
		return false
	}
}

func validateGoogleClaims(vc verifiedGoogleClaims, cfg auth.OrgAuthConfig) (reason string, status int, msg string) {
	if vc.Email == "" {
		return "no_email", http.StatusBadGateway, "google didn't return an email"
	}
	if !vc.EmailVerified {
		return "not_verified", http.StatusForbidden, "google didn't verify this email"
	}
	if cfg.GoogleWorkspaceDomain != "" && !strings.EqualFold(vc.HD, cfg.GoogleWorkspaceDomain) {
		return "domain_mismatch", http.StatusForbidden,
			fmt.Sprintf("only %s users can sign into this organization via Google", cfg.GoogleWorkspaceDomain)
	}
	return "", 0, ""
}

func (h *authAPI) resolveSignInUser(rw http.ResponseWriter, r *http.Request, email string, st googleSignInState) (user auth.User, isNew, ok bool) {
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

func (h *authAPI) resolveActiveOrg(r *http.Request, cfg auth.OrgAuthConfig, user auth.User, isNew bool, email string, st googleSignInState) (tenant, workspace string, roles []core.Role, reason string, status int, msg string) {
	if isNew || user.Tenant == st.Tenant || h.Memberships == nil {
		return user.Tenant, user.Workspace, user.Roles, "", 0, ""
	}
	if m, err := h.Memberships.GetMembership(r.Context(), email, st.Tenant); err == nil {
		return m.Tenant, m.Workspace, m.Roles, "", 0, ""
	}
	domainAuthorized := cfg.GoogleWorkspaceDomain != "" &&
		strings.EqualFold(cfg.GoogleWorkspaceDomain, emailDomain(email))
	inv, hasInvite := h.pendingInvitation(r.Context(), email, st.Tenant)
	if !domainAuthorized && !hasInvite {
		return "", "", nil, "not_invited", http.StatusForbidden,
			"your account isn't a member of this organization — ask an admin to invite you"
	}
	if exceeded, limit := h.seatQuotaExceeded(r.Context(), st.Tenant); exceeded {
		return "", "", nil, "org_full", http.StatusPaymentRequired,
			fmt.Sprintf("this organization has reached its %d-member limit — ask an admin to upgrade", limit)
	}
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
		return "", "", nil, "membership_create_failed", http.StatusInternalServerError,
			fmt.Sprintf("could not enroll you in this organization: %v", err)
	}
	if hasInvite && h.Invitations != nil {
		_ = h.Invitations.MarkAccepted(r.Context(), inv.Token, m.CreatedAt)
	}
	return m.Tenant, m.Workspace, m.Roles, "", 0, ""
}

func emailDomain(email string) string {
	at := strings.LastIndex(email, "@")
	if at < 0 || at == len(email)-1 {
		return ""
	}
	return strings.ToLower(email[at+1:])
}

func (h *authAPI) pendingInvitation(ctx context.Context, email, tenant string) (inv auth.Invitation, ok bool) {
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

func (h *authAPI) completeSignIn(rw http.ResponseWriter, r *http.Request, st googleSignInState, sess auth.Session, token string) {
	target := st.ReturnTo
	if !safeReturnPath(target) {
		target = "/"
	}
	if st.Host != "" && !sameHost(st.Host, r.Host) {
		code, err := h.mintHandoff(r.Context(), token, sess.ExpiresAt)
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
	h.setSessionCookie(rw, r, token, sess.ExpiresAt)
	http.Redirect(rw, r, target, http.StatusFound)
}

type googleUserInfo struct {
	Email         string `json:"email"`
	EmailVerified bool   `json:"email_verified"`
	HD            string `json:"hd"`
	Name          string `json:"name"`
	Sub           string `json:"sub"`
}

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
