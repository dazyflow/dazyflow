// SPDX-FileCopyrightText: 2026 Angels' Ware
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
	// Filed under the id the remote declares, which is the id a graph writes.
	// Anything else and a saved graph cannot name the step it wants.
	for _, id := range []string{"fetch", "render", "post"} {
		if _, ok := c.Get("acme", id); !ok {
			t.Errorf("drop %q is not resolvable by the id it declared", id)
		}
	}
	// And nothing invented a namespaced alias alongside it.
	if _, ok := c.Get("acme", RunnerNamespace+"box/fetch"); ok {
		t.Error("a namespaced id is still being filed")
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
	// The connection lives on the CATALOG, one per runner. Closing a single
	// drop's transport must therefore close nothing, or discarding one drop
	// would take the other eleven down with it.
	a, _ := c.Get("acme", "a")
	if err := a.(*RemoteTransport).Close(); err != nil {
		t.Errorf("closing one drop's transport: %v", err)
	}
	b, _ := c.Get("acme", "b")
	if _, err := b.Execute(t.Context(), core.Job{ID: "j1"}, nil); err != nil {
		t.Errorf("a sibling drop stopped working when one was closed: %v", err)
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
	tr, ok := c.Get("acme", "render")
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

// Two remotes in one tenant declaring the same drop id is refused.
//
// Drops are filed under the id they declare, so the second one would either
// shadow the first or be shadowed by it depending on registration order —
// silently, and differently on every restart. An error at registration is the
// only answer that anyone can act on. (Ids were briefly namespaced as
// runner/<remote>/<drop>, which made this impossible to hit; that namespace is
// gone, so the refusal is load-bearing again.)
func TestRegister_TwoRemotesMayNotDeclareTheSameDropName(t *testing.T) {
	c := NewRemoteCatalog()
	defer c.Close()
	if err := register(t, c, "acme", "box-a", &multiDrop{manifests: drops("fetch")}); err != nil {
		t.Fatalf("box-a: %v", err)
	}
	err := register(t, c, "acme", "box-b", &multiDrop{manifests: drops("fetch")})
	if err == nil {
		t.Fatal("a second remote claimed a drop id already served in this tenant")
	}
	// The message has to name both remotes and the drop, or the operator has
	// to guess which two of their remotes are fighting.
	for _, want := range []string{"box-a", "box-b", "fetch"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want it to name %q", err, want)
		}
	}
	// And the refusal left the first remote exactly as it was.
	if _, ok := c.Get("acme", "fetch"); !ok {
		t.Error("the refused registration disturbed the remote that was already there")
	}
	if len(c.conns) != 1 {
		t.Errorf("conns = %d, want only the first remote", len(c.conns))
	}
}

// A refused re-registration must leave the remote it was replacing intact.
//
// The dangerous ordering is subtle: re-registering "box" retires the drops it
// used to serve, and the clash is only visible once the new set is known. Retire
// first and then refuse, and "box" has silently lost the drop it was still
// serving — the flow that used it breaks, and the operator sees only an error
// about a different drop entirely.
func TestRegister_ARefusedReRegistrationChangesNothing(t *testing.T) {
	c := NewRemoteCatalog()
	defer c.Close()
	if err := register(t, c, "acme", "box", &multiDrop{manifests: drops("fetch")}); err != nil {
		t.Fatalf("box: %v", err)
	}
	if err := register(t, c, "acme", "other", &multiDrop{manifests: drops("legacy")}); err != nil {
		t.Fatalf("other: %v", err)
	}
	// "box" comes back claiming a drop "other" already serves.
	if err := register(t, c, "acme", "box", &multiDrop{manifests: drops("legacy")}); err == nil {
		t.Fatal("box claimed a drop another remote serves")
	}
	// box keeps what it had...
	if _, ok := c.Get("acme", "fetch"); !ok {
		t.Error("the refused re-registration retired the drop box was still serving")
	}
	// ...and other keeps what it had.
	if tr, ok := c.Get("acme", "legacy"); !ok {
		t.Error("legacy vanished")
	} else if rt, isRemote := tr.(*RemoteTransport); isRemote && rt.Descriptor.ID != "other" {
		t.Errorf("legacy now belongs to %q, want other", rt.Descriptor.ID)
	}
	if len(c.conns) != 2 {
		t.Errorf("conns = %d, want both remotes still connected", len(c.conns))
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
	if _, ok := c.Get("acme", "legacy"); !ok {
		t.Fatal("legacy not registered by the first pass")
	}
	if err := register(t, c, "acme", "box", &multiDrop{manifests: drops("fetch")}); err != nil {
		t.Fatalf("re-Register: %v", err)
	}
	if _, ok := c.Get("acme", "fetch"); !ok {
		t.Error("fetch lost across re-registration")
	}
	if _, ok := c.Get("acme", "legacy"); ok {
		t.Error("legacy still resolves after the runner stopped declaring it")
	}
	if len(c.conns) != 1 {
		t.Errorf("conns = %d, want the replaced connection to have been dropped", len(c.conns))
	}
}
