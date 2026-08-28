// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	nodepb "git.sr.ht/~klahr/dazyflow/api/gen/node"
	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/engine/mcp"
)

// ----------------------------------------------------------------------
// Sandbox / Quota providers — minimal stubs for populateSandbox tests.
// ----------------------------------------------------------------------

type fakeSandbox struct {
	root    string
	scratch string
	rootErr error
	scrErr  error
}

func (f *fakeSandbox) Root(tenant, ws string) (string, error) {
	if f.rootErr != nil {
		return "", f.rootErr
	}
	return f.root, nil
}

func (f *fakeSandbox) ScratchRoot(tenant, ws, runID string) (string, error) {
	if f.scrErr != nil {
		return "", f.scrErr
	}
	return f.scratch, nil
}

func (f *fakeSandbox) RemoveScratch(tenant, ws, runID string) error { return nil }

// rootOnlySandbox implements only SandboxProvider (no ScratchProvider) to
// exercise the "scratch unsupported" branch of populateSandbox.
type rootOnlySandbox struct{ root string }

func (s *rootOnlySandbox) Root(tenant, ws string) (string, error) { return s.root, nil }

type fakeQuota struct {
	limit   int64
	used    int64
	usedErr error
}

func (q *fakeQuota) Limit(tenant string) int64         { return q.limit }
func (q *fakeQuota) Used(tenant string) (int64, error) { return q.used, q.usedErr }

// ----------------------------------------------------------------------
// Engine.populateSandbox
// ----------------------------------------------------------------------

func TestEngine_populateSandbox_SandboxAndQuota(t *testing.T) {
	e := &Engine{
		Sandbox: &fakeSandbox{root: "/srv/data/acme/ws", scratch: "/srv/data/acme/ws/.scratch/run-1"},
		Quota:   &fakeQuota{limit: 1000, used: 250},
	}
	job := core.Job{}
	if err := e.populateSandbox(&job, core.Graph{Tenant: "acme", Workspace: "ws"}, "run-1"); err != nil {
		t.Fatalf("populateSandbox: %v", err)
	}
	if job.WorkspaceRoot != "/srv/data/acme/ws" {
		t.Errorf("WorkspaceRoot = %q", job.WorkspaceRoot)
	}
	if job.ScratchRoot != "/srv/data/acme/ws/.scratch/run-1" {
		t.Errorf("ScratchRoot = %q", job.ScratchRoot)
	}
	if job.QuotaLimit != 1000 || job.QuotaUsed != 250 {
		t.Errorf("quota snapshot = (%d,%d), want (1000,250)", job.QuotaLimit, job.QuotaUsed)
	}
}

func TestEngine_populateSandbox_NoScratchWhenRunIDEmpty(t *testing.T) {
	e := &Engine{Sandbox: &fakeSandbox{root: "/r", scratch: "/r/.scratch/x"}}
	job := core.Job{}
	if err := e.populateSandbox(&job, core.Graph{Tenant: "t"}, ""); err != nil {
		t.Fatalf("populateSandbox: %v", err)
	}
	if job.ScratchRoot != "" {
		t.Errorf("ScratchRoot = %q, want empty (runID was empty)", job.ScratchRoot)
	}
}

func TestEngine_populateSandbox_NonScratchProvider(t *testing.T) {
	// Sandbox without ScratchProvider — populateSandbox must not call
	// ScratchRoot. (Already covered indirectly above; this pins the
	// no-implements-ScratchProvider branch too.)
	e := &Engine{Sandbox: &rootOnlySandbox{root: "/r"}}
	job := core.Job{}
	if err := e.populateSandbox(&job, core.Graph{Tenant: "t"}, "run"); err != nil {
		t.Fatalf("populateSandbox: %v", err)
	}
	if job.ScratchRoot != "" {
		t.Errorf("ScratchRoot must stay empty for non-scratch provider")
	}
}

func TestEngine_populateSandbox_SandboxError(t *testing.T) {
	e := &Engine{Sandbox: &fakeSandbox{rootErr: errors.New("bad name")}}
	err := e.populateSandbox(&core.Job{}, core.Graph{Tenant: "t", Workspace: "w"}, "")
	if err == nil || !strings.Contains(err.Error(), "sandbox") {
		t.Errorf("err = %v, want one mentioning 'sandbox'", err)
	}
}

func TestEngine_populateSandbox_ScratchError(t *testing.T) {
	e := &Engine{Sandbox: &fakeSandbox{root: "/r", scrErr: errors.New("disk full")}}
	err := e.populateSandbox(&core.Job{}, core.Graph{Tenant: "t", Workspace: "w"}, "run-1")
	if err == nil || !strings.Contains(err.Error(), "scratch") {
		t.Errorf("err = %v, want one mentioning 'scratch'", err)
	}
}

