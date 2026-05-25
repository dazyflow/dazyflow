package daemon

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"git.sr.ht/~klahr/hazy-flow/auth"
	"git.sr.ht/~klahr/hazy-flow/core"
	"git.sr.ht/~klahr/hazy-flow/engine"
	"git.sr.ht/~klahr/hazy-flow/engine/jobstore"
	"git.sr.ht/~klahr/hazy-flow/workspace"
)

// gatewayHarness assembles a minimal daemon stack + an HTTP gateway,
// returning the Bearer token tests should send for an authenticated
// principal scoped to t/ws.
type gatewayHarness struct {
	gw    *HTTPGateway
	svc   *Service
	store core.JobStore
	ws    *workspace.Store
	bus   *MemoryBus
	token string
}

func newGatewayHarness(t *testing.T) *gatewayHarness {
	t.Helper()
	ks := auth.NewMemKeyStore()
	role := core.Role{Name: "editor", Permissions: []core.Permission{
		core.PermGraphRun, core.PermGraphEdit, core.PermGraphAdmin,
	}}
	_, token, err := auth.IssueAPIKey(ks, t.Context(), "k1", "t", "ws", "alice", []core.Role{role})
	if err != nil {
		t.Fatalf("issue key: %v", err)
	}
	wsStore, _ := workspace.OpenFS("")
	store := jobstore.NewMemory()
	bus := NewMemoryBus()
	eng := &engine.Engine{Resolver: &engine.NodeResolver{Native: engine.Default}}
	svc := &Service{
		Auth:       auth.Chain{&auth.APIKeyAuthenticator{Store: ks}},
		Workspaces: MapWorkspaces{"t/ws": wsStore},
		Jobs:       store,
		Engine:     eng,
		Bus:        bus,
	}
	return &gatewayHarness{
		gw: NewHTTPGateway(svc), svc: svc, store: store, ws: wsStore, bus: bus, token: token,
	}
}

func (h *gatewayHarness) do(t *testing.T, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var bodyReader *bytes.Buffer
	if body != nil {
		b, _ := json.Marshal(body)
		bodyReader = bytes.NewBuffer(b)
	} else {
		bodyReader = bytes.NewBuffer(nil)
	}
	req := httptest.NewRequest(method, path, bodyReader)
	req.Header.Set("Authorization", "Bearer "+h.token)
	req.Header.Set("Content-Type", "application/json")
	rw := httptest.NewRecorder()
	ServeForTest(h.gw, rw, req)
	return rw
}

func TestHTTPGateway_HealthzNoAuth(t *testing.T) {
	h := newGatewayHarness(t)
	req := httptest.NewRequest("GET", "/healthz", nil)
	rw := httptest.NewRecorder()
	ServeForTest(h.gw, rw, req)
	if rw.Code != http.StatusOK {
		t.Errorf("code = %d, want 200", rw.Code)
	}
}

func TestHTTPGateway_RejectsMissingBearer(t *testing.T) {
	h := newGatewayHarness(t)
	req := httptest.NewRequest("GET", "/api/v1/modules", nil)
	rw := httptest.NewRecorder()
	ServeForTest(h.gw, rw, req)
	if rw.Code != http.StatusUnauthorized {
		t.Errorf("code = %d, want 401", rw.Code)
	}
}

func TestHTTPGateway_RejectsBadBearer(t *testing.T) {
	h := newGatewayHarness(t)
	req := httptest.NewRequest("GET", "/api/v1/modules", nil)
	req.Header.Set("Authorization", "Bearer not-a-real-token")
	rw := httptest.NewRecorder()
	ServeForTest(h.gw, rw, req)
	if rw.Code != http.StatusUnauthorized {
		t.Errorf("code = %d, want 401", rw.Code)
	}
}

func TestHTTPGateway_ListModules(t *testing.T) {
	h := newGatewayHarness(t)
	// Require modules package to be linked so engine.Default has entries.
	// The flow modules are imported transitively by the daemon's other
	// files (worker), so the registry is populated.
	rw := h.do(t, "GET", "/api/v1/modules", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("code = %d body = %s", rw.Code, rw.Body.String())
	}
	var out struct {
		Modules []core.Manifest `json:"modules"`
	}
	_ = json.Unmarshal(rw.Body.Bytes(), &out)
	if len(out.Modules) == 0 {
		t.Skip("module registry is empty in this test binary — daemon tests don't import modules/")
	}
}

