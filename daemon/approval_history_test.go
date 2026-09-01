// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/engine"
	"github.com/dazyflow/dazyflow/engine/jobstore"
)

// decidedRec builds a settled approval node-record the way Approve leaves one:
// the pause's `pending_url` carried through, the value routed out the decision
// port, and approver/comment stamped on.
func decidedRec(id, run, node, decision string, finished time.Time, approver, comment string, value any) core.JobRecord {
	out := map[string]core.Ref{
		"pending_url": {MIME: "text/plain", Inline: "https://app.example/approve/" + run + "/" + node},
		"prompt":      {MIME: "text/plain", Inline: "Refund " + node + "?"},
		"approver":    {MIME: "text/plain", Inline: approver},
		"comment":     {MIME: "text/plain", Inline: comment},
	}
	port := "approved"
	if decision == "reject" {
		port = "rejected"
	}
	out[port] = core.Ref{MIME: "application/json", Inline: value}
	f := finished
	return core.JobRecord{
		ID: id, Kind: core.JobKindNode, GraphRunID: run, GraphID: "refunds", NodeID: node,
		Tenant: "acme", Workspace: "default", Status: core.JobStatusSucceeded,
		EnqueuedAt: finished.Add(-time.Hour), FinishedAt: &f,
		Result: &core.Result{Status: core.StatusOK, Output: out},
	}
}

func historySvc(t *testing.T) (*Service, core.JobStore) {
	t.Helper()
	store := jobstore.NewMemory()
	return &Service{Jobs: store, Bus: NewMemoryBus(), Engine: &engine.Engine{
		Resolver: &engine.NodeResolver{Native: engine.Default},
	}}, store
}

var acmePrincipal = core.Principal{Subject: "ops@acme.se", Tenant: "acme", Workspace: "default"}

// acmeRunner is acmePrincipal with the permission cancelling a run requires.
var acmeRunner = core.Principal{
	Subject: "ops@acme.se", Tenant: "acme", Workspace: "default",
	Roles: []core.Role{{Name: "editor", Permissions: []core.Permission{core.PermGraphRun}}},
}

// TestListDecidedApprovals_ReportsBothVerdicts: a rejection is a SUCCEEDED
// record (the step completed and routed the value out `rejected`), so reading
// the decision off the status would report every history row as approved. The
// decision port is the only thing that carries it.
func TestListDecidedApprovals_ReportsBothVerdicts(t *testing.T) {
	svc, store := historySvc(t)
	now := time.Now()
	_ = store.Enqueue(t.Context(), decidedRec("a", "run-1", "gate-a", "approve", now.Add(-time.Minute), "ada@acme.se", "looks fine", "SEK 400"))
	_ = store.Enqueue(t.Context(), decidedRec("b", "run-2", "gate-b", "reject", now.Add(-2*time.Minute), "bo@acme.se", "duplicate", "SEK 900"))

	got, err := svc.ListDecidedApprovals(t.Context(), acmePrincipal, "", "", 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d decisions, want 2: %+v", len(got), got)
	}
	if got[0].Decision != "approve" || got[0].Approver != "ada@acme.se" || got[0].Comment != "looks fine" {
		t.Errorf("first row = %+v", got[0])
	}
	if got[0].Context != "SEK 400" {
		t.Errorf("context = %v, want the value that was decided on", got[0].Context)
	}
	if got[1].Decision != "reject" {
		t.Errorf("second row = %+v, want the rejection", got[1])
	}
}