func TestEngine_populateSandbox_QuotaError(t *testing.T) {
	e := &Engine{Quota: &fakeQuota{limit: 1000, usedErr: errors.New("db unreachable")}}
	err := e.populateSandbox(&core.Job{}, core.Graph{Tenant: "t"}, "")
	if err == nil || !strings.Contains(err.Error(), "quota") {
		t.Errorf("err = %v, want one mentioning 'quota'", err)
	}
}

func TestEngine_populateSandbox_UnlimitedQuotaSkipsUsedLookup(t *testing.T) {
	// Limit=0 = unlimited; the engine must skip the Used() lookup so an
	// unlimited tenant doesn't pay for a (potentially expensive) du.
	e := &Engine{Quota: &fakeQuota{limit: 0, usedErr: errors.New("must not be called")}}
	job := core.Job{}
	if err := e.populateSandbox(&job, core.Graph{Tenant: "t"}, ""); err != nil {
		t.Errorf("populateSandbox: %v", err)
	}
	if job.QuotaUsed != 0 {
		t.Errorf("QuotaUsed = %d, want 0 (unlimited)", job.QuotaUsed)
	}
}

// ----------------------------------------------------------------------
// Engine.Run — error branches not covered by engine_test.go
// ----------------------------------------------------------------------

func TestEngine_Run_NoResolver(t *testing.T) {
	e := &Engine{}
	res, err := e.Run(t.Context(), core.Graph{ID: "g"}, nil)
	if err == nil || !strings.Contains(err.Error(), "no resolver") {
		t.Errorf("err = %v, want one mentioning 'no resolver'", err)
	}
	if res.Status != core.StatusError {
		t.Errorf("status = %q, want error", res.Status)
	}
	if res.Error == nil || res.Error.Code != "no_resolver" {
		t.Errorf("error = %+v, want code=no_resolver", res.Error)
	}
}

func TestEngine_Run_CancelledBeforeFirstLayer(t *testing.T) {
	e := newEngineWith(t, NativeDrop{
		Manifest: noopManifest,
		Execute: func(ctx context.Context, _ core.Job, _ chan<- core.Progress) (core.Result, error) {
			return core.Result{Status: core.StatusOK, Output: map[string]core.Ref{"out": {}}}, nil
		},
	})
	ctx, cancel := context.WithCancel(t.Context())
	cancel() // pre-cancel so the layer-start check trips immediately
	g := core.Graph{Nodes: []core.Node{{ID: "a", Module: "noop"}}}
	res, err := e.Run(ctx, g, nil)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if res.Error == nil || res.Error.Code != "cancelled" {
		t.Errorf("error = %+v, want code=cancelled", res.Error)
	}
}

func TestEngine_Run_CyclicGraphIsInvalid(t *testing.T) {
	e := newEngineWith(t, NativeDrop{Manifest: noopManifest, Execute: func(_ context.Context, _ core.Job, _ chan<- core.Progress) (core.Result, error) {
		return core.Result{Status: core.StatusOK}, nil
	}})
	g := core.Graph{
		Nodes: []core.Node{
			{ID: "a", Module: "noop"},
			{ID: "b", Module: "noop"},
		},
		Edges: []core.Edge{
			{From: "a", FromPort: "out", To: "b", ToPort: "in"},
			{From: "b", FromPort: "out", To: "a", ToPort: "in"},
		},
	}
	res, err := e.Run(t.Context(), g, nil)
	if err == nil {
		t.Fatal("expected error for cyclic graph")
	}
	if res.Error == nil || res.Error.Code != "invalid_graph" {
		t.Errorf("error = %+v, want code=invalid_graph", res.Error)
	}
}

// ----------------------------------------------------------------------
// Engine.RunNode — error branches
// ----------------------------------------------------------------------

func TestEngine_RunNode_UnknownNode(t *testing.T) {
	e := newEngineWith(t)
	g := core.Graph{ID: "g", Nodes: []core.Node{{ID: "a", Module: "noop"}}}
	res, err := e.RunNode(t.Context(), g, "run-1", "ghost", "rec-ghost", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Errorf("err = %v, want one mentioning 'ghost'", err)
	}
	if res.Error == nil || res.Error.Code != "unknown_node" {
		t.Errorf("error = %+v, want code=unknown_node", res.Error)
	}
}

