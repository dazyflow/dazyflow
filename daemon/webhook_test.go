package daemon_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"context"
	"git.sr.ht/~klahr/hazyflow/auth"
	"git.sr.ht/~klahr/hazyflow/core"
	"git.sr.ht/~klahr/hazyflow/daemon"
	"git.sr.ht/~klahr/hazyflow/engine"
	"git.sr.ht/~klahr/hazyflow/engine/jobstore"
	_ "git.sr.ht/~klahr/hazyflow/drops"
	"git.sr.ht/~klahr/hazyflow/workspace"
)

func startWebhookHarness(t *testing.T) (*daemon.Service, *daemon.WebhookListener, core.JobStore, *daemon.MemoryBus, *workspace.Store) {
	t.Helper()
	ks := auth.NewMemKeyStore()
	wsStore, _ := workspace.OpenFS("")
	jobs := jobstore.NewMemory()
	bus := daemon.NewMemoryBus()
	eng := &engine.Engine{Resolver: &engine.NodeResolver{Native: engine.Default}}
	svc := &daemon.Service{
		Auth:       auth.Chain{&auth.APIKeyAuthenticator{Store: ks}},
		Workspaces: daemon.MapWorkspaces{"acme/ws1": wsStore},
		Jobs:       jobs,
		Engine:     eng,
		Bus:        bus,
	}
	wctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	w := daemon.NewWorker(daemon.WorkerConfig{
		ID: "w", PollInterval: 5 * time.Millisecond, MaxRetries: 1,
	}, jobs, eng, bus)
	go func() { _ = w.Run(wctx) }()
	wh := daemon.NewWebhookListener(svc)
	return svc, wh, jobs, bus, wsStore
}

