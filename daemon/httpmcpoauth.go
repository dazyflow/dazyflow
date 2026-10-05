// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/dazyflow/dazyflow/auth"
	"github.com/dazyflow/dazyflow/core"
)

// The OAuth 2.1 authorization server that lets an MCP client — claude.ai, the
// Claude apps, Claude Code — connect to /mcp as a signed-in user, without
// anyone pasting an API key:
//
//   - discovery: RFC 9728 protected-resource and RFC 8414 server metadata;
//   - RFC 7591 dynamic client registration, stateless: the client_id encodes
//     the client's name and redirect URIs. Registration is open to anyone by
//     design of the protocol, so a stored client would prove nothing a
//     self-describing id does not; the redirect allowlist and PKCE are what
//     actually bind a code to the client that asked for it;
//   - /oauth/authorize parks the request and hands the browser to the SPA's
//     /authorize page, where the user approves it under their session;
//   - /oauth/token redeems the single-use code (PKCE S256, required) or
//     rotates a refresh token, against auth.MCPGrantStore.
//
// Codes and parked requests live in the EphemeralStore, so the second leg of
// the flow can land on any replica.

const (
	ephemeralMCPAuthorize = "mcp_authorize"
	ephemeralMCPCode      = "mcp_code"
	mcpAuthorizeTTL       = 10 * time.Minute
	mcpCodeTTL            = time.Minute
	mcpScope              = "mcp"
	mcpClientIDPrefix     = "dzc_"
	maxMCPClientIDBytes   = 2048
)

// Hosts whose https redirect URIs are accepted out of the box: where claude.ai
// and the Claude apps send the user back after approval.
var defaultMCPRedirectHosts = []string{"claude.ai", "claude.com"}

type mcpClient struct {
	Name         string   `json:"n"`
	RedirectURIs []string `json:"r"`
}

type mcpAuthorizeRequest struct {
	ClientID      string `json:"client_id"`
	ClientName    string `json:"client_name"`
	RedirectURI   string `json:"redirect_uri"`
	CodeChallenge string `json:"code_challenge"`
	State         string `json:"state,omitempty"`
	Scope         string `json:"scope"`
}

type mcpCode struct {
	mcpAuthorizeRequest
	Subject   string      `json:"subject"`
	Tenant    string      `json:"tenant"`
	Workspace string      `json:"workspace"`
	Roles     []core.Role `json:"roles"`
}

type mcpOAuthAPI struct {
	urlBuilder
	logger        *log.Logger
	Ephemeral     auth.EphemeralStore
	Grants        auth.MCPGrantStore
	RedirectHosts []string
}

func (h *HTTPGateway) mcpOAuthAPI() *mcpOAuthAPI {
	return &mcpOAuthAPI{urlBuilder: h.urls(), logger: h.logger, Ephemeral: h.Ephemeral, Grants: h.MCPGrants, RedirectHosts: h.MCPRedirectHosts}
}

// revokeMCPGrants runs wherever a subject's sessions are revoked, so a
// connected MCP client is cut off by the same events that sign the user out.
func (h *HTTPGateway) revokeMCPGrants(ctx context.Context, subject string) {
	if h.MCPGrants == nil || subject == "" {
		return
	}
	if _, err := h.MCPGrants.RevokeSubjectGrants(ctx, subject); err != nil {
		h.logger.Printf("revoke MCP connections for %s: %v", subject, err)
	}
}

func (h *mcpOAuthAPI) mcpOAuthEnabled(rw http.ResponseWriter) bool {
	if h.Grants == nil || h.Ephemeral == nil {
		writeJSONError(rw, http.StatusNotImplemented, "MCP OAuth is not configured on this server")
		return false
	}
	return true
}

func (h *mcpOAuthAPI) mcpProtectedResourceMetadata(rw http.ResponseWriter, r *http.Request) {
	base := h.effectiveBaseURL(r)
	writeJSON(rw, http.StatusOK, map[string]any{
		"resource":                 base + "/mcp",
		"resource_name":            "Dazyflow",
		"authorization_servers":    []string{base},
		"scopes_supported":         []string{mcpScope},
		"bearer_methods_supported": []string{"header"},
	})
}

func (h *mcpOAuthAPI) mcpAuthorizationServerMetadata(rw http.ResponseWriter, r *http.Request) {
	if !h.mcpOAuthEnabled(rw) {
		return
	}
	base := h.effectiveBaseURL(r)
	writeJSON(rw, http.StatusOK, map[string]any{
		"issuer":                                base,
		"authorization_endpoint":                base + "/oauth/authorize",
		"token_endpoint":                        base + "/oauth/token",
		"registration_endpoint":                 base + "/oauth/register",
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code", "refresh_token"},
		"code_challenge_methods_supported":      []string{"S256"},
		"token_endpoint_auth_methods_supported": []string{"none"},
		"scopes_supported":                      []string{mcpScope},
	})
}