func TestEngine_RunNode_NoResolver(t *testing.T) {
	e := &Engine{}
	g := core.Graph{Nodes: []core.Node{{ID: "a", Module: "noop"}}}
	res, err := e.RunNode(t.Context(), g, "run-1", "a", "rec-a", nil, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if res.Error == nil || res.Error.Code != "no_resolver" {
		t.Errorf("error = %+v, want code=no_resolver", res.Error)
	}
}

func TestEngine_RunNode_ResolverFails(t *testing.T) {
	// Resolver returns "no transport registered for module …" for
	// modules not in any catalog — that surfaces as resolve_failed.
	e := newEngineWith(t)
	g := core.Graph{Nodes: []core.Node{{ID: "a", Module: "nowhere"}}}
	res, err := e.RunNode(t.Context(), g, "run", "a", "rec-a", nil, nil)
	if err == nil {
		t.Fatal("expected resolve error")
	}
	if res.Error == nil || res.Error.Code != "resolve_failed" {
		t.Errorf("error = %+v, want code=resolve_failed", res.Error)
	}
}

func TestEngine_RunNode_SandboxError(t *testing.T) {
	e := newEngineWith(t, NativeDrop{
		Manifest: noopManifest,
		Execute: func(_ context.Context, _ core.Job, _ chan<- core.Progress) (core.Result, error) {
			return core.Result{Status: core.StatusOK}, nil
		},
	})
	e.Sandbox = &fakeSandbox{rootErr: errors.New("bad name")}
	g := core.Graph{Tenant: "t", Workspace: "w", Nodes: []core.Node{{ID: "a", Module: "noop"}}}
	res, err := e.RunNode(t.Context(), g, "run-1", "a", "rec-a", nil, nil)
	if err == nil || res.Error == nil || res.Error.Code != "sandbox" {
		t.Errorf("res = %+v err = %v, want sandbox error", res, err)
	}
}

// failingSecretProvider returns an error for any Get — used to exercise
// the secret-resolution failure branch in RunNode.
type failingSecretProvider struct{}

func (failingSecretProvider) Scheme() string { return "secret" }
func (failingSecretProvider) Get(_ context.Context, _ string) (string, error) {
	return "", errors.New("provider down")
}

func TestEngine_RunNode_SecretError(t *testing.T) {
	e := newEngineWith(t, NativeDrop{
		Manifest: noopManifest,
		Execute: func(_ context.Context, _ core.Job, _ chan<- core.Progress) (core.Result, error) {
			return core.Result{Status: core.StatusOK}, nil
		},
	})
	e.Secrets = map[string]core.SecretProvider{"secret": failingSecretProvider{}}
	g := core.Graph{
		Nodes: []core.Node{{ID: "a", Module: "noop", Params: map[string]any{"k": "secret://X"}}},
	}
	res, err := e.RunNode(t.Context(), g, "run-1", "a", "rec-a", nil, nil)
	if err == nil || res.Error == nil || res.Error.Code != "secret" {
		t.Errorf("res=%+v err=%v, want secret error", res, err)
	}
}

// stubSigner records the (runID, nodeID) it was asked to sign and returns
// a deterministic URL — lets RunNode's ApprovalSigner branch be observed.
type stubSigner struct{ saw [2]string }

func (s *stubSigner) SignApprovalURL(runID, nodeID string) string {
	s.saw = [2]string{runID, nodeID}
	return "https://approve/" + runID + "/" + nodeID
}

func TestEngine_RunNode_ApprovalURLAttached(t *testing.T) {
	awaitingManifest := core.Manifest{
		ID:             "await",
		Summary:        "Test fixture await.",
		Examples:       []core.ParamsExample{{Title: "default"}},
		AwaitsApproval: true,
		Inputs:         []core.Port{{Port: "in"}},
		Outputs:        []core.Port{{Port: "out"}},
	}
	var seenURL string
	e := newEngineWith(t, NativeDrop{
		Manifest: awaitingManifest,
		Execute: func(_ context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
			seenURL = job.ApprovalURL
			return core.Result{Status: core.StatusAwaiting}, nil
		},
	})
	sig := &stubSigner{}
	e.ApprovalSigner = sig
	g := core.Graph{Nodes: []core.Node{{ID: "approve-step", Module: "await"}}}
	if _, err := e.RunNode(t.Context(), g, "run-99", "approve-step", "rec-approve", nil, nil); err != nil {
		t.Fatalf("RunNode: %v", err)
	}
	if sig.saw[0] != "run-99" || sig.saw[1] != "approve-step" {
		t.Errorf("signer.saw = %v, want [run-99 approve-step]", sig.saw)
	}
	if seenURL != "https://approve/run-99/approve-step" {
		t.Errorf("job.ApprovalURL = %q", seenURL)
	}
}

// ----------------------------------------------------------------------
// NodeResolver chain
// ----------------------------------------------------------------------

func TestNodeResolver_ChainHitsRemote(t *testing.T) {
	reg := NewRegistry()
	remote := NewRemoteCatalog()
	remote.nodes[remoteKey{tenant: "acme", id: "m"}] = &RemoteTransport{manifest: core.Manifest{ID: "m"}}
	r := &NodeResolver{Native: reg, Remote: remote}
	tr, err := r.Resolve(core.WithTenant(context.Background(), "acme"), "m")
	if err != nil || tr.Manifest().ID != "m" {
		t.Errorf("Resolve = (%v,%v)", tr, err)
	}
}

func TestNodeResolver_ChainHitsMCP(t *testing.T) {
	reg := NewRegistry()
	r := &NodeResolver{Native: reg, MCP: mcp.NewCatalog()}
	// MCP catalog is empty so the MCP branch returns ok=false. Resolve
	// must then fall through to the "no transport" error — exercising
	// the Get/false return for the MCP branch.
	_, err := r.Resolve(context.Background(), "ghost")
	if err == nil || !strings.Contains(err.Error(), "no transport") {
		t.Errorf("err = %v, want one mentioning 'no transport'", err)
	}
}

func TestNodeResolver_Manifests_MergesAllCatalogs(t *testing.T) {
	reg := NewRegistry()
	_ = reg.Register(NativeDrop{Manifest: validTestManifest("native-mod"), Execute: noopExecute})
	remote := NewRemoteCatalog()
	remote.nodes[remoteKey{tenant: "acme", id: "remote-mod"}] = &RemoteTransport{manifest: core.Manifest{ID: "remote-mod"}}
	r := &NodeResolver{Native: reg, Remote: remote, MCP: mcp.NewCatalog()}
	// ManifestsForTenant, not Manifests: a runner's drops belong to one tenant,
	// so the unscoped map deliberately carries only the instance-wide catalogs.
	m := r.ManifestsForTenant("acme")
	for _, want := range []string{"native-mod", "remote-mod"} {
		if _, ok := m[want]; !ok {
			t.Errorf("missing %q in merged Manifests (%v)", want, manifestKeys(m))
		}
	}
}

func noopExecute(_ context.Context, _ core.Job, _ chan<- core.Progress) (core.Result, error) {
	return core.Result{Status: core.StatusOK}, nil
}

func manifestKeys(m map[string]core.Manifest) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// ----------------------------------------------------------------------
// Registry.Register and package-level Register
// ----------------------------------------------------------------------

func TestRegistry_Register_RejectsEmptyID(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(NativeDrop{Execute: noopExecute}); err == nil {
		t.Error("Register with empty ID: want error")
	}
}

func TestRegistry_Register_RejectsNoExecute(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(NativeDrop{Manifest: core.Manifest{ID: "x"}}); err == nil {
		t.Error("Register with nil Execute: want error")
	}
}

// validTestManifest returns a manifest that passes registration —
// has the required Summary and Examples set. Used by the duplicate /
// happy-path tests so they exercise the post-validation behavior
// rather than tripping over the same fields they're not testing.
func validTestManifest(id string) core.Manifest {
	return core.Manifest{
		ID:       id,
		Summary:  "Test fixture.",
		Examples: []core.ParamsExample{{Title: "default"}},
	}
}

func TestRegistry_Register_RejectsMissingSummary(t *testing.T) {
	r := NewRegistry()
	m := validTestManifest("x")
	m.Summary = ""
	if err := r.Register(NativeDrop{Manifest: m, Execute: noopExecute}); err == nil {
		t.Error("Register with empty Summary: want error")
	}
}

func TestRegistry_Register_RejectsMissingExamples(t *testing.T) {
	r := NewRegistry()
	m := validTestManifest("x")
	m.Examples = nil
	if err := r.Register(NativeDrop{Manifest: m, Execute: noopExecute}); err == nil {
		t.Error("Register with empty Examples: want error")
	}
}

func TestRegistry_Register_RejectsDuplicate(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(NativeDrop{Manifest: validTestManifest("x"), Execute: noopExecute}); err != nil {
		t.Fatalf("first Register: %v", err)
	}
	if err := r.Register(NativeDrop{Manifest: validTestManifest("x"), Execute: noopExecute}); err == nil {
		t.Error("duplicate Register: want error")
	}
}

func TestRegistry_Get_Miss(t *testing.T) {
	r := NewRegistry()
	if tr, ok := r.Get("ghost"); ok || tr != nil {
		t.Errorf("Get(ghost) = (%v,%v), want (nil,false)", tr, ok)
	}
}

func TestPackageRegister_PanicsOnError(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("package Register with bad input: want panic")
		}
	}()
	Register(NativeDrop{}) // empty manifest ID — must panic.
}

