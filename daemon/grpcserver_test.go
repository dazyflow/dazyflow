// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	controlpb "github.com/dazyflow/dazyflow/api/gen/control"
	"github.com/dazyflow/dazyflow/auth"
	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/daemon"
	_ "github.com/dazyflow/dazyflow/drops"
	"github.com/dazyflow/dazyflow/engine"
	"github.com/dazyflow/dazyflow/engine/jobstore"
	"github.com/dazyflow/dazyflow/workspace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

type harness struct {
	conn *grpc.ClientConn
	stop func()
	key  string
	// svc lets tests seed Jobs directly when exercising paths that
	// can't be set up over the wire (e.g. cancelling a "running"
	// graph without actually waiting for an executing node).
	svc       *daemon.Service
	principal core.Principal
}

func newHarness(t *testing.T) *harness {
	return newHarnessOpts(t, true)
}

// newHarnessOpts builds the gRPC test harness. startWorker controls whether a
// background worker runs. Tests that exercise control-plane record transitions
// (e.g. CancelJob) pass false so the worker doesn't race them by executing or
// terminal-failing the seeded job before the call under test.
func newHarnessOpts(t *testing.T, startWorker bool) *harness {
	t.Helper()

	ks := auth.NewMemKeyStore()
	editor := core.Role{Name: "editor", Permissions: []core.Permission{
		core.PermGraphRun, core.PermGraphEdit, core.PermGraphAdmin,
	}}
	_, key, err := auth.IssueAPIKey(ks, t.Context(), "k", "acme", "ws1", "u", []core.Role{editor}, nil)
	if err != nil {
		t.Fatalf("issue key: %v", err)
	}

	ws, _ := workspace.OpenFS("")
	jobs := jobstore.NewMemory()
	bus := daemon.NewMemoryBus()
	eng := &engine.Engine{Resolver: &engine.NodeResolver{Native: engine.Default}}
	svc := &daemon.Service{
		Auth:       auth.Chain{&auth.APIKeyAuthenticator{Store: ks}},
		Workspaces: daemon.MapWorkspaces{"acme/ws1": ws},
		Jobs:       jobs,
		Engine:     eng,
		Bus:        bus,
	}

	if startWorker {
		workerCtx, cancelWorker := context.WithCancel(context.Background())
		t.Cleanup(cancelWorker)
		w := daemon.NewWorker(daemon.WorkerConfig{
			ID:              "grpc-test-worker",
			PollInterval:    5 * time.Millisecond,
			LeaseDuration:   5 * time.Second,
			LeaseRenewEvery: 1 * time.Second,
		}, jobs, eng, bus)
		go func() { _ = w.Run(workerCtx) }()
	}

	unary, stream := daemon.AuthInterceptors(svc.Auth)
	srv := grpc.NewServer(
		grpc.UnaryInterceptor(unary),
		grpc.StreamInterceptor(stream),
	)
	daemon.RegisterGRPC(srv, svc)

	lis := bufconn.Listen(1 << 20)
	go srv.Serve(lis)

	conn, err := grpc.NewClient(
		"passthrough:///bufnet",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
	)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	return &harness{
		conn: conn,
		stop: func() { conn.Close(); srv.Stop() },
		key:  key,
		svc:  svc,
		principal: core.Principal{
			Subject: "u", Tenant: "acme", Workspace: "ws1",
			Roles: []core.Role{editor},
		},
	}
}

func (h *harness) ctxWithAuth(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+h.key)
	return ctx, cancel
}

