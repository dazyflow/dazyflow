package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"git.sr.ht/~klahr/dazyflow/auth"
	"git.sr.ht/~klahr/dazyflow/core"
)

// fakeProvider stands in for a real OAuth server. It serves the
// /token endpoint with a configurable response. Tests build one,
// register it in the OAuthRegistry, and drive the flow without
// touching the real Slack/Google/etc. APIs.
type fakeProvider struct {
	server       *httptest.Server
	tokenStatus  int
	tokenBody    string
	lastFormBody url.Values
}

func newFakeProvider(t *testing.T) *fakeProvider {
	t.Helper()
	fp := &fakeProvider{
		tokenStatus: 200,
		tokenBody: `{
			"access_token":"xoxb-test-12345",
			"refresh_token":"xoxe-1-refresh",
			"token_type":"Bearer",
			"expires_in":3600,
			"scope":"chat:write,channels:read",
			"team": {"id":"T123","name":"Test Workspace"}
		}`,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/authorize", func(w http.ResponseWriter, r *http.Request) {
		// Real providers serve an HTML consent page here. For the
		// tests we don't actually drive a browser through this; the
		// flow tests construct the callback URL by hand using the
		// state token from the authorize redirect.
		w.WriteHeader(200)
	})
	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		fp.lastFormBody = r.PostForm
		w.WriteHeader(fp.tokenStatus)
		_, _ = w.Write([]byte(fp.tokenBody))
	})
	fp.server = httptest.NewServer(mux)
	t.Cleanup(fp.server.Close)
	return fp
}

func (fp *fakeProvider) provider() OAuthProvider {
	return OAuthProvider{
		Name:         "test",
		AuthorizeURL: fp.server.URL + "/oauth/authorize",
		TokenURL:     fp.server.URL + "/oauth/token",
		Scopes:       []string{"chat:write", "channels:read"},
		ClientID:     "test-client",
		ClientSecret: "test-secret",
	}
}

// newOAuthHarness builds a gateway with EncryptedSecrets +
// OAuthRegistry wired up, registers the fake provider, and returns
// everything tests need.
func newOAuthHarness(t *testing.T) (*gatewayHarness, *fakeProvider) {
	t.Helper()
	h := newGatewayHarness(t)
	// Wire encrypted secrets so OAuth has somewhere to store tokens.
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 7)
	}
	es, err := NewEncryptedSecrets(key, NewMemSecretsStore())
	if err != nil {
		t.Fatalf("NewEncryptedSecrets: %v", err)
	}
	h.gw.EncryptedSecrets = es

	fp := newFakeProvider(t)
	reg := NewOAuthRegistry("https://example.test", es)
	reg.Register(fp.provider())
	// Stub HTTPClient so callbacks talk to the fakeProvider rather
	// than the real internet.
	reg.HTTPClient = fp.server.Client()
	h.gw.OAuth = reg

	// Upgrade the default token to include secret:write so the
	// authorize endpoint is reachable.
	role := core.Role{Name: "oauth-admin", Permissions: []core.Permission{
		core.PermGraphRun, core.PermGraphEdit, core.PermGraphAdmin,
		core.PermSecretRead, core.PermSecretWrite,
	}}
	_, tok, err := auth.IssueAPIKey(h.ks, t.Context(), "oauth-key", "t", "ws", "alice", []core.Role{role}, nil)
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}
	h.token = tok
	return h, fp
}

// ---- State store ----------------------------------------------------

func TestOAuthState_MintConsume(t *testing.T) {
	s := newOAuthStateStore(time.Minute)
	state, err := s.mint(pendingOAuth{tenant: "t", provider: "p", account: "a", returnTo: "/"})
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if len(state) < 32 {
		t.Errorf("state too short: %q", state)
	}
	p, ok := s.consume(state)
	if !ok {
		t.Fatal("consume returned not-found")
	}
	if p.tenant != "t" || p.provider != "p" || p.account != "a" {
		t.Errorf("consume returned %+v", p)
	}
}

func TestOAuthState_SingleUse(t *testing.T) {
	// Consume must remove the entry — second consume sees nothing.
	// This is the replay defense.
	s := newOAuthStateStore(time.Minute)
	state, _ := s.mint(pendingOAuth{tenant: "t"})
	_, ok := s.consume(state)
	if !ok {
		t.Fatal("first consume failed")
	}
	_, ok = s.consume(state)
	if ok {
		t.Error("second consume should fail (single-use)")
	}
}