// ----------------------------------------------------------------------
// RemoteCatalog full lifecycle via bufconn — covers NewRemoteCatalog,
// Register (success and bufconn dial), Get, Manifests, Close,
// RemoteTransport.Close.
// ----------------------------------------------------------------------

func TestRemoteCatalog_Lifecycle(t *testing.T) {
	c := NewRemoteCatalog()
	// Pre-register a transport so we can exercise Get/Manifests/Close
	// without standing up a real gRPC server again (TestRemoteTransport_RoundTrip
	// already covers the dial+manifest path).
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	nodepb.RegisterNodeServiceServer(srv, &fakeServer{})
	go srv.Serve(lis)
	defer srv.Stop()
	conn, err := grpc.NewClient(
		"passthrough:///bufnet",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
	)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	c.nodes[remoteKey{tenant: "acme", id: "remote-echo"}] = &RemoteTransport{
		Descriptor: RemoteDescriptor{ID: "remote-echo", Tenant: "acme"},
		manifest:   core.Manifest{ID: "remote-echo"},
		client:     nodepb.NewNodeServiceClient(conn),
	}

	// Manifest accessor on the transport.
	if c.nodes[remoteKey{tenant: "acme", id: "remote-echo"}].Manifest().ID != "remote-echo" {
		t.Errorf("Manifest.ID = %q", c.nodes[remoteKey{tenant: "acme", id: "remote-echo"}].Manifest().ID)
	}

	// Get hit + miss.
	if tr, ok := c.Get("acme", "remote-echo"); !ok || tr.Manifest().ID != "remote-echo" {
		t.Errorf("Get hit = (%v,%v)", tr, ok)
	}
	if tr, ok := c.Get("acme", "ghost"); ok || tr != nil {
		t.Errorf("Get miss = (%v,%v), want (nil,false)", tr, ok)
	}

	// ManifestsFor includes the registered remote, for its own tenant only.
	m := c.ManifestsFor("acme")
	if _, ok := m["remote-echo"]; !ok {
		t.Errorf("ManifestsFor missing remote-echo: %v", manifestKeys(m))
	}
	if other := c.ManifestsFor("other"); len(other) != 0 {
		t.Errorf("ManifestsFor leaked to another tenant: %v", manifestKeys(other))
	}

	// Close shuts the conn and clears the cache.
	if err := c.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	if len(c.nodes) != 0 {
		t.Errorf("nodes not cleared after Close: %d", len(c.nodes))
	}
}

