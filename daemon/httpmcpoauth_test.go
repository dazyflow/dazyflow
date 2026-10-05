// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/dazyflow/dazyflow/auth"
	"github.com/dazyflow/dazyflow/core"
)

const claudeCallback = "https://claude.ai/api/mcp/auth_callback"

// newMCPOAuthHarness adds what the OAuth flow needs on top of the gateway
// harness: a grant store, and a signed-in session to approve with.
func newMCPOAuthHarness(t *testing.T) (*gatewayHarness, string) {
	t.Helper()
	h := newGatewayHarness(t)
	sessions := auth.NewMemSessionStore()
	grants := auth.NewMemMCPGrantStore()
	h.gw.Sessions = sessions
	h.gw.MCPGrants = grants
	h.svc.Auth = append(h.svc.Auth.(auth.Chain),
		&auth.SessionAuthenticator{Store: sessions},
		&auth.MCPGrantAuthenticator{Store: grants})
	_, session, err := auth.IssueSession(t.Context(), sessions, auth.User{
		Subject: "alice", Tenant: "t", Workspace: "ws",
		Roles: []core.Role{{Name: "editor", Permissions: []core.Permission{core.PermGraphRun, core.PermGraphEdit}}},
	}, 3600e9)
	if err != nil {
		t.Fatal(err)
	}
	return h, session
}

func serve(h *gatewayHarness, req *http.Request) *httptest.ResponseRecorder {
	rw := httptest.NewRecorder()
	ServeForTest(h.gw, rw, req)
	return rw
}

func bearer(req *http.Request, token string) *http.Request {
	req.Header.Set("Authorization", "Bearer "+token)
	return req
}

func postForm(h *gatewayHarness, path string, form url.Values) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return serve(h, req)
}

func decodeBody[T any](t *testing.T, rw *httptest.ResponseRecorder, wantStatus int) T {
	t.Helper()
	if rw.Code != wantStatus {
		t.Fatalf("status = %d, want %d; body = %s", rw.Code, wantStatus, rw.Body)
	}
	var v T
	if err := json.Unmarshal(rw.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode: %v; body = %s", err, rw.Body)
	}
	return v
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	Error        string `json:"error"`
}

// authorizeMCPClient walks the browser leg — register, authorize, approve —
// and returns what the client needs to redeem the code.
func authorizeMCPClient(t *testing.T, h *gatewayHarness, session string) (clientID, code, verifier string) {
	t.Helper()
	reg := decodeBody[struct {
		ClientID string `json:"client_id"`
	}](t, serve(h, httptest.NewRequest(http.MethodPost, "/oauth/register",
		strings.NewReader(`{"client_name":"Claude","redirect_uris":["`+claudeCallback+`"]}`))), http.StatusCreated)

	verifier = strings.Repeat("v", 50)
	sum := sha256.Sum256([]byte(verifier))
	q := url.Values{
		"response_type": {"code"}, "client_id": {reg.ClientID}, "redirect_uri": {claudeCallback},
		"code_challenge": {base64.RawURLEncoding.EncodeToString(sum[:])}, "code_challenge_method": {"S256"},
		"state": {"st8"}, "resource": {"http://example.com/mcp"},
	}
	rw := serve(h, httptest.NewRequest(http.MethodGet, "/oauth/authorize?"+q.Encode(), nil))
	if rw.Code != http.StatusFound {
		t.Fatalf("authorize: status = %d, body = %s", rw.Code, rw.Body)
	}
	loc, _ := url.Parse(rw.Header().Get("Location"))
	if loc.Path != "/authorize" {
		t.Fatalf("authorize redirected to %s, want the consent page", loc)
	}
	reqID := loc.Query().Get("request")

	info := decodeBody[map[string]string](t, serve(h, bearer(httptest.NewRequest(http.MethodGet, "/api/v1/me/mcp-authorizations/"+reqID, nil), session)), http.StatusOK)
	if info["client_name"] != "Claude" || info["redirect_host"] != "claude.ai" {
		t.Errorf("consent info = %v", info)
	}
	approved := decodeBody[map[string]string](t, serve(h, bearer(httptest.NewRequest(http.MethodPost, "/api/v1/me/mcp-authorizations/"+reqID+"/approve", nil), session)), http.StatusOK)
	back, _ := url.Parse(approved["redirect_url"])
	if !strings.HasPrefix(approved["redirect_url"], claudeCallback+"?") || back.Query().Get("state") != "st8" {
		t.Fatalf("approve redirect = %s", approved["redirect_url"])
	}
	return reg.ClientID, back.Query().Get("code"), verifier
}