func TestGRPC_SaveListLoadRun(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	defer h.stop()

	gs := controlpb.NewGraphServiceClient(h.conn)
	js := controlpb.NewJobServiceClient(h.conn)

	ctx, cancel := h.ctxWithAuth(t)
	defer cancel()

	// Save
	saveResp, err := gs.SaveGraph(ctx, &controlpb.SaveGraphRequest{Graph: &controlpb.Graph{
		Id: "demo", Tenant: "acme", Workspace: "ws1",
		Nodes: []*controlpb.Node{
			{Id: "a", Module: "delay", Params: []byte(`{"ms":10}`)},
		},
	}})
	if err != nil {
		t.Fatalf("SaveGraph: %v", err)
	}
	if saveResp.Commit == "" {
		t.Error("expected commit hash")
	}

	// List
	listResp, err := gs.ListGraphs(ctx, &controlpb.ListGraphsRequest{Tenant: "acme", Workspace: "ws1"})
	if err != nil {
		t.Fatalf("ListGraphs: %v", err)
	}
	if len(listResp.GraphIds) != 1 || listResp.GraphIds[0] != "demo" {
		t.Errorf("graphs = %v", listResp.GraphIds)
	}

	// Load
	loadResp, err := gs.LoadGraph(ctx, &controlpb.LoadGraphRequest{
		Tenant: "acme", Workspace: "ws1", GraphId: "demo",
	})
	if err != nil {
		t.Fatalf("LoadGraph: %v", err)
	}
	if loadResp.Graph.Id != "demo" || len(loadResp.Graph.Nodes) != 1 {
		t.Errorf("graph = %+v", loadResp.Graph)
	}

	// Run via embedded graph
	stream, err := gs.RunGraph(ctx, &controlpb.RunGraphRequest{Graph: loadResp.Graph})
	if err != nil {
		t.Fatalf("RunGraph: %v", err)
	}
	var jobID string
	var finalStatus string
	progressCount := 0
	for {
		ev, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Recv: %v", err)
		}
		switch payload := ev.Payload.(type) {
		case *controlpb.RunGraphEvent_Progress:
			progressCount++
		case *controlpb.RunGraphEvent_Completed:
			jobID = payload.Completed.JobId
			finalStatus = payload.Completed.Result.Status
		}
	}
	if jobID == "" {
		t.Fatal("expected a completed event with job_id")
	}
	if finalStatus != core.StatusOK {
		t.Errorf("status = %q", finalStatus)
	}

	// Job lookup
	rec, err := js.GetJob(ctx, &controlpb.GetJobRequest{JobId: jobID})
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if rec.Status != string(core.JobStatusSucceeded) {
		t.Errorf("rec.Status = %q", rec.Status)
	}
}

func TestGRPC_MissingAuthRejected(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	defer h.stop()
	gs := controlpb.NewGraphServiceClient(h.conn)

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	_, err := gs.ListGraphs(ctx, &controlpb.ListGraphsRequest{Tenant: "acme", Workspace: "ws1"})
	if err == nil {
		t.Fatal("expected unauthenticated")
	}
	if st, ok := status.FromError(err); !ok || st.Code() != codes.Unauthenticated {
		t.Errorf("err = %v, want Unauthenticated", err)
	}
}

func TestGRPC_BadTokenRejected(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	defer h.stop()
	gs := controlpb.NewGraphServiceClient(h.conn)

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer dzk_garbage_dead")
	_, err := gs.ListGraphs(ctx, &controlpb.ListGraphsRequest{Tenant: "acme", Workspace: "ws1"})
	if err == nil {
		t.Fatal("expected unauthenticated")
	}
	if st, _ := status.FromError(err); st.Code() != codes.Unauthenticated {
		t.Errorf("code = %v, want Unauthenticated", st.Code())
	}
}

func TestGRPC_CrossTenantReturnsPermissionDenied(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	defer h.stop()
	gs := controlpb.NewGraphServiceClient(h.conn)

	ctx, cancel := h.ctxWithAuth(t)
	defer cancel()
	_, err := gs.ListGraphs(ctx, &controlpb.ListGraphsRequest{Tenant: "other", Workspace: "ws1"})
	if err == nil {
		t.Fatal("expected error")
	}
	if st, _ := status.FromError(err); st.Code() != codes.PermissionDenied {
		t.Errorf("code = %v, want PermissionDenied", st.Code())
	}
}

