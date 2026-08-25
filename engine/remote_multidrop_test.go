// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"context"
	"net"
	"strings"
	"testing"

	"google.golang.org/grpc"

	nodepb "git.sr.ht/~klahr/dazyflow/api/gen/node"
	"git.sr.ht/~klahr/dazyflow/core"
)

// One runner, many drops.
//
// ListManifests is plural from the outset so a runner can grow to serve
// several drops without the daemon and every already-written runner having to
// change in lockstep. These tests cover what registration does with a set:
// which key each drop lands under, what is refused, and that the connection is
// shared rather than duplicated per drop.

// multiDrop is a NodeService that serves whatever manifests it is given, so a
// test can describe a runner by its declared drops alone.
type multiDrop struct {
	nodepb.UnimplementedNodeServiceServer
	manifests []*nodepb.Manifest
	// lastDropID records what Execute was told to run, which is the only way a
	// runner serving several drops can tell them apart.
	lastDropID string
}

func (m *multiDrop) ListManifests(_ context.Context, _ *nodepb.ListManifestsRequest) (*nodepb.ListManifestsResponse, error) {
	return &nodepb.ListManifestsResponse{Manifests: m.manifests}, nil
}

func (m *multiDrop) Execute(job *nodepb.Job, stream nodepb.NodeService_ExecuteServer) error {
	m.lastDropID = job.DropId
	return stream.Send(&nodepb.Event{Payload: &nodepb.Event_Result{
		Result: &nodepb.Result{JobId: job.JobId, Status: "ok"},
	}})
}

func drops(ids ...string) []*nodepb.Manifest {
	out := make([]*nodepb.Manifest, 0, len(ids))
	for _, id := range ids {
		out = append(out, &nodepb.Manifest{Id: id, Version: "1.0"})
	}
	return out
}

// serve stands up a multiDrop on a real loopback port and returns its address.
// A real listener rather than bufconn because Register dials by endpoint
// string, which is the path under test.
func serve(t *testing.T, srvImpl *multiDrop) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := grpc.NewServer()
	nodepb.RegisterNodeServiceServer(srv, srvImpl)
	go srv.Serve(lis)
	t.Cleanup(srv.Stop)
	return lis.Addr().String()
}

func register(t *testing.T, c *RemoteCatalog, tenant, name string, srvImpl *multiDrop) error {
	t.Helper()
	return c.Register(RemoteDescriptor{
		ID: name, Tenant: tenant, Endpoint: serve(t, srvImpl), Insecure: true,
	})
}

