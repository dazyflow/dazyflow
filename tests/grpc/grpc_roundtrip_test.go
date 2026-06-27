// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package grpc_test stands up the daemon's gRPC server in-process and drives
// every control-plane RPC through the generated controlpb client, plus a
// purpose-built NodeService server driven through the generated nodepb client.
//
// Its sole reason for existing is coverage of the generated *_grpc.pb.go
// dispatch/handler/client code in api/gen/control (controlpb) and
// api/gen/node (nodepb): nothing else in the suite drives a live gRPC
// round-trip across BOTH packages, so the generated stubs sit at 0% under
// -coverpkg. We run the real server over a bufconn listener and a real
// grpc.ClientConn so the generated marshaling and stream plumbing executes.
package grpc_test

import (
	"context"
	"io"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	controlpb "git.sr.ht/~klahr/dazyflow/api/gen/control"
	nodepb "git.sr.ht/~klahr/dazyflow/api/gen/node"
	"git.sr.ht/~klahr/dazyflow/auth"
	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/daemon"
	_ "git.sr.ht/~klahr/dazyflow/drops"
	"git.sr.ht/~klahr/dazyflow/engine"
	"git.sr.ht/~klahr/dazyflow/engine/jobstore"
	"git.sr.ht/~klahr/dazyflow/workspace"
)

// covHarness bundles a live control-plane gRPC server (the daemon handlers)
// and a live NodeService gRPC server, each behind its own bufconn-backed
// ClientConn, so this file can exercise the generated stubs in both
// controlpb and nodepb from a single test.
type covHarness struct {
	controlConn *grpc.ClientConn
	nodeConn    *grpc.ClientConn
	key         string
	stop        func()
}

func newCovHarness(t *testing.T) *covHarness {
	t.Helper()

	// --- control plane: daemon Service with in-memory stores ---
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

	workerCtx, cancelWorker := context.WithCancel(context.Background())
	w := daemon.NewWorker(daemon.WorkerConfig{
		ID:              "grpc-cov-worker",
		PollInterval:    5 * time.Millisecond,
		LeaseDuration:   5 * time.Second,
		LeaseRenewEvery: 1 * time.Second,
	}, jobs, eng, bus)
	go func() { _ = w.Run(workerCtx) }()

	unary, stream := daemon.AuthInterceptors(svc.Auth)
	controlSrv := grpc.NewServer(
		grpc.UnaryInterceptor(unary),
		grpc.StreamInterceptor(stream),
	)
	daemon.RegisterGRPC(controlSrv, svc)

	controlLis := bufconn.Listen(1 << 20)
	go func() { _ = controlSrv.Serve(controlLis) }()

	controlConn, err := grpc.NewClient(
		"passthrough:///control",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return controlLis.DialContext(ctx)
		}),
	)
	if err != nil {
		t.Fatalf("dial control: %v", err)
	}

	// --- node plane: a self-contained NodeService implementation ---
	nodeSrv := grpc.NewServer()
	nodepb.RegisterNodeServiceServer(nodeSrv, &echoNode{})
	nodeLis := bufconn.Listen(1 << 20)
	go func() { _ = nodeSrv.Serve(nodeLis) }()

	nodeConn, err := grpc.NewClient(
		"passthrough:///node",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return nodeLis.DialContext(ctx)
		}),
	)
	if err != nil {
		t.Fatalf("dial node: %v", err)
	}

	return &covHarness{
		controlConn: controlConn,
		nodeConn:    nodeConn,
		key:         key,
		stop: func() {
			controlConn.Close()
			nodeConn.Close()
			controlSrv.Stop()
			nodeSrv.Stop()
			cancelWorker()
		},
	}
}

func (h *covHarness) authCtx(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+h.key)
	return ctx, cancel
}

// echoNode is a minimal NodeService that drives the generated nodepb server
// dispatch and the Execute server-streaming path (one Progress event then a
// terminal Result).
type echoNode struct {
	nodepb.UnimplementedNodeServiceServer
}

