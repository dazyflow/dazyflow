// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

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

	controlpb "github.com/dazyflow/dazyflow/api/gen/control"
	nodepb "github.com/dazyflow/dazyflow/api/gen/node"
	"github.com/dazyflow/dazyflow/auth"
	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/daemon"
	_ "github.com/dazyflow/dazyflow/drops"
	"github.com/dazyflow/dazyflow/engine"
	"github.com/dazyflow/dazyflow/engine/jobstore"
	"github.com/dazyflow/dazyflow/workspace"
)

type covHarness struct {
	controlConn *grpc.ClientConn
	nodeConn    *grpc.ClientConn
	key         string
	stop        func()
}

func newCovHarness(t *testing.T) *covHarness {
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

type echoNode struct {
	nodepb.UnimplementedNodeServiceServer
}

func (echoNode) ListManifests(_ context.Context, _ *nodepb.ListManifestsRequest) (*nodepb.ListManifestsResponse, error) {
	return &nodepb.ListManifestsResponse{Manifests: []*nodepb.Manifest{{
		Id:             "echo",
		Version:        "1.0",
		Label:          "Echo",
		ExecutionModel: "batch",
		ProcessModel:   "long_lived",
		Inputs:         []*nodepb.Port{{Id: "in"}},
		Outputs:        []*nodepb.Port{{Id: "out"}},
		Idempotent:     true,
	}}}, nil
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

func TestGRPCCov_AllControlRPCs(t *testing.T) {
	h := newCovHarness(t)
	defer h.stop()

	gs := controlpb.NewGraphServiceClient(h.controlConn)
	js := controlpb.NewJobServiceClient(h.controlConn)
	ds := controlpb.NewDropServiceClient(h.controlConn)

	ctx, cancel := h.authCtx(t)
	defer cancel()

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

	listResp, err := gs.ListGraphs(ctx, &controlpb.ListGraphsRequest{Tenant: "acme", Workspace: "ws1"})
	if err != nil {
		t.Fatalf("ListGraphs: %v", err)
	}
	if len(listResp.GraphIds) != 1 || listResp.GraphIds[0] != "cov" {
		t.Errorf("ListGraphs = %v", listResp.GraphIds)
	}

	loadResp, err := gs.LoadGraph(ctx, &controlpb.LoadGraphRequest{
		Tenant: "acme", Workspace: "ws1", GraphId: "cov",
	})
	if err != nil {
		t.Fatalf("LoadGraph: %v", err)
	}
	if loadResp.Graph.Id != "cov" {
		t.Errorf("LoadGraph id = %q", loadResp.Graph.Id)
	}

	if _, err := gs.PromoteGraph(ctx, &controlpb.PromoteGraphRequest{
		Tenant: "acme", Workspace: "ws1", GraphId: "cov",
		Env: "staging", Commit: saveResp.Commit,
	}); err != nil {
		t.Fatalf("PromoteGraph: %v", err)
	}

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

	rec, err := js.GetJob(ctx, &controlpb.GetJobRequest{JobId: jobID})
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if rec.Id != jobID {
		t.Errorf("GetJob id = %q want %q", rec.Id, jobID)
	}

	jobsResp, err := js.ListJobsForGraph(ctx, &controlpb.ListJobsForGraphRequest{GraphId: "cov"})
	if err != nil {
		t.Fatalf("ListJobsForGraph: %v", err)
	}
	if len(jobsResp.Jobs) == 0 {
		t.Error("ListJobsForGraph: expected at least one job")
	}

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
			if st, _ := status.FromError(err); st.Code() == codes.Unimplemented {
				break
			}
			t.Fatalf("StreamJobLogs Recv: %v", err)
		}
	}

	if _, err := js.CancelJob(ctx, &controlpb.CancelJobRequest{JobId: jobID, Reason: "cov"}); err == nil {
		t.Log("CancelJob on terminal job returned nil (idempotent)")
	}

	dropsResp, err := ds.ListDrops(ctx, &controlpb.ListDropsRequest{})
	if err != nil {
		t.Fatalf("ListDrops: %v", err)
	}
	if len(dropsResp.Drops) == 0 {
		t.Error("ListDrops: expected built-in drops")
	}
}

func TestGRPCCov_NodeService(t *testing.T) {
	h := newCovHarness(t)
	defer h.stop()

	client := nodepb.NewNodeServiceClient(h.nodeConn)

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	res, err := client.ListManifests(ctx, &nodepb.ListManifestsRequest{})
	if err != nil {
		t.Fatalf("ListManifests: %v", err)
	}
	if len(res.Manifests) != 1 || res.Manifests[0].Id != "echo" {
		t.Errorf("ListManifests = %+v, want one manifest with id \"echo\"", res.Manifests)
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
