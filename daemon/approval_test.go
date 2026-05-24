package daemon

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"git.sr.ht/~klahr/hazy-flow/core"
	"git.sr.ht/~klahr/hazy-flow/engine"
	"git.sr.ht/~klahr/hazy-flow/engine/jobstore"
)

func TestHMACApprovalSigner_DeterministicAndVerifies(t *testing.T) {
	s := &HMACApprovalSigner{BaseURL: "https://hzd", Secret: []byte("topsecret")}
	url1 := s.SignApprovalURL("run-1", "node-A")
	url2 := s.SignApprovalURL("run-1", "node-A")
	if url1 != url2 {
		t.Errorf("URL not deterministic: %q vs %q", url1, url2)
	}
	if !strings.Contains(url1, "/approve/run-1/node-A?token=") {
		t.Errorf("URL shape unexpected: %q", url1)
	}
	tok := strings.TrimPrefix(strings.Split(url1, "token=")[1], "")
	if !s.verifyToken("run-1", "node-A", tok) {
		t.Error("verify returned false for self-signed token")
	}
	if s.verifyToken("run-2", "node-A", tok) {
		t.Error("verify accepted token for wrong run ID")
	}
	if s.verifyToken("run-1", "node-A", "deadbeef") {
		t.Error("verify accepted a bogus token")
	}
}

func TestHMACApprovalSigner_DifferentSecretsProduceDifferentTokens(t *testing.T) {
	s1 := &HMACApprovalSigner{BaseURL: "https://hzd", Secret: []byte("aaa")}
	s2 := &HMACApprovalSigner{BaseURL: "https://hzd", Secret: []byte("bbb")}
	if s1.computeToken("r", "n") == s2.computeToken("r", "n") {
		t.Error("two different secrets produced the same token")
	}
}

func TestApprove_RequiresAwaitingRecord(t *testing.T) {
	store := jobstore.NewMemory()
	bus := NewMemoryBus()
	svc := &Service{Jobs: store, Bus: bus, Engine: &engine.Engine{
		Resolver: &engine.NodeResolver{Native: engine.Default},
	}}

	// Record exists but is already terminal (succeeded) — cannot resume.
	rec := core.JobRecord{
		ID:         NodeJobID("run-1", "node-A"),
		Kind:       core.JobKindNode,
		GraphRunID: "run-1",
		NodeID:     "node-A",
		Status:     core.JobStatusSucceeded,
	}
	_ = store.Enqueue(t.Context(), rec)

	err := svc.Approve(t.Context(), "run-1", "node-A", ApprovalDecision{Decision: "approve"})
	if err == nil || !strings.Contains(err.Error(), "not awaiting") {
		t.Fatalf("err = %v, want 'not awaiting'", err)
	}
}

func TestApprove_RejectsBadDecision(t *testing.T) {
	svc := &Service{Jobs: jobstore.NewMemory(), Bus: NewMemoryBus()}
	err := svc.Approve(t.Context(), "r", "n", ApprovalDecision{Decision: "maybe"})
	if err == nil || !strings.Contains(err.Error(), "approve or reject") {
		t.Fatalf("err = %v", err)
	}
}

func TestApprovalListener_RejectsBadToken(t *testing.T) {
	signer := &HMACApprovalSigner{BaseURL: "https://x", Secret: []byte("k")}
	svc := &Service{Jobs: jobstore.NewMemory(), Bus: NewMemoryBus()}
	a := NewApprovalListener(svc, signer)

	req := httptest.NewRequest("POST", "/approve/run-1/node-A?token=garbage", nil)
	rw := httptest.NewRecorder()
	ServeApprovalForTest(a, rw, req)
	if rw.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rw.Code)
	}
}

func TestApprovalListener_RejectsMissingPath(t *testing.T) {
	signer := &HMACApprovalSigner{BaseURL: "https://x", Secret: []byte("k")}
	svc := &Service{Jobs: jobstore.NewMemory(), Bus: NewMemoryBus()}
	a := NewApprovalListener(svc, signer)

	req := httptest.NewRequest("POST", "/approve/onlypiece?token=any", nil)
	rw := httptest.NewRecorder()
	ServeApprovalForTest(a, rw, req)
	if rw.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rw.Code)
	}
}

func TestApprovalListener_RejectsNonPost(t *testing.T) {
	signer := &HMACApprovalSigner{BaseURL: "https://x", Secret: []byte("k")}
	svc := &Service{Jobs: jobstore.NewMemory(), Bus: NewMemoryBus()}
	a := NewApprovalListener(svc, signer)

	req := httptest.NewRequest("GET", "/approve/r/n?token=x", nil)
	rw := httptest.NewRecorder()
	ServeApprovalForTest(a, rw, req)
	if rw.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rw.Code)
	}
}

// Sanity check: the unused context is silencing-the-linter-only here;
// we want to make sure t.Context exists in this Go version.
var _ = context.Background