func (echoNode) GetManifest(_ context.Context, _ *nodepb.GetManifestRequest) (*nodepb.Manifest, error) {
	return &nodepb.Manifest{
		Id:             "echo",
		Version:        "1.0",
		Label:          "Echo",
		ExecutionModel: "batch",
		ProcessModel:   "long_lived",
		Inputs:         []*nodepb.Port{{Id: "in"}},
		Outputs:        []*nodepb.Port{{Id: "out"}},
		Idempotent:     true,
	}, nil
}

func (echoNode) Execute(job *nodepb.Job, stream nodepb.NodeService_ExecuteServer) error {
	if err := stream.Send(&nodepb.Event{Payload: &nodepb.Event_Progress{Progress: &nodepb.Progress{
		JobId:   job.JobId,
		NodeId:  job.NodeId,
		Percent: 0.5,
		Message: "working",
	}}}); err != nil {
		return err
	}
	return stream.Send(&nodepb.Event{Payload: &nodepb.Event_Result{Result: &nodepb.Result{
		JobId:  job.JobId,
		Status: "ok",
		Output: map[string]*nodepb.Ref{"out": {Mime: "text/plain", Inline: []byte(`"echoed"`)}},
	}}})
}

// TestGRPCCov_AllControlRPCs drives every controlpb RPC over the wire:
// SaveGraph, ListGraphs, LoadGraph, RunGraph (stream), GetJob,
// ListJobsForGraph, StreamJobLogs (stream), PromoteGraph, CancelJob,
// and the DropService ListDrops.
func TestGRPCCov_AllControlRPCs(t *testing.T) {
	h := newCovHarness(t)
	defer h.stop()

	gs := controlpb.NewGraphServiceClient(h.controlConn)
	js := controlpb.NewJobServiceClient(h.controlConn)
	ds := controlpb.NewDropServiceClient(h.controlConn)

	ctx, cancel := h.authCtx(t)
	defer cancel()

	// SaveGraph
	saveResp, err := gs.SaveGraph(ctx, &controlpb.SaveGraphRequest{Graph: &controlpb.Graph{
		Id: "cov", Tenant: "acme", Workspace: "ws1",
		Nodes: []*controlpb.Node{{Id: "a", Module: "delay", Params: []byte(`{"ms":5}`)}},
	}})
	if err != nil {
		t.Fatalf("SaveGraph: %v", err)
	}
	if saveResp.Commit == "" {
		t.Error("SaveGraph: empty commit")
	}

	// ListGraphs
	listResp, err := gs.ListGraphs(ctx, &controlpb.ListGraphsRequest{Tenant: "acme", Workspace: "ws1"})
	if err != nil {
		t.Fatalf("ListGraphs: %v", err)
	}
	if len(listResp.GraphIds) != 1 || listResp.GraphIds[0] != "cov" {
		t.Errorf("ListGraphs = %v", listResp.GraphIds)
	}

	// LoadGraph
	loadResp, err := gs.LoadGraph(ctx, &controlpb.LoadGraphRequest{
		Tenant: "acme", Workspace: "ws1", GraphId: "cov",
	})
	if err != nil {
		t.Fatalf("LoadGraph: %v", err)
	}
	if loadResp.Graph.Id != "cov" {
		t.Errorf("LoadGraph id = %q", loadResp.Graph.Id)
	}

	// PromoteGraph (promote the just-saved commit to an env)
	if _, err := gs.PromoteGraph(ctx, &controlpb.PromoteGraphRequest{
		Tenant: "acme", Workspace: "ws1", GraphId: "cov",
		Env: "staging", Commit: saveResp.Commit,
	}); err != nil {
		t.Fatalf("PromoteGraph: %v", err)
	}

	// RunGraph (server-streaming) — receive until EOF.
	runStream, err := gs.RunGraph(ctx, &controlpb.RunGraphRequest{Graph: loadResp.Graph})
	if err != nil {
		t.Fatalf("RunGraph: %v", err)
	}
	var jobID, finalStatus string
	for {
		ev, err := runStream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("RunGraph Recv: %v", err)
		}
		switch p := ev.Payload.(type) {
		case *controlpb.RunGraphEvent_Progress:
			// drive the progress oneof branch
			_ = p.Progress.GetJobId()
		case *controlpb.RunGraphEvent_Completed:
			jobID = p.Completed.JobId
			finalStatus = p.Completed.Result.Status
		}
	}
	if jobID == "" {
		t.Fatal("RunGraph: no completed event")
	}
	if finalStatus != core.StatusOK {
		t.Errorf("RunGraph status = %q", finalStatus)
	}

	// GetJob
	rec, err := js.GetJob(ctx, &controlpb.GetJobRequest{JobId: jobID})
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if rec.Id != jobID {
		t.Errorf("GetJob id = %q want %q", rec.Id, jobID)
	}

	// ListJobsForGraph
	jobsResp, err := js.ListJobsForGraph(ctx, &controlpb.ListJobsForGraphRequest{GraphId: "cov"})
	if err != nil {
		t.Fatalf("ListJobsForGraph: %v", err)
	}
	if len(jobsResp.Jobs) == 0 {
		t.Error("ListJobsForGraph: expected at least one job")
	}

	// StreamJobLogs (server-streaming) — follow=false replays the persisted
	// log to EOF. The job is already terminal, so this returns promptly.
	logStream, err := js.StreamJobLogs(ctx, &controlpb.StreamJobLogsRequest{JobId: jobID, Follow: false})
	if err != nil {
		t.Fatalf("StreamJobLogs: %v", err)
	}
	for {
		_, err := logStream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			// RunLogs may be unconfigured (Unimplemented) on this in-memory
			// Service; that still exercised the generated stream dispatch.
			if st, _ := status.FromError(err); st.Code() == codes.Unimplemented {
				break
			}
			t.Fatalf("StreamJobLogs Recv: %v", err)
		}
	}

	// CancelJob on an already-terminal job: the generated unary path executes;
	// the handler maps it to FailedPrecondition (terminal) — either way the
	// stub round-trips. We accept any error here since the run finished.
	if _, err := js.CancelJob(ctx, &controlpb.CancelJobRequest{JobId: jobID, Reason: "cov"}); err == nil {
		t.Log("CancelJob on terminal job returned nil (idempotent)")
	}

	// DropService.ListDrops
	dropsResp, err := ds.ListDrops(ctx, &controlpb.ListDropsRequest{})
	if err != nil {
		t.Fatalf("ListDrops: %v", err)
	}
	if len(dropsResp.Drops) == 0 {
		t.Error("ListDrops: expected built-in drops")
	}
}

