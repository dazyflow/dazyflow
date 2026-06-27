package daemon

import (
	"net/http"
	"testing"
)

// approveAuthed: the authenticated approval endpoint. Approving a run that
// doesn't exist (or isn't in the caller's tenant) is rejected before the
// service Approve call.

func TestApproveAuthed_UnknownRun(t *testing.T) {
	h := newGatewayHarness(t)
	rw := h.do(t, "POST", "/api/v1/approvals/ghostrun/nodeA", nil)
	if rw.Code != http.StatusNotFound && rw.Code != http.StatusForbidden {
		t.Fatalf("approve unknown run = %d (%s), want 404/403", rw.Code, rw.Body.String())
	}
}

func TestApproveAuthed_UnknownRunWithDecision(t *testing.T) {
	h := newGatewayHarness(t)
	rw := h.do(t, "POST", "/api/v1/approvals/ghostrun/nodeA?decision=reject", nil)
	if rw.Code != http.StatusNotFound && rw.Code != http.StatusForbidden {
		t.Fatalf("approve(reject) unknown run = %d (%s), want 404/403", rw.Code, rw.Body.String())
	}
}
