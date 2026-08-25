// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"context"
	"net"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	nodepb "git.sr.ht/~klahr/dazyflow/api/gen/node"
	"git.sr.ht/~klahr/dazyflow/core"
)

// fakeServer implements NodeService against an in-memory bufconn so the
// transport can be exercised without a real network or external binary.
type fakeServer struct {
	nodepb.UnimplementedNodeServiceServer
}

func (s *fakeServer) ListManifests(_ context.Context, _ *nodepb.ListManifestsRequest) (*nodepb.ListManifestsResponse, error) {
	return &nodepb.ListManifestsResponse{Manifests: []*nodepb.Manifest{{
		Id:      "remote-echo",
		Version: "1.0",
		Inputs:  []*nodepb.Port{{Id: "in"}},
		Outputs: []*nodepb.Port{{Id: "out"}},
	}}}, nil
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
	// Echo the inline value back. Not a Ref path: a runner is on another
	// machine, so RemoteTransport refuses a job carrying one before it dials.
	out := map[string]*nodepb.Ref{
		"out": {Mime: "text/plain", Inline: job.Input["in"].GetInline()},
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
	res, err := client.ListManifests(t.Context(), &nodepb.ListManifestsRequest{})
	if err != nil {
		t.Fatalf("ListManifests: %v", err)
	}
	manifest := res.Manifests[0]

	transport := &RemoteTransport{
		Descriptor: RemoteDescriptor{ID: "remote-echo", Endpoint: "bufnet"},
		manifest:   manifestFromPB(manifest),
		dropID:     "remote-echo",
		conn:       conn,
		client:     client,
	}

	progress := make(chan core.Progress, 4)
	job := core.Job{
		ID:    "j1",
		Input: map[string]core.Ref{"in": {Inline: "hello", MIME: "text/plain"}},
	}
	result, err := transport.Execute(t.Context(), job, progress)
	close(progress)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Status != core.StatusOK {
		t.Fatalf("status = %q", result.Status)
	}
	if got := result.Output["out"].Inline; got != "hello" {
		t.Errorf("out.inline = %v, want hello", got)
	}

	var pevents []core.Progress
	for p := range progress {
		pevents = append(pevents, p)
	}
	if len(pevents) != 1 || pevents[0].Message != "halfway" {
		t.Errorf("progress = %+v", pevents)
	}
}