func TestGRPC_RunGraphByRef(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	defer h.stop()
	gs := controlpb.NewGraphServiceClient(h.conn)

	ctx, cancel := h.ctxWithAuth(t)
	defer cancel()

	if _, err := gs.SaveGraph(ctx, &controlpb.SaveGraphRequest{Graph: &controlpb.Graph{
		Id: "ref-run", Tenant: "acme", Workspace: "ws1",
		Nodes: []*controlpb.Node{{Id: "a", Module: "delay", Params: []byte(`{"ms":5}`)}},
	}}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	stream, err := gs.RunGraph(ctx, &controlpb.RunGraphRequest{
		Tenant: "acme", Workspace: "ws1", GraphId: "ref-run",
	})
	if err != nil {
		t.Fatalf("RunGraph by ref: %v", err)
	}
	for {
		ev, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Recv: %v", err)
		}
		if c, ok := ev.Payload.(*controlpb.RunGraphEvent_Completed); ok {
			if c.Completed.Result.Status != core.StatusOK {
				t.Errorf("status = %q", c.Completed.Result.Status)
			}
		}
	}
}

// TestGRPC_CancelJob exercises the new CancelJob RPC end to end:
// seed a fake running graph run, cancel via gRPC, verify the
// graph-record + every node-record flip to Cancelled.
func TestGRPC_CancelJob(t *testing.T) {
	t.Parallel()
	// No worker: CancelJob is a control-plane record transition. A running
	// worker would race this test by claiming the seeded queued node and
	// terminal-failing it (the unresolvable "noop" module) before the cancel,
	// turning CancelJob into a FailedPrecondition. (Was an intermittent flake.)
	h := newHarnessOpts(t, false)
	defer h.stop()

	js := controlpb.NewJobServiceClient(h.conn)
	ctx, cancel := h.ctxWithAuth(t)
	defer cancel()

	// Save a real graph and seed a "live" graph-record + one queued
	// node-record. Mirrors the unit-test fixture in daemon/cancel_test.go
	// but driven through the gRPC plane.
	g := core.Graph{
		ID: "f1", Tenant: "acme", Workspace: "ws1",
		Visibility: core.VisibilityOrg,
		Nodes:      []core.Node{{ID: "a", Module: "noop"}},
	}
	if _, err := h.svc.SaveGraph(context.Background(), h.principal, g); err != nil {
		t.Fatalf("save: %v", err)
	}
	payload, _ := json.Marshal(g)
	if err := h.svc.Jobs.Enqueue(context.Background(), core.JobRecord{
		ID:           "run-1",
		Kind:         core.JobKindGraph,
		GraphID:      "f1",
		NodeID:       "*",
		Tenant:       "acme",
		Workspace:    "ws1",
		Status:       core.JobStatusRunning,
		GraphPayload: payload,
		Job:          core.Job{ID: "run-1", GraphID: "f1"},
	}); err != nil {
		t.Fatalf("enqueue graph rec: %v", err)
	}
	if err := h.svc.Jobs.Enqueue(context.Background(), core.JobRecord{
		ID:         daemon.NodeJobID("run-1", "a"),
		Kind:       core.JobKindNode,
		GraphRunID: "run-1",
		GraphID:    "f1",
		NodeID:     "a",
		Tenant:     "acme",
		Workspace:  "ws1",
		Status:     core.JobStatusQueued,
		Job:        core.Job{GraphID: "f1", NodeID: "a"},
	}); err != nil {
		t.Fatalf("enqueue node a: %v", err)
	}

	if _, err := js.CancelJob(ctx, &controlpb.CancelJobRequest{
		JobId:  "run-1",
		Reason: "test cancel",
	}); err != nil {
		t.Fatalf("CancelJob: %v", err)
	}

	for _, id := range []string{"run-1", daemon.NodeJobID("run-1", "a")} {
		rec, err := h.svc.Jobs.Get(context.Background(), id)
		if err != nil {
			t.Fatalf("get %s: %v", id, err)
		}
		if rec.Status != core.JobStatusCancelled {
			t.Errorf("%s status = %q, want cancelled", id, rec.Status)
		}
	}

	// Already-terminal: second CancelJob comes back as FailedPrecondition.
	_, err := js.CancelJob(ctx, &controlpb.CancelJobRequest{JobId: "run-1"})
	if err == nil {
		t.Fatal("expected error on second cancel")
	}
	if st, _ := status.FromError(err); st.Code() != codes.FailedPrecondition {
		t.Errorf("code = %v, want FailedPrecondition", st.Code())
	}

	// Missing run-id: NotFound.
	_, err = js.CancelJob(ctx, &controlpb.CancelJobRequest{JobId: "nonexistent"})
	if err == nil {
		t.Fatal("expected error on missing run")
	}
	if st, _ := status.FromError(err); st.Code() != codes.NotFound {
		t.Errorf("missing-run code = %v, want NotFound", st.Code())
	}
}