// TestRemoteCatalog_Register_InsecureDialAndHandshake covers the
// Register dial + GetManifest path against a real TCP gRPC server in
// Insecure (cleartext-by-opt-in) mode. Distinct from
// TestRemoteCatalog_Lifecycle which hand-injects a pre-built
// transport — this one exercises the dial+manifest fetch end-to-end.
func TestRemoteCatalog_Register_InsecureDialAndHandshake(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := grpc.NewServer()
	nodepb.RegisterNodeServiceServer(srv, &fakeServer{})
	go srv.Serve(lis)
	defer srv.Stop()

	c := NewRemoteCatalog()
	c.DialTimeout = 5 * time.Second
	desc := RemoteDescriptor{ID: "remote-echo", Tenant: "acme", Endpoint: lis.Addr().String(), Insecure: true}
	if err := c.Register(desc); err != nil {
		t.Fatalf("Register: %v", err)
	}
	id := "remote-echo"
	tr, ok := c.Get("acme", id)
	if !ok || tr.Manifest().ID != id {
		t.Errorf("Get after Register = (%v,%v)", tr, ok)
	}
	if err := c.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

// The descriptor's ID names the RUNNER, and drop ids come from the manifests
// the runner declares, so there is no longer an id to "mismatch". Registration
// under a name unrelated to the drops it serves is now the normal case.
// remote_multidrop_test.go covers what IS validated instead.
func TestRemoteCatalog_Register_RemoteNameNeedNotMatchDropID(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := grpc.NewServer()
	nodepb.RegisterNodeServiceServer(srv, &fakeServer{}) // serves drop "remote-echo"
	go srv.Serve(lis)
	defer srv.Stop()

	c := NewRemoteCatalog()
	desc := RemoteDescriptor{ID: "billing-box", Tenant: "acme", Endpoint: lis.Addr().String(), Insecure: true}
	if err := c.Register(desc); err != nil {
		t.Fatalf("Register: %v", err)
	}
	// The remote is called "billing-box" and serves a drop called "remote-echo".
	// The drop resolves by the id it declared; the remote's own name is routing
	// metadata and never part of it.
	if _, ok := c.Get("acme", "remote-echo"); !ok {
		t.Error("drop not resolvable by the id it declared")
	}
	if _, ok := c.Get("acme", "billing-box"); ok {
		t.Error("the remote's name resolved as though it were a drop id")
	}
	if err := c.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

// TestRemoteCatalog_Register_DefaultTimeoutZeroed exercises the
// "timeout <= 0 → 5s default" branch.
func TestRemoteCatalog_Register_DefaultTimeoutZeroed(t *testing.T) {
	c := &RemoteCatalog{nodes: map[remoteKey]*RemoteTransport{}} // DialTimeout=0
	// No TLS, no Insecure → credentialsForDescriptor errors out fast,
	// but only AFTER the default-timeout assignment runs. Tenant is set so
	// the tenant check (which runs first) doesn't short-circuit the branch
	// this test is actually about.
	if err := c.Register(RemoteDescriptor{ID: "x", Tenant: "acme", Endpoint: "127.0.0.1:1"}); err == nil {
		t.Error("Register without TLS/Insecure: want error")
	}
}

func TestRemoteCatalog_Register_RejectsCleartextWithoutOptIn(t *testing.T) {
	c := NewRemoteCatalog()
	c.DialTimeout = time.Millisecond
	// No TLS configured, Insecure=false → must refuse with the explicit
	// "TLS not configured" credential error.
	err := c.Register(RemoteDescriptor{ID: "x", Tenant: "acme", Endpoint: "127.0.0.1:1"})
	if err == nil || !strings.Contains(err.Error(), "TLS not configured") {
		t.Errorf("err = %v, want one mentioning 'TLS not configured'", err)
	}
}

func TestRemoteTransport_Close_NilConn(t *testing.T) {
	// A transport with no conn is harmless to Close (no-op, no panic).
	tr := &RemoteTransport{}
	if err := tr.Close(); err != nil {
		t.Errorf("Close on nil conn = %v, want nil", err)
	}
}

// ----------------------------------------------------------------------
// Protobuf conversion edge cases
// ----------------------------------------------------------------------

func TestRefToPB_InlineRoundTrip(t *testing.T) {
	ref := core.Ref{MIME: "application/json", Ref: "r1", Inline: map[string]any{"k": "v"}}
	pb, err := refToPB(ref)
	if err != nil {
		t.Fatalf("refToPB: %v", err)
	}
	if len(pb.Inline) == 0 {
		t.Errorf("Inline bytes empty")
	}
	back := refFromPB(pb)
	if back.MIME != "application/json" || back.Ref != "r1" {
		t.Errorf("back = %+v", back)
	}
	m, ok := back.Inline.(map[string]any)
	if !ok || m["k"] != "v" {
		t.Errorf("back.Inline = %+v, want map with k=v", back.Inline)
	}
}

func TestRefFromPB_NoInline(t *testing.T) {
	pb := &nodepb.Ref{Mime: "text/plain", Ref: "r"}
	r := refFromPB(pb)
	if r.Inline != nil {
		t.Errorf("Inline = %+v, want nil", r.Inline)
	}
}

func TestProgressFromPB_WithData(t *testing.T) {
	pb := &nodepb.Progress{
		JobId:   "j",
		NodeId:  "n",
		Percent: 0.42,
		Message: "halfway",
		Data:    []byte(`{"phase":"extract"}`),
	}
	p := progressFromPB(pb)
	if p.JobID != "j" || p.NodeID != "n" || p.Message != "halfway" {
		t.Errorf("progress = %+v", p)
	}
	if p.Percent == nil || *p.Percent != 0.42 {
		t.Errorf("percent = %v", p.Percent)
	}
	if p.Data["phase"] != "extract" {
		t.Errorf("data = %+v", p.Data)
	}
}

func TestResultFromPB_WithError(t *testing.T) {
	pb := &nodepb.Result{
		JobId:  "j",
		Status: core.StatusError,
		Error:  &nodepb.JobError{Code: "boom", Message: "broke"},
		Output: map[string]*nodepb.Ref{"out": {Mime: "text/plain", Ref: "x"}},
	}
	r := resultFromPB(pb)
	if r.Error == nil || r.Error.Code != "boom" || r.Error.Message != "broke" {
		t.Errorf("error = %+v", r.Error)
	}
	if r.Output["out"].Ref != "x" {
		t.Errorf("output = %+v", r.Output)
	}
}

func TestPortFromPB_MinMax(t *testing.T) {
	pb := &nodepb.Port{Id: "in", Mime: []string{"text/plain"}, Required: true, Variadic: true, Min: 1, Max: 5}
	p := portFromPB(pb)
	if p.Port != "in" || !p.Required || !p.Variadic {
		t.Errorf("port = %+v", p)
	}
	if p.Min == nil || *p.Min != 1 {
		t.Errorf("min = %v", p.Min)
	}
	if p.Max == nil || *p.Max != 5 {
		t.Errorf("max = %v", p.Max)
	}
}

// ----------------------------------------------------------------------
// Upstream-path helpers: extra map/slice flavors
// ----------------------------------------------------------------------

func TestUpstream_GetField_MapStringString(t *testing.T) {
	prior := map[string]core.Result{
		"reader": {Output: map[string]core.Ref{
			"meta": {Inline: map[string]string{"status": "ok"}},
		}},
	}
	got, err := resolveUpstreamPath(prior, "reader.meta.status")
	if err != nil || got != "ok" {
		t.Errorf("got %v err %v", got, err)
	}
}

func TestUpstream_GetField_MapStringString_Missing(t *testing.T) {
	prior := map[string]core.Result{
		"r": {Output: map[string]core.Ref{
			"m": {Inline: map[string]string{"a": "1"}},
		}},
	}
	_, err := resolveUpstreamPath(prior, "r.m.absent")
	if err == nil || !strings.Contains(err.Error(), "not present") {
		t.Errorf("err = %v, want one mentioning 'not present'", err)
	}
}

func TestUpstream_IndexValue_Variants(t *testing.T) {
	cases := []struct {
		name  string
		value any
		path  string
		want  any
	}{
		{"[]string", []string{"a", "b"}, "n.p[1]", "b"},
		{"[]map[string]any", []map[string]any{{"k": "v"}}, "n.p[0].k", "v"},
		{"[]map[string]string", []map[string]string{{"k": "v"}}, "n.p[0].k", "v"},
		{"[]any with map elements", []any{map[string]any{"k": "v"}}, "n.p[0].k", "v"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			prior := map[string]core.Result{
				"n": {Output: map[string]core.Ref{"p": {Inline: c.value}}},
			}
			got, err := resolveUpstreamPath(prior, c.path)
			if err != nil || got != c.want {
				t.Errorf("got %v err %v, want %v", got, err, c.want)
			}
		})
	}
}

func TestUpstream_IndexValue_OutOfRange_AllTypes(t *testing.T) {
	for name, value := range map[string]any{
		"[]string":            []string{"only"},
		"[]map[string]any":    []map[string]any{{"k": "v"}},
		"[]map[string]string": []map[string]string{{"k": "v"}},
	} {
		t.Run(name, func(t *testing.T) {
			prior := map[string]core.Result{
				"n": {Output: map[string]core.Ref{"p": {Inline: value}}},
			}
			_, err := resolveUpstreamPath(prior, "n.p[99]")
			if err == nil || !strings.Contains(err.Error(), "out of range") {
				t.Errorf("err = %v, want 'out of range'", err)
			}
		})
	}
}

// ----------------------------------------------------------------------
// Template helpers — error surfacing in SubstituteString and value walk
// ----------------------------------------------------------------------

func TestSubstituteString_PassThroughWhenNoPlaceholder(t *testing.T) {
	got, err := SubstituteString(t.Context(), "plain text, no placeholders", nil)
	if err != nil || got != "plain text, no placeholders" {
		t.Errorf("got %q err %v", got, err)
	}
}

// ----------------------------------------------------------------------
// resolveSlice — direct exercise of the slice walk
// ----------------------------------------------------------------------

func TestResolveSecrets_NestedSlice(t *testing.T) {
	providers := newProviders(stubProvider{
		scheme: "secret",
		values: map[string]string{"TOK": "secret-xyz"},
	})
	job := &core.Job{
		Params: map[string]any{
			"items": []any{
				"${secret.TOK}",
				map[string]any{"auth": "${secret.TOK}"},
				[]any{"${secret.TOK}", "literal"},
			},
		},
	}
	if err := resolveSecrets(t.Context(), providers, job); err != nil {
		t.Fatalf("resolveSecrets: %v", err)
	}
	items := job.Params["items"].([]any)
	if items[0] != "secret-xyz" {
		t.Errorf("items[0] = %v", items[0])
	}
	if items[1].(map[string]any)["auth"] != "secret-xyz" {
		t.Errorf("nested map auth = %v", items[1])
	}
	inner := items[2].([]any)
	if inner[0] != "secret-xyz" {
		t.Errorf("inner slice [0] = %v", inner[0])
	}
}

func TestResolveSecrets_NestedSliceError(t *testing.T) {
	providers := newProviders(stubProvider{scheme: "secret", err: errors.New("backend down")})
	job := &core.Job{Params: map[string]any{
		"items": []any{"secret://X"},
	}}
	err := resolveSecrets(t.Context(), providers, job)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "items") {
		t.Errorf("err = %v, want annotation with 'items'", err)
	}
}

