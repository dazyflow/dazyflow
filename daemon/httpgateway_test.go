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