// TestListDecidedApprovals_OrdersByDecisionNotEnqueue is the reason the store
// grew NewestByFinished. An approval parked for three weeks and decided this
// morning is the newest DECISION and the oldest RECORD; ordering by enqueue
// time buries it — and under a limit, drops it off the page entirely.
func TestListDecidedApprovals_OrdersByDecisionNotEnqueue(t *testing.T) {
	svc, store := historySvc(t)
	now := time.Now()

	old := decidedRec("old", "run-old", "gate", "approve", now.Add(-time.Minute), "ada@acme.se", "", nil)
	old.EnqueuedAt = now.Add(-21 * 24 * time.Hour) // parked three weeks
	_ = store.Enqueue(t.Context(), old)

	recent := decidedRec("recent", "run-recent", "gate", "approve", now.Add(-time.Hour), "bo@acme.se", "", nil)
	recent.EnqueuedAt = now.Add(-2 * time.Hour) // enqueued and decided today
	_ = store.Enqueue(t.Context(), recent)

	got, err := svc.ListDecidedApprovals(t.Context(), acmePrincipal, "", "", 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 2 || got[0].RunID != "run-old" {
		t.Fatalf("order = %v, want the long-parked run first (decided most recently)",
			[]string{got[0].RunID, got[1].RunID})
	}

	// Under a limit the ordering decides membership, not just position.
	one, err := svc.ListDecidedApprovals(t.Context(), acmePrincipal, "", "", 1)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(one) != 1 || one[0].RunID != "run-old" {
		t.Errorf("limit=1 returned %+v, want only the most recently decided", one)
	}
}

// TestListDecidedApprovals_ExcludesNonApprovals: the history must not sweep in
// every succeeded step of every run, nor the approvals still sitting in the
// inbox.
func TestListDecidedApprovals_ExcludesNonApprovals(t *testing.T) {
	svc, store := historySvc(t)
	now := time.Now()
	fin := now.Add(-time.Minute)

	_ = store.Enqueue(t.Context(), decidedRec("decided", "run-1", "gate", "approve", fin, "ada@acme.se", "", nil))

	// An ordinary succeeded step: no approval marker.
	_ = store.Enqueue(t.Context(), core.JobRecord{
		ID: "plain", Kind: core.JobKindNode, GraphRunID: "run-1", NodeID: "http",
		Tenant: "acme", Workspace: "default", Status: core.JobStatusSucceeded,
		FinishedAt: &fin,
		Result:     &core.Result{Status: core.StatusOK, Output: map[string]core.Ref{"body": {Inline: "hi"}}},
	})
	// Still parked — belongs to the inbox above, not the history below.
	_ = store.Enqueue(t.Context(), core.JobRecord{
		ID: "parked", Kind: core.JobKindNode, GraphRunID: "run-2", NodeID: "gate",
		Tenant: "acme", Workspace: "default", Status: core.JobStatusAwaiting,
		Result: &core.Result{Status: core.StatusAwaiting, Output: map[string]core.Ref{
			"pending_url": {Inline: "https://app.example/approve/run-2/gate"},
		}},
	})
	// Marked as an approval but carrying no decision port (an older record, or
	// a resume that isn't a decision): skipped rather than shown as a verdict
	// we'd have to invent.
	_ = store.Enqueue(t.Context(), core.JobRecord{
		ID: "verdictless", Kind: core.JobKindNode, GraphRunID: "run-3", NodeID: "gate",
		Tenant: "acme", Workspace: "default", Status: core.JobStatusSucceeded,
		FinishedAt: &fin,
		Result: &core.Result{Status: core.StatusOK, Output: map[string]core.Ref{
			"pending_url": {Inline: "https://app.example/approve/run-3/gate"},
		}},
	})

	got, err := svc.ListDecidedApprovals(t.Context(), acmePrincipal, "", "", 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 || got[0].RunID != "run-1" {
		t.Fatalf("got %+v, want only the decided approval", got)
	}
}

// TestListDecidedApprovals_ScopedToTenantAndWorkspace: history is a record of
// who decided what, so a leak across tenants is a disclosure, not a glitch.
func TestListDecidedApprovals_ScopedToTenantAndWorkspace(t *testing.T) {
	svc, store := historySvc(t)
	now := time.Now()

	_ = store.Enqueue(t.Context(), decidedRec("mine", "run-1", "gate", "approve", now, "ada@acme.se", "", nil))
	other := decidedRec("theirs", "run-2", "gate", "approve", now, "eve@other.se", "", nil)
	other.Tenant = "other"
	_ = store.Enqueue(t.Context(), other)
	otherWS := decidedRec("otherws", "run-3", "gate", "approve", now, "cal@acme.se", "", nil)
	otherWS.Workspace = "finance"
	_ = store.Enqueue(t.Context(), otherWS)

	got, err := svc.ListDecidedApprovals(t.Context(), acmePrincipal, "", "", 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 || got[0].RunID != "run-1" {
		t.Fatalf("got %+v, want only this tenant's default workspace", got)
	}

	// An admin with no workspace binding sees the tenant, and can narrow.
	admin := core.Principal{Subject: "root@acme.se", Tenant: "acme"}
	all, err := svc.ListDecidedApprovals(t.Context(), admin, "", "", 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 2 {
		t.Errorf("tenant-wide got %d, want both acme workspaces", len(all))
	}
	narrowed, err := svc.ListDecidedApprovals(t.Context(), admin, "", "finance", 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(narrowed) != 1 || narrowed[0].RunID != "run-3" {
		t.Errorf("narrowed = %+v, want only the finance workspace", narrowed)
	}
}

// TestApprove_ThenAppearsInHistory drives the real path end to end. It is the
// test that matters: the history reads fields Approve writes, and the two are
// in different files — a rename of the `approved` port or the `approver` key
// would leave both halves compiling and the page silently empty.
func TestApprove_ThenAppearsInHistory(t *testing.T) {
	store := jobstore.NewMemory()
	graph := core.Graph{
		ID: "refunds", Name: "Refunds", Tenant: "acme", Workspace: "default",
		Nodes: []core.Node{{ID: "gate", Module: "await_approval"}},
	}
	payload, _ := json.Marshal(graph)
	_ = store.Enqueue(t.Context(), core.JobRecord{
		ID: "run-1", Kind: core.JobKindGraph, GraphID: "refunds", NodeID: "*",
		Tenant: "acme", Workspace: "default",
		Status: core.JobStatusRunning, GraphPayload: payload,
	})
	// Parked exactly as the module leaves it: the marker URL, the prompt, and
	// the value stashed on the internal `context` key.
	_ = store.Enqueue(t.Context(), core.JobRecord{
		ID: NodeJobID("run-1", "gate"), Kind: core.JobKindNode,
		GraphRunID: "run-1", GraphID: "refunds", NodeID: "gate",
		Tenant: "acme", Workspace: "default", Status: core.JobStatusAwaiting,
		Result: &core.Result{Status: core.StatusAwaiting, Output: map[string]core.Ref{
			"pending_url": {Inline: "https://app.example/approve/run-1/gate"},
			"prompt":      {Inline: "Refund order 4471?"},
			"context":     {Inline: map[string]any{"order": "4471", "amount": "SEK 400"}},
		}},
	})
	svc := &Service{Jobs: store, Bus: NewMemoryBus(), Engine: &engine.Engine{
		Resolver: &engine.NodeResolver{Native: engine.Default},
	}}

	// Before: the inbox has it, the history doesn't.
	pending, err := svc.ListPendingApprovals(t.Context(), acmePrincipal, "", "")
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending = %+v, err = %v; want the parked approval", pending, err)
	}
	if h, _ := svc.ListDecidedApprovals(t.Context(), acmePrincipal, "", "", 0); len(h) != 0 {
		t.Fatalf("history = %+v, want empty before the decision", h)
	}

	if err := svc.Approve(t.Context(), "run-1", "gate", ApprovalDecision{
		Decision: "reject", Approver: "ada@acme.se", Comment: "already refunded",
	}); err != nil {
		t.Fatalf("approve: %v", err)
	}

	// After: it has moved from one list to the other, carrying the evidence.
	if p, _ := svc.ListPendingApprovals(t.Context(), acmePrincipal, "", ""); len(p) != 0 {
		t.Errorf("inbox still holds %+v after the decision", p)
	}
	got, err := svc.ListDecidedApprovals(t.Context(), acmePrincipal, "", "", 0)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("history = %+v, want the decision", got)
	}
	d := got[0]
	if d.Decision != "reject" || d.Approver != "ada@acme.se" || d.Comment != "already refunded" {
		t.Errorf("decision row = %+v", d)
	}
	if d.Prompt != "Refund order 4471?" {
		t.Errorf("prompt = %q, want the author's question", d.Prompt)
	}
	// The value the flow wired in must survive the move: the pause stashes it
	// on `context`, the decision routes it out `rejected`, and the history
	// reads it back from there.
	ctx, ok := d.Context.(map[string]any)
	if !ok || ctx["order"] != "4471" {
		t.Errorf("context = %#v, want the decided value", d.Context)
	}
	if d.DecidedAt.IsZero() {
		t.Error("decided_at is zero")
	}
}

// --- HTTP surface ---

// TestHTTPGateway_DecidedApprovals covers the endpoint the history section
// calls: the decided list, its scoping, and the one input it takes.
func TestHTTPGateway_DecidedApprovals(t *testing.T) {
	h := newGatewayHarness(t)
	now := time.Now()
	fin := now.Add(-time.Minute)
	_ = h.store.Enqueue(t.Context(), core.JobRecord{
		ID: NodeJobID("run-d", "gate"), Kind: core.JobKindNode,
		GraphRunID: "run-d", GraphID: "g1", NodeID: "gate",
		Tenant: "t", Workspace: "ws", Status: core.JobStatusSucceeded,
		FinishedAt: &fin,
		Result: &core.Result{Status: core.StatusOK, Output: map[string]core.Ref{
			"pending_url": {Inline: "https://dzd/approve/run-d/gate"},
			"prompt":      {Inline: "Ship it?"},
			"rejected":    {Inline: "release-9"},
			"approver":    {Inline: "alice"},
			"comment":     {Inline: "not this week"},
		}},
	})

	// A cancelled request, older, so the two outcomes also pin the merge order
	// over the wire.
	cancelledAt := now.Add(-time.Hour)
	_ = h.store.Enqueue(t.Context(), core.JobRecord{
		ID: NodeJobID("run-c", "gate"), Kind: core.JobKindNode,
		GraphRunID: "run-c", GraphID: "g1", NodeID: "gate",
		Tenant: "t", Workspace: "ws", Status: core.JobStatusCancelled,
		FinishedAt: &cancelledAt,
		Result: &core.Result{
			Status: core.StatusError,
			Error:  &core.JobError{Code: "cancelled", Message: "called off by bob"},
			Output: map[string]core.Ref{
				"pending_url": {Inline: "https://dzd/approve/run-c/gate"},
				"prompt":      {Inline: "Ship the other one?"},
			},
		},
	})

	rw := h.do(t, "GET", "/api/v1/approvals/decided", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("code = %d body = %s", rw.Code, rw.Body.String())
	}
	var out struct {
		Approvals []struct {
			RunID     string `json:"run_id"`
			Decision  string `json:"decision"`
			Approver  string `json:"approver"`
			Comment   string `json:"comment"`
			Reason    string `json:"reason"`
			Prompt    string `json:"prompt"`
			Context   any    `json:"context"`
			DecidedAt string `json:"decided_at"`
		} `json:"approvals"`
	}
	if err := json.Unmarshal(rw.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Approvals) != 2 {
		t.Fatalf("approvals = %d, want 2: %s", len(out.Approvals), rw.Body.String())
	}
	got := out.Approvals[0]
	if got.RunID != "run-d" || got.Decision != "reject" || got.Approver != "alice" ||
		got.Comment != "not this week" || got.Prompt != "Ship it?" || got.Context != "release-9" {
		t.Errorf("row = %+v", got)
	}
	if got.DecidedAt == "" {
		t.Error("decided_at missing")
	}
	cancelled := out.Approvals[1]
	if cancelled.Decision != "cancelled" || cancelled.Reason != "called off by bob" {
		t.Errorf("cancelled row = %+v", cancelled)
	}
	if cancelled.Approver != "" || cancelled.Comment != "" {
		t.Errorf("cancelled row names a decider: %+v", cancelled)
	}

	// A junk limit is a client bug, and answering 200 with a defaulted page
	// hides it.
	if bad := h.do(t, "GET", "/api/v1/approvals/decided?limit=soon", nil); bad.Code != http.StatusBadRequest {
		t.Errorf("limit=soon → %d, want 400", bad.Code)
	}
	if bad := h.do(t, "GET", "/api/v1/approvals/decided?limit=0", nil); bad.Code != http.StatusBadRequest {
		t.Errorf("limit=0 → %d, want 400", bad.Code)
	}

	// Unauthenticated: history names who decided what, so it is behind the
	// same auth as the inbox.
	req := httptest.NewRequest("GET", "/api/v1/approvals/decided", nil)
	anon := httptest.NewRecorder()
	ServeForTest(h.gw, anon, req)
	if anon.Code != http.StatusUnauthorized {
		t.Errorf("anonymous → %d, want 401", anon.Code)
	}
}

// --- cancelled approvals ---

// parkedApprovalRun seeds a running graph with one approval parked on it, the
// way the module leaves things: the marker URL, the prompt, and the value
// stashed on the internal `context` key.
func parkedApprovalRun(t *testing.T, store core.JobStore, runID string) {
	t.Helper()
	graph := core.Graph{
		ID: "refunds", Name: "Refunds", Tenant: "acme", Workspace: "default",
		Nodes: []core.Node{{ID: "gate", Module: "await_approval"}},
	}
	payload, _ := json.Marshal(graph)
	if err := store.Enqueue(t.Context(), core.JobRecord{
		ID: runID, Kind: core.JobKindGraph, GraphID: "refunds", NodeID: "*",
		Tenant: "acme", Workspace: "default",
		Status: core.JobStatusRunning, GraphPayload: payload,
	}); err != nil {
		t.Fatalf("enqueue graph record: %v", err)
	}
	if err := store.Enqueue(t.Context(), core.JobRecord{
		ID: NodeJobID(runID, "gate"), Kind: core.JobKindNode,
		GraphRunID: runID, GraphID: "refunds", NodeID: "gate",
		Tenant: "acme", Workspace: "default", Status: core.JobStatusAwaiting,
		Result: &core.Result{Status: core.StatusAwaiting, Output: map[string]core.Ref{
			"pending_url": {Inline: "https://app.example/approve/" + runID + "/gate"},
			"prompt":      {Inline: "Refund order 4471?"},
			"context":     {Inline: map[string]any{"order": "4471"}},
		}},
	}); err != nil {
		t.Fatalf("enqueue node record: %v", err)
	}
}

// TestCancelRun_KeepsWhatAParkedStepPublished: cancelling used to overwrite the
// node's result with a bare error, erasing the only record of what the request
// had been for — the prompt, the value, the approval URL. Nothing downstream
// reads those on a cancelled node (classifyEdge blocks every edge out of one),
// but the run page and the approvals history do.
func TestCancelRun_KeepsWhatAParkedStepPublished(t *testing.T) {
	svc, store := historySvc(t)
	parkedApprovalRun(t, store, "run-x")

	if err := svc.CancelGraphRun(t.Context(), acmeRunner, "run-x", "no longer needed"); err != nil {
		t.Fatalf("cancel: %v", err)
	}

	rec, err := store.Get(t.Context(), NodeJobID("run-x", "gate"))
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if rec.Status != core.JobStatusCancelled {
		t.Fatalf("status = %q, want cancelled", rec.Status)
	}
	if rec.Result == nil || rec.Result.Error == nil || rec.Result.Error.Code != "cancelled" {
		t.Fatalf("result = %+v, want the cancel error", rec.Result)
	}
	if _, ok := rec.Result.Output["prompt"]; !ok {
		t.Error("the prompt was erased by the cancel")
	}
	if _, ok := rec.Result.Output["context"]; !ok {
		t.Error("the value being decided on was erased by the cancel")
	}
}

// TestCancelRun_LeavesAPlainNodeResultAlone: only a step that had already
// published something has anything to carry. Everything else keeps the
// error-only result it always had.
func TestCancelRun_LeavesAPlainNodeResultAlone(t *testing.T) {
	svc, store := historySvc(t)
	graph := core.Graph{
		ID: "g", Tenant: "acme", Workspace: "default",
		Nodes: []core.Node{{ID: "slow", Module: "http_request"}},
	}
	payload, _ := json.Marshal(graph)
	_ = store.Enqueue(t.Context(), core.JobRecord{
		ID: "run-y", Kind: core.JobKindGraph, GraphID: "g", NodeID: "*",
		Tenant: "acme", Workspace: "default", Status: core.JobStatusRunning,
		GraphPayload: payload,
	})
	_ = store.Enqueue(t.Context(), core.JobRecord{
		ID: NodeJobID("run-y", "slow"), Kind: core.JobKindNode,
		GraphRunID: "run-y", GraphID: "g", NodeID: "slow",
		Tenant: "acme", Workspace: "default", Status: core.JobStatusRunning,
	})

	if err := svc.CancelGraphRun(t.Context(), acmeRunner, "run-y", "stop"); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	rec, _ := store.Get(t.Context(), NodeJobID("run-y", "slow"))
	if rec.Result == nil || rec.Result.Error == nil {
		t.Fatalf("result = %+v, want the cancel error", rec.Result)
	}
	if len(rec.Result.Output) != 0 {
		t.Errorf("output = %+v, want none invented", rec.Result.Output)
	}
}

// TestListDecidedApprovals_IncludesCancelled: a request the run was called off
// under is settled — nobody decided it, and it is not waiting anywhere either.
// Left out of the history it simply vanished.
func TestListDecidedApprovals_IncludesCancelled(t *testing.T) {
	svc, store := historySvc(t)
	parkedApprovalRun(t, store, "run-x")

	// While parked it belongs to the inbox and not the history.
	if h, _ := svc.ListDecidedApprovals(t.Context(), acmePrincipal, "", "", 0); len(h) != 0 {
		t.Fatalf("history = %+v, want empty while parked", h)
	}
	if err := svc.CancelGraphRun(t.Context(), acmeRunner, "run-x", "customer withdrew the claim"); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if p, _ := svc.ListPendingApprovals(t.Context(), acmePrincipal, "", ""); len(p) != 0 {
		t.Errorf("inbox still holds %+v after the cancel", p)
	}

	got, err := svc.ListDecidedApprovals(t.Context(), acmePrincipal, "", "", 0)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("history = %+v, want the cancelled request", got)
	}
	d := got[0]
	if d.Decision != "cancelled" {
		t.Errorf("decision = %q, want cancelled", d.Decision)
	}
	if d.Reason != "customer withdrew the claim" {
		t.Errorf("reason = %q, want the cancel message", d.Reason)
	}
	if d.Approver != "" || d.Comment != "" {
		t.Errorf("nobody decided this one, but it names %q / %q", d.Approver, d.Comment)
	}
	if d.Prompt != "Refund order 4471?" {
		t.Errorf("prompt = %q, want the question it was called off on", d.Prompt)
	}
	// The value survives the cancel on the internal `context` key — only a
	// decision moves it to a port.
	ctx, ok := d.Context.(map[string]any)
	if !ok || ctx["order"] != "4471" {
		t.Errorf("context = %#v, want the value it was waiting on", d.Context)
	}
	if d.DecidedAt.IsZero() {
		t.Error("decided_at is zero")
	}
}

// TestListDecidedApprovals_MergesBothOutcomesInOrder: decisions and
// cancellations come from two separate indexed queries, so the merge is the
// only thing keeping the page in one honest order — and under a limit it also
// decides which rows appear at all.
func TestListDecidedApprovals_MergesBothOutcomesInOrder(t *testing.T) {
	svc, store := historySvc(t)
	now := time.Now()

	// Newest and oldest are decisions; the cancellation sits between them.
	_ = store.Enqueue(t.Context(), decidedRec("newest", "run-1", "gate", "approve", now.Add(-1*time.Minute), "ada@acme.se", "", nil))
	_ = store.Enqueue(t.Context(), decidedRec("oldest", "run-3", "gate", "reject", now.Add(-30*time.Minute), "bo@acme.se", "", nil))
	mid := now.Add(-10 * time.Minute)
	_ = store.Enqueue(t.Context(), core.JobRecord{
		ID: "middle", Kind: core.JobKindNode, GraphRunID: "run-2", GraphID: "refunds", NodeID: "gate",
		Tenant: "acme", Workspace: "default", Status: core.JobStatusCancelled,
		EnqueuedAt: now.Add(-time.Hour), FinishedAt: &mid,
		Result: &core.Result{
			Status: core.StatusError,
			Error:  &core.JobError{Code: "cancelled", Message: "called off"},
			Output: map[string]core.Ref{"pending_url": {Inline: "u"}, "prompt": {Inline: "?"}},
		},
	})

	got, err := svc.ListDecidedApprovals(t.Context(), acmePrincipal, "", "", 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	runs := make([]string, 0, len(got))
	for _, d := range got {
		runs = append(runs, d.RunID)
	}
	if len(runs) != 3 || runs[0] != "run-1" || runs[1] != "run-2" || runs[2] != "run-3" {
		t.Fatalf("order = %v, want the cancellation interleaved by its finish time", runs)
	}

	// Under a limit the merge, not the per-query order, decides membership.
	two, err := svc.ListDecidedApprovals(t.Context(), acmePrincipal, "", "", 2)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(two) != 2 || two[0].RunID != "run-1" || two[1].RunID != "run-2" {
		t.Errorf("limit=2 = %+v, want the two newest across both outcomes", two)
	}
}