func TestHTTPGateway_SaveAndLoadGraph(t *testing.T) {
	h := newGatewayHarness(t)
	g := core.Graph{
		ID: "my-graph", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{{
			ID: "n1", Module: "noop",
			Position: &core.Position{X: 100, Y: 50},
		}},
	}
	rw := h.do(t, "PUT", "/api/v1/graphs/t/ws/my-graph", g)
	if rw.Code != http.StatusOK {
		t.Fatalf("save: code = %d body = %s", rw.Code, rw.Body.String())
	}
	rw = h.do(t, "GET", "/api/v1/graphs/t/ws/my-graph", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("load: code = %d body = %s", rw.Code, rw.Body.String())
	}
	var loaded core.Graph
	if err := json.Unmarshal(rw.Body.Bytes(), &loaded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if loaded.ID != "my-graph" {
		t.Errorf("id = %q", loaded.ID)
	}
	// Position must round-trip.
	if loaded.Nodes[0].Position == nil ||
		loaded.Nodes[0].Position.X != 100 ||
		loaded.Nodes[0].Position.Y != 50 {
		t.Errorf("position lost: %+v", loaded.Nodes[0].Position)
	}
}

func TestHTTPGateway_ListGraphsRequiresParams(t *testing.T) {
	h := newGatewayHarness(t)
	rw := h.do(t, "GET", "/api/v1/graphs", nil)
	if rw.Code != http.StatusBadRequest {
		t.Errorf("code = %d, want 400", rw.Code)
	}
}

func TestHTTPGateway_JobSnapshotNotFound(t *testing.T) {
	h := newGatewayHarness(t)
	rw := h.do(t, "GET", "/api/v1/jobs/does-not-exist", nil)
	if rw.Code != http.StatusNotFound {
		t.Errorf("code = %d, want 404", rw.Code)
	}
}

func TestHTTPGateway_NodeSnapshotMissingParentGraphIs404(t *testing.T) {
	h := newGatewayHarness(t)
	rw := h.do(t, "GET", "/api/v1/jobs/no-such-run/nodes/some-node", nil)
	if rw.Code != http.StatusNotFound {
		t.Errorf("code = %d, want 404", rw.Code)
	}
}

func TestHTTPGateway_NodeSnapshotReturnsRecord(t *testing.T) {
	h := newGatewayHarness(t)
	// Seed both records directly so we exercise the endpoint without
	// having to drive a worker through a graph here.
	graphRec := core.JobRecord{
		ID:           "run-xyz",
		Kind:         core.JobKindGraph,
		GraphID:      "g",
		Tenant:       "t",
		Workspace:    "ws",
		Status:       core.JobStatusSucceeded,
		GraphPayload: []byte(`{"id":"g","tenant":"t","workspace":"ws"}`),
	}
	_ = h.store.Enqueue(t.Context(), graphRec)

	nodeRec := core.JobRecord{
		ID:         NodeJobID("run-xyz", "step1"),
		Kind:       core.JobKindNode,
		GraphRunID: "run-xyz",
		GraphID:    "g",
		NodeID:     "step1",
		Tenant:     "t",
		Workspace:  "ws",
		Status:     core.JobStatusSucceeded,
		Result: &core.Result{
			Status: core.StatusOK,
			Output: map[string]core.Ref{
				"out": {MIME: "text/plain", Inline: "hello"},
			},
		},
	}
	_ = h.store.Enqueue(t.Context(), nodeRec)

	rw := h.do(t, "GET", "/api/v1/jobs/run-xyz/nodes/step1", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("code = %d body = %s", rw.Code, rw.Body.String())
	}
	var got core.JobRecord
	if err := json.Unmarshal(rw.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Status != core.JobStatusSucceeded {
		t.Errorf("status = %q", got.Status)
	}
	if got.Result == nil || got.Result.Output["out"].Inline != "hello" {
		t.Errorf("output = %+v", got.Result)
	}
}

func TestHTTPGateway_ListRunsReturnsNewestFirst(t *testing.T) {
	h := newGatewayHarness(t)
	// First save the graph so the tenant-scope check passes.
	if _, err := h.ws.Save(core.Graph{
		ID: "g1", Tenant: "t", Workspace: "ws",
	}, "test"); err != nil {
		t.Fatalf("save graph: %v", err)
	}
	// Seed three graph-records and one bogus node-record (which must be
	// filtered out of the runs list).
	for i, id := range []string{"run-a", "run-b", "run-c"} {
		_ = h.store.Enqueue(t.Context(), core.JobRecord{
			ID: id, Kind: core.JobKindGraph, GraphID: "g1",
			Tenant: "t", Workspace: "ws",
			Status: core.JobStatusSucceeded,
			// EnqueuedAt is set by the store; use ordering by relying on
			// each Enqueue happening sequentially.
		})
		_ = i
	}
	_ = h.store.Enqueue(t.Context(), core.JobRecord{
		ID: NodeJobID("run-a", "n1"), Kind: core.JobKindNode,
		GraphID: "g1", Tenant: "t", Workspace: "ws",
	})

	rw := h.do(t, "GET", "/api/v1/graphs/t/ws/g1/runs", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("code = %d body = %s", rw.Code, rw.Body.String())
	}
	var out struct {
		Runs []struct {
			ID string `json:"id"`
		} `json:"runs"`
	}
	_ = json.Unmarshal(rw.Body.Bytes(), &out)
	if len(out.Runs) != 3 {
		t.Fatalf("runs = %+v, want 3 (no node-record)", out.Runs)
	}
	// Memory store sorts ListByGraph by enqueued_at DESC, so newest
	// insertion comes first.
	if out.Runs[0].ID != "run-c" {
		t.Errorf("first = %q, want run-c", out.Runs[0].ID)
	}
}

func TestHTTPGateway_ListRunsUnknownGraphIs404(t *testing.T) {
	h := newGatewayHarness(t)
	rw := h.do(t, "GET", "/api/v1/graphs/t/ws/no-such/runs", nil)
	if rw.Code != http.StatusNotFound {
		t.Errorf("code = %d, want 404", rw.Code)
	}
}

func TestHTTPGateway_ListRunsStatusFilter(t *testing.T) {
	h := newGatewayHarness(t)
	if _, err := h.ws.Save(core.Graph{
		ID: "g1", Tenant: "t", Workspace: "ws",
	}, "test"); err != nil {
		t.Fatalf("save graph: %v", err)
	}
	for _, e := range []struct {
		id     string
		status core.JobStatus
	}{
		{"run-1", core.JobStatusSucceeded},
		{"run-2", core.JobStatusFailed},
		{"run-3", core.JobStatusSucceeded},
	} {
		_ = h.store.Enqueue(t.Context(), core.JobRecord{
			ID: e.id, Kind: core.JobKindGraph, GraphID: "g1",
			Tenant: "t", Workspace: "ws", Status: e.status,
		})
	}
	rw := h.do(t, "GET", "/api/v1/graphs/t/ws/g1/runs?status=failed", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("code = %d", rw.Code)
	}
	var out struct {
		Runs []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"runs"`
	}
	_ = json.Unmarshal(rw.Body.Bytes(), &out)
	if len(out.Runs) != 1 || out.Runs[0].ID != "run-2" {
		t.Errorf("filtered = %+v, want only run-2", out.Runs)
	}
}

func TestHTTPGateway_ListRunsOffsetLimit(t *testing.T) {
	h := newGatewayHarness(t)
	if _, err := h.ws.Save(core.Graph{ID: "g1", Tenant: "t", Workspace: "ws"}, "test"); err != nil {
		t.Fatalf("save: %v", err)
	}
	for _, id := range []string{"r1", "r2", "r3", "r4", "r5"} {
		_ = h.store.Enqueue(t.Context(), core.JobRecord{
			ID: id, Kind: core.JobKindGraph, GraphID: "g1",
			Tenant: "t", Workspace: "ws", Status: core.JobStatusSucceeded,
		})
	}
	rw := h.do(t, "GET", "/api/v1/graphs/t/ws/g1/runs?limit=2&offset=2", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("code = %d", rw.Code)
	}
	var out struct {
		Runs []struct{ ID string } `json:"runs"`
	}
	_ = json.Unmarshal(rw.Body.Bytes(), &out)
	if len(out.Runs) != 2 {
		t.Fatalf("len = %d, want 2", len(out.Runs))
	}
	// newest first: r5, r4, [r3, r2], r1 — offset 2 + limit 2 = [r3, r2]
	if out.Runs[0].ID != "r3" || out.Runs[1].ID != "r2" {
		t.Errorf("got %+v, want [r3 r2]", out.Runs)
	}
}

func TestHTTPGateway_ListAllRunsAcrossGraphs(t *testing.T) {
	h := newGatewayHarness(t)
	for i, e := range []struct {
		id      string
		graphID string
	}{
		{"r1", "gA"},
		{"r2", "gB"},
		{"r3", "gA"},
	} {
		_ = i
		_ = h.store.Enqueue(t.Context(), core.JobRecord{
			ID: e.id, Kind: core.JobKindGraph, GraphID: e.graphID,
			Tenant: "t", Workspace: "ws", Status: core.JobStatusSucceeded,
		})
	}
	// A node-record in the same tenant that must NOT appear.
	_ = h.store.Enqueue(t.Context(), core.JobRecord{
		ID: NodeJobID("r1", "n"), Kind: core.JobKindNode,
		GraphID: "gA", Tenant: "t", Workspace: "ws",
	})
	rw := h.do(t, "GET", "/api/v1/runs", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("code = %d body = %s", rw.Code, rw.Body.String())
	}
	var out struct {
		Runs []struct {
			ID      string `json:"id"`
			GraphID string `json:"graph_id"`
		} `json:"runs"`
	}
	_ = json.Unmarshal(rw.Body.Bytes(), &out)
	if len(out.Runs) != 3 {
		t.Fatalf("len = %d, want 3, got %+v", len(out.Runs), out.Runs)
	}
	// The cross-graph payload must carry graph_id so the UI can link
	// to each run's editor.
	if out.Runs[0].GraphID == "" {
		t.Error("missing graph_id in cross-graph runs response")
	}
}

func TestHTTPGateway_ListAllRunsScopedToTenant(t *testing.T) {
	h := newGatewayHarness(t)
	// Run in OUR tenant + workspace
	_ = h.store.Enqueue(t.Context(), core.JobRecord{
		ID: "ours", Kind: core.JobKindGraph, GraphID: "gA",
		Tenant: "t", Workspace: "ws", Status: core.JobStatusSucceeded,
	})
	// Run belonging to a different tenant — must NOT show up.
	_ = h.store.Enqueue(t.Context(), core.JobRecord{
		ID: "theirs", Kind: core.JobKindGraph, GraphID: "gA",
		Tenant: "other", Workspace: "ws", Status: core.JobStatusSucceeded,
	})
	rw := h.do(t, "GET", "/api/v1/runs", nil)
	var out struct {
		Runs []struct{ ID string } `json:"runs"`
	}
	_ = json.Unmarshal(rw.Body.Bytes(), &out)
	if len(out.Runs) != 1 || out.Runs[0].ID != "ours" {
		t.Errorf("cross-tenant leak: got %+v", out.Runs)
	}
}

func TestHTTPGateway_NodeSnapshotUnknownNodeIs404(t *testing.T) {
	h := newGatewayHarness(t)
	// Parent graph exists; node doesn't.
	_ = h.store.Enqueue(t.Context(), core.JobRecord{
		ID:           "run-xyz",
		Kind:         core.JobKindGraph,
		Tenant:       "t",
		Workspace:    "ws",
		Status:       core.JobStatusRunning,
		GraphPayload: []byte(`{"id":"g"}`),
	})
	rw := h.do(t, "GET", "/api/v1/jobs/run-xyz/nodes/ghost", nil)
	if rw.Code != http.StatusNotFound {
		t.Errorf("code = %d, want 404", rw.Code)
	}
}

func TestHTTPGateway_CORSHeadersOnPreflight(t *testing.T) {
	h := newGatewayHarness(t)
	req := httptest.NewRequest("OPTIONS", "/api/v1/modules", nil)
	rw := httptest.NewRecorder()
	ServeForTest(h.gw, rw, req)
	if rw.Code != http.StatusNoContent {
		t.Errorf("preflight code = %d, want 204", rw.Code)
	}
	if rw.Header().Get("Access-Control-Allow-Origin") == "" {
		t.Error("missing Access-Control-Allow-Origin")
	}
	if !strings.Contains(rw.Header().Get("Access-Control-Allow-Methods"), "PUT") {
		t.Errorf("ACAM = %q, expected to include PUT", rw.Header().Get("Access-Control-Allow-Methods"))
	}
}
