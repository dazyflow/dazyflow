// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/engine"
	"git.sr.ht/~klahr/dazyflow/engine/jobstore"
)

func TestHMACApprovalSigner_DeterministicAndVerifies(t *testing.T) {
	s := &HMACApprovalSigner{BaseURL: "https://dzd", Secret: []byte("topsecret")}
	url1 := s.SignApprovalURL("run-1", "node-A")
	if !strings.Contains(url1, "/approve/run-1/node-A?exp=") ||
		!strings.Contains(url1, "&token=") {
		t.Errorf("URL shape unexpected: %q", url1)
	}
	// computeToken is deterministic given (run, node, exp).
	exp := time.Now().Add(time.Hour).Unix()
	tok := s.computeToken("run-1", "node-A", exp)
	if s.computeToken("run-1", "node-A", exp) != tok {
		t.Error("computeToken not deterministic for the same inputs")
	}
	if !s.verifyToken("run-1", "node-A", exp, tok) {
		t.Error("verify returned false for self-signed token")
	}
	if s.verifyToken("run-2", "node-A", exp, tok) {
		t.Error("verify accepted token for wrong run ID")
	}
	if s.verifyToken("run-1", "node-A", exp+1, tok) {
		t.Error("verify accepted token for a tampered (extended) expiry")
	}
	if s.verifyToken("run-1", "node-A", exp, "deadbeef") {
		t.Error("verify accepted a bogus token")
	}
	// A valid signature past its expiry must still be rejected.
	pastExp := time.Now().Add(-time.Hour).Unix()
	pastTok := s.computeToken("run-1", "node-A", pastExp)
	if s.verifyToken("run-1", "node-A", pastExp, pastTok) {
		t.Error("verify accepted an expired token")
	}
}

func TestHMACApprovalSigner_DifferentSecretsProduceDifferentTokens(t *testing.T) {
	s1 := &HMACApprovalSigner{BaseURL: "https://dzd", Secret: []byte("aaa")}
	s2 := &HMACApprovalSigner{BaseURL: "https://dzd", Secret: []byte("bbb")}
	exp := time.Now().Add(time.Hour).Unix()
	if s1.computeToken("r", "n", exp) == s2.computeToken("r", "n", exp) {
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

// TestApprovalListener_ValidTokenResumes exercises the now-wired HMAC
// path end to end: a valid signed token resumes an awaiting node.
func TestApprovalListener_ValidTokenResumes(t *testing.T) {
	store := jobstore.NewMemory()
	bus := NewMemoryBus()
	svc := &Service{Jobs: store, Bus: bus, Engine: &engine.Engine{
		Resolver: &engine.NodeResolver{Native: engine.Default},
	}}

	// Graph-record (carries the payload Approve loads to advance) + an
	// awaiting node-record for node-A.
	graph := core.Graph{ID: "g", Nodes: []core.Node{{ID: "node-A", Module: "noop"}}}
	payload, _ := json.Marshal(graph)
	_ = store.Enqueue(t.Context(), core.JobRecord{
		ID: "run-1", Kind: core.JobKindGraph, GraphID: "g", NodeID: "*",
		Status: core.JobStatusRunning, GraphPayload: payload,
	})
	_ = store.Enqueue(t.Context(), core.JobRecord{
		ID: NodeJobID("run-1", "node-A"), Kind: core.JobKindNode,
		GraphRunID: "run-1", NodeID: "node-A", Status: core.JobStatusAwaiting,
	})

	signer := &HMACApprovalSigner{BaseURL: "https://x", Secret: []byte("shared-secret")}
	a := NewApprovalListener(svc, signer)
	exp := time.Now().Add(time.Hour).Unix()
	token := signer.computeToken("run-1", "node-A", exp)

	req := httptest.NewRequest("POST",
		fmt.Sprintf("/approve/run-1/node-A?token=%s&decision=approve&exp=%d&approver=alice", token, exp), nil)
	rw := httptest.NewRecorder()
	ServeApprovalForTest(a, rw, req)
	if rw.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rw.Code, rw.Body.String())
	}

	// The awaiting node is now resumed (succeeded) and routed out the
	// approved port (Branch-style), not rejected.
	rec, _ := store.Get(t.Context(), NodeJobID("run-1", "node-A"))
	if rec.Status != core.JobStatusSucceeded {
		t.Errorf("node status = %q, want succeeded", rec.Status)
	}
	if rec.Result == nil {
		t.Fatalf("resume result is nil")
	}
	if _, ok := rec.Result.Output["approved"]; !ok {
		t.Errorf("approved port missing on approve: %+v", rec.Result.Output)
	}
	if _, ok := rec.Result.Output["rejected"]; ok {
		t.Errorf("rejected port should be absent on approve: %+v", rec.Result.Output)
	}
}
