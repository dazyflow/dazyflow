package e2e

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"git.sr.ht/~klahr/hazyflow/auth"
	"git.sr.ht/~klahr/hazyflow/core"
	"git.sr.ht/~klahr/hazyflow/daemon"
	"git.sr.ht/~klahr/hazyflow/engine"
	"git.sr.ht/~klahr/hazyflow/engine/jobstore"
	_ "git.sr.ht/~klahr/hazyflow/drops"
)

// approvalHarness builds a full stack (service + worker + approval
// listener) and returns the pieces the test asserts against.
type approvalHarness struct {
	svc        *daemon.Service
	store      core.JobStore
	bus        *daemon.MemoryBus
	signer     *daemon.HMACApprovalSigner
	listener   *daemon.ApprovalListener
	approveURL string
	t          *testing.T
}

func newApprovalHarness(t *testing.T) *approvalHarness {
	t.Helper()
	store := jobstore.NewMemory()
	bus := daemon.NewMemoryBus()
	signer := &daemon.HMACApprovalSigner{BaseURL: "http://placeholder", Secret: []byte("approval-test-key")}
	eng := &engine.Engine{
		Resolver:       &engine.NodeResolver{Native: engine.Default},
		ApprovalSigner: signer,
	}
	role := core.Role{Name: "editor", Permissions: []core.Permission{
		core.PermGraphRun, core.PermGraphEdit, core.PermGraphAdmin,
	}}
	ks := auth.NewMemKeyStore()
	_, _, _ = auth.IssueAPIKey(ks, t.Context(), "k", "t", "ws", "u", []core.Role{role}, nil)

	svc := &daemon.Service{
		Auth:   auth.Chain{&auth.APIKeyAuthenticator{Store: ks}},
		Jobs:   store,
		Engine: eng,
		Bus:    bus,
	}
	wctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	w := daemon.NewWorker(daemon.WorkerConfig{
		ID: "w", PollInterval: 5 * time.Millisecond, MaxRetries: 1,
	}, store, eng, bus)
	go func() { _ = w.Run(wctx) }()

	listener := daemon.NewApprovalListener(svc, signer)
	mux := http.NewServeMux()
	mux.HandleFunc("/approve/", func(rw http.ResponseWriter, r *http.Request) {
		daemon.ServeApprovalForTest(listener, rw, r)
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	signer.BaseURL = ts.URL

	return &approvalHarness{
		svc:        svc,
		store:      store,
		bus:        bus,
		signer:     signer,
		listener:   listener,
		approveURL: ts.URL,
		t:          t,
	}
}

// TestAwaitApproval_E2E_ApproveResumesDownstream is the headline test.
// A graph with sleep → await_approval → sleep submits, parks on the
// approval, an external POST approves, and the downstream sleep runs
// and the graph finalizes successfully.
func TestAwaitApproval_E2E_ApproveResumesDownstream(t *testing.T) {
	h := newApprovalHarness(t)

	role := core.Role{Name: "editor", Permissions: []core.Permission{
		core.PermGraphRun, core.PermGraphEdit, core.PermGraphAdmin,
	}}
	p := core.Principal{Tenant: "t", Workspace: "ws", Roles: []core.Role{role}}

	g := core.Graph{
		ID: "needs-approval", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{
			{ID: "prep", Module: "sleep", Params: map[string]any{"ms": 1}},
			{ID: "ask", Module: "await_approval", Params: map[string]any{
				"prompt": "Refund $9,500 to customer?",
			}},
			{ID: "execute", Module: "sleep", Params: map[string]any{"ms": 1}},
			{ID: "denied", Module: "sleep", Params: map[string]any{"ms": 1}},
		},
		Edges: []core.Edge{
			{From: "prep", FromPort: "out", To: "ask", ToPort: "context"},
			{From: "ask", FromPort: "approved", To: "execute", ToPort: "in"},
			{From: "ask", FromPort: "rejected", To: "denied", ToPort: "in"},
		},
	}

	runID, err := h.svc.SubmitGraph(t.Context(), p, g)
	if err != nil {
		t.Fatalf("SubmitGraph: %v", err)
	}

	// Wait until the approval node has parked.
	var askRec core.JobRecord
	waitFor(t, "ask node to reach awaiting", func() bool {
		askRec, _ = h.store.Get(t.Context(), daemon.NodeJobID(runID, "ask"))
		return askRec.Status == core.JobStatusAwaiting
	})

	// The graph-record must still be running (not terminal) while the
	// approval is pending.
	graphRec, _ := h.store.Get(t.Context(), runID)
	if core.IsTerminalStatus(graphRec.Status) {
		t.Fatalf("graph already terminal while awaiting: %q", graphRec.Status)
	}

	// And neither downstream branch has fired yet — both should be
	// absent from the store.
	if _, err := h.store.Get(t.Context(), daemon.NodeJobID(runID, "execute")); err == nil {
		t.Errorf("execute fired before approval")
	}
	if _, err := h.store.Get(t.Context(), daemon.NodeJobID(runID, "denied")); err == nil {
		t.Errorf("denied fired before approval")
	}

	// Hit the approval URL the module emitted on pending_url.
	approvalURL, _ := askRec.Result.Output["pending_url"].Inline.(string)
	if !strings.Contains(approvalURL, "/approve/") {
		t.Fatalf("pending_url malformed: %q", approvalURL)
	}
	req, _ := http.NewRequest("POST", approvalURL+"&decision=approve&approver=alice", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST approve: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("approve status = %d", resp.StatusCode)
	}

	// Wait for the graph to finalize.
	terminal := waitForFire(t, h.store, runID)
	if terminal != core.JobStatusSucceeded {
		t.Fatalf("terminal status = %q", terminal)
	}

	// The approved branch ran; the rejected branch was skipped.
	exec, _ := h.store.Get(t.Context(), daemon.NodeJobID(runID, "execute"))
	if exec.Status != core.JobStatusSucceeded {
		t.Errorf("execute status = %q, want succeeded", exec.Status)
	}
	denied, _ := h.store.Get(t.Context(), daemon.NodeJobID(runID, "denied"))
	if denied.Status != core.JobStatusSkipped {
		t.Errorf("denied status = %q, want skipped", denied.Status)
	}

	// The resumed ask record carries the decision and approver.
	resumed, _ := h.store.Get(t.Context(), daemon.NodeJobID(runID, "ask"))
	if resumed.Status != core.JobStatusSucceeded {
		t.Errorf("ask status = %q, want succeeded", resumed.Status)
	}
	if got, _ := resumed.Result.Output["decision"].Inline.(string); got != "approve" {
		t.Errorf("decision = %q", got)
	}
	if got, _ := resumed.Result.Output["approver"].Inline.(string); got != "alice" {
		t.Errorf("approver = %q", got)
	}
}

// TestAwaitApproval_E2E_RejectRoutesToRejectedBranch flips the decision:
// the approved branch should be skipped, the rejected branch should run.
func TestAwaitApproval_E2E_RejectRoutesToRejectedBranch(t *testing.T) {
	h := newApprovalHarness(t)
	role := core.Role{Name: "editor", Permissions: []core.Permission{
		core.PermGraphRun, core.PermGraphEdit, core.PermGraphAdmin,
	}}
	p := core.Principal{Tenant: "t", Workspace: "ws", Roles: []core.Role{role}}

	g := core.Graph{
		ID: "reject-flow", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{
			{ID: "ask", Module: "await_approval"},
			{ID: "execute", Module: "sleep", Params: map[string]any{"ms": 1}},
			{ID: "denied", Module: "sleep", Params: map[string]any{"ms": 1}},
		},
		Edges: []core.Edge{
			{From: "ask", FromPort: "approved", To: "execute", ToPort: "in"},
			{From: "ask", FromPort: "rejected", To: "denied", ToPort: "in"},
		},
	}
	runID, err := h.svc.SubmitGraph(t.Context(), p, g)
	if err != nil {
		t.Fatalf("SubmitGraph: %v", err)
	}

	var askRec core.JobRecord
	waitFor(t, "ask node to reach awaiting", func() bool {
		askRec, _ = h.store.Get(t.Context(), daemon.NodeJobID(runID, "ask"))
		return askRec.Status == core.JobStatusAwaiting
	})
	approvalURL, _ := askRec.Result.Output["pending_url"].Inline.(string)

	req, _ := http.NewRequest("POST", approvalURL+"&decision=reject&approver=bob&comment=too+risky", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST approve: %v", err)
	}
	resp.Body.Close()

	terminal := waitForFire(t, h.store, runID)
	if terminal != core.JobStatusSucceeded {
		t.Fatalf("terminal = %q", terminal)
	}

	exec, _ := h.store.Get(t.Context(), daemon.NodeJobID(runID, "execute"))
	if exec.Status != core.JobStatusSkipped {
		t.Errorf("execute = %q, want skipped", exec.Status)
	}
	denied, _ := h.store.Get(t.Context(), daemon.NodeJobID(runID, "denied"))
	if denied.Status != core.JobStatusSucceeded {
		t.Errorf("denied = %q, want succeeded", denied.Status)
	}
	resumed, _ := h.store.Get(t.Context(), daemon.NodeJobID(runID, "ask"))
	if got, _ := resumed.Result.Output["comment"].Inline.(string); got != "too risky" {
		t.Errorf("comment = %q", got)
	}
}

// TestAwaitApproval_E2E_DoubleApproveIs409 verifies that a second
// approval call on a now-succeeded node returns a conflict rather than
// silently double-firing dependents.
func TestAwaitApproval_E2E_DoubleApproveIs409(t *testing.T) {
	h := newApprovalHarness(t)
	role := core.Role{Name: "editor", Permissions: []core.Permission{
		core.PermGraphRun, core.PermGraphEdit, core.PermGraphAdmin,
	}}
	p := core.Principal{Tenant: "t", Workspace: "ws", Roles: []core.Role{role}}

	g := core.Graph{
		ID: "double-approve", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{{ID: "ask", Module: "await_approval"}},
	}
	runID, _ := h.svc.SubmitGraph(t.Context(), p, g)

	var askRec core.JobRecord
	waitFor(t, "ask node to reach awaiting", func() bool {
		askRec, _ = h.store.Get(t.Context(), daemon.NodeJobID(runID, "ask"))
		return askRec.Status == core.JobStatusAwaiting
	})
	approvalURL, _ := askRec.Result.Output["pending_url"].Inline.(string)

	for i, want := range []int{http.StatusOK, http.StatusConflict} {
		req, _ := http.NewRequest("POST", approvalURL+"&decision=approve", nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("POST %d: %v", i, err)
		}
		resp.Body.Close()
		if resp.StatusCode != want {
			t.Errorf("call %d: status = %d, want %d", i, resp.StatusCode, want)
		}
	}
}
