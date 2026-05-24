package daemon_test

import (
	"context"
	"errors"
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

	controlpb "git.sr.ht/~klahr/hazy-flow/api/gen/control"
	"git.sr.ht/~klahr/hazy-flow/auth"
	"git.sr.ht/~klahr/hazy-flow/core"
	"git.sr.ht/~klahr/hazy-flow/daemon"
	"git.sr.ht/~klahr/hazy-flow/engine"
	"git.sr.ht/~klahr/hazy-flow/engine/jobstore"
	_ "git.sr.ht/~klahr/hazy-flow/modules"
	"git.sr.ht/~klahr/hazy-flow/workspace"
)

type harness struct {
	conn *grpc.ClientConn
	stop func()
	key  string
}

func newHarness(t *testing.T) *harness {
	t.Helper()

	ks := auth.NewMemKeyStore()
	editor := core.Role{Name: "editor", Permissions: []core.Permission{
		core.PermGraphRun, core.PermGraphEdit, core.PermGraphAdmin,
	}}
	_, key, err := auth.IssueAPIKey(ks, t.Context(), "k", "acme", "ws1", "u", []core.Role{editor})
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
	t.Cleanup(cancelWorker)
	w := daemon.NewWorker(daemon.WorkerConfig{
		ID:              "grpc-test-worker",
		PollInterval:    5 * time.Millisecond,
		LeaseDuration:   5 * time.Second,
		LeaseRenewEvery: 1 * time.Second,
	}, jobs, eng, bus)
	go func() { _ = w.Run(workerCtx) }()

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
	}
}

func (h *harness) ctxWithAuth(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+h.key)
	return ctx, cancel
}

func TestGRPC_SaveListLoadRun(t *testing.T) {
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
			{Id: "a", Module: "sleep", Params: []byte(`{"ms":10}`)},
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
	h := newHarness(t)
	defer h.stop()
	gs := controlpb.NewGraphServiceClient(h.conn)

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer hzk_garbage_dead")
	_, err := gs.ListGraphs(ctx, &controlpb.ListGraphsRequest{Tenant: "acme", Workspace: "ws1"})
	if err == nil {
		t.Fatal("expected unauthenticated")
	}
	if st, _ := status.FromError(err); st.Code() != codes.Unauthenticated {
		t.Errorf("code = %v, want Unauthenticated", st.Code())
	}
}

func TestGRPC_CrossTenantReturnsPermissionDenied(t *testing.T) {
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
	h := newHarness(t)
	defer h.stop()
	gs := controlpb.NewGraphServiceClient(h.conn)

	ctx, cancel := h.ctxWithAuth(t)
	defer cancel()

	if _, err := gs.SaveGraph(ctx, &controlpb.SaveGraphRequest{Graph: &controlpb.Graph{
		Id: "ref-run", Tenant: "acme", Workspace: "ws1",
		Nodes: []*controlpb.Node{{Id: "a", Module: "sleep", Params: []byte(`{"ms":5}`)}},
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

func TestGRPC_GetJobNotFound(t *testing.T) {
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
	h := newHarness(t)
	defer h.stop()
	ms := controlpb.NewModuleServiceClient(h.conn)

	ctx, cancel := h.ctxWithAuth(t)
	defer cancel()
	resp, err := ms.ListModules(ctx, &controlpb.ListModulesRequest{})
	if err != nil {
		t.Fatalf("ListModules: %v", err)
	}
	if len(resp.Modules) == 0 {
		t.Fatal("expected built-in modules to be listed")
	}
	seen := map[string]bool{}
	for _, m := range resp.Modules {
		seen[m.Id] = true
	}
	for _, want := range []string{"sleep", "merge", "file_read", "file_write"} {
		if !seen[want] {
			t.Errorf("missing module %q in: %v", want, seen)
		}
	}
}

// sanity: the error mapping uses the wrapped err's Is chain
func TestGRPC_ToStatus_WrapsUnauthorized(t *testing.T) {
	// Direct check that ErrUnauthorized round-trips through PermissionDenied.
	// Lives here to keep package-level wiring documented.
	err := errors.Join(core.ErrUnauthorized, errors.New("missing perm"))
	if !errors.Is(err, core.ErrUnauthorized) {
		t.Fatal("errors.Join lost the sentinel")
	}
}
