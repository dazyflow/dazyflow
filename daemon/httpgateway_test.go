// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"git.sr.ht/~klahr/dazyflow/auth"
	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/engine"
	"git.sr.ht/~klahr/dazyflow/engine/jobstore"
	"git.sr.ht/~klahr/dazyflow/workspace"
)

// gatewayHarness assembles a minimal daemon stack + an HTTP gateway,
// returning the Bearer token tests should send for an authenticated
// principal scoped to t/ws.
type gatewayHarness struct {
	gw            *HTTPGateway
	svc           *Service
	store         core.JobStore
	ws            *workspace.Store
	bus           *MemoryBus
	ks            *auth.MemKeyStore
	token         string // editor token
	adminToken    string // organization:admin token (issued lazily by adminDo)
	platformToken string // platform:admin token (issued lazily by platformDo)
}

func newGatewayHarness(t *testing.T) *gatewayHarness {
	t.Helper()
	ks := auth.NewMemKeyStore()
	role := core.Role{Name: "editor", Permissions: []core.Permission{
		core.PermGraphRun, core.PermGraphEdit, core.PermGraphAdmin,
	}}
	_, token, err := auth.IssueAPIKey(ks, t.Context(), "k1", "t", "ws", "alice", []core.Role{role}, nil)
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
		AdminKeys:  ks,
	}
	return &gatewayHarness{
		gw: NewHTTPGateway(svc), svc: svc, store: store, ws: wsStore, bus: bus, ks: ks, token: token,
	}
}

// adminDo runs the request with a organization:admin bearer token, minting
// one on first use so individual tests don't have to wire it.
func (h *gatewayHarness) adminDo(t *testing.T, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	if h.adminToken == "" {
		role := core.Role{Name: "admin", Permissions: []core.Permission{core.PermOrganizationAdmin}}
		_, tok, err := auth.IssueAPIKey(h.ks, t.Context(), "k-admin", "t", "ws", "root", []core.Role{role}, nil)
		if err != nil {
			t.Fatalf("issue admin key: %v", err)
		}
		h.adminToken = tok
	}
	var bodyReader *bytes.Buffer
	if body != nil {
		b, _ := json.Marshal(body)
		bodyReader = bytes.NewBuffer(b)
	} else {
		bodyReader = bytes.NewBuffer(nil)
	}
	req := httptest.NewRequest(method, path, bodyReader)
	req.Header.Set("Authorization", "Bearer "+h.adminToken)
	req.Header.Set("Content-Type", "application/json")
	rw := httptest.NewRecorder()
	ServeForTest(h.gw, rw, req)
	return rw
}

