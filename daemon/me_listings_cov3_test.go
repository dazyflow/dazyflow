package daemon

import (
	"net/http"
	"testing"

	"git.sr.ht/~klahr/dazyflow/auth"
	"git.sr.ht/~klahr/dazyflow/core"
)

// unboundToken issues an API key with no tenant/workspace binding so the
// /me listings hit their missing_scope branch.
func unboundToken(t *testing.T, h *gatewayHarness) string {
	t.Helper()
	role := core.Role{Name: "free", Permissions: []core.Permission{core.PermGraphRun}}
	_, tok, err := auth.IssueAPIKey(h.ks, t.Context(), "k-unbound-list", "", "", "nobody", []core.Role{role}, nil)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	return tok
}

func TestListFlowsMe_MissingScope(t *testing.T) {
	h := newGatewayHarness(t)
	tok := unboundToken(t, h)
	saved := h.token
	h.token = tok
	defer func() { h.token = saved }()
	rw := h.do(t, "GET", "/api/v1/me/flows", nil)
	if rw.Code != http.StatusBadRequest {
		t.Fatalf("unbound flows = %d (%s), want 400", rw.Code, rw.Body.String())
	}
}

func TestListFlowsMe_OK(t *testing.T) {
	h := newGatewayHarness(t)
	covSeedFlow(t, h, "f1")
	rw := h.do(t, "GET", "/api/v1/me/flows", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("list flows = %d (%s), want 200", rw.Code, rw.Body.String())
	}
}

func TestSuggestionsMe_MissingScope(t *testing.T) {
	h := newGatewayHarness(t)
	tok := unboundToken(t, h)
	saved := h.token
	h.token = tok
	defer func() { h.token = saved }()
	rw := h.do(t, "GET", "/api/v1/me/flows/suggestions", nil)
	if rw.Code != http.StatusBadRequest {
		t.Fatalf("unbound suggestions = %d (%s), want 400", rw.Code, rw.Body.String())
	}
}

func TestSuggestionsMe_OK(t *testing.T) {
	h := newGatewayHarness(t)
	covSeedFlow(t, h, "f1")
	rw := h.do(t, "GET", "/api/v1/me/flows/suggestions", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("suggestions = %d (%s), want 200", rw.Code, rw.Body.String())
	}
}

func TestHistoryFlowMe_OKWithLimit(t *testing.T) {
	h := newGatewayHarness(t)
	covSeedFlow(t, h, "f1")
	rw := h.do(t, "GET", "/api/v1/me/flows/"+cov3FlowID+"/history?limit=5", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("history with limit = %d (%s), want 200", rw.Code, rw.Body.String())
	}
}

func TestHistoryFlowMe_BadLimitFallsBack(t *testing.T) {
	h := newGatewayHarness(t)
	covSeedFlow(t, h, "f1")
	// Non-numeric limit is ignored (falls back to default), still 200.
	rw := h.do(t, "GET", "/api/v1/me/flows/"+cov3FlowID+"/history?limit=abc", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("history bad limit = %d (%s), want 200", rw.Code, rw.Body.String())
	}
}
