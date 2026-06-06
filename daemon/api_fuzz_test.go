package daemon

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"git.sr.ht/~klahr/hazyflow/auth"
	"git.sr.ht/~klahr/hazyflow/core"
	"git.sr.ht/~klahr/hazyflow/engine"
	"git.sr.ht/~klahr/hazyflow/engine/jobstore"
	"git.sr.ht/~klahr/hazyflow/workspace"
)

// Fuzz harnesses for the HTTP API surface. Contract for every target:
// the handler must NOT panic and must NOT return a 5xx for any input.
// Bad input is a 4xx; a 5xx means the server crashed on user input.
// Crash inputs land under testdata/fuzz/<TargetName>/ and become
// permanent regression cases.

// assertNo5xx flags only true server-crash statuses (500, 502, 503, 504).
// 501 "Not Implemented" is the documented "feature off" response and is
// expected on unwired surfaces.
func assertNo5xx(t *testing.T, where string, rw *httptest.ResponseRecorder) {
	t.Helper()
	switch rw.Code {
	case http.StatusInternalServerError, http.StatusBadGateway,
		http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		t.Errorf("%s: server-error status=%d body=%q", where, rw.Code, rw.Body.String())
	}
}

// fuzzHarness mirrors gatewayHarness but is built from *testing.F so we
// can do one-time setup outside f.Fuzz without abusing a zero-value
// *testing.T.
type fuzzHarness struct {
	gw    *HTTPGateway
	svc   *Service
	ks    *auth.MemKeyStore
	token string
}

func newFuzzHarness(f *testing.F) *fuzzHarness {
	f.Helper()
	ks := auth.NewMemKeyStore()
	role := core.Role{Name: "editor", Permissions: []core.Permission{
		core.PermGraphRun, core.PermGraphEdit, core.PermGraphAdmin,
		core.PermSecretRead, core.PermSecretWrite, core.PermOrganizationAdmin,
	}}
	_, token, err := auth.IssueAPIKey(ks, context.Background(), "k1", "t", "ws", "alice", []core.Role{role}, nil)
	if err != nil {
		f.Fatalf("issue key: %v", err)
	}
	wsStore, _ := workspace.OpenFS("")
	store := jobstore.NewMemory()
	bus := NewMemoryBus()
	eng := &engine.Engine{Resolver: &engine.NodeResolver{Native: engine.Default}}
	svc := &Service{
		Auth:       auth.Chain{&auth.APIKeyAuthenticator{Store: ks}},
		Workspaces: MapWorkspaces{"t/ws": wsStore},
		Jobs:       store,
		Engine:     eng,
		Bus:        bus,
		AdminKeys:  ks,
	}
	return &fuzzHarness{gw: NewHTTPGateway(svc), svc: svc, ks: ks, token: token}
}

// withSignup wires the Users/Sessions stores so signup + signin reach
// the JSON decode + validation code instead of bouncing on 501.
func (h *fuzzHarness) withSignup(f *testing.F) *fuzzHarness {
	f.Helper()
	users, err := auth.OpenJSONUserStore("")
	if err != nil {
		f.Fatalf("open user store: %v", err)
	}
	h.gw.Users = users
	h.gw.Sessions = auth.NewMemSessionStore()
	h.gw.EnableSignup = true
	h.svc.Auth = auth.Chain{
		&auth.APIKeyAuthenticator{Store: h.ks},
		&auth.SessionAuthenticator{Store: h.gw.Sessions},
	}
	return h
}

func (h *fuzzHarness) withSecrets(f *testing.F) *fuzzHarness {
	f.Helper()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	es, err := NewEncryptedSecrets(key, NewMemSecretsStore())
	if err != nil {
		f.Fatalf("NewEncryptedSecrets: %v", err)
	}
	h.gw.EncryptedSecrets = es
	return h
}

func (h *fuzzHarness) withSlackEvents(secret string) *fuzzHarness {
	handler := NewSlackEventsHandler(h.svc, secret)
	frozen := time.Unix(1700000000, 0).UTC()
	handler.now = func() time.Time { return frozen }
	h.gw.SlackEvents = handler
	return h
}

func (h *fuzzHarness) withGitHubEvents(secret string) *fuzzHarness {
	h.gw.GitHubEvents = NewGitHubEventsHandler(h.svc, secret)
	return h
}