func TestOAuthState_Expiry(t *testing.T) {
	s := newOAuthStateStore(10 * time.Millisecond)
	state, _ := s.mint(pendingOAuth{tenant: "t"})
	time.Sleep(20 * time.Millisecond)
	_, ok := s.consume(state)
	if ok {
		t.Error("expired state should not consume")
	}
}

// ---- Token exchange & storage --------------------------------------

func TestOAuth_ExchangeAndStore(t *testing.T) {
	es, _ := NewEncryptedSecrets(make([]byte, 32), NewMemSecretsStore())
	fp := newFakeProvider(t)
	reg := NewOAuthRegistry("https://example.test", es)
	reg.Register(fp.provider())
	reg.HTTPClient = fp.server.Client()

	tok, err := reg.exchangeCode(t.Context(), fp.provider(), "the-code")
	if err != nil {
		t.Fatalf("exchangeCode: %v", err)
	}
	if tok.AccessToken != "xoxb-test-12345" {
		t.Errorf("access_token = %q", tok.AccessToken)
	}
	if tok.RefreshToken != "xoxe-1-refresh" {
		t.Errorf("refresh_token = %q", tok.RefreshToken)
	}
	if tok.ExpiresAt == nil {
		t.Error("expires_at not set from expires_in")
	}
	// Extras must capture provider-specific fields (Slack's team
	// object lives here).
	team, ok := tok.Extras["team"].(map[string]any)
	if !ok || team["id"] != "T123" {
		t.Errorf("extras.team = %+v, want {id:T123,...}", tok.Extras["team"])
	}

	// Round-trip through the encrypted store.
	name, err := reg.store(t.Context(), "acme", "test", "main", tok)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	if name != "oauth.test.main" {
		t.Errorf("name = %q, want oauth.test.main", name)
	}
	got, err := reg.GetOAuthToken(core.WithTenant(t.Context(), "acme"), "test", "main")
	if err != nil {
		t.Fatalf("GetOAuthToken: %v", err)
	}
	if got.AccessToken != tok.AccessToken {
		t.Errorf("round-trip access_token mismatch")
	}
}

func TestOAuth_ExchangeFailsOnNon2xx(t *testing.T) {
	es, _ := NewEncryptedSecrets(make([]byte, 32), NewMemSecretsStore())
	fp := newFakeProvider(t)
	fp.tokenStatus = 400
	fp.tokenBody = `{"error":"invalid_grant"}`
	reg := NewOAuthRegistry("https://example.test", es)
	reg.HTTPClient = fp.server.Client()

	_, err := reg.exchangeCode(t.Context(), fp.provider(), "bad-code")
	if err == nil {
		t.Fatal("expected error for 400 from provider")
	}
	if !strings.Contains(err.Error(), "400") {
		t.Errorf("error missing status: %v", err)
	}
}

func TestOAuth_ExchangeFailsOnProviderError(t *testing.T) {
	// Slack returns HTTP 200 with `error` in the JSON body. The
	// generic OAuth response shape doesn't catch that, so we
	// special-case it.
	es, _ := NewEncryptedSecrets(make([]byte, 32), NewMemSecretsStore())
	fp := newFakeProvider(t)
	fp.tokenBody = `{"ok":false,"error":"invalid_code"}`
	reg := NewOAuthRegistry("https://example.test", es)
	reg.HTTPClient = fp.server.Client()
	_, err := reg.exchangeCode(t.Context(), fp.provider(), "bad")
	if err == nil || !strings.Contains(err.Error(), "invalid_code") {
		t.Errorf("err = %v, want containing invalid_code", err)
	}
}

func TestOAuth_GetTokenRequiresTenantInCtx(t *testing.T) {
	es, _ := NewEncryptedSecrets(make([]byte, 32), NewMemSecretsStore())
	reg := NewOAuthRegistry("https://example.test", es)
	_, err := reg.GetOAuthToken(t.Context(), "slack", "default")
	if err == nil {
		t.Fatal("expected error without tenant in ctx")
	}
}

// ---- End-to-end HTTP flow ------------------------------------------

