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
		Nodes:    []core.Node{{ID: "a", Module: "sleep", Params: map[string]any{"ms": 1}}},
		Triggers: []core.GraphTrigger{{Type: "webhook", Secret: "s3cr3t"}},
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
		Nodes:    []core.Node{{ID: "a", Module: "sleep", Params: map[string]any{"ms": 1}}},
		Triggers: []core.GraphTrigger{{Type: "webhook", Secret: "correct"}},
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
		Nodes: []core.Node{{ID: "a", Module: "sleep", Params: map[string]any{"ms": 1}}},
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