// TestGRPCCov_NodeService drives the generated nodepb client and server:
// GetManifest (unary) and Execute (server-streaming, received to EOF).
func TestGRPCCov_NodeService(t *testing.T) {
	h := newCovHarness(t)
	defer h.stop()

	client := nodepb.NewNodeServiceClient(h.nodeConn)

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	man, err := client.GetManifest(ctx, &nodepb.GetManifestRequest{})
	if err != nil {
		t.Fatalf("GetManifest: %v", err)
	}
	if man.Id != "echo" {
		t.Errorf("GetManifest id = %q", man.Id)
	}

	stream, err := client.Execute(ctx, &nodepb.Job{
		JobId:  "j1",
		NodeId: "n1",
		Input:  map[string]*nodepb.Ref{"in": {Mime: "text/plain", Inline: []byte(`"hi"`)}},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var sawProgress, sawResult bool
	for {
		ev, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Execute Recv: %v", err)
		}
		switch p := ev.Payload.(type) {
		case *nodepb.Event_Progress:
			sawProgress = true
			_ = p.Progress.GetMessage()
		case *nodepb.Event_Result:
			sawResult = true
			if p.Result.Status != "ok" {
				t.Errorf("Execute result status = %q", p.Result.Status)
			}
		}
	}
	if !sawProgress || !sawResult {
		t.Errorf("Execute: progress=%v result=%v, want both", sawProgress, sawResult)
	}
}