func TestHTTPOAuth_AuthorizeRedirectsToProvider(t *testing.T) {
	h, fp := newOAuthHarness(t)
	rw := h.do(t, "GET", "/api/v1/oauth/test/authorize?account=main&return_to=/apps", nil)
	if rw.Code != http.StatusFound {
		t.Fatalf("status=%d body=%s", rw.Code, rw.Body.String())
	}
	loc := rw.Header().Get("Location")
	if !strings.HasPrefix(loc, fp.server.URL+"/oauth/authorize") {
		t.Errorf("Location=%q, want to start with provider authorize URL", loc)
	}
	u, _ := url.Parse(loc)
	if u.Query().Get("client_id") != "test-client" {
		t.Errorf("client_id = %q", u.Query().Get("client_id"))
	}
	if u.Query().Get("state") == "" {
		t.Error("state missing in redirect")
	}
	if u.Query().Get("redirect_uri") != "https://example.test/api/v1/oauth/test/callback" {
		t.Errorf("redirect_uri = %q", u.Query().Get("redirect_uri"))
	}
}

func TestHTTPOAuth_AuthorizeUnknownProvider(t *testing.T) {
	h, _ := newOAuthHarness(t)
	rw := h.do(t, "GET", "/api/v1/oauth/ghost/authorize", nil)
	if rw.Code != http.StatusNotFound {
		t.Errorf("status=%d, want 404", rw.Code)
	}
}