// ----------------------------------------------------------------------
// forwardProgress — ctx cancel mid-flight drops remaining events but
// keeps draining the input channel so Execute can't block on it.
// ----------------------------------------------------------------------

func TestForwardProgress_DrainsAfterCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	in := make(chan core.Progress, 2)
	out := make(chan GraphProgress) // unbuffered so the send blocks
	done := make(chan struct{})
	go forwardProgress(ctx, "j", "n", in, out, nil, done)

	// Send one event with no reader on out; the goroutine blocks in
	// `case out <- …`. Cancel — it must abandon, drain remaining events
	// from `in`, then close `done`.
	in <- core.Progress{Message: "first"}
	in <- core.Progress{Message: "second"}
	cancel()
	close(in)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("forwardProgress did not exit after ctx cancel")
	}
}

// ----------------------------------------------------------------------
// validate() falls back to core.Validate when the resolver doesn't
// implement Manifests — covered by handing a bare interface that
// implements Resolve only.
// ----------------------------------------------------------------------

type bareResolver struct {
	fn func(string) (core.Transport, error)
}

func (b bareResolver) Resolve(_ context.Context, id string) (core.Transport, error) {
	return b.fn(id)
}

func TestEngine_validate_FallsBackToCoreValidateWithoutManifests(t *testing.T) {
	// A resolver that doesn't expose Manifests forces validate() onto
	// the core.Validate path. Pass a graph that core.Validate accepts
	// (no nodes) so we observe Run completing cleanly.
	e := &Engine{Resolver: bareResolver{fn: func(id string) (core.Transport, error) {
		return nil, errors.New("never reached")
	}}}
	res, err := e.Run(t.Context(), core.Graph{ID: "empty"}, nil)
	if err != nil {
		t.Fatalf("Run on empty graph: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Errorf("status = %q, want ok", res.Status)
	}
}

// ----------------------------------------------------------------------
// resolveMap nested-map error annotation: the "%s.%w" branch only
// fires when the failure comes from inside a nested map (not a string
// or slice). Pin it explicitly.
// ----------------------------------------------------------------------

func TestResolveSecrets_NestedMapErrorAnnotated(t *testing.T) {
	providers := newProviders(stubProvider{scheme: "secret", err: errors.New("backend down")})
	job := &core.Job{Params: map[string]any{
		"outer": map[string]any{
			"inner": "secret://X",
		},
	}}
	err := resolveSecrets(t.Context(), providers, job)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "outer") || !strings.Contains(err.Error(), "inner") {
		t.Errorf("err = %v, want one mentioning 'outer' and 'inner'", err)
	}
}