func TestMCPOAuth_DiscoveryDocuments(t *testing.T) {
	t.Parallel()
	h, _ := newMCPOAuthHarness(t)
	prm := decodeBody[map[string]any](t, serve(h, httptest.NewRequest(http.MethodGet, "/.well-known/oauth-protected-resource/mcp", nil)), http.StatusOK)
	if prm["resource"] != "http://example.com/mcp" {
		t.Errorf("resource = %v", prm["resource"])
	}
	as := decodeBody[map[string]any](t, serve(h, httptest.NewRequest(http.MethodGet, "/.well-known/oauth-authorization-server", nil)), http.StatusOK)
	if as["issuer"] != "http://example.com" || as["token_endpoint"] != "http://example.com/oauth/token" {
		t.Errorf("metadata = %v", as)
	}
}

func TestMCPOAuth_FullFlowThenMCPCallThenRefreshRotation(t *testing.T) {
	t.Parallel()
	h, session := newMCPOAuthHarness(t)
	clientID, code, verifier := authorizeMCPClient(t, h, session)

	redeem := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "client_id": {clientID},
		"redirect_uri": {claudeCallback}, "code_verifier": {verifier}}
	tok := decodeBody[tokenResponse](t, postForm(h, "/oauth/token", redeem), http.StatusOK)
	if !strings.HasPrefix(tok.AccessToken, auth.MCPAccessTokenPrefix) || tok.RefreshToken == "" || tok.ExpiresIn <= 0 {
		t.Fatalf("token response = %+v", tok)
	}
	if again := decodeBody[tokenResponse](t, postForm(h, "/oauth/token", redeem), http.StatusBadRequest); again.Error != "invalid_grant" {
		t.Errorf("code reuse: error = %q", again.Error)
	}

	h.token = tok.AccessToken
	if text, isErr := callTool(t, h, "list_flows", map[string]any{}); isErr {
		t.Fatalf("list_flows over OAuth token: %s", text)
	}

	refresh := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {tok.RefreshToken}}
	tok2 := decodeBody[tokenResponse](t, postForm(h, "/oauth/token", refresh), http.StatusOK)
	if tok2.AccessToken == tok.AccessToken || tok2.RefreshToken == tok.RefreshToken {
		t.Fatal("refresh did not rotate the tokens")
	}
	if replay := decodeBody[tokenResponse](t, postForm(h, "/oauth/token", refresh), http.StatusBadRequest); replay.Error != "invalid_grant" {
		t.Errorf("refresh replay: error = %q", replay.Error)
	}
	if rw := h.mcp(t, tok2.AccessToken, `{"jsonrpc":"2.0","id":1,"method":"ping"}`, nil); rw.Code != http.StatusUnauthorized {
		t.Errorf("after a refresh replay the connection must be revoked; status = %d", rw.Code)
	}
}

// An assistant holding an OAuth token is software, not the person: it takes
// the API-key path where policy differs by credential kind, so a delete needs
// graph:admin instead of a password it cannot type.
func TestMCPOAuth_TokenIsTreatedAsAKeyNotASession(t *testing.T) {
	t.Parallel()
	h, session := newMCPOAuthHarness(t)
	clientID, code, verifier := authorizeMCPClient(t, h, session)
	tok := decodeBody[tokenResponse](t, postForm(h, "/oauth/token", url.Values{"grant_type": {"authorization_code"},
		"code": {code}, "client_id": {clientID}, "redirect_uri": {claudeCallback}, "code_verifier": {verifier}}), http.StatusOK)
	h.token = tok.AccessToken
	if text, isErr := callTool(t, h, "create_flow", map[string]any{"id": "doomed", "nodes": []map[string]any{{"id": "a", "module": "noop"}}}); isErr {
		t.Fatalf("create_flow: %s", text)
	}
	text, isErr := callTool(t, h, "delete_flow", map[string]any{"id": "doomed"})
	if !isErr || !strings.Contains(text, "admin_scope_required") {
		t.Errorf("delete without graph:admin: isError=%v %s", isErr, text)
	}
}