func TestHTTPOAuth_AuthorizeRequiresSecretWrite(t *testing.T) {
	// Runner-only role lacks secret:write → 403.
	h, _ := newOAuthHarness(t)
	role := core.Role{Name: "runner", Permissions: []core.Permission{core.PermGraphRun}}
	_, tok, _ := auth.IssueAPIKey(h.ks, t.Context(), "runner", "t", "ws", "bob", []core.Role{role}, nil)
	req := httptest.NewRequest("GET", "/api/v1/oauth/test/authorize", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rw := httptest.NewRecorder()
	ServeForTest(h.gw, rw, req)
	if rw.Code != http.StatusForbidden {
		t.Errorf("status=%d, want 403", rw.Code)
	}
}

func TestHTTPOAuth_AuthorizeBadReturnTo(t *testing.T) {
	// Absolute URL in return_to → open-redirect vector → must reject.
	h, _ := newOAuthHarness(t)
	rw := h.do(t, "GET", "/api/v1/oauth/test/authorize?return_to=https://evil.example.com/", nil)
	if rw.Code != http.StatusBadRequest {
		t.Errorf("status=%d, want 400 for absolute return_to", rw.Code)
	}
}

// callbackWithBinding builds an OAuth callback request that carries the
// dz_oauth_state cookie set by the authorize step, so the browser-binding
// check (RFC 6749 §10.12) passes — mirroring a real browser that keeps the
// cookie across the redirect.
func callbackWithBinding(authResp *httptest.ResponseRecorder, target string) *http.Request {
	req := httptest.NewRequest("GET", target, nil)
	for _, c := range authResp.Result().Cookies() {
		if c.Name == oauthStateCookie {
			req.AddCookie(c)
		}
	}
	return req
}

func TestHTTPOAuth_CallbackHappyPath(t *testing.T) {
	// 1. Hit authorize, capture the state from the redirect.
	// 2. POST that state + a fake code to /callback.
	// 3. Verify the token landed in encrypted secrets and the user
	//    got redirected to return_to with oauth=success.
	h, _ := newOAuthHarness(t)
	rw := h.do(t, "GET", "/api/v1/oauth/test/authorize?account=main&return_to=/apps", nil)
	if rw.Code != http.StatusFound {
		t.Fatalf("authorize status=%d", rw.Code)
	}
	loc, _ := url.Parse(rw.Header().Get("Location"))
	state := loc.Query().Get("state")
	if state == "" {
		t.Fatal("state missing")
	}

	// Callback is unauthenticated, but carries the browser-binding cookie.
	req := callbackWithBinding(rw, "/api/v1/oauth/test/callback?code=the-code&state="+state)
	cb := httptest.NewRecorder()
	ServeForTest(h.gw, cb, req)
	if cb.Code != http.StatusFound {
		t.Fatalf("callback status=%d body=%s", cb.Code, cb.Body.String())
	}
	loc2, _ := url.Parse(cb.Header().Get("Location"))
	if loc2.Path != "/apps" {
		t.Errorf("redirect path = %q, want /apps", loc2.Path)
	}
	if loc2.Query().Get("oauth") != "success" {
		t.Errorf("oauth=%q, want success (full URL %s)", loc2.Query().Get("oauth"), cb.Header().Get("Location"))
	}
	if loc2.Query().Get("account") != "main" {
		t.Errorf("account=%q", loc2.Query().Get("account"))
	}

	// Verify the token landed in encrypted secrets under the expected name.
	raw, err := h.gw.EncryptedSecrets.Get(core.WithTenant(t.Context(), "t"), "oauth.test.main")
	if err != nil {
		t.Fatalf("get stored secret: %v", err)
	}
	var stored StoredOAuthToken
	if err := json.Unmarshal([]byte(raw), &stored); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if stored.AccessToken != "xoxb-test-12345" {
		t.Errorf("stored access_token = %q", stored.AccessToken)
	}
}

func TestHTTPOAuth_CallbackRejectsWrongBrowser(t *testing.T) {
	// A flow started via the browser-redirect path is bound to the browser
	// that started it. A callback that arrives WITHOUT the matching
	// dz_oauth_state cookie (e.g. an attacker who induced the victim to
	// complete the attacker's flow) must be rejected and store no token.
	h, _ := newOAuthHarness(t)
	rw := h.do(t, "GET", "/api/v1/oauth/test/authorize?account=main&return_to=/apps", nil)
	loc, _ := url.Parse(rw.Header().Get("Location"))
	state := loc.Query().Get("state")

	// No cookie attached → binding check fails.
	req := httptest.NewRequest("GET", "/api/v1/oauth/test/callback?code=the-code&state="+state, nil)
	cb := httptest.NewRecorder()
	ServeForTest(h.gw, cb, req)
	if cb.Code != http.StatusBadRequest {
		t.Fatalf("callback without binding cookie: status=%d, want 400", cb.Code)
	}
	if _, err := h.gw.EncryptedSecrets.Get(core.WithTenant(t.Context(), "t"), "oauth.test.main"); err == nil {
		t.Error("token was stored despite failed browser binding")
	}
}

func TestHTTPOAuth_CallbackBadState(t *testing.T) {
	// A callback with a state we never minted (or already consumed)
	// must 400 rather than processing the code.
	h, _ := newOAuthHarness(t)
	req := httptest.NewRequest("GET", "/api/v1/oauth/test/callback?code=x&state=never-minted", nil)
	rw := httptest.NewRecorder()
	ServeForTest(h.gw, rw, req)
	if rw.Code != http.StatusBadRequest {
		t.Errorf("status=%d, want 400", rw.Code)
	}
}

func TestHTTPOAuth_CallbackReplayRejected(t *testing.T) {
	// Same state used twice — second use must 400.
	h, _ := newOAuthHarness(t)
	rw := h.do(t, "GET", "/api/v1/oauth/test/authorize?return_to=/x", nil)
	loc, _ := url.Parse(rw.Header().Get("Location"))
	state := loc.Query().Get("state")

	req := callbackWithBinding(rw, "/api/v1/oauth/test/callback?code=c&state="+state)
	first := httptest.NewRecorder()
	ServeForTest(h.gw, first, req)
	if first.Code != http.StatusFound {
		t.Fatalf("first callback: %d", first.Code)
	}
	// Replay the same state.
	req2 := callbackWithBinding(rw, "/api/v1/oauth/test/callback?code=c&state="+state)
	replay := httptest.NewRecorder()
	ServeForTest(h.gw, replay, req2)
	if replay.Code != http.StatusBadRequest {
		t.Errorf("replay status=%d, want 400", replay.Code)
	}
}

func TestHTTPOAuth_CallbackProviderDeniedConsent(t *testing.T) {
	// User clicks "Deny" → provider redirects with ?error=access_denied.
	// We should bounce back to return_to with oauth=error.
	h, _ := newOAuthHarness(t)
	rw := h.do(t, "GET", "/api/v1/oauth/test/authorize?return_to=/apps", nil)
	loc, _ := url.Parse(rw.Header().Get("Location"))
	state := loc.Query().Get("state")

	req := callbackWithBinding(rw, "/api/v1/oauth/test/callback?error=access_denied&state="+state)
	cb := httptest.NewRecorder()
	ServeForTest(h.gw, cb, req)
	if cb.Code != http.StatusFound {
		t.Fatalf("status=%d", cb.Code)
	}
	loc2, _ := url.Parse(cb.Header().Get("Location"))
	if loc2.Query().Get("oauth") != "error" {
		t.Errorf("oauth=%q, want error", loc2.Query().Get("oauth"))
	}
	if !strings.Contains(loc2.Query().Get("error"), "access_denied") {
		t.Errorf("error=%q", loc2.Query().Get("error"))
	}
	// And no token written to the store.
	_, err := h.gw.EncryptedSecrets.Get(core.WithTenant(t.Context(), "t"), "oauth.test.default")
	if err == nil {
		t.Error("expected no stored token after denied consent")
	}
}

func TestHTTPOAuth_ListProvidersShowsConnectedAccounts(t *testing.T) {
	// After a successful flow, GET /oauth/providers should report
	// the accounts under each provider.
	h, _ := newOAuthHarness(t)
	// Complete a flow first.
	rw := h.do(t, "GET", "/api/v1/oauth/test/authorize?account=main&return_to=/x", nil)
	loc, _ := url.Parse(rw.Header().Get("Location"))
	state := loc.Query().Get("state")
	req := callbackWithBinding(rw, "/api/v1/oauth/test/callback?code=c&state="+state)
	cb := httptest.NewRecorder()
	ServeForTest(h.gw, cb, req)

	listRW := h.do(t, "GET", "/api/v1/oauth/providers", nil)
	if listRW.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", listRW.Code, listRW.Body.String())
	}
	var resp struct {
		Providers []struct {
			Name     string   `json:"name"`
			Accounts []string `json:"accounts"`
		} `json:"providers"`
	}
	_ = json.Unmarshal(listRW.Body.Bytes(), &resp)
	if len(resp.Providers) != 1 {
		t.Fatalf("providers = %+v", resp.Providers)
	}
	if resp.Providers[0].Name != "test" {
		t.Errorf("name = %q", resp.Providers[0].Name)
	}
	if len(resp.Providers[0].Accounts) != 1 || resp.Providers[0].Accounts[0] != "main" {
		t.Errorf("accounts = %v, want [main]", resp.Providers[0].Accounts)
	}
}