// platformDo runs the request with a platform:admin bearer token (no
// tenant binding), minting one on first use. Used for instance-wide
// settings that tenant admins must not reach (e.g. OAuth provider creds).
func (h *gatewayHarness) platformDo(t *testing.T, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	if h.platformToken == "" {
		role := core.Role{Name: "platform", Permissions: []core.Permission{core.PermPlatformAdmin}}
		_, tok, err := auth.IssueAPIKey(h.ks, t.Context(), "k-platform", "", "", "op", []core.Role{role}, nil)
		if err != nil {
			t.Fatalf("issue platform key: %v", err)
		}
		h.platformToken = tok
	}
	var bodyReader *bytes.Buffer
	if body != nil {
		b, _ := json.Marshal(body)
		bodyReader = bytes.NewBuffer(b)
	} else {
		bodyReader = bytes.NewBuffer(nil)
	}
	req := httptest.NewRequest(method, path, bodyReader)
	req.Header.Set("Authorization", "Bearer "+h.platformToken)
	req.Header.Set("Content-Type", "application/json")
	rw := httptest.NewRecorder()
	ServeForTest(h.gw, rw, req)
	return rw
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

// TestHTTPGateway_LabelRevision covers the label route: naming the current
// draft (HEAD) attaches a per-commit label that surfaces in the history
// listing, and an empty label clears it.
func TestHTTPGateway_LabelRevision(t *testing.T) {
	h := newGatewayHarness(t)
	const fid = "t%2Fws%2Flabelme"
	if rw := h.do(t, "PUT", "/api/v1/me/flows/"+fid, core.Graph{
		ID: "labelme", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{{ID: "a", Module: "noop"}},
	}); rw.Code != http.StatusOK {
		t.Fatalf("create flow: code=%d body=%s", rw.Code, rw.Body.String())
	}

	// Name the current draft.
	rw := h.do(t, "POST", "/api/v1/me/flows/"+fid+"/label", map[string]any{"label": "Black Friday config"})
	if rw.Code != http.StatusOK {
		t.Fatalf("label: code=%d body=%s", rw.Code, rw.Body.String())
	}
	var lr struct {
		Commit string `json:"commit"`
		Label  string `json:"label"`
	}
	if err := json.Unmarshal(rw.Body.Bytes(), &lr); err != nil {
		t.Fatalf("unmarshal label resp: %v", err)
	}
	if lr.Label != "Black Friday config" || lr.Commit == "" {
		t.Fatalf("label resp = %+v, want labeled non-empty commit", lr)
	}

	// History surfaces the label keyed to that commit.
	labelInHistory := func() string {
		rw := h.do(t, "GET", "/api/v1/me/flows/"+fid+"/history", nil)
		if rw.Code != http.StatusOK {
			t.Fatalf("history: code=%d body=%s", rw.Code, rw.Body.String())
		}
		var hist struct {
			Revisions []workspace.Revision `json:"revisions"`
		}
		if err := json.Unmarshal(rw.Body.Bytes(), &hist); err != nil {
			t.Fatalf("unmarshal history: %v", err)
		}
		for _, r := range hist.Revisions {
			if r.Commit == lr.Commit {
				return r.Label
			}
		}
		t.Fatalf("labeled commit %s not found in history", lr.Commit)
		return ""
	}
	if got := labelInHistory(); got != "Black Friday config" {
		t.Fatalf("history label = %q, want \"Black Friday config\"", got)
	}

	// Empty label clears it.
	if rw := h.do(t, "POST", "/api/v1/me/flows/"+fid+"/label", map[string]any{"label": ""}); rw.Code != http.StatusOK {
		t.Fatalf("clear label: code=%d body=%s", rw.Code, rw.Body.String())
	}
	if got := labelInHistory(); got != "" {
		t.Fatalf("after clear, history label = %q, want \"\"", got)
	}
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

func TestHTTPGateway_ReadyzNoCheckIsReady(t *testing.T) {
	h := newGatewayHarness(t)
	req := httptest.NewRequest("GET", "/readyz", nil)
	rw := httptest.NewRecorder()
	ServeForTest(h.gw, rw, req)
	if rw.Code != http.StatusOK {
		t.Errorf("code = %d, want 200 (nil ReadyCheck == ready)", rw.Code)
	}
}

func TestHTTPGateway_ReadyzFailingCheck503(t *testing.T) {
	h := newGatewayHarness(t)
	h.gw.ReadyCheck = func(context.Context) error { return errReadyTest }
	req := httptest.NewRequest("GET", "/readyz", nil)
	rw := httptest.NewRecorder()
	ServeForTest(h.gw, rw, req)
	if rw.Code != http.StatusServiceUnavailable {
		t.Errorf("code = %d, want 503 when ReadyCheck fails", rw.Code)
	}
}

var errReadyTest = errors.New("dep down")

func TestHTTPGateway_RequestIsHTTPS(t *testing.T) {
	h := newGatewayHarness(t)
	mk := func(xfp string) *http.Request {
		r := httptest.NewRequest("GET", "/x", nil)
		if xfp != "" {
			r.Header.Set("X-Forwarded-Proto", xfp)
		}
		return r // httptest requests have r.TLS == nil
	}
	// TrustProxyHeaders off: forwarded-proto is ignored.
	h.gw.TrustProxyHeaders = false
	if h.gw.requestIsHTTPS(mk("https")) {
		t.Error("must NOT trust X-Forwarded-Proto when TrustProxyHeaders is off")
	}
	// On: forwarded https counts as secure; http (or absent) doesn't.
	h.gw.TrustProxyHeaders = true
	if !h.gw.requestIsHTTPS(mk("https")) {
		t.Error("should treat X-Forwarded-Proto:https as secure when trusted")
	}
	if h.gw.requestIsHTTPS(mk("http")) {
		t.Error("X-Forwarded-Proto:http is not secure")
	}
	if h.gw.requestIsHTTPS(mk("")) {
		t.Error("absent X-Forwarded-Proto is not secure")
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
	rw := h.do(t, "PUT", "/api/v1/me/flows/t%2Fws%2Fmy-graph", g)
	if rw.Code != http.StatusOK {
		t.Fatalf("save: code = %d body = %s", rw.Code, rw.Body.String())
	}
	rw = h.do(t, "GET", "/api/v1/me/flows/t%2Fws%2Fmy-graph", nil)
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

func TestHTTPGateway_ListFlows_UsesPrincipalScope(t *testing.T) {
	// The /me/flows route falls back to the caller's tenant +
	// workspace from the session when ?tenant=&workspace= aren't
	// supplied. Distinct from the legacy /api/v1/graphs which
	// 400'd on missing params — the /me/ prefix means "use my scope".
	h := newGatewayHarness(t)
	rw := h.do(t, "GET", "/api/v1/me/flows", nil)
	if rw.Code != http.StatusOK {
		t.Errorf("code = %d, want 200 (principal scope should apply)", rw.Code)
	}
}

func TestHTTPGateway_JobSnapshotNotFound(t *testing.T) {
	h := newGatewayHarness(t)
	rw := h.do(t, "GET", "/api/v1/me/runs/does-not-exist", nil)
	if rw.Code != http.StatusNotFound {
		t.Errorf("code = %d, want 404", rw.Code)
	}
}

func TestHTTPGateway_NodeSnapshotMissingParentGraphIs404(t *testing.T) {
	h := newGatewayHarness(t)
	rw := h.do(t, "GET", "/api/v1/me/runs/no-such-run/nodes/some-node", nil)
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

	rw := h.do(t, "GET", "/api/v1/me/runs/run-xyz/nodes/step1", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("code = %d body = %s", rw.Code, rw.Body.String())
	}
	var got nodeRunView
	if err := json.Unmarshal(rw.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Status != core.JobStatusSucceeded {
		t.Errorf("status = %q", got.Status)
	}
	if got.NodeID != "step1" {
		t.Errorf("node_id = %q", got.NodeID)
	}
	if got.Outputs["out"].Inline != "hello" {
		t.Errorf("outputs = %+v", got.Outputs)
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

	rw := h.do(t, "GET", "/api/v1/me/flows/t%2Fws%2Fg1/runs", nil)
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
	rw := h.do(t, "GET", "/api/v1/me/flows/t%2Fws%2Fno-such/runs", nil)
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
	rw := h.do(t, "GET", "/api/v1/me/flows/t%2Fws%2Fg1/runs?status=failed", nil)
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
	rw := h.do(t, "GET", "/api/v1/me/flows/t%2Fws%2Fg1/runs?limit=2&offset=2", nil)
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
	rw := h.do(t, "GET", "/api/v1/me/runs", nil)
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

func TestHTTPGateway_ListPendingApprovals(t *testing.T) {
	h := newGatewayHarness(t)
	// Parent graph-record so any tenant-scope check downstream passes.
	_ = h.store.Enqueue(t.Context(), core.JobRecord{
		ID: "run-1", Kind: core.JobKindGraph, GraphID: "g1",
		Tenant: "t", Workspace: "ws", Status: core.JobStatusRunning,
	})
	// Awaiting await_approval node — should appear.
	_ = h.store.Enqueue(t.Context(), core.JobRecord{
		ID: NodeJobID("run-1", "approval"), Kind: core.JobKindNode,
		GraphRunID: "run-1", GraphID: "g1",
		NodeID: "approval", Tenant: "t", Workspace: "ws",
		Status: core.JobStatusAwaiting,
		Result: &core.Result{
			Status: core.StatusAwaiting,
			Output: map[string]core.Ref{
				"pending_url": {MIME: "text/plain", Inline: "https://dzd/approve/run-1/approval?token=abc"},
				"prompt":      {MIME: "text/plain", Inline: "Approve invoice?"},
			},
		},
	})
	// Awaiting subgraph node — must NOT appear (no pending_url).
	_ = h.store.Enqueue(t.Context(), core.JobRecord{
		ID: NodeJobID("run-1", "subgraph"), Kind: core.JobKindNode,
		GraphRunID: "run-1", GraphID: "g1",
		NodeID: "subgraph", Tenant: "t", Workspace: "ws",
		Status: core.JobStatusAwaiting,
		Result: &core.Result{
			Output: map[string]core.Ref{
				"pending_child_graph_id": {Inline: "child"},
			},
		},
	})
	// Awaiting node in different tenant — must NOT appear.
	_ = h.store.Enqueue(t.Context(), core.JobRecord{
		ID: NodeJobID("run-other", "approval"), Kind: core.JobKindNode,
		GraphRunID: "run-other", GraphID: "g2",
		NodeID: "approval", Tenant: "other", Workspace: "ws",
		Status: core.JobStatusAwaiting,
		Result: &core.Result{
			Output: map[string]core.Ref{
				"pending_url": {Inline: "https://dzd/approve/run-other/approval?token=zzz"},
			},
		},
	})

	rw := h.do(t, "GET", "/api/v1/approvals/pending", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("code = %d body = %s", rw.Code, rw.Body.String())
	}
	var out struct {
		Approvals []struct {
			RunID  string `json:"run_id"`
			NodeID string `json:"node_id"`
			Prompt string `json:"prompt"`
			URL    string `json:"url"`
		} `json:"approvals"`
	}
	_ = json.Unmarshal(rw.Body.Bytes(), &out)
	if len(out.Approvals) != 1 {
		t.Fatalf("approvals = %+v, want only the await_approval row", out.Approvals)
	}
	if out.Approvals[0].NodeID != "approval" {
		t.Errorf("node_id = %q", out.Approvals[0].NodeID)
	}
	if out.Approvals[0].Prompt != "Approve invoice?" {
		t.Errorf("prompt = %q", out.Approvals[0].Prompt)
	}
}

func TestHTTPGateway_ApproveAuthedResumesAwaitingNode(t *testing.T) {
	h := newGatewayHarness(t)
	// Need a parent graph-record + the awaiting node-record + a
	// payload so AdvanceAfterCompletion can load the graph and not
	// no-op on the dispatch.
	_ = h.store.Enqueue(t.Context(), core.JobRecord{
		ID: "run-1", Kind: core.JobKindGraph,
		GraphID: "g1", Tenant: "t", Workspace: "ws",
		Status:       core.JobStatusRunning,
		GraphPayload: []byte(`{"id":"g1","tenant":"t","workspace":"ws","nodes":[{"id":"a","module":"await_approval"}]}`),
	})
	_ = h.store.Enqueue(t.Context(), core.JobRecord{
		ID: NodeJobID("run-1", "a"), Kind: core.JobKindNode,
		GraphRunID: "run-1", GraphID: "g1",
		NodeID: "a", Tenant: "t", Workspace: "ws",
		Status: core.JobStatusAwaiting,
		Result: &core.Result{
			Status: core.StatusAwaiting,
			Output: map[string]core.Ref{
				"pending_url": {Inline: "https://dzd/approve/run-1/a?token=x"},
			},
		},
	})
	rw := h.do(t, "POST", "/api/v1/approvals/run-1/a?decision=approve&comment=looks+good", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("code = %d body = %s", rw.Code, rw.Body.String())
	}
	// Record should now be succeeded with the decision recorded.
	rec, _ := h.store.Get(t.Context(), NodeJobID("run-1", "a"))
	if rec.Status != core.JobStatusSucceeded {
		t.Errorf("status = %q, want succeeded", rec.Status)
	}
	// The decision is surfaced Branch-style: approve routes out the approved
	// port (and not rejected).
	if rec.Result == nil {
		t.Fatalf("resume result is nil")
	}
	if _, ok := rec.Result.Output["approved"]; !ok {
		t.Errorf("approved port missing on approve: %+v", rec.Result.Output)
	}
	if _, ok := rec.Result.Output["rejected"]; ok {
		t.Errorf("rejected port should be absent on approve: %+v", rec.Result.Output)
	}
	if got, _ := rec.Result.Output["comment"].Inline.(string); got != "looks good" {
		t.Errorf("comment = %q", got)
	}
	// "Approved by" defaults to the authenticated principal's subject.
	if got, _ := rec.Result.Output["approver"].Inline.(string); got != "alice" {
		t.Errorf("approver output = %q, want alice (principal subject)", got)
	}
}

// TestHTTPGateway_ApproveAuthedIgnoresSpoofedApprover locks in that the
// authenticated approval path attributes the approval to the proven
// principal, never a client-supplied ?approver=. Otherwise a valid caller
// could forge who approved in the audit trail and the node record.
func TestHTTPGateway_ApproveAuthedIgnoresSpoofedApprover(t *testing.T) {
	h := newGatewayHarness(t)
	// The approver is no longer a graph output; attribution lives in the
	// audit trail, so wire a log to observe it.
	h.gw.Audit = NewMemAuditLog()
	_ = h.store.Enqueue(t.Context(), core.JobRecord{
		ID: "run-spoof", Kind: core.JobKindGraph,
		GraphID: "g1", Tenant: "t", Workspace: "ws",
		Status:       core.JobStatusRunning,
		GraphPayload: []byte(`{"id":"g1","tenant":"t","workspace":"ws","nodes":[{"id":"a","module":"await_approval"}]}`),
	})
	_ = h.store.Enqueue(t.Context(), core.JobRecord{
		ID: NodeJobID("run-spoof", "a"), Kind: core.JobKindNode,
		GraphRunID: "run-spoof", GraphID: "g1",
		NodeID: "a", Tenant: "t", Workspace: "ws",
		Status: core.JobStatusAwaiting,
		Result: &core.Result{
			Status: core.StatusAwaiting,
			Output: map[string]core.Ref{"pending_url": {Inline: "https://dzd/approve/run-spoof/a?token=x"}},
		},
	})
	// Caller tries to attribute the approval to "mallory".
	rw := h.do(t, "POST", "/api/v1/approvals/run-spoof/a?decision=approve&approver=mallory", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("code = %d body = %s", rw.Code, rw.Body.String())
	}
	// Attribution must come from the authenticated principal in the audit
	// trail, never the client-supplied ?approver=.
	events, _ := h.gw.Audit.List(t.Context(), core.AuditQuery{Tenant: "t"})
	var approval *core.AuditEvent
	for i := range events {
		if events[i].Action == "approval" {
			approval = &events[i]
			break
		}
	}
	if approval == nil {
		t.Fatal("no approval audit event recorded")
	}
	if approval.Actor == "mallory" {
		t.Fatal("spoofed ?approver= was honored; approval must be attributed to the principal")
	}
	if approval.Actor != "alice" {
		t.Errorf("approval actor = %q, want alice (the authenticated principal)", approval.Actor)
	}
}

func TestHTTPGateway_ApproveAuthedRejectsCrossTenant(t *testing.T) {
	h := newGatewayHarness(t)
	// Belongs to a different tenant — the bearer principal can't see it.
	_ = h.store.Enqueue(t.Context(), core.JobRecord{
		ID: "run-other", Kind: core.JobKindGraph,
		GraphID: "g", Tenant: "other", Workspace: "ws",
		Status:       core.JobStatusRunning,
		GraphPayload: []byte(`{}`),
	})
	rw := h.do(t, "POST", "/api/v1/approvals/run-other/a?decision=approve", nil)
	if rw.Code != http.StatusNotFound && rw.Code != http.StatusForbidden {
		t.Errorf("code = %d, want 404 or 403", rw.Code)
	}
}

func TestHTTPGateway_ListAllRunsAcceptsWorkspaceNarrow(t *testing.T) {
	h := newGatewayHarness(t)
	// One run in our workspace, one in a sibling workspace within the
	// same tenant. An admin without a workspace binding should be
	// able to narrow to either via ?workspace=.
	_ = h.store.Enqueue(t.Context(), core.JobRecord{
		ID: "r-ws", Kind: core.JobKindGraph, GraphID: "g",
		Tenant: "t", Workspace: "ws", Status: core.JobStatusSucceeded,
	})
	_ = h.store.Enqueue(t.Context(), core.JobRecord{
		ID: "r-ws2", Kind: core.JobKindGraph, GraphID: "g2",
		Tenant: "t", Workspace: "ws2", Status: core.JobStatusSucceeded,
	})

	// Issue an unscoped admin key for this tenant.
	role := core.Role{Name: "ta", Permissions: []core.Permission{core.PermOrganizationAdmin}}
	_, adminTok, err := auth.IssueAPIKey(h.ks, t.Context(), "k-narrow-admin", "t", "", "root3", []core.Role{role}, nil)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	doAdmin := func(path string) []string {
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("Authorization", "Bearer "+adminTok)
		rw := httptest.NewRecorder()
		ServeForTest(h.gw, rw, req)
		if rw.Code != http.StatusOK {
			t.Fatalf("%s: code=%d body=%s", path, rw.Code, rw.Body.String())
		}
		var out struct {
			Runs []struct{ ID string } `json:"runs"`
		}
		_ = json.Unmarshal(rw.Body.Bytes(), &out)
		ids := make([]string, len(out.Runs))
		for i, r := range out.Runs {
			ids[i] = r.ID
		}
		return ids
	}
	// Unfiltered: both visible.
	all := doAdmin("/api/v1/me/runs")
	if len(all) != 2 {
		t.Errorf("unfiltered len = %d, want 2 (saw %v)", len(all), all)
	}
	// Narrow to ws.
	onlyWS := doAdmin("/api/v1/me/runs?workspace=ws")
	if len(onlyWS) != 1 || onlyWS[0] != "r-ws" {
		t.Errorf("workspace=ws filtered: %v, want [r-ws]", onlyWS)
	}
	// Narrow to ws2.
	onlyWS2 := doAdmin("/api/v1/me/runs?workspace=ws2")
	if len(onlyWS2) != 1 || onlyWS2[0] != "r-ws2" {
		t.Errorf("workspace=ws2 filtered: %v, want [r-ws2]", onlyWS2)
	}
}

func TestHTTPGateway_ListAllRuns_ScopedPrincipalIgnoresWorkspaceQuery(t *testing.T) {
	h := newGatewayHarness(t)
	// Two workspaces with runs.
	_ = h.store.Enqueue(t.Context(), core.JobRecord{
		ID: "mine", Kind: core.JobKindGraph, GraphID: "g",
		Tenant: "t", Workspace: "ws", Status: core.JobStatusSucceeded,
	})
	_ = h.store.Enqueue(t.Context(), core.JobRecord{
		ID: "theirs", Kind: core.JobKindGraph, GraphID: "g",
		Tenant: "t", Workspace: "ws2", Status: core.JobStatusSucceeded,
	})
	// h.do uses the bootstrap editor key bound to workspace "ws". Even
	// if we pass ?workspace=ws2, the principal's binding wins and
	// they only see their own workspace's runs.
	rw := h.do(t, "GET", "/api/v1/me/runs?workspace=ws2", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("code = %d", rw.Code)
	}
	var out struct {
		Runs []struct{ ID string } `json:"runs"`
	}
	_ = json.Unmarshal(rw.Body.Bytes(), &out)
	for _, r := range out.Runs {
		if r.ID == "theirs" {
			t.Error("scoped principal leaked across workspaces via ?workspace=")
		}
	}
}

func TestHTTPGateway_ListPendingApprovalsAcceptsWorkspaceNarrow(t *testing.T) {
	h := newGatewayHarness(t)
	// Pending approval in ws and ws2.
	for _, e := range []struct {
		runID string
		ws    string
	}{
		{"r1", "ws"},
		{"r2", "ws2"},
	} {
		_ = h.store.Enqueue(t.Context(), core.JobRecord{
			ID: e.runID, Kind: core.JobKindGraph, GraphID: "g",
			Tenant: "t", Workspace: e.ws, Status: core.JobStatusRunning,
		})
		_ = h.store.Enqueue(t.Context(), core.JobRecord{
			ID: NodeJobID(e.runID, "a"), Kind: core.JobKindNode,
			GraphRunID: e.runID, GraphID: "g", NodeID: "a",
			Tenant: "t", Workspace: e.ws, Status: core.JobStatusAwaiting,
			Result: &core.Result{Output: map[string]core.Ref{
				"pending_url": {Inline: "https://dzd/approve"},
			}},
		})
	}
	role := core.Role{Name: "ta", Permissions: []core.Permission{core.PermOrganizationAdmin}}
	_, adminTok, _ := auth.IssueAPIKey(h.ks, t.Context(), "k-narrow-app-admin", "t", "", "root4", []core.Role{role}, nil)

	doAdmin := func(path string) []string {
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("Authorization", "Bearer "+adminTok)
		rw := httptest.NewRecorder()
		ServeForTest(h.gw, rw, req)
		if rw.Code != http.StatusOK {
			t.Fatalf("%s: code=%d", path, rw.Code)
		}
		var out struct {
			Approvals []struct {
				RunID string `json:"run_id"`
			} `json:"approvals"`
		}
		_ = json.Unmarshal(rw.Body.Bytes(), &out)
		ids := make([]string, len(out.Approvals))
		for i, a := range out.Approvals {
			ids[i] = a.RunID
		}
		return ids
	}
	// Unfiltered: both visible.
	all := doAdmin("/api/v1/approvals/pending")
	if len(all) != 2 {
		t.Errorf("unfiltered: %v, want 2", all)
	}
	// Narrow to ws.
	narrow := doAdmin("/api/v1/approvals/pending?workspace=ws")
	if len(narrow) != 1 || narrow[0] != "r1" {
		t.Errorf("narrowed: %v, want [r1]", narrow)
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
	rw := h.do(t, "GET", "/api/v1/me/runs", nil)
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
	rw := h.do(t, "GET", "/api/v1/me/runs/run-xyz/nodes/ghost", nil)
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

// --- API key admin endpoints ------------------------------------------

func TestHTTPGateway_AdminListAPIKeys_RequiresTenantAdmin(t *testing.T) {
	h := newGatewayHarness(t)
	// Editor token (no organization:admin) should be denied.
	rw := h.do(t, "GET", "/api/v1/admin/api-keys", nil)
	if rw.Code != http.StatusForbidden {
		t.Errorf("editor code = %d, want 403", rw.Code)
	}
	// Tenant-admin token succeeds and sees the bootstrap key.
	rw = h.adminDo(t, "GET", "/api/v1/admin/api-keys", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("admin code = %d body = %s", rw.Code, rw.Body.String())
	}
	var out struct {
		Keys []APIKeySummary `json:"keys"`
	}
	_ = json.Unmarshal(rw.Body.Bytes(), &out)
	if len(out.Keys) < 2 {
		t.Errorf("keys = %d, want at least the editor + admin keys", len(out.Keys))
	}
	// Hash + salt are never exposed.
	raw := rw.Body.String()
	if strings.Contains(raw, "Hash") || strings.Contains(raw, "Salt") {
		t.Error("payload leaks Hash/Salt field")
	}
}

func TestHTTPGateway_AdminIssueAPIKey_ReturnsSecretOnce(t *testing.T) {
	h := newGatewayHarness(t)
	rw := h.adminDo(t, "POST", "/api/v1/admin/api-keys", map[string]any{
		"subject": "bot-1",
		"roles": []map[string]any{
			{"name": "runner", "permissions": []string{"graph:run"}},
		},
	})
	if rw.Code != http.StatusCreated {
		t.Fatalf("code = %d body = %s", rw.Code, rw.Body.String())
	}
	var issued IssuedAPIKey
	if err := json.Unmarshal(rw.Body.Bytes(), &issued); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if issued.Secret == "" {
		t.Fatal("secret missing from issue response")
	}
	// The Authenticator should accept the brand-new key.
	if _, err := h.svc.Authenticate(t.Context(), issued.Secret); err != nil {
		t.Errorf("issued secret didn't authenticate: %v", err)
	}
}

func TestHTTPGateway_AdminIssueAPIKey_RejectsMissingFields(t *testing.T) {
	h := newGatewayHarness(t)
	// Missing subject
	rw := h.adminDo(t, "POST", "/api/v1/admin/api-keys", map[string]any{
		"roles": []map[string]any{{"name": "r", "permissions": []string{"graph:run"}}},
	})
	if rw.Code != http.StatusBadRequest {
		t.Errorf("missing-subject code = %d, want 400", rw.Code)
	}
	// Missing roles
	rw = h.adminDo(t, "POST", "/api/v1/admin/api-keys", map[string]any{
		"subject": "x",
	})
	if rw.Code != http.StatusBadRequest {
		t.Errorf("missing-roles code = %d, want 400", rw.Code)
	}
}

func TestHTTPGateway_AdminRevokeAPIKey(t *testing.T) {
	h := newGatewayHarness(t)
	// First create a fresh key we'll then revoke.
	rw := h.adminDo(t, "POST", "/api/v1/admin/api-keys", map[string]any{
		"id":      "doomed",
		"subject": "doomed",
		"roles":   []map[string]any{{"name": "r", "permissions": []string{"graph:run"}}},
	})
	if rw.Code != http.StatusCreated {
		t.Fatalf("issue code = %d", rw.Code)
	}
	var issued IssuedAPIKey
	_ = json.Unmarshal(rw.Body.Bytes(), &issued)

	// Authenticates before revoke.
	if _, err := h.svc.Authenticate(t.Context(), issued.Secret); err != nil {
		t.Fatalf("pre-revoke auth: %v", err)
	}

	rw = h.adminDo(t, "DELETE", "/api/v1/admin/api-keys/doomed", nil)
	if rw.Code != http.StatusNoContent {
		t.Fatalf("revoke code = %d body = %s", rw.Code, rw.Body.String())
	}
	// Fails to authenticate after revoke.
	if _, err := h.svc.Authenticate(t.Context(), issued.Secret); err == nil {
		t.Error("revoked key still authenticated")
	}
}

func TestHTTPGateway_AdminListUsersGroupsBySubject(t *testing.T) {
	h := newGatewayHarness(t)
	// Two keys for charlie (editor + runner) and one for bob. Distinct
	// from the harness's bootstrap subjects ("alice", "root") so the
	// roll-up counts only see what this test produced.
	for _, params := range []map[string]any{
		{
			"subject": "charlie",
			"roles":   []map[string]any{{"name": "editor", "permissions": []string{"graph:edit", "graph:run"}}},
		},
		{
			"subject": "charlie",
			"roles":   []map[string]any{{"name": "runner", "permissions": []string{"graph:run", "secret:read"}}},
		},
		{
			"subject": "bob",
			"roles":   []map[string]any{{"name": "ops", "permissions": []string{"organization:admin"}}},
		},
	} {
		if rw := h.adminDo(t, "POST", "/api/v1/admin/api-keys", params); rw.Code != http.StatusCreated {
			t.Fatalf("issue %v: %d %s", params, rw.Code, rw.Body.String())
		}
	}

	rw := h.adminDo(t, "GET", "/api/v1/admin/users", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("code = %d body = %s", rw.Code, rw.Body.String())
	}
	var out struct {
		Users []UserSummary `json:"users"`
	}
	_ = json.Unmarshal(rw.Body.Bytes(), &out)

	// Expect alice, bob, root (the admin harness key), and the editor
	// bootstrap key — 4 distinct subjects.
	bySubject := map[string]UserSummary{}
	for _, u := range out.Users {
		bySubject[u.Subject] = u
	}
	charlie, ok := bySubject["charlie"]
	if !ok {
		t.Fatalf("missing charlie in users = %+v", out.Users)
	}
	if charlie.ActiveKeys != 2 {
		t.Errorf("charlie.ActiveKeys = %d, want 2", charlie.ActiveKeys)
	}
	// Permissions union: graph:edit + graph:run + secret:read
	gotPerms := map[core.Permission]bool{}
	for _, p := range charlie.Permissions {
		gotPerms[p] = true
	}
	for _, want := range []core.Permission{"graph:edit", "graph:run", "secret:read"} {
		if !gotPerms[want] {
			t.Errorf("charlie missing permission %q in %+v", want, charlie.Permissions)
		}
	}
	// Role names union: editor + runner
	if len(charlie.RoleNames) != 2 {
		t.Errorf("charlie.RoleNames = %v, want 2 entries", charlie.RoleNames)
	}
}

func TestHTTPGateway_AdminListUsers_CountsRevokedSeparately(t *testing.T) {
	h := newGatewayHarness(t)
	rw := h.adminDo(t, "POST", "/api/v1/admin/api-keys", map[string]any{
		"id":      "kill-me",
		"subject": "doomed",
		"roles":   []map[string]any{{"name": "r", "permissions": []string{"graph:run"}}},
	})
	if rw.Code != http.StatusCreated {
		t.Fatalf("issue: %d", rw.Code)
	}
	if rw := h.adminDo(t, "DELETE", "/api/v1/admin/api-keys/kill-me", nil); rw.Code != http.StatusNoContent {
		t.Fatalf("revoke: %d", rw.Code)
	}
	rw = h.adminDo(t, "GET", "/api/v1/admin/users", nil)
	var out struct {
		Users []UserSummary `json:"users"`
	}
	_ = json.Unmarshal(rw.Body.Bytes(), &out)
	for _, u := range out.Users {
		if u.Subject != "doomed" {
			continue
		}
		if u.ActiveKeys != 0 || u.RevokedKeys != 1 {
			t.Errorf("doomed = %+v, want 0 active / 1 revoked", u)
		}
		if len(u.Permissions) != 0 {
			t.Errorf("revoked key shouldn't contribute permissions: %+v", u.Permissions)
		}
	}
}

func TestHTTPGateway_AdminListUsers_RequiresTenantAdmin(t *testing.T) {
	h := newGatewayHarness(t)
	rw := h.do(t, "GET", "/api/v1/admin/users", nil)
	if rw.Code != http.StatusForbidden {
		t.Errorf("editor code = %d, want 403", rw.Code)
	}
}

// --- Platform admin ---------------------------------------------------

func TestHTTPGateway_PlatformAdminListsAcrossTenants(t *testing.T) {
	h := newGatewayHarness(t)
	// Issue keys in two different tenants. The bootstrap key is in
	// tenant "t" already; add one in "other-tenant".
	role := core.Role{Name: "r", Permissions: []core.Permission{core.PermGraphRun}}
	if _, _, err := auth.IssueAPIKey(h.ks, t.Context(), "k-other", "other-tenant", "ws", "stranger", []core.Role{role}, nil); err != nil {
		t.Fatalf("issue other-tenant: %v", err)
	}
	// Mint a platform admin key (no tenant binding).
	platform := core.Role{Name: "platform", Permissions: []core.Permission{core.PermPlatformAdmin}}
	_, platformTok, err := auth.IssueAPIKey(h.ks, t.Context(), "k-platform", "", "", "op", []core.Role{platform}, nil)
	if err != nil {
		t.Fatalf("issue platform: %v", err)
	}

	doPlatform := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("Authorization", "Bearer "+platformTok)
		rw := httptest.NewRecorder()
		ServeForTest(h.gw, rw, req)
		return rw
	}

	// /api/v1/admin/tenants: returns both tenants observed in the
	// key store. Platform admin only.
	rw := doPlatform("/api/v1/admin/tenants")
	if rw.Code != http.StatusOK {
		t.Fatalf("tenants code = %d body = %s", rw.Code, rw.Body.String())
	}
	var tOut struct {
		Tenants []string `json:"tenants"`
	}
	_ = json.Unmarshal(rw.Body.Bytes(), &tOut)
	if len(tOut.Tenants) < 2 {
		t.Errorf("tenants = %v, want at least 2", tOut.Tenants)
	}

	// /api/v1/admin/api-keys?tenant=other-tenant should list the
	// "stranger" key even though the platform admin has no tenant.
	rw = doPlatform("/api/v1/admin/api-keys?tenant=other-tenant")
	if rw.Code != http.StatusOK {
		t.Fatalf("keys code = %d body = %s", rw.Code, rw.Body.String())
	}
	var kOut struct {
		Keys []APIKeySummary `json:"keys"`
	}
	_ = json.Unmarshal(rw.Body.Bytes(), &kOut)
	if len(kOut.Keys) != 1 || kOut.Keys[0].ID != "k-other" {
		t.Errorf("keys for other-tenant = %+v, want [k-other]", kOut.Keys)
	}
}

func TestHTTPGateway_PlatformAdminCanIssueInAnyTenant(t *testing.T) {
	h := newGatewayHarness(t)
	platform := core.Role{Name: "platform", Permissions: []core.Permission{core.PermPlatformAdmin}}
	_, platformTok, _ := auth.IssueAPIKey(h.ks, t.Context(), "k-platform-issue", "", "", "op", []core.Role{platform}, nil)

	body, _ := json.Marshal(map[string]any{
		"subject": "first-customer-admin",
		"tenant":  "brand-new-tenant",
		"roles":   []map[string]any{{"name": "ta", "permissions": []string{"organization:admin"}}},
	})
	req := httptest.NewRequest("POST", "/api/v1/admin/api-keys", bytes.NewBuffer(body))
	req.Header.Set("Authorization", "Bearer "+platformTok)
	req.Header.Set("Content-Type", "application/json")
	rw := httptest.NewRecorder()
	ServeForTest(h.gw, rw, req)
	if rw.Code != http.StatusCreated {
		t.Fatalf("issue code = %d body = %s", rw.Code, rw.Body.String())
	}
	var issued IssuedAPIKey
	_ = json.Unmarshal(rw.Body.Bytes(), &issued)
	if issued.Tenant != "brand-new-tenant" {
		t.Errorf("issued tenant = %q, want brand-new-tenant", issued.Tenant)
	}
	// The new key works for its tenant.
	if _, err := h.svc.Authenticate(t.Context(), issued.Secret); err != nil {
		t.Errorf("new tenant's bootstrap key didn't authenticate: %v", err)
	}
}

// A tenant admin must not be able to mint a key carrying platform:admin —
// that would escalate from per-tenant admin to cross-tenant super-admin. A
// platform admin issuing the same role is fine.
func TestHTTPGateway_TenantAdminCantGrantPlatformAdmin(t *testing.T) {
	h := newGatewayHarness(t)

	issue := func(tok string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]any{
			"subject": "escalated",
			"tenant":  "t",
			"roles":   []map[string]any{{"name": "super", "permissions": []string{"platform:admin"}}},
		})
		req := httptest.NewRequest("POST", "/api/v1/admin/api-keys", bytes.NewBuffer(body))
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Content-Type", "application/json")
		rw := httptest.NewRecorder()
		ServeForTest(h.gw, rw, req)
		return rw
	}

	// Tenant admin → refused.
	taRole := core.Role{Name: "ta", Permissions: []core.Permission{core.PermOrganizationAdmin}}
	_, taTok, err := auth.IssueAPIKey(h.ks, t.Context(), "k-ta-esc", "t", "ws", "root", []core.Role{taRole}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rw := issue(taTok); rw.Code == http.StatusCreated {
		t.Fatalf("tenant admin was allowed to mint a platform:admin key: %s", rw.Body.String())
	}

	// Platform admin → allowed.
	paRole := core.Role{Name: "pa", Permissions: []core.Permission{core.PermPlatformAdmin}}
	_, paTok, err := auth.IssueAPIKey(h.ks, t.Context(), "k-pa-esc", "", "", "op", []core.Role{paRole}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rw := issue(paTok); rw.Code != http.StatusCreated {
		t.Fatalf("platform admin couldn't mint a platform:admin key: %d %s", rw.Code, rw.Body.String())
	}
}

func TestHTTPGateway_TenantAdminCantSpecifyForeignTenant(t *testing.T) {
	h := newGatewayHarness(t)
	// h.adminDo uses a tenant-admin key bound to tenant "t". Try to
	// list keys in tenant "elsewhere" — should be refused, not a leak.
	rw := h.adminDo(t, "GET", "/api/v1/admin/api-keys?tenant=elsewhere", nil)
	if rw.Code == http.StatusOK {
		var out struct {
			Keys []APIKeySummary `json:"keys"`
		}
		_ = json.Unmarshal(rw.Body.Bytes(), &out)
		for _, k := range out.Keys {
			if k.Tenant != "t" {
				t.Errorf("tenant admin saw foreign tenant %q", k.Tenant)
			}
		}
	}
	// 500/400/403 are all acceptable refusals — we just need it NOT
	// to silently return another tenant's keys.
}

func TestHTTPGateway_AdminEndpoints501WhenUnconfigured(t *testing.T) {
	h := newGatewayHarness(t)
	h.svc.AdminKeys = nil // simulate an dzd built without admin tooling
	rw := h.adminDo(t, "GET", "/api/v1/admin/api-keys", nil)
	if rw.Code != http.StatusNotImplemented {
		t.Errorf("unconfigured code = %d, want 501", rw.Code)
	}
}

// ---- Sample-node endpoint ------------------------------------------
//
// The "Sample this node" affordance fires a partial run that ends at
// the chosen node. The handler filters the graph through
// core.UpstreamSubset before calling SubmitGraph; these tests pin the
// HTTP contract (accept / not-found / authz) and that the submitted
// run carries the subset, not the full graph.

func TestHTTPGateway_SampleNode_Accepts(t *testing.T) {
	h := newGatewayHarness(t)
	// Three-node chain a → b → c. Sampling b should run a + b only;
	// c stays untouched.
	g := core.Graph{
		ID: "chain", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{
			{ID: "a", Module: "noop"},
			{ID: "b", Module: "noop"},
			{ID: "c", Module: "noop"},
		},
		Edges: []core.Edge{
			{From: "a", To: "b", FromPort: "out", ToPort: "in"},
			{From: "b", To: "c", FromPort: "out", ToPort: "in"},
		},
	}
	if _, err := h.ws.Save(g, "test"); err != nil {
		t.Fatalf("save graph: %v", err)
	}
	rw := h.do(t, "POST", "/api/v1/me/flows/t%2Fws%2Fchain/nodes/b/sample", nil)
	if rw.Code != http.StatusAccepted {
		t.Fatalf("code = %d body = %s", rw.Code, rw.Body.String())
	}
	var out struct {
		JobID       string `json:"job_id"`
		SampledNode string `json:"sampled_node"`
	}
	if err := json.Unmarshal(rw.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.JobID == "" {
		t.Error("job_id empty")
	}
	if out.SampledNode != "b" {
		t.Errorf("sampled_node = %q, want b", out.SampledNode)
	}
	// The submitted run's graph payload must be the SUBSET — c
	// should not appear. Loading the graph-record proves the
	// daemon kept only the upstream chain of b.
	rec, err := h.store.Get(t.Context(), out.JobID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	var submitted core.Graph
	if err := json.Unmarshal(rec.GraphPayload, &submitted); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	ids := map[string]bool{}
	for _, n := range submitted.Nodes {
		ids[n.ID] = true
	}
	if !ids["a"] || !ids["b"] {
		t.Errorf("subset missing a/b: %v", ids)
	}
	if ids["c"] {
		t.Error("subset leaked downstream node c")
	}
}

func TestHTTPGateway_SampleNode_UnknownNodeIs404(t *testing.T) {
	h := newGatewayHarness(t)
	if _, err := h.ws.Save(core.Graph{
		ID: "g", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{{ID: "only", Module: "noop"}},
	}, "test"); err != nil {
		t.Fatalf("save: %v", err)
	}
	rw := h.do(t, "POST", "/api/v1/me/flows/t%2Fws%2Fg/nodes/ghost/sample", nil)
	if rw.Code != http.StatusNotFound {
		t.Errorf("code = %d, want 404; body=%s", rw.Code, rw.Body.String())
	}
}

func TestHTTPGateway_SampleNode_UnknownGraphIs404(t *testing.T) {
	h := newGatewayHarness(t)
	// No save — graph doesn't exist.
	rw := h.do(t, "POST", "/api/v1/me/flows/t%2Fws%2Fmissing/nodes/x/sample", nil)
	if rw.Code != http.StatusNotFound {
		t.Errorf("code = %d, want 404", rw.Code)
	}
}

func TestHTTPGateway_SampleNode_RequiresAuth(t *testing.T) {
	h := newGatewayHarness(t)
	req := httptest.NewRequest("POST", "/api/v1/me/flows/t%2Fws%2Fg/nodes/x/sample", nil)
	// no Authorization header
	rw := httptest.NewRecorder()
	ServeForTest(h.gw, rw, req)
	if rw.Code != http.StatusUnauthorized {
		t.Errorf("code = %d, want 401", rw.Code)
	}
}

// ---- Cron validate endpoint ----------------------------------------
//
// The SettingsModal hits this on every keystroke to give users an
// inline "this cron is bad" / "next fires" hint. The handler MUST
// agree with the scheduler's parser — these tests pin the contract.

func TestHTTPGateway_ValidateCron_ValidExpression(t *testing.T) {
	h := newGatewayHarness(t)
	rw := h.do(t, "POST", "/api/v1/validate/cron", map[string]any{"expr": "0 9 * * 1-5"})
	if rw.Code != http.StatusOK {
		t.Fatalf("code = %d body = %s", rw.Code, rw.Body.String())
	}
	var out struct {
		Valid     bool     `json:"valid"`
		Error     string   `json:"error"`
		NextFires []string `json:"next_fires"`
	}
	if err := json.Unmarshal(rw.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !out.Valid {
		t.Errorf("valid expression rejected: %q", out.Error)
	}
	if len(out.NextFires) != 3 {
		t.Errorf("expected 3 next fires, got %d (%v)", len(out.NextFires), out.NextFires)
	}
}

func TestHTTPGateway_ValidateCron_InvalidExpression(t *testing.T) {
	h := newGatewayHarness(t)
	// 7 fields where 5 are allowed.
	rw := h.do(t, "POST", "/api/v1/validate/cron", map[string]any{"expr": "totally not a cron"})
	if rw.Code != http.StatusOK {
		t.Fatalf("code = %d (validate endpoint returns 200 even for invalid input)", rw.Code)
	}
	var out struct {
		Valid bool   `json:"valid"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rw.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Valid {
		t.Errorf("invalid expression accepted")
	}
	if out.Error == "" {
		t.Errorf("invalid expression missing error message")
	}
}

func TestHTTPGateway_ValidateCron_EmptyExpression(t *testing.T) {
	h := newGatewayHarness(t)
	rw := h.do(t, "POST", "/api/v1/validate/cron", map[string]any{"expr": "   "})
	if rw.Code != http.StatusOK {
		t.Fatalf("code = %d", rw.Code)
	}
	var out struct {
		Valid bool   `json:"valid"`
		Error string `json:"error"`
	}
	_ = json.Unmarshal(rw.Body.Bytes(), &out)
	if out.Valid {
		t.Errorf("empty expression should be invalid")
	}
}

func TestHTTPGateway_ValidateCron_RequiresAuth(t *testing.T) {
	h := newGatewayHarness(t)
	req := httptest.NewRequest("POST", "/api/v1/validate/cron", bytes.NewBufferString(`{"expr":"0 9 * * *"}`))
	req.Header.Set("Content-Type", "application/json")
	// no Authorization header
	rw := httptest.NewRecorder()
	ServeForTest(h.gw, rw, req)
	if rw.Code != http.StatusUnauthorized {
		t.Errorf("code = %d, want 401", rw.Code)
	}
}

func TestHTTPGateway_SaveGraph_IncludesLintInResponse(t *testing.T) {
	h := newGatewayHarness(t)
	// Save a graph that should trigger the secret_to_persistence
	// lint: an http_request reading from a tenant secret, feeding
	// directly into file_write.
	g := core.Graph{
		ID: "leaky", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{
			{
				ID: "call", Module: "http_request",
				Params: map[string]any{
					"url":     "https://api.example.com",
					"headers": map[string]any{"Authorization": "Bearer ${secret.api_key}"},
				},
			},
			{ID: "save", Module: "file_write", Params: map[string]any{"path": "out.txt"}},
		},
		Edges: []core.Edge{
			{From: "call", To: "save", FromPort: "body", ToPort: "data"},
		},
	}
	rw := h.do(t, "PUT", "/api/v1/me/flows/t%2Fws%2Fleaky", g)
	if rw.Code != http.StatusOK {
		t.Fatalf("code = %d body = %s", rw.Code, rw.Body.String())
	}
	var out struct {
		Commit string           `json:"commit"`
		FlowID string           `json:"flow_id"`
		Lint   []core.LintIssue `json:"lint"`
	}
	if err := json.Unmarshal(rw.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.FlowID != "t/ws/leaky" {
		t.Errorf("flow_id=%q", out.FlowID)
	}
	if len(out.Lint) != 1 {
		t.Fatalf("expected 1 lint issue in response, got %d (%+v)", len(out.Lint), out.Lint)
	}
	if out.Lint[0].Code != "secret_to_persistence" {
		t.Errorf("code=%q", out.Lint[0].Code)
	}
	if out.Lint[0].Severity != core.LintWarn {
		t.Errorf("severity=%q want warn", out.Lint[0].Severity)
	}
}

// ---- CSRF defense (cookie-auth + verifyCookieOrigin) -----------------
//
// The middleware adds Origin-header verification to cookie-auth POST/PUT
// /DELETE requests. Bearer-auth requests (no cookie) are unaffected.

func TestHTTPGateway_CSRF_BearerAuthUnaffectedByOrigin(t *testing.T) {
	h := newGatewayHarness(t)
	// Bearer-auth POST with arbitrary or no Origin — should pass
	// (the new middleware only kicks in for cookie-auth).
	rw := h.do(t, "PUT", "/api/v1/me/flows/t%2Fws%2Fg-bearer", core.Graph{
		ID: "g-bearer", Tenant: "t", Workspace: "ws",
	})
	if rw.Code != http.StatusOK {
		t.Fatalf("bearer-auth PUT should pass: code=%d body=%s", rw.Code, rw.Body.String())
	}
}

func TestHTTPGateway_CSRF_CookieAuthRequiresOrigin(t *testing.T) {
	h := newGatewayHarness(t)
	h.gw.AllowedOrigins = []string{"https://app.example.com"}
	// Build a request that LOOKS like a CSRF attack: a session cookie
	// is attached but no Origin header is set.
	req := httptest.NewRequest("PUT", "/api/v1/me/flows/t%2Fws%2Fg-csrf", bytes.NewBufferString(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: "dazyflow_session", Value: "any-session"})
	rw := httptest.NewRecorder()
	ServeForTest(h.gw, rw, req)
	if rw.Code != http.StatusForbidden {
		t.Errorf("cookie-auth POST without Origin should be 403, got %d (%s)", rw.Code, rw.Body.String())
	}
}

func TestHTTPGateway_CSRF_AllowedOriginPasses(t *testing.T) {
	h := newGatewayHarness(t)
	h.gw.AllowedOrigins = []string{"https://app.example.com"}
	g := core.Graph{ID: "g-allowed", Tenant: "t", Workspace: "ws"}
	body, _ := json.Marshal(g)
	req := httptest.NewRequest("PUT", "/api/v1/me/flows/t%2Fws%2Fg-allowed", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+h.token) // still need real auth
	req.Header.Set("Origin", "https://app.example.com")
	req.AddCookie(&http.Cookie{Name: "dazyflow_session", Value: "any-session"})
	rw := httptest.NewRecorder()
	ServeForTest(h.gw, rw, req)
	if rw.Code != http.StatusOK {
		t.Errorf("cookie-auth POST with allowed Origin should pass, got %d (%s)", rw.Code, rw.Body.String())
	}
}

func TestHTTPGateway_CSRF_DisallowedOriginRejected(t *testing.T) {
	h := newGatewayHarness(t)
	h.gw.AllowedOrigins = []string{"https://app.example.com"}
	req := httptest.NewRequest("PUT", "/api/v1/me/flows/t%2Fws%2Fg-evil", bytes.NewBufferString(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://evil.example.com")
	req.AddCookie(&http.Cookie{Name: "dazyflow_session", Value: "any-session"})
	rw := httptest.NewRecorder()
	ServeForTest(h.gw, rw, req)
	if rw.Code != http.StatusForbidden {
		t.Errorf("cookie-auth POST from disallowed origin should be 403, got %d", rw.Code)
	}
}

func TestHTTPGateway_CSRF_GetMethodNotAffected(t *testing.T) {
	// GET is allowed-through regardless of cookie/Origin — the
	// middleware only guards state-changing methods. Reading data
	// from a malicious origin still loses to CORS (which the
	// browser enforces), so this is safe.
	h := newGatewayHarness(t)
	h.gw.AllowedOrigins = []string{"https://app.example.com"}
	req := httptest.NewRequest("GET", "/api/v1/me", nil)
	req.Header.Set("Authorization", "Bearer "+h.token)
	req.AddCookie(&http.Cookie{Name: "dazyflow_session", Value: "any"})
	// No Origin header — should still pass since it's a GET.
	rw := httptest.NewRecorder()
	ServeForTest(h.gw, rw, req)
	if rw.Code == http.StatusForbidden {
		t.Errorf("GET should be allowed through CSRF middleware regardless of Origin")
	}
}

func TestHTTPGateway_SaveGraph_NoLintReturnsEmpty(t *testing.T) {
	h := newGatewayHarness(t)
	g := core.Graph{
		ID: "clean", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{{ID: "n", Module: "noop"}},
	}
	rw := h.do(t, "PUT", "/api/v1/me/flows/t%2Fws%2Fclean", g)
	if rw.Code != http.StatusOK {
		t.Fatalf("code = %d", rw.Code)
	}
	var out struct {
		Lint []core.LintIssue `json:"lint"`
	}
	_ = json.Unmarshal(rw.Body.Bytes(), &out)
	if len(out.Lint) != 0 {
		t.Errorf("clean graph should have no lint, got %+v", out.Lint)
	}
}

func TestHTTPGateway_ValidateCron_AgreesWithSchedulerParser(t *testing.T) {
	// The validate endpoint MUST use the same parser config the
	// scheduler does — otherwise users get a green "valid" hint on
	// an expression the scheduler then refuses at rescan time.
	// Spot-check by trying a "@yearly"-style expression that
	// robfig/cron supports only with the optional Descriptor flag —
	// the scheduler doesn't enable that, so the endpoint shouldn't
	// either.
	h := newGatewayHarness(t)
	rw := h.do(t, "POST", "/api/v1/validate/cron", map[string]any{"expr": "@yearly"})
	if rw.Code != http.StatusOK {
		t.Fatalf("code = %d", rw.Code)
	}
	var out struct {
		Valid bool `json:"valid"`
	}
	_ = json.Unmarshal(rw.Body.Bytes(), &out)
	if out.Valid {
		t.Errorf("@yearly accepted but scheduler's 5-field parser doesn't allow descriptors")
	}
}

func TestParseRunListTime(t *testing.T) {
	cases := []struct {
		name string
		in   string
		ok   bool
		want time.Time
	}{
		{"empty", "", false, time.Time{}},
		{"rfc3339", "2026-06-27T13:30:00Z", true, time.Date(2026, 6, 27, 13, 30, 0, 0, time.UTC)},
		{"date_only", "2026-06-27", true, time.Date(2026, 6, 27, 0, 0, 0, 0, time.UTC)},
		{"garbage", "not-a-date", false, time.Time{}},
		{"epoch_millis_not_accepted", "1750000000000", false, time.Time{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := parseRunListTime(c.in)
			if ok != c.ok {
				t.Fatalf("ok = %v, want %v", ok, c.ok)
			}
			if ok && !got.Equal(c.want) {
				t.Errorf("got %v, want %v", got, c.want)
			}
		})
	}
}

// TestParseRunListOpts_DateRange confirms the ?since=/?until= query params land
// on ListGraphRunsOpts, and that a malformed value leaves that bound unset
// rather than erroring the request.
func TestParseRunListOpts_DateRange(t *testing.T) {
	req := httptest.NewRequest("GET",
		"/api/v1/me/runs?since=2026-06-01&until=2026-06-27T00:00:00Z&junk=x", nil)
	opts := parseRunListOpts(req)
	if want := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC); !opts.Since.Equal(want) {
		t.Errorf("Since = %v, want %v", opts.Since, want)
	}
	if want := time.Date(2026, 6, 27, 0, 0, 0, 0, time.UTC); !opts.Until.Equal(want) {
		t.Errorf("Until = %v, want %v", opts.Until, want)
	}

	// A malformed since is ignored (zero), not an error.
	bad := parseRunListOpts(httptest.NewRequest("GET", "/api/v1/me/runs?since=nonsense", nil))
	if !bad.Since.IsZero() {
		t.Errorf("malformed since = %v, want zero", bad.Since)
	}
}

// The ACAO value depends on the request's Origin, so every response must say
// so. Announcing Vary only on the matching branch let a shared cache replay one
// origin's Access-Control-Allow-Origin to a different origin.
func TestHTTPGateway_CORSAlwaysVariesOnOrigin(t *testing.T) {
	h := newGatewayHarness(t)
	h.gw.AllowedOrigins = []string{"https://app.example.com"}

	for _, origin := range []string{"https://app.example.com", "https://evil.example.com", ""} {
		req := httptest.NewRequest("GET", "/api/v1/modules", nil)
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		rw := httptest.NewRecorder()
		ServeForTest(h.gw, rw, req)
		if got := rw.Header().Get("Vary"); got != "Origin" {
			t.Errorf("Origin=%q: Vary = %q, want %q", origin, got, "Origin")
		}
	}
}

// A disallowed origin in credentialed mode gets no ACAO at all — never the
// comma-joined AllowedOrigins list, which is not a valid header value.
func TestHTTPGateway_CORSDisallowedOriginGetsNoACAO(t *testing.T) {
	h := newGatewayHarness(t)
	h.gw.AllowedOrigins = []string{"https://app.example.com", "https://other.example.com"}

	req := httptest.NewRequest("GET", "/api/v1/modules", nil)
	req.Header.Set("Origin", "https://evil.example.com")
	rw := httptest.NewRecorder()
	ServeForTest(h.gw, rw, req)

	if got := rw.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("ACAO = %q, want empty for a disallowed origin", got)
	}
	if got := rw.Header().Get("Access-Control-Allow-Credentials"); got != "" {
		t.Errorf("ACAC = %q, want empty for a disallowed origin", got)
	}
}

// The allowed origin is reflected exactly, with credentials enabled.
func TestHTTPGateway_CORSAllowedOriginIsReflected(t *testing.T) {
	h := newGatewayHarness(t)
	h.gw.AllowedOrigins = []string{"https://app.example.com"}

	req := httptest.NewRequest("GET", "/api/v1/modules", nil)
	req.Header.Set("Origin", "https://app.example.com")
	rw := httptest.NewRecorder()
	ServeForTest(h.gw, rw, req)

	if got := rw.Header().Get("Access-Control-Allow-Origin"); got != "https://app.example.com" {
		t.Errorf("ACAO = %q, want the exact origin reflected", got)
	}
	if got := rw.Header().Get("Access-Control-Allow-Credentials"); got != "true" {
		t.Errorf("ACAC = %q, want %q", got, "true")
	}
}