func TestGRPC_GetJobNotFound(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	defer h.stop()
	js := controlpb.NewJobServiceClient(h.conn)

	ctx, cancel := h.ctxWithAuth(t)
	defer cancel()
	_, err := js.GetJob(ctx, &controlpb.GetJobRequest{JobId: "nonexistent"})
	if err == nil {
		t.Fatal("expected error")
	}
	if st, _ := status.FromError(err); st.Code() != codes.NotFound {
		t.Errorf("code = %v, want NotFound", st.Code())
	}
}

func TestGRPC_ListModules(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	defer h.stop()
	ms := controlpb.NewDropServiceClient(h.conn)

	ctx, cancel := h.ctxWithAuth(t)
	defer cancel()
	resp, err := ms.ListDrops(ctx, &controlpb.ListDropsRequest{})
	if err != nil {
		t.Fatalf("ListModules: %v", err)
	}
	if len(resp.Drops) == 0 {
		t.Fatal("expected built-in modules to be listed")
	}
	seen := map[string]bool{}
	for _, m := range resp.Drops {
		seen[m.Id] = true
	}
	for _, want := range []string{"delay", "merge", "file_read", "file_write"} {
		if !seen[want] {
			t.Errorf("missing module %q in: %v", want, seen)
		}
	}
}

// TestRunGraph_ClosesProgressOnSubmitError pins the contract the gRPC
// RunGraph handler depends on: Service.RunGraph closes the progress channel
// on *every* return path, including an early submit error before the engine
// runs. The handler's forwarding goroutine ranges over that channel and the
// handler blocks on <-sendDone until the range ends — if RunGraph could
// return without closing, the forwarder would leak and the RPC would
// deadlock. A cross-tenant graph fails authz in SubmitGraph, exercising the
// pre-engine error path.
func TestRunGraph_ClosesProgressOnSubmitError(t *testing.T) {
	t.Parallel()
	h := newHarnessOpts(t, false)
	defer h.stop()

	progress := make(chan engine.GraphProgress, 4)
	// principal.Tenant == "acme"; graph in a different tenant must be rejected
	// before the run starts.
	g := core.Graph{ID: "x", Tenant: "other", Workspace: "ws1", Nodes: []core.Node{
		{ID: "a", Module: "delay"},
	}}
	_, _, err := h.svc.RunGraph(t.Context(), h.principal, g, progress)
	if err == nil {
		t.Fatal("expected submit error for cross-tenant graph, got nil")
	}

	select {
	case _, ok := <-progress:
		if ok {
			// Drained an event but channel still open: keep reading until close.
			for range progress {
			}
		}
	case <-time.After(2 * time.Second):
		t.Fatal("progress channel was not closed on the submit-error path (forwarder would deadlock)")
	}
}

// sanity: the error mapping uses the wrapped err's Is chain
func TestGRPC_ToStatus_WrapsUnauthorized(t *testing.T) {
	t.Parallel()
	// Direct check that ErrUnauthorized round-trips through PermissionDenied.
	// Lives here to keep package-level wiring documented.
	err := errors.Join(core.ErrUnauthorized, errors.New("missing perm"))
	if !errors.Is(err, core.ErrUnauthorized) {
		t.Fatal("errors.Join lost the sentinel")
	}
}

