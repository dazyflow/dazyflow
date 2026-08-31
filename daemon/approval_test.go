// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	neturl "net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/engine"
	"github.com/dazyflow/dazyflow/engine/jobstore"
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

// TestHMACApprovalSigner_SignIsStableAcrossResigns guards the retry contract:
// SignApprovalURL must reproduce the same URL when the same pause is re-signed,
// which is what lets a retried await_approval Execute (after a lease expiry)
// re-emit a link already emailed to an approver. exp is part of the SIGNED
// payload, so reading the clock raw made every re-sign mint a different URL and
// token — the emailed link still worked, but valid links accumulated for the
// full TTL and any consumer deduping on URL saw a new one each attempt.
//
// The existing TestHMACApprovalSigner_DeterministicAndVerifies only covers
// computeToken for a FIXED exp, so it never exercised this.
func TestHMACApprovalSigner_SignIsStableAcrossResigns(t *testing.T) {
	base := time.Date(2026, 7, 26, 14, 5, 0, 0, time.UTC)
	now := base
	s := &HMACApprovalSigner{
		BaseURL: "https://dzd",
		Secret:  []byte("topsecret"),
		now:     func() time.Time { return now },
	}

	first := s.SignApprovalURL("run-1", "node-A")

	t.Run("re-sign later in the same bucket reproduces the URL", func(t *testing.T) {
		// Far enough apart that an unbucketed clock read would differ, but still
		// inside the same bucket.
		for _, advance := range []time.Duration{time.Second, 30 * time.Second, 45 * time.Minute} {
			now = base.Add(advance)
			if got := s.SignApprovalURL("run-1", "node-A"); got != first {
				t.Errorf("re-sign after %s differs:\n first: %s\n got  : %s", advance, first, got)
			}
		}
	})

	t.Run("a different pause still gets a different URL", func(t *testing.T) {
		now = base
		if s.SignApprovalURL("run-1", "node-B") == first {
			t.Error("a different node produced the same URL")
		}
		if s.SignApprovalURL("run-2", "node-A") == first {
			t.Error("a different run produced the same URL")
		}
	})

	t.Run("the bucketed token verifies and is still expiry-bound", func(t *testing.T) {
		now = base
		url := s.SignApprovalURL("run-1", "node-A")
		exp, token := parseApprovalURL(t, url)

		if !s.verifyToken("run-1", "node-A", exp, token) {
			t.Fatal("bucketed token failed to verify")
		}
		// The signed expiry is still honoured, and still can't be extended.
		if s.verifyToken("run-1", "node-A", exp+1, token) {
			t.Error("verify accepted a tampered (extended) expiry")
		}
		now = time.Unix(exp, 0).Add(time.Second)
		if s.verifyToken("run-1", "node-A", exp, token) {
			t.Error("verify accepted a token past its expiry")
		}
	})

	t.Run("bucketing does not shorten the TTL by more than one bucket", func(t *testing.T) {
		now = base
		exp, _ := parseApprovalURL(t, s.SignApprovalURL("run-1", "node-A"))
		life := time.Unix(exp, 0).Sub(now)
		if life > approvalTokenTTL || life <= approvalTokenTTL-approvalTokenBucket {
			t.Errorf("effective TTL %s outside (%s, %s]",
				life, approvalTokenTTL-approvalTokenBucket, approvalTokenTTL)
		}
	})
}

