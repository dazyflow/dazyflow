package e2e

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"git.sr.ht/~klahr/hazy-flow/auth"
	"git.sr.ht/~klahr/hazy-flow/core"
	"git.sr.ht/~klahr/hazy-flow/daemon"
	"git.sr.ht/~klahr/hazy-flow/engine"
	"git.sr.ht/~klahr/hazy-flow/engine/jobstore"
	_ "git.sr.ht/~klahr/hazy-flow/modules"
	"git.sr.ht/~klahr/hazy-flow/workspace"
)

// TestSecrets_E2E_AuthorizationHeader exercises the full chain:
//   - graph stores secret reference (env://API_KEY) in params
//   - hzd engine resolves it just before Execute
//   - http_request sends the resolved value as the Authorization header
//   - JobStore retains the unresolved reference, never the value
func TestSecrets_E2E_AuthorizationHeader(t *testing.T) {
	t.Setenv("API_KEY", "secret-token-12345")

	// Backing server that captures the Authorization header.
	var captured string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = r.Header.Get("Authorization")
		w.WriteHeader(200)
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	// Build the stack with both providers configured.
	ks := auth.NewMemKeyStore()
	role := core.Role{Name: "editor", Permissions: []core.Permission{
		core.PermGraphRun, core.PermGraphEdit, core.PermGraphAdmin,
	}}
	_, _, _ = auth.IssueAPIKey(ks, t.Context(), "k", "t", "ws", "u", []core.Role{role})
	p := core.Principal{Subject: "u", Tenant: "t", Workspace: "ws", Roles: []core.Role{role}}

	wsStore, _ := workspace.OpenFS("")
	jobs := jobstore.NewMemory()
	bus := daemon.NewMemoryBus()

	builtin := daemon.NewBuiltinProvider()
	builtin.Set("backup-token", "from-builtin-store")

	eng := &engine.Engine{
		Resolver: &engine.NodeResolver{Native: engine.Default},
		Secrets: map[string]core.SecretProvider{
			"env":     daemon.EnvProvider{},
			"builtin": builtin,
		},
	}
	svc := &daemon.Service{
		Auth:       auth.Chain{&auth.APIKeyAuthenticator{Store: ks}},
		Workspaces: daemon.MapWorkspaces{"t/ws": wsStore},
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

	g := core.Graph{
		ID: "fetch", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{{
			ID: "call", Module: "http_request",
			Params: map[string]any{
				"url": srv.URL,
				"headers": map[string]any{
					"Authorization": "env://API_KEY", // unresolved reference
				},
				"allow_private_networks": true,
			},
		}},
	}
	runID, err := svc.SubmitGraph(t.Context(), p, g)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	terminal := waitForFire(t, bus, runID, 5*time.Second)
	if terminal != core.JobStatusSucceeded {
		t.Fatalf("status = %q, want succeeded", terminal)
	}

	// The server must have received the RESOLVED value.
	if captured != "secret-token-12345" {
		t.Errorf("Authorization header at server = %q, want secret-token-12345", captured)
	}

	// The JobStore's graph-record must still contain the UNRESOLVED
	// reference — the engine resolves only the in-memory Job, never
	// the persisted graph payload. This guarantees the audit trail
	// shows what the user wrote ("env://API_KEY"), not the cleartext.
	graphRec, err := jobs.Get(t.Context(), runID)
	if err != nil {
		t.Fatalf("get graph-record: %v", err)
	}
	if !strings.Contains(string(graphRec.GraphPayload), "env://API_KEY") {
		t.Errorf("graph payload should still contain env://API_KEY; got %s",
			string(graphRec.GraphPayload))
	}
	if strings.Contains(string(graphRec.GraphPayload), "secret-token-12345") {
		t.Error("graph payload leaked resolved secret value!")
	}
}

func TestSecrets_E2E_MissingSecretFailsNodeCleanly(t *testing.T) {
	// Don't set the env var — node should fail with a clear error, not
	// silently send an empty Authorization header.
	ks := auth.NewMemKeyStore()
	role := core.Role{Name: "editor", Permissions: []core.Permission{
		core.PermGraphRun, core.PermGraphEdit, core.PermGraphAdmin,
	}}
	_, _, _ = auth.IssueAPIKey(ks, t.Context(), "k", "t", "ws", "u", []core.Role{role})
	p := core.Principal{Subject: "u", Tenant: "t", Workspace: "ws", Roles: []core.Role{role}}

	wsStore, _ := workspace.OpenFS("")
	jobs := jobstore.NewMemory()
	bus := daemon.NewMemoryBus()
	eng := &engine.Engine{
		Resolver: &engine.NodeResolver{Native: engine.Default},
		Secrets: map[string]core.SecretProvider{
			"env": daemon.EnvProvider{},
		},
	}
	svc := &daemon.Service{
		Auth:       auth.Chain{&auth.APIKeyAuthenticator{Store: ks}},
		Workspaces: daemon.MapWorkspaces{"t/ws": wsStore},
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

	g := core.Graph{
		ID: "bad", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{{
			ID: "call", Module: "http_request",
			Params: map[string]any{
				"url":     "http://example.com",
				"headers": map[string]any{"Authorization": "env://DEFINITELY_NOT_SET_99"},
			},
		}},
	}
	runID, _ := svc.SubmitGraph(t.Context(), p, g)
	terminal := waitForFire(t, bus, runID, 5*time.Second)
	if terminal != core.JobStatusFailed {
		t.Fatalf("status=%q, want failed", terminal)
	}
	rec, _ := jobs.Get(t.Context(), daemon.NodeJobID(runID, "call"))
	if rec.Result == nil || rec.Result.Error == nil {
		t.Fatalf("expected error result; got %+v", rec.Result)
	}
	if !strings.Contains(rec.Result.Error.Message, "DEFINITELY_NOT_SET_99") {
		t.Errorf("error message = %q; expected to mention the missing key", rec.Result.Error.Message)
	}
}

func waitForFire(t *testing.T, bus *daemon.MemoryBus, runID string, timeout time.Duration) core.JobStatus {
	t.Helper()
	events, cancel := bus.Subscribe(runID)
	defer cancel()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		select {
		case <-deadline.C:
			t.Fatalf("timed out waiting for terminal on %s", runID)
		case ev, ok := <-events:
			if !ok {
				t.Fatalf("event channel closed")
			}
			if ev.Terminal != nil {
				return ev.Terminal.Status
			}
		}
	}
}