func TestHTTPOAuth_NotConfiguredIs501(t *testing.T) {
	// Without OAuth registry wired up, all three endpoints must 501.
	h := newGatewayHarness(t)
	for _, c := range []struct{ method, path string }{
		{"GET", "/api/v1/oauth/providers"},
		{"GET", "/api/v1/oauth/test/authorize"},
		{"GET", "/api/v1/oauth/test/callback"},
	} {
		t.Run(c.method+" "+c.path, func(t *testing.T) {
			req := httptest.NewRequest(c.method, c.path, bytes.NewBufferString(""))
			req.Header.Set("Authorization", "Bearer "+h.token)
			rw := httptest.NewRecorder()
			ServeForTest(h.gw, rw, req)
			if rw.Code != http.StatusNotImplemented {
				t.Errorf("status=%d, want 501", rw.Code)
			}
		})
	}
}

// ---- secretNameFor naming convention -------------------------------

func TestSecretNameFor(t *testing.T) {
	cases := []struct {
		provider, account, want string
	}{
		{"slack", "main", "oauth.slack.main"},
		{"slack", "", "oauth.slack.default"},
		{"github", "personal", "oauth.github.personal"},
	}
	for _, c := range cases {
		got := secretNameFor(c.provider, c.account)
		if got != c.want {
			t.Errorf("secretNameFor(%q, %q) = %q, want %q", c.provider, c.account, got, c.want)
		}
	}
}

// Ensure form body sent to the provider's /token contains the
// canonical OAuth fields. Caught a bug once where we accidentally
// sent the redirect_uri with a trailing slash.
func TestOAuth_ExchangeFormBodyShape(t *testing.T) {
	es, _ := NewEncryptedSecrets(make([]byte, 32), NewMemSecretsStore())
	fp := newFakeProvider(t)
	reg := NewOAuthRegistry("https://example.test", es)
	reg.HTTPClient = fp.server.Client()

	_, _ = reg.exchangeCode(context.Background(), fp.provider(), "the-code")
	want := map[string]string{
		"client_id":     "test-client",
		"client_secret": "test-secret",
		"code":          "the-code",
		"redirect_uri":  "https://example.test/api/v1/oauth/test/callback",
		"grant_type":    "authorization_code",
	}
	for k, v := range want {
		if fp.lastFormBody.Get(k) != v {
			t.Errorf("form[%q] = %q, want %q", k, fp.lastFormBody.Get(k), v)
		}
	}
}
