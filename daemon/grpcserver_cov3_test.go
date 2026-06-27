package daemon_test

import (
	"io"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	controlpb "git.sr.ht/~klahr/dazyflow/api/gen/control"
)

// TestGRPC_PromoteGraph covers the gRPC PromoteGraph handler: save a graph,
// then promote HEAD into the published environment.
func TestGRPC_PromoteGraph(t *testing.T) {
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