// ---------------------------------------------------------------------
// Auth surface
// ---------------------------------------------------------------------

func FuzzSignIn(f *testing.F) {
	h := newFuzzHarness(f).withSignup(f)
	f.Add([]byte(`{"email":"a@b.com","password":"hunter2hunter"}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(``))
	f.Add([]byte(`{"email":null,"password":42}`))
	f.Add([]byte(`{"email":"  ","password":"x"}`))
	f.Add([]byte(`{"email":"` + makeASCIIRepeat(300) + `","password":"x"}`))
	f.Add([]byte(`{"email":[],"password":{}}`))
	f.Fuzz(func(t *testing.T, body []byte) {
		req := httptest.NewRequest("POST", "/api/v1/auth/signin", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rw := httptest.NewRecorder()
		ServeForTest(h.gw, rw, req)
		assertNo5xx(t, "signin", rw)
	})
}

func FuzzSignUp(f *testing.F) {
	h := newFuzzHarness(f).withSignup(f)
	f.Add([]byte(`{"email":"new@example.com","password":"supersecret"}`))
	f.Add([]byte(`{"email":"","password":""}`))
	f.Add([]byte(`{"email":"a@b","password":"x"}`))
	f.Add([]byte(`{"email":"\t \r\n@b.com","password":"hunter2hunter"}`))
	f.Add([]byte(`{"email":"a@b.co","password":"` + makeASCIIRepeat(257) + `"}`))
	f.Add([]byte(`{"email":"` + makeASCIIRepeat(255) + `@b.com","password":"hunter2hunter"}`))
	f.Add([]byte(`{"email":42,"password":42}`))
	f.Add([]byte(`{"email":"x","password":null}`))
	f.Fuzz(func(t *testing.T, body []byte) {
		req := httptest.NewRequest("POST", "/api/v1/auth/signup", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rw := httptest.NewRecorder()
		ServeForTest(h.gw, rw, req)
		assertNo5xx(t, "signup", rw)
	})
}

func FuzzWhoamiBearer(f *testing.F) {
	h := newFuzzHarness(f).withSignup(f)
	f.Add("Bearer foo")
	f.Add("Bearer ")
	f.Add("")
	f.Add("Bearer " + makeASCIIRepeat(4096))
	f.Add("Basic dXNlcjpwYXNz")
	f.Add("Bearer \x00\x01\x02")
	f.Fuzz(func(t *testing.T, authz string) {
		req := httptest.NewRequest("GET", "/api/v1/me", nil)
		if authz != "" {
			req.Header.Set("Authorization", authz)
		}
		rw := httptest.NewRecorder()
		ServeForTest(h.gw, rw, req)
		assertNo5xx(t, "whoami", rw)
	})
}

// ---------------------------------------------------------------------
// Graph surface
// ---------------------------------------------------------------------

func FuzzSaveGraph(f *testing.F) {
	h := newFuzzHarness(f)
	f.Add([]byte(`{"id":"g","nodes":[],"edges":[]}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(`null`))
	f.Add([]byte(`{"nodes":null,"edges":null}`))
	f.Add([]byte(`{"nodes":[{"id":"n1","module":"webhook_input","params":{"a":"b"}}],"edges":[]}`))
	f.Add([]byte(`{"id":"g","nodes":[{"id":"x","module":"http_request"}],"edges":[{"from":"x","to":"x"}]}`))
	f.Add([]byte(`{"nodes":[{"id":"a"},{"id":"b"}],"edges":[{"from":"a","to":"b"},{"from":"b","to":"a"}]}`))
	f.Add([]byte(`{"nodes":[],"edges":[{"from":"ghost","to":"phantom"}]}`))
	f.Add([]byte(`{"nodes":[{"id":"n","params":{"x":[[[[[[[1]]]]]]]}}]}`))
	f.Fuzz(func(t *testing.T, body []byte) {
		req := httptest.NewRequest("PUT", "/api/v1/me/flows/t%2Fws%2Ffuzz-graph", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+h.token)
		req.Header.Set("Content-Type", "application/json")
		rw := httptest.NewRecorder()
		ServeForTest(h.gw, rw, req)
		assertNo5xx(t, "saveGraph", rw)
	})
}

func FuzzRunGraphPath(f *testing.F) {
	h := newFuzzHarness(f)
	f.Add("t", "ws", "missing")
	f.Add("t", "ws", "\x01")
	f.Add("t", "ws", "..%2F..")
	f.Add(makeASCIIRepeat(1024), "ws", "g")
	f.Fuzz(func(t *testing.T, tenant, workspace, id string) {
		// flow_id is the percent-encoded composite — encode the slashes
		// inside as %2F so the value stays in a single mux segment.
		path := "/api/v1/me/flows/" + escapePathSeg(tenant+"/"+workspace+"/"+id) + "/run"
		req, err := http.NewRequest("POST", path, nil)
		if err != nil {
			t.Skip()
		}
		req.Header.Set("Authorization", "Bearer "+h.token)
		rw := httptest.NewRecorder()
		ServeForTest(h.gw, rw, req)
		assertNo5xx(t, "runGraph", rw)
	})
}

func FuzzSampleNodePath(f *testing.F) {
	h := newFuzzHarness(f)
	f.Add("t", "ws", "missing", "node1")
	f.Add("t", "ws", "g", "")
	f.Add("t", "ws", "g", "\x00\x01")
	f.Add("t", "ws", "g", "..")
	f.Fuzz(func(t *testing.T, tenant, workspace, id, nodeID string) {
		path := "/api/v1/me/flows/" + escapePathSeg(tenant+"/"+workspace+"/"+id) +
			"/nodes/" + escapePathSeg(nodeID) + "/sample"
		req, err := http.NewRequest("POST", path, nil)
		if err != nil {
			t.Skip()
		}
		req.Header.Set("Authorization", "Bearer "+h.token)
		rw := httptest.NewRecorder()
		ServeForTest(h.gw, rw, req)
		assertNo5xx(t, "sampleNode", rw)
	})
}

func FuzzValidateCron(f *testing.F) {
	h := newFuzzHarness(f)
	f.Add([]byte(`{"expr":"*/5 * * * *"}`))
	f.Add([]byte(`{"expr":""}`))
	f.Add([]byte(`{"expr":"@yearly"}`))
	f.Add([]byte(`{"expr":"60 24 32 13 8"}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(`{"expr":null}`))
	f.Add([]byte(`{"expr":"` + makeASCIIRepeat(4096) + `"}`))
	f.Add([]byte(`{"expr":"  * * * *"}`))
	f.Fuzz(func(t *testing.T, body []byte) {
		req := httptest.NewRequest("POST", "/api/v1/validate/cron", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+h.token)
		req.Header.Set("Content-Type", "application/json")
		rw := httptest.NewRecorder()
		ServeForTest(h.gw, rw, req)
		assertNo5xx(t, "validateCron", rw)
	})
}

// ---------------------------------------------------------------------
// Secrets, API keys, OAuth
// ---------------------------------------------------------------------

func FuzzPutSecretName(f *testing.F) {
	h := newFuzzHarness(f).withSecrets(f)
	body := []byte(`{"value":"v"}`)
	f.Add("ok_name")
	f.Add("")
	f.Add("..")
	f.Add("../etc/passwd")
	f.Add("a/b")
	f.Add("a b")
	f.Add("\x00")
	f.Add("name-é")
	f.Add(makeASCIIRepeat(129))
	f.Fuzz(func(t *testing.T, name string) {
		req, err := http.NewRequest("PUT", "/api/v1/secrets/"+escapePathSeg(name), bytes.NewReader(body))
		if err != nil {
			t.Skip()
		}
		req.Header.Set("Authorization", "Bearer "+h.token)
		req.Header.Set("Content-Type", "application/json")
		rw := httptest.NewRecorder()
		ServeForTest(h.gw, rw, req)
		assertNo5xx(t, "putSecret name", rw)
	})
}

func FuzzPutSecretBody(f *testing.F) {
	h := newFuzzHarness(f).withSecrets(f)
	f.Add([]byte(`{"value":"hello"}`))
	f.Add([]byte(`{"value":""}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(`{"value":null}`))
	f.Add([]byte(`{"value":42}`))
	f.Add([]byte(`{"value":" "}`))
	f.Add([]byte(`{"value":"` + makeASCIIRepeat(64*1024+10) + `"}`))
	f.Fuzz(func(t *testing.T, body []byte) {
		req := httptest.NewRequest("PUT", "/api/v1/secrets/fuzz_target", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+h.token)
		req.Header.Set("Content-Type", "application/json")
		rw := httptest.NewRecorder()
		ServeForTest(h.gw, rw, req)
		assertNo5xx(t, "putSecret body", rw)
	})
}

func FuzzIssueAPIKey(f *testing.F) {
	h := newFuzzHarness(f)
	f.Add([]byte(`{"subject":"alice","tenant":"t","workspace":"ws","roles":[{"name":"editor","permissions":["graph:run"]}]}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(`{"subject":"","tenant":"","workspace":""}`))
	f.Add([]byte(`{"subject":42}`))
	f.Add([]byte(`{"roles":"editor"}`))
	f.Add([]byte(`{"subject":"a","tenant":"t","workspace":"ws","expires_at":"not-a-date"}`))
	f.Add([]byte(`{"subject":"` + makeASCIIRepeat(4096) + `"}`))
	f.Fuzz(func(t *testing.T, body []byte) {
		req := httptest.NewRequest("POST", "/api/v1/admin/api-keys", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+h.token)
		req.Header.Set("Content-Type", "application/json")
		rw := httptest.NewRecorder()
		ServeForTest(h.gw, rw, req)
		assertNo5xx(t, "issueAPIKey", rw)
	})
}

func FuzzOAuthCallbackQuery(f *testing.F) {
	// OAuth is intentionally NOT configured: the unwired path returns
	// 501 and must never panic. The configured path requires a full
	// OAuthRegistry that's too heavy for fuzzing.
	h := newFuzzHarness(f)
	f.Add("state=abc&code=xyz")
	f.Add("")
	f.Add("state=&code=")
	f.Add("state=" + makeASCIIRepeat(2048))
	f.Add("error=access_denied&state=foo")
	f.Fuzz(func(t *testing.T, qs string) {
		req, err := http.NewRequest("GET", "/api/v1/oauth/slack/callback?"+qs, nil)
		if err != nil {
			t.Skip()
		}
		rw := httptest.NewRecorder()
		ServeForTest(h.gw, rw, req)
		assertNo5xx(t, "oauthCallback", rw)
	})
}

// ---------------------------------------------------------------------
// Webhook + Slack/GitHub events (HMAC ingress)
// ---------------------------------------------------------------------

func FuzzWebhookTriggerBody(f *testing.F) {
	// Per-graph secret auth: with no matching graph the listener 404s.
	// This target verifies the public listener handles untrusted bodies
	// cleanly before the auth check.
	wl := NewWebhookListener(&Service{Workspaces: MapWorkspaces{}})
	f.Add([]byte(``))
	f.Add([]byte(`not-json`))
	f.Add([]byte(`{"a":1}`))
	f.Add(bytes.Repeat([]byte{0x00}, 1024))
	f.Add(bytes.Repeat([]byte{0xff}, 1024))
	f.Fuzz(func(t *testing.T, body []byte) {
		req := httptest.NewRequest("POST", "/trigger/t/ws/g", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer not-the-real-secret")
		rw := httptest.NewRecorder()
		ServeWebhookForTest(wl, rw, req)
		assertNo5xx(t, "webhook trigger", rw)
	})
}

func FuzzSlackEventsBadHMAC(f *testing.F) {
	h := newFuzzHarness(f).withSlackEvents("shh-this-is-the-test-secret")
	frozen := time.Unix(1700000000, 0).UTC()
	f.Add([]byte(``), "v0=deadbeef")
	f.Add([]byte(`{"type":"url_verification","challenge":"x"}`), "")
	f.Add([]byte(`{"type":"url_verification","challenge":"x"}`), "garbage")
	f.Add([]byte(`{}`), "v0=")
	f.Fuzz(func(t *testing.T, body []byte, sig string) {
		req := httptest.NewRequest("POST", "/api/v1/events/slack/t", bytes.NewReader(body))
		req.Header.Set("X-Slack-Request-Timestamp", strconv.FormatInt(frozen.Unix(), 10))
		if sig != "" {
			req.Header.Set("X-Slack-Signature", sig)
		}
		req.Header.Set("Content-Type", "application/json")
		rw := httptest.NewRecorder()
		ServeForTest(h.gw, rw, req)
		assertNo5xx(t, "slack bad-hmac", rw)
	})
}

func FuzzSlackEventsValidHMAC(f *testing.F) {
	secret := "shh-this-is-the-test-secret"
	h := newFuzzHarness(f).withSlackEvents(secret)
	frozen := time.Unix(1700000000, 0).UTC()
	f.Add([]byte(`{"type":"url_verification","challenge":"x"}`))
	f.Add([]byte(`{"type":"event_callback","team_id":"T1","event":{"type":"app_mention","user":"U","text":"hi","channel":"C","ts":"1"}}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(`not-json`))
	f.Add([]byte(`{"type":"event_callback","event":null}`))
	f.Add([]byte(`{"type":"event_callback","event":"not-an-object"}`))
	f.Add([]byte(`{"type":"unknown_type"}`))
	f.Fuzz(func(t *testing.T, body []byte) {
		ts := frozen.Unix()
		req := httptest.NewRequest("POST", "/api/v1/events/slack/t", bytes.NewReader(body))
		req.Header.Set("X-Slack-Request-Timestamp", strconv.FormatInt(ts, 10))
		req.Header.Set("X-Slack-Signature", slackSig(secret, ts, body))
		req.Header.Set("Content-Type", "application/json")
		rw := httptest.NewRecorder()
		ServeForTest(h.gw, rw, req)
		assertNo5xx(t, "slack valid-hmac", rw)
	})
}

func FuzzGitHubEventsBadHMAC(f *testing.F) {
	h := newFuzzHarness(f).withGitHubEvents("test-webhook-secret")
	f.Add([]byte(`{}`), "sha256=deadbeef", "push")
	f.Add([]byte(``), "", "")
	f.Add([]byte(`garbage`), "garbage", "push")
	f.Fuzz(func(t *testing.T, body []byte, sig, event string) {
		req := httptest.NewRequest("POST", "/api/v1/events/github/t", bytes.NewReader(body))
		if sig != "" {
			req.Header.Set("X-Hub-Signature-256", sig)
		}
		if event != "" {
			req.Header.Set("X-GitHub-Event", event)
		}
		req.Header.Set("Content-Type", "application/json")
		rw := httptest.NewRecorder()
		ServeForTest(h.gw, rw, req)
		assertNo5xx(t, "github bad-hmac", rw)
	})
}

func FuzzGitHubEventsValidHMAC(f *testing.F) {
	secret := "test-webhook-secret"
	h := newFuzzHarness(f).withGitHubEvents(secret)
	f.Add([]byte(`{}`), "ping")
	f.Add([]byte(`{"ref":"refs/heads/main","commits":[]}`), "push")
	f.Add([]byte(`{"action":"opened","pull_request":{}}`), "pull_request")
	f.Add([]byte(`{"action":"closed","pull_request":null}`), "pull_request")
	f.Add([]byte(`not-json`), "push")
	f.Add([]byte(`null`), "push")
	f.Add([]byte(`{"commits":[null,null,null]}`), "push")
	f.Add([]byte(`{"action":42}`), "pull_request")
	f.Fuzz(func(t *testing.T, body []byte, event string) {
		req := httptest.NewRequest("POST", "/api/v1/events/github/t", bytes.NewReader(body))
		req.Header.Set("X-Hub-Signature-256", ghSig(secret, body))
		req.Header.Set("X-GitHub-Event", event)
		req.Header.Set("X-GitHub-Delivery", "fuzz")
		req.Header.Set("Content-Type", "application/json")
		rw := httptest.NewRecorder()
		ServeForTest(h.gw, rw, req)
		assertNo5xx(t, "github valid-hmac", rw)
	})
}

// ---------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------

func makeASCIIRepeat(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = 'x'
	}
	return string(b)
}

// escapePathSeg percent-encodes a fuzzer-supplied path segment so the
// request line stays valid. The mux decodes once before PathValue, so
// the handler still sees the raw fuzzed bytes.
func escapePathSeg(s string) string {
	var b bytes.Buffer
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') ||
			c == '-' || c == '_' || c == '.' || c == '~' {
			b.WriteByte(c)
			continue
		}
		fmt.Fprintf(&b, "%%%02X", c)
	}
	return b.String()
}

func slackSig(secret string, ts int64, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(mac, "v0:%d:", ts)
	mac.Write(body)
	return "v0=" + hex.EncodeToString(mac.Sum(nil))
}

func ghSig(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}
