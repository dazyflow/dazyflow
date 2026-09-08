// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package chaos

import (
	"context"
	"encoding/json"
	"log"
	"os"
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

func TestMain(m *testing.M) {
	if os.Getenv("DAZYFLOW_CHAOS") == "" {
		os.Exit(0)
	}
	os.Exit(m.Run())
}

type harness struct {
	svc  *daemon.Service
	jobs core.JobStore
	ws   *workspace.Store
	p    core.Principal
	t    *testing.T
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	return newHarnessLogging(t, nil)
}

func newHarnessLogging(t *testing.T, logger *log.Logger) *harness {
	t.Helper()
	ks := auth.NewMemKeyStore()
	role := core.Role{Name: "editor", Permissions: []core.Permission{
		core.PermGraphRun, core.PermGraphEdit, core.PermGraphAdmin,
	}}
	_, key, err := auth.IssueAPIKey(ks, t.Context(), "qa", "acme", "ws1", "qa@acme", []core.Role{role}, nil)
	if err != nil {
		t.Fatalf("issue key: %v", err)
	}
	wsStore, err := workspace.OpenFS("")
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	jobs := jobstore.NewMemory()
	bus := daemon.NewMemoryBus()
	eng := &engine.Engine{Resolver: &engine.NodeResolver{Native: engine.Default}}
	svc := &daemon.Service{
		Auth:          auth.Chain{&auth.APIKeyAuthenticator{Store: ks}},
		Workspaces:    daemon.MapWorkspaces{"acme/ws1": wsStore},
		Jobs:          jobs,
		Engine:        eng,
		Bus:           bus,
		MaxGraphNodes: 1000,
		MaxGraphEdges: 5000,
	}
	wctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	w := daemon.NewWorker(daemon.WorkerConfig{
		ID: "chaos-worker", PollInterval: 5 * time.Millisecond, MaxRetries: 1,
		Logger: logger,
	}, jobs, eng, bus)
	w.SubGraphRunner = svc
	go func() { _ = w.Run(wctx) }()

	p, err := svc.Authenticate(t.Context(), key)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	return &harness{svc: svc, jobs: jobs, ws: wsStore, p: p, t: t}
}

func graph(id string, nodes []core.Node, edges []core.Edge) core.Graph {
	return core.Graph{ID: id, Tenant: "acme", Workspace: "ws1", Nodes: nodes, Edges: edges}
}

func textNode(id, val string) core.Node {
	return core.Node{ID: id, Module: "text", Params: map[string]any{"text": val}}
}
func b64Node(id string) core.Node {
	return core.Node{ID: id, Module: "base64", Params: map[string]any{"mode": "encode"}}
}

func firstLine(err error) string {
	if err == nil {
		return ""
	}
	s, _, _ := strings.Cut(err.Error(), "\n")
	return s
}

// statusHung marks a run that never reached a terminal status in budget.
const statusHung core.JobStatus = "HUNG"

func (h *harness) submit(g core.Graph, budget time.Duration) (core.JobStatus, error) {
	h.t.Helper()
	runID, err := h.svc.SubmitGraph(h.t.Context(), h.p, g)
	if err != nil {
		return "", err
	}
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		rec, err := h.jobs.Get(context.Background(), runID)
		if err == nil && core.IsTerminalStatus(rec.Status) {
			return rec.Status, nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	return statusHung, nil
}

func (h *harness) save(g core.Graph) {
	h.t.Helper()
	commit, err := h.ws.Save(g, "qa")
	if err != nil {
		h.t.Fatalf("save %s: %v", g.ID, err)
	}
	if err := h.ws.PromoteToEnvironment(g.ID, workspace.PublishedEnv, commit); err != nil {
		h.t.Fatalf("publish %s: %v", g.ID, err)
	}
}

func (h *harness) storedBytes(graphID string) int {
	recs, err := h.jobs.ListByGraph(context.Background(), graphID)
	if err != nil {
		return -1
	}
	total := 0
	for _, r := range recs {
		b, _ := json.Marshal(r)
		total += len(b)
	}
	return total
}

func (h *harness) countRuns() int {
	recs, err := h.jobs.ListGraphRuns(context.Background(), core.ListGraphRunsOpts{Limit: 1000000})
	if err != nil {
		return -1
	}
	return len(recs)
}

func withinDeadline(t *testing.T, what string, budget time.Duration, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { defer close(done); fn() }()
	select {
	case <-done:
	case <-time.After(budget):
		t.Fatalf("%s did not finish within %s — hang", what, budget)
	}
}
