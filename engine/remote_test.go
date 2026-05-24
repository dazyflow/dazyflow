package engine

import (
	"context"
	"net"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	nodepb "git.sr.ht/~klahr/hazy-flow/api/gen/node"
	"git.sr.ht/~klahr/hazy-flow/core"
)

// fakeServer implements NodeService against an in-memory bufconn so the
// transport can be exercised without a real network or external binary.
type fakeServer struct {
	nodepb.UnimplementedNodeServiceServer
}

func (s *fakeServer) GetManifest(_ context.Context, _ *nodepb.GetManifestRequest) (*nodepb.Manifest, error) {
	return &nodepb.Manifest{
		Id:      "remote-echo",
		Version: "1.0",
		Inputs:  []*nodepb.Port{{Id: "in"}},
		Outputs: []*nodepb.Port{{Id: "out"}},
	}, nil
}

func (s *fakeServer) Execute(job *nodepb.Job, stream nodepb.NodeService_ExecuteServer) error {
	// Send a progress tick.
	if err := stream.Send(&nodepb.Event{
		Payload: &nodepb.Event_Progress{
			Progress: &nodepb.Progress{JobId: job.JobId, Percent: 0.5, Message: "halfway"},
		},
	}); err != nil {
		return err
	}
	out := map[string]*nodepb.Ref{
		"out": {Mime: "text/plain", Ref: job.Input["in"].Ref + "-echoed"},
	}
	return stream.Send(&nodepb.Event{
		Payload: &nodepb.Event_Result{
			Result: &nodepb.Result{JobId: job.JobId, Status: "ok", Output: out},
		},
	})
}

func TestRemoteTransport_RoundTrip(t *testing.T) {
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	nodepb.RegisterNodeServiceServer(srv, &fakeServer{})
	go srv.Serve(lis)
	defer srv.Stop()

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
	defer conn.Close()

	client := nodepb.NewNodeServiceClient(conn)
	manifest, err := client.GetManifest(t.Context(), &nodepb.GetManifestRequest{})
	if err != nil {
		t.Fatalf("GetManifest: %v", err)
	}

	transport := &RemoteTransport{
		Descriptor: RemoteDescriptor{ID: "remote-echo", Endpoint: "bufnet"},
		manifest:   manifestFromPB(manifest),
		conn:       conn,
		client:     client,
	}

	progress := make(chan core.Progress, 4)
	job := core.Job{
		ID:    "j1",
		Input: map[string]core.Ref{"in": {Ref: "hello", MIME: "text/plain"}},
	}
	result, err := transport.Execute(t.Context(), job, progress)
	close(progress)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Status != core.StatusOK {
		t.Fatalf("status = %q", result.Status)
	}
	if result.Output["out"].Ref != "hello-echoed" {
		t.Errorf("out.ref = %q, want hello-echoed", result.Output["out"].Ref)
	}

	var pevents []core.Progress
	for p := range progress {
		pevents = append(pevents, p)
	}
	if len(pevents) != 1 || pevents[0].Message != "halfway" {
		t.Errorf("progress = %+v", pevents)
	}
}