func TestMCPOAuth_WrongVerifierIsRefused(t *testing.T) {
	t.Parallel()
	h, session := newMCPOAuthHarness(t)
	clientID, code, _ := authorizeMCPClient(t, h, session)
	res := decodeBody[tokenResponse](t, postForm(h, "/oauth/token", url.Values{"grant_type": {"authorization_code"},
		"code": {code}, "client_id": {clientID}, "redirect_uri": {claudeCallback}, "code_verifier": {strings.Repeat("x", 50)}}), http.StatusBadRequest)
	if res.Error != "invalid_grant" {
		t.Errorf("error = %q", res.Error)
	}
}

func TestMCPOAuth_RegistrationRefusesForeignRedirect(t *testing.T) {
	t.Parallel()
	h, _ := newMCPOAuthHarness(t)
	for _, u := range []string{"https://evil.example/cb", "http://claude.ai/cb", "https://claude.ai/cb#frag"} {
		rw := serve(h, httptest.NewRequest(http.MethodPost, "/oauth/register",
			strings.NewReader(`{"client_name":"x","redirect_uris":["`+u+`"]}`)))
		if rw.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d", u, rw.Code)
		}
	}
	for _, u := range []string{"http://localhost:33418/callback", "http://127.0.0.1:9/cb"} {
		rw := serve(h, httptest.NewRequest(http.MethodPost, "/oauth/register",
			strings.NewReader(`{"client_name":"Claude Code","redirect_uris":["`+u+`"]}`)))
		if rw.Code != http.StatusCreated {
			t.Errorf("%s: status = %d, body = %s", u, rw.Code, rw.Body)
		}
	}
}

// An API key or an MCP access token must never approve a connection: only an
// interactive session proves the user is there.
func TestMCPOAuth_ApprovalRequiresASession(t *testing.T) {
	t.Parallel()
	h, _ := newMCPOAuthHarness(t)
	rw := serve(h, bearer(httptest.NewRequest(http.MethodPost, "/api/v1/me/mcp-authorizations/whatever/approve", nil), h.token))
	if rw.Code != http.StatusForbidden {
		t.Errorf("API key approve: status = %d", rw.Code)
	}
}

func TestMCPOAuth_ConnectionsListAndDisconnect(t *testing.T) {
	t.Parallel()
	h, session := newMCPOAuthHarness(t)
	clientID, code, verifier := authorizeMCPClient(t, h, session)
	tok := decodeBody[tokenResponse](t, postForm(h, "/oauth/token", url.Values{"grant_type": {"authorization_code"},
		"code": {code}, "client_id": {clientID}, "redirect_uri": {claudeCallback}, "code_verifier": {verifier}}), http.StatusOK)

	list := decodeBody[struct {
		Connections []struct {
			ID         string `json:"id"`
			ClientName string `json:"client_name"`
		} `json:"connections"`
	}](t, serve(h, bearer(httptest.NewRequest(http.MethodGet, "/api/v1/me/mcp-connections", nil), session)), http.StatusOK)
	if len(list.Connections) != 1 || list.Connections[0].ClientName != "Claude" {
		t.Fatalf("connections = %+v", list.Connections)
	}
	rw := serve(h, bearer(httptest.NewRequest(http.MethodDelete, "/api/v1/me/mcp-connections/"+list.Connections[0].ID, nil), session))
	if rw.Code != http.StatusNoContent {
		t.Fatalf("disconnect: status = %d, body = %s", rw.Code, rw.Body)
	}
	if rw := h.mcp(t, tok.AccessToken, `{"jsonrpc":"2.0","id":1,"method":"ping"}`, nil); rw.Code != http.StatusUnauthorized {
		t.Errorf("disconnected token still works; status = %d", rw.Code)
	}
}