func (h *mcpOAuthAPI) mcpRegisterClient(rw http.ResponseWriter, r *http.Request) {
	if !h.mcpOAuthEnabled(rw) {
		return
	}
	var req struct {
		ClientName   string   `json:"client_name"`
		RedirectURIs []string `json:"redirect_uris"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeOAuthError(rw, http.StatusBadRequest, "invalid_client_metadata", "body must be a JSON client registration")
		return
	}
	if len(req.RedirectURIs) == 0 || len(req.RedirectURIs) > 5 {
		writeOAuthError(rw, http.StatusBadRequest, "invalid_redirect_uri", "register between 1 and 5 redirect_uris")
		return
	}
	for _, u := range req.RedirectURIs {
		if !h.mcpRedirectAllowed(u) {
			writeOAuthError(rw, http.StatusBadRequest, "invalid_redirect_uri",
				"redirect_uri "+u+" is not allowed on this server (https on an allowed host, or http on loopback)")
			return
		}
	}
	name := strings.TrimSpace(req.ClientName)
	if name == "" {
		name = "MCP client"
	}
	if len(name) > 100 {
		name = name[:100]
	}
	raw, _ := json.Marshal(mcpClient{Name: name, RedirectURIs: req.RedirectURIs})
	clientID := mcpClientIDPrefix + base64.RawURLEncoding.EncodeToString(raw)
	if len(clientID) > maxMCPClientIDBytes {
		writeOAuthError(rw, http.StatusBadRequest, "invalid_client_metadata", "client metadata too large")
		return
	}
	writeJSON(rw, http.StatusCreated, map[string]any{
		"client_id":                  clientID,
		"client_id_issued_at":        time.Now().Unix(),
		"client_name":                name,
		"redirect_uris":              req.RedirectURIs,
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"token_endpoint_auth_method": "none",
	})
}

func decodeMCPClientID(id string) (mcpClient, bool) {
	rest, ok := strings.CutPrefix(id, mcpClientIDPrefix)
	if !ok || len(id) > maxMCPClientIDBytes {
		return mcpClient{}, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(rest)
	if err != nil {
		return mcpClient{}, false
	}
	var c mcpClient
	if json.Unmarshal(raw, &c) != nil || len(c.RedirectURIs) == 0 {
		return mcpClient{}, false
	}
	return c, true
}

// mcpRedirectAllowed: https on claude.ai, claude.com or an operator-listed
// host, or http on loopback for local clients like Claude Code. Re-checked at
// authorize time too, so a client_id minted before an allowlist change
// cannot outlive it.
func (h *mcpOAuthAPI) mcpRedirectAllowed(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Fragment != "" || u.User != nil || u.Host == "" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	switch u.Scheme {
	case "https":
		return slices.Contains(defaultMCPRedirectHosts, host) || slices.Contains(h.RedirectHosts, host)
	case "http":
		if host == "localhost" {
			return true
		}
		ip := net.ParseIP(host)
		return ip != nil && ip.IsLoopback()
	}
	return false
}

func (h *mcpOAuthAPI) mcpAuthorize(rw http.ResponseWriter, r *http.Request) {
	if !h.mcpOAuthEnabled(rw) {
		return
	}
	q := r.URL.Query()
	client, ok := decodeMCPClientID(q.Get("client_id"))
	redirectURI := q.Get("redirect_uri")
	// Until the redirect URI is proven, errors go to the browser, never to it.
	if !ok {
		writeAPIError(rw, http.StatusBadRequest, "invalid_client", "unknown client_id; register the client first")
		return
	}
	if !slices.Contains(client.RedirectURIs, redirectURI) || !h.mcpRedirectAllowed(redirectURI) {
		writeAPIError(rw, http.StatusBadRequest, "invalid_redirect_uri", "redirect_uri is not registered for this client")
		return
	}
	state := q.Get("state")
	fail := func(code, desc string) {
		http.Redirect(rw, r, withQuery(redirectURI, url.Values{
			"error": {code}, "error_description": {desc}, "state": {state},
		}), http.StatusFound)
	}
	if q.Get("response_type") != "code" {
		fail("unsupported_response_type", "response_type must be code")
		return
	}
	if q.Get("code_challenge_method") != "S256" || len(q.Get("code_challenge")) != 43 {
		fail("invalid_request", "PKCE with code_challenge_method=S256 is required")
		return
	}
	base := h.effectiveBaseURL(r)
	if res := q.Get("resource"); res != "" && strings.TrimRight(res, "/") != base+"/mcp" {
		fail("invalid_target", "resource must be "+base+"/mcp")
		return
	}
	pending := mcpAuthorizeRequest{
		ClientID:      q.Get("client_id"),
		ClientName:    client.Name,
		RedirectURI:   redirectURI,
		CodeChallenge: q.Get("code_challenge"),
		State:         state,
		Scope:         mcpScope,
	}
	id, err := randomToken()
	if err != nil {
		fail("server_error", "could not start authorization")
		return
	}
	payload, _ := json.Marshal(pending)
	if err := h.Ephemeral.Put(r.Context(), ephemeralMCPAuthorize, id, payload, time.Now().Add(mcpAuthorizeTTL)); err != nil {
		h.logger.Printf("mcp oauth: park authorize request: %v", err)
		fail("server_error", "could not start authorization")
		return
	}
	http.Redirect(rw, r, base+"/authorize?request="+url.QueryEscape(id), http.StatusFound)
}

// mcpAuthorizationRequest serves the consent page what it shows: who is
// asking and where approval sends the user.
func (h *mcpOAuthAPI) mcpAuthorizationRequest(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if !h.mcpOAuthEnabled(rw) || !requireSessionCredential(rw, r, "approving an MCP connection") {
		return
	}
	raw, _, err := h.Ephemeral.Get(r.Context(), ephemeralMCPAuthorize, r.PathValue("id"))
	var pending mcpAuthorizeRequest
	if err != nil || json.Unmarshal(raw, &pending) != nil {
		writeAPIError(rw, http.StatusNotFound, "not_found", "this authorization request has expired; start connecting again from the app")
		return
	}
	u, _ := url.Parse(pending.RedirectURI)
	writeJSON(rw, http.StatusOK, map[string]any{
		"client_name":   pending.ClientName,
		"redirect_host": u.Host,
		"scope":         pending.Scope,
		"account":       p.Subject,
		"workspace":     p.Tenant + "/" + p.Workspace,
	})
}

func (h *mcpOAuthAPI) mcpApproveAuthorization(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	h.mcpDecideAuthorization(rw, r, p, true)
}

func (h *mcpOAuthAPI) mcpDenyAuthorization(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	h.mcpDecideAuthorization(rw, r, p, false)
}

// mcpDecideAuthorization is session-only: an MCP access token must never be
// able to approve another connection for its user.
func (h *mcpOAuthAPI) mcpDecideAuthorization(rw http.ResponseWriter, r *http.Request, p core.Principal, approve bool) {
	if !h.mcpOAuthEnabled(rw) || !requireSessionCredential(rw, r, "approving an MCP connection") {
		return
	}
	raw, err := h.Ephemeral.Take(r.Context(), ephemeralMCPAuthorize, r.PathValue("id"))
	var pending mcpAuthorizeRequest
	if err != nil || json.Unmarshal(raw, &pending) != nil {
		writeAPIError(rw, http.StatusNotFound, "not_found", "this authorization request has expired; start connecting again from the app")
		return
	}
	if !approve {
		writeJSON(rw, http.StatusOK, map[string]string{"redirect_url": withQuery(pending.RedirectURI, url.Values{
			"error": {"access_denied"}, "state": {pending.State},
		})})
		return
	}
	code, err := randomToken()
	if err != nil {
		writeJSONError(rw, http.StatusInternalServerError, "could not mint authorization code")
		return
	}
	payload, _ := json.Marshal(mcpCode{
		mcpAuthorizeRequest: pending,
		Subject:             p.Subject,
		Tenant:              p.Tenant,
		Workspace:           p.Workspace,
		Roles:               p.Roles,
	})
	if err := h.Ephemeral.Put(r.Context(), ephemeralMCPCode, code, payload, time.Now().Add(mcpCodeTTL)); err != nil {
		h.logger.Printf("mcp oauth: store code: %v", err)
		writeJSONError(rw, http.StatusInternalServerError, "could not mint authorization code")
		return
	}
	writeJSON(rw, http.StatusOK, map[string]string{"redirect_url": withQuery(pending.RedirectURI, url.Values{
		"code": {code}, "state": {pending.State}, "iss": {h.effectiveBaseURL(r)},
	})})
}

func (h *mcpOAuthAPI) mcpToken(rw http.ResponseWriter, r *http.Request) {
	if !h.mcpOAuthEnabled(rw) {
		return
	}
	rw.Header().Set("Cache-Control", "no-store")
	if err := r.ParseForm(); err != nil {
		writeOAuthError(rw, http.StatusBadRequest, "invalid_request", "body must be application/x-www-form-urlencoded")
		return
	}
	now := time.Now()
	var (
		g               auth.MCPGrant
		access, refresh string
		err             error
	)
	switch r.PostForm.Get("grant_type") {
	case "authorization_code":
		g, access, refresh, err = h.redeemMCPCode(r.Context(), r.PostForm, now)
	case "refresh_token":
		g, access, refresh, err = auth.RefreshMCPGrant(r.Context(), h.Grants, r.PostForm.Get("refresh_token"), now)
	default:
		writeOAuthError(rw, http.StatusBadRequest, "unsupported_grant_type", "grant_type must be authorization_code or refresh_token")
		return
	}
	if err != nil {
		var oe oauthError
		if errors.As(err, &oe) {
			writeOAuthError(rw, http.StatusBadRequest, oe.code, oe.desc)
			return
		}
		if errors.Is(err, auth.ErrInvalidCredential) || errors.Is(err, auth.ErrMCPRefreshReused) {
			writeOAuthError(rw, http.StatusBadRequest, "invalid_grant", err.Error())
			return
		}
		h.logger.Printf("mcp oauth: token: %v", err)
		writeOAuthError(rw, http.StatusInternalServerError, "server_error", "could not issue tokens")
		return
	}
	writeJSON(rw, http.StatusOK, map[string]any{
		"access_token":  access,
		"token_type":    "Bearer",
		"expires_in":    int(g.AccessExpiresAt.Sub(now).Seconds()),
		"refresh_token": refresh,
		"scope":         g.Scope,
	})
}

func (h *mcpOAuthAPI) redeemMCPCode(ctx context.Context, form url.Values, now time.Time) (auth.MCPGrant, string, string, error) {
	raw, err := h.Ephemeral.Take(ctx, ephemeralMCPCode, form.Get("code"))
	var c mcpCode
	if err != nil || json.Unmarshal(raw, &c) != nil {
		return auth.MCPGrant{}, "", "", oauthError{"invalid_grant", "authorization code is invalid, expired or already used"}
	}
	if form.Get("client_id") != c.ClientID || form.Get("redirect_uri") != c.RedirectURI {
		return auth.MCPGrant{}, "", "", oauthError{"invalid_grant", "client_id or redirect_uri does not match the authorization request"}
	}
	verifier := form.Get("code_verifier")
	sum := sha256.Sum256([]byte(verifier))
	if len(verifier) < 43 || len(verifier) > 128 ||
		subtle.ConstantTimeCompare([]byte(base64.RawURLEncoding.EncodeToString(sum[:])), []byte(c.CodeChallenge)) != 1 {
		return auth.MCPGrant{}, "", "", oauthError{"invalid_grant", "code_verifier does not match code_challenge"}
	}
	u, _ := url.Parse(c.RedirectURI)
	return auth.IssueMCPGrant(ctx, h.Grants, auth.MCPGrant{
		Subject:      c.Subject,
		Tenant:       c.Tenant,
		Workspace:    c.Workspace,
		Roles:        c.Roles,
		ClientName:   c.ClientName,
		RedirectHost: u.Host,
		Scope:        c.Scope,
	}, now)
}

func (h *mcpOAuthAPI) listMCPConnections(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if !h.mcpOAuthEnabled(rw) {
		return
	}
	grants, err := h.Grants.ListGrantsBySubject(r.Context(), p.Subject)
	if err != nil {
		writeJSONError(rw, http.StatusInternalServerError, "list connections: "+err.Error())
		return
	}
	out := make([]map[string]any, 0, len(grants))
	for _, g := range grants {
		last := g.CreatedAt
		if g.RefreshedAt.After(last) {
			last = g.RefreshedAt
		}
		out = append(out, map[string]any{
			"id":            g.ID,
			"client_name":   g.ClientName,
			"redirect_host": g.RedirectHost,
			"workspace":     g.Tenant + "/" + g.Workspace,
			"created_at":    g.CreatedAt,
			"last_seen_at":  last,
		})
	}
	writeJSON(rw, http.StatusOK, map[string]any{"connections": out})
}

func (h *mcpOAuthAPI) deleteMCPConnection(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if !h.mcpOAuthEnabled(rw) || !requireSessionCredential(rw, r, "disconnecting an MCP client") {
		return
	}
	g, err := h.Grants.GetGrant(r.Context(), r.PathValue("id"))
	if err != nil || g.Subject != p.Subject {
		writeAPIError(rw, http.StatusNotFound, "not_found", "no such connection")
		return
	}
	if err := h.Grants.DeleteGrant(r.Context(), g.ID); err != nil {
		writeJSONError(rw, http.StatusInternalServerError, "disconnect: "+err.Error())
		return
	}
	rw.WriteHeader(http.StatusNoContent)
}

type oauthError struct{ code, desc string }

func (e oauthError) Error() string { return e.code + ": " + e.desc }

func writeOAuthError(rw http.ResponseWriter, status int, code, desc string) {
	writeJSON(rw, status, map[string]string{"error": code, "error_description": desc})
}

func withQuery(raw string, add url.Values) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	q := u.Query()
	for k, vs := range add {
		if len(vs) > 0 && vs[0] != "" {
			q.Set(k, vs[0])
		}
	}
	u.RawQuery = q.Encode()
	return u.String()
}

func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