// ----------------------------------------------------------------------
// SubstituteString: after a substituter error, subsequent matches in
// the same string skip rather than re-erroring. Without this branch
// being exercised, the second ${...} in the string would never be hit.
// ----------------------------------------------------------------------

func TestSubstituteString_FirstErrorSkipsRemainingPlaceholders(t *testing.T) {
	calls := 0
	sub := func(_ context.Context, _, _ string) (string, bool, error) {
		calls++
		return "", true, errors.New("boom")
	}
	_, err := SubstituteString(t.Context(), "${secret.A} and ${secret.B}", sub)
	if err == nil {
		t.Fatal("expected error")
	}
	if calls != 1 {
		t.Errorf("substituter called %d times, want 1 (later matches must skip)", calls)
	}
}

// ----------------------------------------------------------------------
// getField on a non-object value returns a typed error.
// ----------------------------------------------------------------------

func TestUpstream_GetField_OnScalarFails(t *testing.T) {
	prior := map[string]core.Result{
		"n": {Output: map[string]core.Ref{"p": {Inline: "just a string"}}},
	}
	_, err := resolveUpstreamPath(prior, "n.p.field")
	if err == nil || !strings.Contains(err.Error(), "expected object") {
		t.Errorf("err = %v, want one mentioning 'expected object'", err)
	}
}

