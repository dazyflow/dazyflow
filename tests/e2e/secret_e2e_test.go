// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package e2e

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dazyflow/dazyflow/auth"
	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/daemon"
	_ "github.com/dazyflow/dazyflow/drops"
	"github.com/dazyflow/dazyflow/engine"
	"github.com/dazyflow/dazyflow/engine/jobstore"
	"github.com/dazyflow/dazyflow/workspace"
)

// TestSecrets_E2E_AuthorizationHeader exercises the full chain:
//   - graph stores secret reference (builtin://API_KEY) in params
//   - dzd engine resolves it just before Execute
//   - http_request sends the resolved value as the Authorization header
//   - JobStore retains the unresolved reference, never the value
func TestSecrets_E2E_AuthorizationHeader(t *testing.T) {
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
	_, _, _ = auth.IssueAPIKey(ks, t.Context(), "k", "t", "ws", "u", []core.Role{role}, nil)
	p := core.Principal{Subject: "u", Tenant: "t", Workspace: "ws", Roles: []core.Role{role}}

	wsStore, _ := workspace.OpenFS("")
	jobs := jobstore.NewMemory()
	bus := daemon.NewMemoryBus()

	builtin := daemon.NewBuiltinProvider()
	builtin.Set("backup-token", "from-builtin-store")
	builtin.Set("API_KEY", "secret-token-12345")

	eng := &engine.Engine{
		Resolver: &engine.NodeResolver{Native: engine.Default},
		Secrets: map[string]core.SecretProvider{
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
					"Authorization": "builtin://API_KEY", // unresolved reference
				},
				"allow_private_networks": true,
			},
		}},
	}
	runID, err := svc.SubmitGraph(t.Context(), p, g)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	terminal := waitForFire(t, jobs, runID)
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
	// shows what the user wrote ("builtin://API_KEY"), not the cleartext.
	graphRec, err := jobs.Get(t.Context(), runID)
	if err != nil {
		t.Fatalf("get graph-record: %v", err)
	}
	if !strings.Contains(string(graphRec.GraphPayload), "builtin://API_KEY") {
		t.Errorf("graph payload should still contain builtin://API_KEY; got %s",
			string(graphRec.GraphPayload))
	}
	if strings.Contains(string(graphRec.GraphPayload), "secret-token-12345") {
		t.Error("graph payload leaked resolved secret value!")
	}
}

func TestSecrets_E2E_MissingSecretFailsNodeCleanly(t *testing.T) {
	// Don't store the secret — node should fail with a clear error, not
	// silently send an empty Authorization header.
	ks := auth.NewMemKeyStore()
	role := core.Role{Name: "editor", Permissions: []core.Permission{
		core.PermGraphRun, core.PermGraphEdit, core.PermGraphAdmin,
	}}
	_, _, _ = auth.IssueAPIKey(ks, t.Context(), "k", "t", "ws", "u", []core.Role{role}, nil)
	p := core.Principal{Subject: "u", Tenant: "t", Workspace: "ws", Roles: []core.Role{role}}

	wsStore, _ := workspace.OpenFS("")
	jobs := jobstore.NewMemory()
	bus := daemon.NewMemoryBus()
	eng := &engine.Engine{
		Resolver: &engine.NodeResolver{Native: engine.Default},
		Secrets: map[string]core.SecretProvider{
			"builtin": daemon.NewBuiltinProvider(), // empty: the key is absent
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
				"headers": map[string]any{"Authorization": "builtin://DEFINITELY_NOT_SET_99"},
			},
		}},
	}
	runID, _ := svc.SubmitGraph(t.Context(), p, g)
	terminal := waitForFire(t, jobs, runID)
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

// waitForFire polls the job store until runID reaches a terminal status and
// returns it. It deliberately polls the store rather than subscribing to the
// bus: a subscriber that registers AFTER the run already finished misses the
// terminal event and would then hang for the whole ceiling — the
// subscribe-after-finish race that WaitGraph reconciles in production
// (daemon/service.go re-reads the record post-subscribe). Polling has no such
// window, so it's race-free even when CPU contention reorders scheduling.
func waitForFire(t *testing.T, store core.JobStore, runID string) core.JobStatus {
	t.Helper()
	var status core.JobStatus
	waitFor(t, "run "+runID+" to reach a terminal status", func() bool {
		rec, err := store.Get(context.Background(), runID)
		if err != nil {
			return false
		}
		status = rec.Status
		return core.IsTerminalStatus(status)
	})
	return status
}