// parseApprovalURL pulls the exp and token query params out of a signed
// approval URL.
func parseApprovalURL(t *testing.T, raw string) (int64, string) {
	t.Helper()
	u, err := neturl.Parse(raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	exp, err := strconv.ParseInt(u.Query().Get("exp"), 10, 64)
	if err != nil {
		t.Fatalf("bad exp in %q: %v", raw, err)
	}
	return exp, u.Query().Get("token")
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

// conflictOnCompleteStore is the losing side of two concurrent approves: the
// record still reads as awaiting when Approve fetches it, but the terminal write
// loses the store's conditional UPDATE to the winner and comes back ErrConflict.
// Wrapping the real store keeps every other operation honest; only Complete is
// forced. This is the interleaving a live race produces, made deterministic.
type conflictOnCompleteStore struct {
	core.JobStore
}

func (conflictOnCompleteStore) Complete(context.Context, string, core.JobStatus, *core.Result) error {
	return core.ErrConflict
}

// TestApprovalListener_ClassifiesErrorsBySentinel pins the HTTP mapping of the
// approval outcomes. These were classified with strings.Contains on the error
// text, which mapped the CONCURRENT duplicate approve to 500: it loses inside
// Complete and surfaces as "job state conflict", matching neither "not awaiting"
// nor "not found". The sequential duplicate matched and returned 409, so the
// endpoint reported two different statuses for the same user-visible event.
func TestApprovalListener_ClassifiesErrorsBySentinel(t *testing.T) {
	// awaiting builds a service whose run-1/node-A record is parked awaiting,
	// with the graph payload Approve needs to advance the run.
	awaiting := func(t *testing.T, wrap func(core.JobStore) core.JobStore) *Service {
		t.Helper()
		store := jobstore.NewMemory()
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
		var js core.JobStore = store
		if wrap != nil {
			js = wrap(store)
		}
		return &Service{Jobs: js, Bus: NewMemoryBus(), Engine: &engine.Engine{
			Resolver: &engine.NodeResolver{Native: engine.Default},
		}}
	}

	// call signs a valid link for run-1/node-A and drives the listener.
	call := func(t *testing.T, svc *Service, query string) *httptest.ResponseRecorder {
		t.Helper()
		signer := &HMACApprovalSigner{BaseURL: "https://x", Secret: []byte("k")}
		exp := time.Now().Add(time.Hour).Unix()
		token := signer.computeToken("run-1", "node-A", exp)
		req := httptest.NewRequest("POST", fmt.Sprintf(
			"/approve/run-1/node-A?token=%s&exp=%d%s", token, exp, query), nil)
		rw := httptest.NewRecorder()
		ServeApprovalForTest(NewApprovalListener(svc, signer), rw, req)
		return rw
	}

	t.Run("concurrent duplicate approve is 409", func(t *testing.T) {
		svc := awaiting(t, func(s core.JobStore) core.JobStore {
			return conflictOnCompleteStore{JobStore: s}
		})
		if rw := call(t, svc, "&decision=approve"); rw.Code != http.StatusConflict {
			t.Fatalf("status = %d, want 409; body=%s", rw.Code, rw.Body.String())
		}
	})

	t.Run("sequential duplicate approve is 409", func(t *testing.T) {
		svc := awaiting(t, nil)
		if rw := call(t, svc, "&decision=approve"); rw.Code != http.StatusOK {
			t.Fatalf("first approve: status = %d, want 200; body=%s", rw.Code, rw.Body.String())
		}
		// Second click: the record now reads terminal.
		if rw := call(t, svc, "&decision=approve"); rw.Code != http.StatusConflict {
			t.Fatalf("second approve: status = %d, want 409; body=%s", rw.Code, rw.Body.String())
		}
	})

	t.Run("unknown run is 404", func(t *testing.T) {
		svc := &Service{Jobs: jobstore.NewMemory(), Bus: NewMemoryBus()}
		if rw := call(t, svc, "&decision=approve"); rw.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404; body=%s", rw.Code, rw.Body.String())
		}
	})

	t.Run("bogus decision is 400, not 500", func(t *testing.T) {
		svc := awaiting(t, nil)
		if rw := call(t, svc, "&decision=maybe"); rw.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400; body=%s", rw.Code, rw.Body.String())
		}
	})
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

// The link path has no principal, so it audits through its own writer. Before
// that existed, a decision taken from an approval email left nothing in the
// trail at all — only a stdout line — which made the one approval route that
// carries no proven identity also the one route with no record of who used it.
func TestApprovalListener_AuditsTheDecision(t *testing.T) {
	store := jobstore.NewMemory()
	graph := core.Graph{ID: "g", Nodes: []core.Node{{ID: "node-A", Module: "noop"}}}
	payload, _ := json.Marshal(graph)
	_ = store.Enqueue(t.Context(), core.JobRecord{
		ID: "run-1", Kind: core.JobKindGraph, GraphID: "g", NodeID: "*", Tenant: "acme",
		Status: core.JobStatusRunning, GraphPayload: payload,
	})
	_ = store.Enqueue(t.Context(), core.JobRecord{
		ID: NodeJobID("run-1", "node-A"), Kind: core.JobKindNode,
		GraphRunID: "run-1", NodeID: "node-A", Status: core.JobStatusAwaiting,
	})
	svc := &Service{Jobs: store, Bus: NewMemoryBus(), Engine: &engine.Engine{
		Resolver: &engine.NodeResolver{Native: engine.Default},
	}}
	signer := &HMACApprovalSigner{BaseURL: "https://x", Secret: []byte("k")}
	audit := NewMemAuditLog()
	listener := NewApprovalListener(svc, signer)
	listener.Audit = audit

	exp := time.Now().Add(time.Hour).Unix()
	token := signer.computeToken("run-1", "node-A", exp)
	req := httptest.NewRequest("POST", fmt.Sprintf(
		"/approve/run-1/node-A?token=%s&exp=%d&decision=approve&approver=manager@acme.se",
		token, exp), nil)
	rw := httptest.NewRecorder()
	ServeApprovalForTest(listener, rw, req)
	if rw.Code != http.StatusOK {
		t.Fatalf("approve = %d, body %s", rw.Code, rw.Body.String())
	}

	events, err := audit.List(t.Context(), core.AuditQuery{Tenant: "acme", Limit: 10})
	if err != nil {
		t.Fatalf("list audit: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("want 1 audit event, got %d: %+v", len(events), events)
	}
	e := events[0]
	if e.Action != "approval" || e.Target != "run-1/node-A" || e.Detail != "approve" {
		t.Errorf("event = %+v", e)
	}
	// Tenant is read off the graph record, since there is no principal here.
	if e.Tenant != "acme" {
		t.Errorf("tenant = %q, want acme", e.Tenant)
	}
	// The name came off a query string. Recording it as if it were an
	// authenticated subject would make the trail claim more than it knows.
	if !strings.Contains(e.Actor, "manager@acme.se") || !strings.Contains(e.Actor, "self-declared") {
		t.Errorf("actor = %q, want the name marked self-declared", e.Actor)
	}
}

// No ?approver= at all: still audited, still marked as unverified.
func TestApprovalListener_AuditsAnonymousDecision(t *testing.T) {
	store := jobstore.NewMemory()
	graph := core.Graph{ID: "g", Nodes: []core.Node{{ID: "node-A", Module: "noop"}}}
	payload, _ := json.Marshal(graph)
	_ = store.Enqueue(t.Context(), core.JobRecord{
		ID: "run-1", Kind: core.JobKindGraph, GraphID: "g", NodeID: "*", Tenant: "acme",
		Status: core.JobStatusRunning, GraphPayload: payload,
	})
	_ = store.Enqueue(t.Context(), core.JobRecord{
		ID: NodeJobID("run-1", "node-A"), Kind: core.JobKindNode,
		GraphRunID: "run-1", NodeID: "node-A", Status: core.JobStatusAwaiting,
	})
	svc := &Service{Jobs: store, Bus: NewMemoryBus(), Engine: &engine.Engine{
		Resolver: &engine.NodeResolver{Native: engine.Default},
	}}
	signer := &HMACApprovalSigner{BaseURL: "https://x", Secret: []byte("k")}
	audit := NewMemAuditLog()
	listener := NewApprovalListener(svc, signer)
	listener.Audit = audit
	exp := time.Now().Add(time.Hour).Unix()
	token := signer.computeToken("run-1", "node-A", exp)
	req := httptest.NewRequest("POST", fmt.Sprintf(
		"/approve/run-1/node-A?token=%s&exp=%d&decision=reject", token, exp), nil)
	ServeApprovalForTest(listener, httptest.NewRecorder(), req)

	events, _ := audit.List(t.Context(), core.AuditQuery{Tenant: "acme", Limit: 10})
	if len(events) != 1 || events[0].Detail != "reject" {
		t.Fatalf("events = %+v", events)
	}
	if !strings.Contains(events[0].Actor, "unidentified") {
		t.Errorf("actor = %q, want it flagged as unidentified", events[0].Actor)
	}
}