// ----------------------------------------------------------------------
// isValidScheme rejects empty + uppercase + special characters.
// ----------------------------------------------------------------------

func TestIsValidScheme(t *testing.T) {
	good := []string{"env", "vault", "x-y", "env_1", "abc123"}
	bad := []string{"", "ENV", "ENv", "with space", "weird$char", "ünicode"}
	for _, s := range good {
		if !isValidScheme(s) {
			t.Errorf("isValidScheme(%q) = false, want true", s)
		}
	}
	for _, s := range bad {
		if isValidScheme(s) {
			t.Errorf("isValidScheme(%q) = true, want false", s)
		}
	}
}

// ----------------------------------------------------------------------
// cancelledResult — direct call so the function is exercised end-to-end
// (it's also exercised by the Run cancel test, but pin the shape here).
// ----------------------------------------------------------------------

func TestCancelledResult(t *testing.T) {
	res := cancelledResult("g", map[string]core.Result{"a": {Status: core.StatusOK}}, context.Canceled)
	if res.Status != core.StatusError {
		t.Errorf("status = %q", res.Status)
	}
	if res.Error == nil || res.Error.Code != "cancelled" {
		t.Errorf("error = %+v", res.Error)
	}
	if _, ok := res.Nodes["a"]; !ok {
		t.Errorf("nodes lost: %+v", res.Nodes)
	}
}