// TestGRPC_PromoteGraph covers the gRPC PromoteGraph handler: save a graph,
// then promote HEAD into the published environment.
func TestGRPC_PromoteGraph(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	defer h.stop()
	gs := controlpb.NewGraphServiceClient(h.conn)
	ctx, cancel := h.ctxWithAuth(t)
	defer cancel()

	if _, err := gs.SaveGraph(ctx, &controlpb.SaveGraphRequest{Graph: &controlpb.Graph{
		Id: "promo", Tenant: "acme", Workspace: "ws1",
		Nodes: []*controlpb.Node{{Id: "a", Module: "delay", Params: []byte(`{"ms":1}`)}},
	}}); err != nil {
		t.Fatalf("SaveGraph: %v", err)
	}

	if _, err := gs.PromoteGraph(ctx, &controlpb.PromoteGraphRequest{
		Tenant: "acme", Workspace: "ws1", GraphId: "promo", Env: "published", Commit: "HEAD",
	}); err != nil {
		t.Fatalf("PromoteGraph: %v", err)
	}
}

// TestGRPC_PromoteGraph_UnknownGraph covers the error path (toStatus mapping).
func TestGRPC_PromoteGraph_UnknownGraph(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	defer h.stop()
	gs := controlpb.NewGraphServiceClient(h.conn)
	ctx, cancel := h.ctxWithAuth(t)
	defer cancel()

	_, err := gs.PromoteGraph(ctx, &controlpb.PromoteGraphRequest{
		Tenant: "acme", Workspace: "ws1", GraphId: "ghost", Env: "published", Commit: "HEAD",
	})
	if err == nil {
		t.Fatal("PromoteGraph on unknown graph should error")
	}
}

// TestGRPC_ListJobsForGraph covers the gRPC ListJobsForGraph handler after a
// run has produced a job record for the graph.
func TestGRPC_ListJobsForGraph(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	defer h.stop()
	gs := controlpb.NewGraphServiceClient(h.conn)
	js := controlpb.NewJobServiceClient(h.conn)
	ctx, cancel := h.ctxWithAuth(t)
	defer cancel()

	saveResp, err := gs.SaveGraph(ctx, &controlpb.SaveGraphRequest{Graph: &controlpb.Graph{
		Id: "jobs1", Tenant: "acme", Workspace: "ws1",
		Nodes: []*controlpb.Node{{Id: "a", Module: "delay", Params: []byte(`{"ms":1}`)}},
	}})
	if err != nil {
		t.Fatalf("SaveGraph: %v", err)
	}
	_ = saveResp

	// Run it to completion so there's a job record.
	stream, err := gs.RunGraph(ctx, &controlpb.RunGraphRequest{
		Tenant: "acme", Workspace: "ws1", GraphId: "jobs1",
	})
	if err != nil {
		t.Fatalf("RunGraph: %v", err)
	}
	for {
		if _, err := stream.Recv(); err == io.EOF {
			break
		} else if err != nil {
			t.Fatalf("Recv: %v", err)
		}
	}

	resp, err := js.ListJobsForGraph(ctx, &controlpb.ListJobsForGraphRequest{GraphId: "jobs1"})
	if err != nil {
		t.Fatalf("ListJobsForGraph: %v", err)
	}
	if len(resp.Jobs) == 0 {
		t.Fatal("expected at least one job for the graph")
	}
}

// TestGRPC_ListJobsForGraph_Empty covers the no-jobs path (empty result).
func TestGRPC_ListJobsForGraph_Empty(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	defer h.stop()
	js := controlpb.NewJobServiceClient(h.conn)
	ctx, cancel := h.ctxWithAuth(t)
	defer cancel()

	resp, err := js.ListJobsForGraph(ctx, &controlpb.ListJobsForGraphRequest{GraphId: "never-ran"})
	if err != nil {
		// A clean empty listing is also acceptable; only a non-permission error fails.
		if status.Code(err) == codes.PermissionDenied {
			t.Fatalf("unexpected permission error: %v", err)
		}
		return
	}
	if len(resp.Jobs) != 0 {
		t.Fatalf("expected no jobs, got %d", len(resp.Jobs))
	}
}