func TestWebhook_FiresWithValidSecret(t *testing.T) {
	_, wh, jobs, bus, wsStore := startWebhookHarness(t)
	g := core.Graph{
		ID: "wh-ok", Tenant: "acme", Workspace: "ws1",
		Nodes: []core.Node{
			{ID: "in", Module: "webhook_input", Params: map[string]any{"secret": "s3cr3t"}},
			{ID: "a", Module: "delay", Params: map[string]any{"ms": 1}},
		},
	}
	if _, err := wsStore.Save(g, "test"); err != nil {
		t.Fatalf("save: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Wrap the handler so we can dispatch into the listener via
		// its public handle method through the same mux pattern.
		http.NewServeMux().ServeHTTP(w, r) // placeholder; we go through the real listener below
	}))
	srv.Close()

	// Stand the listener up on a fresh httptest server using a thin
	// adapter. The simplest: use httptest.NewServer with a wrap that
	// routes /trigger/* to wh's handler via the same mux it builds.
	mux := http.NewServeMux()
	mux.HandleFunc("/trigger/", func(rw http.ResponseWriter, r *http.Request) {
		// Reach into the listener through a tiny inline handler that
		// shares its logic. We do this via the exposed Serve method on
		// the listener — but Serve binds a real port. So instead we
		// reach the handler indirectly by serving the listener's mux
		// once a request comes in.
		callPrivateHandler(t, wh, rw, r)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	req, _ := http.NewRequest("POST", ts.URL+"/trigger/acme/ws1/wh-ok",
		bytes.NewReader([]byte(`{"event":"hello"}`)))
	req.Header.Set("Authorization", "Bearer s3cr3t")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d, want 202; body=%s", resp.StatusCode, body)
	}
	var out struct {
		JobID string `json:"job_id"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if out.JobID == "" {
		t.Fatal("response missing job_id")
	}

	// Wait for the graph to actually run.
	terminal := waitForTerminalEvent(t, bus, jobs, out.JobID, 5*time.Second)
	if terminal.Status != core.JobStatusSucceeded {
		t.Errorf("graph status = %q", terminal.Status)
	}
	rec, _ := jobs.Get(t.Context(), out.JobID)
	if rec.Tenant != "acme" {
		t.Errorf("job tenant = %q", rec.Tenant)
	}
}

func TestWebhook_RejectsBadSecret(t *testing.T) {
	_, wh, _, _, wsStore := startWebhookHarness(t)
	_, _ = wsStore.Save(core.Graph{
		ID: "wh-secret", Tenant: "acme", Workspace: "ws1",
		Nodes: []core.Node{
			{ID: "in", Module: "webhook_input", Params: map[string]any{"secret": "correct"}},
			{ID: "a", Module: "delay", Params: map[string]any{"ms": 1}},
		},
	}, "test")

	mux := http.NewServeMux()
	mux.HandleFunc("/trigger/", func(rw http.ResponseWriter, r *http.Request) {
		callPrivateHandler(t, wh, rw, r)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	req, _ := http.NewRequest("POST", ts.URL+"/trigger/acme/ws1/wh-secret", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status=%d, want 401", resp.StatusCode)
	}
}

func TestWebhook_UnknownGraph(t *testing.T) {
	_, wh, _, _, _ := startWebhookHarness(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/trigger/", func(rw http.ResponseWriter, r *http.Request) {
		callPrivateHandler(t, wh, rw, r)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	req, _ := http.NewRequest("POST", ts.URL+"/trigger/acme/ws1/missing", nil)
	req.Header.Set("Authorization", "Bearer whatever")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status=%d, want 404", resp.StatusCode)
	}
}

func TestWebhook_GraphWithoutWebhookTriggerRejected(t *testing.T) {
	_, wh, _, _, wsStore := startWebhookHarness(t)
	// Graph exists but has no webhook trigger.
	_, _ = wsStore.Save(core.Graph{
		ID: "no-trigger", Tenant: "acme", Workspace: "ws1",
		Nodes: []core.Node{{ID: "a", Module: "delay", Params: map[string]any{"ms": 1}}},
	}, "test")

	mux := http.NewServeMux()
	mux.HandleFunc("/trigger/", func(rw http.ResponseWriter, r *http.Request) {
		callPrivateHandler(t, wh, rw, r)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	req, _ := http.NewRequest("POST", ts.URL+"/trigger/acme/ws1/no-trigger", nil)
	req.Header.Set("Authorization", "Bearer x")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status=%d, want 404", resp.StatusCode)
	}
}

func TestWebhook_RejectsGET(t *testing.T) {
	_, wh, _, _, _ := startWebhookHarness(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/trigger/", func(rw http.ResponseWriter, r *http.Request) {
		callPrivateHandler(t, wh, rw, r)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/trigger/acme/ws1/anything")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("status=%d, want 405", resp.StatusCode)
	}
}

func TestWebhook_MalformedPath(t *testing.T) {
	_, wh, _, _, _ := startWebhookHarness(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/trigger/", func(rw http.ResponseWriter, r *http.Request) {
		callPrivateHandler(t, wh, rw, r)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	for _, p := range []string{"/trigger/", "/trigger/just-one", "/trigger/two/parts"} {
		t.Run(p, func(t *testing.T) {
			req, _ := http.NewRequest("POST", ts.URL+p, nil)
			req.Header.Set("Authorization", "Bearer x")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("do: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest && resp.StatusCode != http.StatusNotFound {
				t.Errorf("path=%q status=%d, want 400 or 404", p, resp.StatusCode)
			}
		})
	}
}

// TestWebhook_BodyParsingByContentType pins the Content-Type-driven
// decoding: JSON and form-urlencoded both become a field-addressable
// map (so ${trigger.body.email} works), text stays a string, and an
// unknown MIME passes through as raw bytes.
func TestWebhook_BodyParsingByContentType(t *testing.T) {
	newReq := func(ct string) *http.Request {
		r, _ := http.NewRequest("POST", "/trigger/acme/ws1/g", nil)
		if ct != "" {
			r.Header.Set("Content-Type", ct)
		}
		return r
	}

	t.Run("urlencoded becomes a map", func(t *testing.T) {
		seed := daemon.BuildWebhookSeedForTest(
			[]byte("name=Anna&email=anna%40example.com"),
			newReq("application/x-www-form-urlencoded"),
		)
		body, ok := seed.Output["body"].Inline.(map[string]any)
		if !ok {
			t.Fatalf("body = %T, want map[string]any", seed.Output["body"].Inline)
		}
		if body["name"] != "Anna" || body["email"] != "anna@example.com" {
			t.Errorf("parsed body = %v", body)
		}
	})

	t.Run("urlencoded with charset param still parses", func(t *testing.T) {
		seed := daemon.BuildWebhookSeedForTest(
			[]byte("x=1"),
			newReq("application/x-www-form-urlencoded; charset=utf-8"),
		)
		if body, ok := seed.Output["body"].Inline.(map[string]any); !ok || body["x"] != "1" {
			t.Errorf("body = %v (%T)", seed.Output["body"].Inline, seed.Output["body"].Inline)
		}
	})

	t.Run("json becomes a map", func(t *testing.T) {
		seed := daemon.BuildWebhookSeedForTest(
			[]byte(`{"event":"hello"}`), newReq("application/json"))
		if body, ok := seed.Output["body"].Inline.(map[string]any); !ok || body["event"] != "hello" {
			t.Errorf("body = %v (%T)", seed.Output["body"].Inline, seed.Output["body"].Inline)
		}
	})

	t.Run("text stays a string", func(t *testing.T) {
		seed := daemon.BuildWebhookSeedForTest([]byte("hi there"), newReq("text/plain"))
		if seed.Output["body"].Inline != "hi there" {
			t.Errorf("body = %v, want string", seed.Output["body"].Inline)
		}
	})

	t.Run("unknown MIME stays raw bytes", func(t *testing.T) {
		raw := []byte{0x00, 0x01, 0x02}
		seed := daemon.BuildWebhookSeedForTest(raw, newReq("application/octet-stream"))
		if _, ok := seed.Output["body"].Inline.([]byte); !ok {
			t.Errorf("body = %T, want []byte", seed.Output["body"].Inline)
		}
	})
}

// TestWebhook_BodyParsing_EdgeCases covers the awkward inputs real
// senders produce: case-variant Content-Type (HTTP media types are
// case-insensitive — RFC 9110 §8.3.1), charset params on a cased type,
// malformed url-encoding, and multi-value headers.
func TestWebhook_BodyParsing_EdgeCases(t *testing.T) {
	newReq := func(ct string) *http.Request {
		r, _ := http.NewRequest("POST", "/trigger/acme/ws1/g", nil)
		if ct != "" {
			r.Header.Set("Content-Type", ct)
		}
		return r
	}

	// Content-Type is case-insensitive: these MUST parse the same as
	// their lowercase forms, not fall through to raw bytes.
	jsonMapCases := []struct {
		name, ct, body string
	}{
		{"json upper subtype", "Application/JSON", `{"event":"hi"}`},
		{"json all caps", "APPLICATION/JSON", `{"event":"hi"}`},
		{"json cased + charset", "application/JSON; charset=utf-8", `{"event":"hi"}`},
		{"json mixed", "ApPlIcAtIoN/jSoN", `{"event":"hi"}`},
	}
	for _, tc := range jsonMapCases {
		t.Run(tc.name, func(t *testing.T) {
			seed := daemon.BuildWebhookSeedForTest([]byte(tc.body), newReq(tc.ct))
			body, ok := seed.Output["body"].Inline.(map[string]any)
			if !ok {
				t.Fatalf("Content-Type %q: body = %T, want parsed map[string]any (case-insensitive media type)", tc.ct, seed.Output["body"].Inline)
			}
			if body["event"] != "hi" {
				t.Errorf("parsed body = %v", body)
			}
		})
	}

	t.Run("urlencoded uppercase parses to map", func(t *testing.T) {
		seed := daemon.BuildWebhookSeedForTest([]byte("a=1&b=2"), newReq("APPLICATION/X-WWW-FORM-URLENCODED"))
		body, ok := seed.Output["body"].Inline.(map[string]any)
		if !ok || body["a"] != "1" || body["b"] != "2" {
			t.Fatalf("uppercase urlencoded body = %#v (%T), want parsed map", seed.Output["body"].Inline, seed.Output["body"].Inline)
		}
	})

	t.Run("text uppercase stays string", func(t *testing.T) {
		seed := daemon.BuildWebhookSeedForTest([]byte("hello"), newReq("TEXT/PLAIN"))
		if seed.Output["body"].Inline != "hello" {
			t.Errorf("TEXT/PLAIN body = %#v, want string", seed.Output["body"].Inline)
		}
	})

	t.Run("malformed urlencoding falls back to string", func(t *testing.T) {
		// %ZZ is not valid percent-encoding; url.ParseQuery errors and we
		// hand the graph the raw text rather than failing the trigger.
		seed := daemon.BuildWebhookSeedForTest([]byte("a=%ZZ"), newReq("application/x-www-form-urlencoded"))
		if seed.Output["body"].Inline != "a=%ZZ" {
			t.Errorf("malformed urlencoded body = %#v, want raw string fallback", seed.Output["body"].Inline)
		}
	})

	t.Run("multi-value header keeps first", func(t *testing.T) {
		r, _ := http.NewRequest("POST", "/trigger/acme/ws1/g", nil)
		r.Header.Add("X-Custom", "first")
		r.Header.Add("X-Custom", "second")
		seed := daemon.BuildWebhookSeedForTest([]byte("x"), r)
		hdrs := seed.Output["headers"].Inline.(map[string]any)
		if hdrs["X-Custom"] != "first" {
			t.Errorf("X-Custom = %v, want \"first\" (first value)", hdrs["X-Custom"])
		}
	})

	// The headers port must NOT expose the credential headers: the
	// webhook bearer secret (Authorization) and any Cookie. Otherwise a
	// downstream node that forwards ${trigger.headers} to an external
	// service would leak the per-graph secret.
	t.Run("credential headers stripped from headers port", func(t *testing.T) {
		r, _ := http.NewRequest("POST", "/trigger/acme/ws1/g", nil)
		r.Header.Set("Authorization", "Bearer s3cr3t")
		r.Header.Set("Cookie", "session=abc")
		r.Header.Set("X-Keep", "ok")
		seed := daemon.BuildWebhookSeedForTest([]byte("x"), r)
		hdrs := seed.Output["headers"].Inline.(map[string]any)
		if _, leaked := hdrs["Authorization"]; leaked {
			t.Errorf("Authorization leaked into headers port: %v", hdrs["Authorization"])
		}
		if _, leaked := hdrs["Cookie"]; leaked {
			t.Errorf("Cookie leaked into headers port: %v", hdrs["Cookie"])
		}
		if hdrs["X-Keep"] != "ok" {
			t.Errorf("non-credential header dropped: X-Keep=%v", hdrs["X-Keep"])
		}
	})
}

// FuzzBuildWebhookSeed asserts the seed builder's invariants on
// arbitrary bodies + Content-Types: it never panics, always emits both
// the body and headers ports, and headers are always a JSON object.
func FuzzBuildWebhookSeed(f *testing.F) {
	f.Add("application/json", []byte(`{"a":1}`))
	f.Add("application/json", []byte(`{not json`))
	f.Add("application/x-www-form-urlencoded", []byte("a=1&b=%ZZ"))
	f.Add("text/plain", []byte("hi"))
	f.Add("Application/JSON", []byte(`{"a":1}`))
	f.Add("", []byte{})
	f.Add("application/octet-stream", []byte{0x00, 0xff, 0x01})
	f.Add("application/json; charset=utf-8", []byte(`[1,2,3]`))

	f.Fuzz(func(t *testing.T, ct string, body []byte) {
		r, err := http.NewRequest("POST", "/trigger/acme/ws1/g", nil)
		if err != nil {
			t.Skip()
		}
		if ct != "" {
			r.Header.Set("Content-Type", ct)
		}
		seed := daemon.BuildWebhookSeedForTest(body, r) // must not panic
		if _, ok := seed.Output["body"]; !ok {
			t.Fatalf("missing body port for ct=%q", ct)
		}
		hdr, ok := seed.Output["headers"]
		if !ok {
			t.Fatalf("missing headers port for ct=%q", ct)
		}
		if _, ok := hdr.Inline.(map[string]any); !ok {
			t.Fatalf("headers port not a map: %T", hdr.Inline)
		}
	})
}

// TestWebhook_BodyLimit asserts the MaxBodyBytes cap: a body exactly at
// the limit is accepted, one byte over is rejected with 413.
func TestWebhook_BodyLimit(t *testing.T) {
	_, wh, _, _, wsStore := startWebhookHarness(t)
	wh.MaxBodyBytes = 16 // tiny cap so we don't shovel a megabyte
	_, _ = wsStore.Save(core.Graph{
		ID: "lim", Tenant: "acme", Workspace: "ws1",
		Nodes: []core.Node{
			{ID: "in", Module: "webhook_input", Params: map[string]any{"secret": "s"}},
			{ID: "a", Module: "delay", Params: map[string]any{"ms": 1}},
		},
	}, "test")
	mux := http.NewServeMux()
	mux.HandleFunc("/trigger/", func(rw http.ResponseWriter, r *http.Request) {
		callPrivateHandler(t, wh, rw, r)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	post := func(n int) int {
		req, _ := http.NewRequest("POST", ts.URL+"/trigger/acme/ws1/lim", bytes.NewReader(bytes.Repeat([]byte("a"), n)))
		req.Header.Set("Authorization", "Bearer s")
		req.Header.Set("Content-Type", "text/plain")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("post %d: %v", n, err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if code := post(16); code == http.StatusRequestEntityTooLarge {
		t.Errorf("body at limit (16) rejected with 413, want accepted")
	}
	if code := post(17); code != http.StatusRequestEntityTooLarge {
		t.Errorf("body over limit (17) = %d, want 413", code)
	}
}

// TestWebhook_DisabledGraphRejected — a paused flow's webhook returns
// 403 flow_disabled (not 404), so a caller like Stripe sees "endpoint
// known but off" and doesn't treat it as an unknown-URL retry.
func TestWebhook_DisabledGraphRejected(t *testing.T) {
	_, wh, _, _, wsStore := startWebhookHarness(t)
	_, _ = wsStore.Save(core.Graph{
		ID: "off", Tenant: "acme", Workspace: "ws1", Disabled: true,
		Nodes: []core.Node{{ID: "in", Module: "webhook_input", Params: map[string]any{"secret": "s"}}},
	}, "test")
	mux := http.NewServeMux()
	mux.HandleFunc("/trigger/", func(rw http.ResponseWriter, r *http.Request) {
		callPrivateHandler(t, wh, rw, r)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	req, _ := http.NewRequest("POST", ts.URL+"/trigger/acme/ws1/off", strings.NewReader("x"))
	req.Header.Set("Authorization", "Bearer s")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("disabled webhook = %d, want 403", resp.StatusCode)
	}
}

// TestWebhook_MissingAuthRejected — a request with no Authorization
// header at all (not just a wrong one) is rejected with 401.
func TestWebhook_MissingAuthRejected(t *testing.T) {
	_, wh, _, _, wsStore := startWebhookHarness(t)
	_, _ = wsStore.Save(core.Graph{
		ID: "needauth", Tenant: "acme", Workspace: "ws1",
		Nodes: []core.Node{{ID: "in", Module: "webhook_input", Params: map[string]any{"secret": "s"}}},
	}, "test")
	mux := http.NewServeMux()
	mux.HandleFunc("/trigger/", func(rw http.ResponseWriter, r *http.Request) {
		callPrivateHandler(t, wh, rw, r)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	req, _ := http.NewRequest("POST", ts.URL+"/trigger/acme/ws1/needauth", strings.NewReader("x"))
	// No Authorization header set at all.
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("missing auth = %d, want 401", resp.StatusCode)
	}
}

// callPrivateHandler reaches into the listener and calls the same
// handler Serve installs onto its mux. We expose this indirection via
// a small accessor below; doing it through Serve would require binding
// a real OS port and tearing it down, which is what we're avoiding.
func callPrivateHandler(t *testing.T, wh *daemon.WebhookListener, rw http.ResponseWriter, r *http.Request) {
	t.Helper()
	daemon.ServeWebhookForTest(wh, rw, r)
}

// silence unused import lint when this file is compiled in isolation
var _ = strings.HasPrefix