func TestRegister_FilesEveryDeclaredDrop(t *testing.T) {
	c := NewRemoteCatalog()
	defer c.Close()
	if err := register(t, c, "acme", "box", &multiDrop{manifests: drops("fetch", "render", "post")}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	// Filed under the namespaced id, not the bare one the runner declared:
	// runner/<runner>/<drop>, so a saved graph shows which steps leave the
	// daemon.
	for _, id := range []string{"fetch", "render", "post"} {
		want := RunnerDropID("box", id)
		if _, ok := c.Get("acme", want); !ok {
			t.Errorf("drop %q not registered under %q", id, want)
		}
		if _, ok := c.Get("acme", id); ok {
			t.Errorf("drop %q is reachable by its bare id, bypassing the namespace", id)
		}
	}
}

// Twelve drops should cost one TCP connection, not twelve. The catalog owns
// the connection and the per-drop transports share it.
func TestRegister_SharesOneConnectionAcrossDrops(t *testing.T) {
	c := NewRemoteCatalog()
	defer c.Close()
	if err := register(t, c, "acme", "box", &multiDrop{manifests: drops("a", "b", "c")}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if len(c.conns) != 1 {
		t.Errorf("conns = %d, want 1 shared connection", len(c.conns))
	}
	a, _ := c.Get("acme", RunnerDropID("box", "a"))
	b, _ := c.Get("acme", RunnerDropID("box", "b"))
	if a.(*RemoteTransport).conn != b.(*RemoteTransport).conn {
		t.Error("drops from one runner hold different connections")
	}
}

// A runner serving several drops has no way to tell which one a job is for —
// a job names only the graph and node it came from. The transport stamps it.
func TestExecute_TellsTheRunnerWhichDrop(t *testing.T) {
	impl := &multiDrop{manifests: drops("fetch", "render")}
	c := NewRemoteCatalog()
	defer c.Close()
	if err := register(t, c, "acme", "box", impl); err != nil {
		t.Fatalf("Register: %v", err)
	}
	tr, ok := c.Get("acme", RunnerDropID("box", "render"))
	if !ok {
		t.Fatal("render not registered")
	}
	if _, err := tr.Execute(t.Context(), core.Job{ID: "j1"}, nil); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if impl.lastDropID != "render" {
		t.Errorf("runner was told drop_id=%q, want \"render\"", impl.lastDropID)
	}
}

func TestRegister_RefusesRunnerServingNothing(t *testing.T) {
	c := NewRemoteCatalog()
	defer c.Close()
	err := register(t, c, "acme", "box", &multiDrop{})
	if err == nil || !strings.Contains(err.Error(), "serves no drops") {
		t.Fatalf("err = %v, want one about serving no drops", err)
	}
}

func TestRegister_RefusesDropWithNoID(t *testing.T) {
	c := NewRemoteCatalog()
	defer c.Close()
	err := register(t, c, "acme", "box", &multiDrop{manifests: drops("fetch", "")})
	if err == nil || !strings.Contains(err.Error(), "no id") {
		t.Fatalf("err = %v, want one about a drop with no id", err)
	}
}

func TestRegister_RefusesDuplicateDropWithinOneRunner(t *testing.T) {
	c := NewRemoteCatalog()
	defer c.Close()
	err := register(t, c, "acme", "box", &multiDrop{manifests: drops("fetch", "fetch")})
	if err == nil || !strings.Contains(err.Error(), "twice") {
		t.Fatalf("err = %v, want one about a duplicated drop", err)
	}
}

// Two runners in one tenant may both declare a drop called "fetch", and both
// stay reachable. That is the namespace doing the work: before ids were
// runner/<runner>/<drop> this had to be refused at registration, because
// otherwise which one executed depended on registration order.
func TestRegister_TwoRunnersMayDeclareTheSameDropName(t *testing.T) {
	c := NewRemoteCatalog()
	defer c.Close()
	if err := register(t, c, "acme", "box-a", &multiDrop{manifests: drops("fetch")}); err != nil {
		t.Fatalf("box-a: %v", err)
	}
	if err := register(t, c, "acme", "box-b", &multiDrop{manifests: drops("fetch")}); err != nil {
		t.Fatalf("box-b: %v", err)
	}
	a, okA := c.Get("acme", RunnerDropID("box-a", "fetch"))
	b, okB := c.Get("acme", RunnerDropID("box-b", "fetch"))
	if !okA || !okB {
		t.Fatalf("reachable: box-a=%v box-b=%v", okA, okB)
	}
	if a == b {
		t.Fatal("both resolved to the same transport — the ids collided")
	}
}

// Same drop id in two DIFFERENT tenants is not a clash — it is the expected
// case, and the whole reason the catalog is keyed by tenant.
func TestRegister_SameDropIDInDifferentTenantsIsFine(t *testing.T) {
	c := NewRemoteCatalog()
	defer c.Close()
	if err := register(t, c, "acme", "box", &multiDrop{manifests: drops("fetch")}); err != nil {
		t.Fatalf("acme: %v", err)
	}
	if err := register(t, c, "globex", "box", &multiDrop{manifests: drops("fetch")}); err != nil {
		t.Fatalf("globex: %v", err)
	}
	if len(c.conns) != 2 {
		t.Errorf("conns = %d, want one per tenant's runner", len(c.conns))
	}
}

// Re-registering a runner replaces it. A drop it used to serve and no longer
// declares must disappear, or the catalog would keep resolving a step the
// runner has actually dropped.
func TestRegister_ReplacingARunnerRetiresDropsItNoLongerServes(t *testing.T) {
	c := NewRemoteCatalog()
	defer c.Close()
	if err := register(t, c, "acme", "box", &multiDrop{manifests: drops("fetch", "legacy")}); err != nil {
		t.Fatalf("first Register: %v", err)
	}
	if _, ok := c.Get("acme", RunnerDropID("box", "legacy")); !ok {
		t.Fatal("legacy not registered by the first pass")
	}
	if err := register(t, c, "acme", "box", &multiDrop{manifests: drops("fetch")}); err != nil {
		t.Fatalf("re-Register: %v", err)
	}
	if _, ok := c.Get("acme", RunnerDropID("box", "fetch")); !ok {
		t.Error("fetch lost across re-registration")
	}
	if _, ok := c.Get("acme", RunnerDropID("box", "legacy")); ok {
		t.Error("legacy still resolves after the runner stopped declaring it")
	}
	if len(c.conns) != 1 {
		t.Errorf("conns = %d, want the replaced connection to have been dropped", len(c.conns))
	}
}
