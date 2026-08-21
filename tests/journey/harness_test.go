// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package journey runs the scenarios in tests/scenarios.md as
// experience tests, from the perspective of a brand-new, non-technical
// user. Instead of poking the engine internals, each test boots the
// real stack (the same HTTP API the web UI calls, plus a worker that
// actually executes flows) and walks the steps a newcomer takes:
//
//	sign up  ->  find the building blocks in the catalog  ->  see which
//	accounts to connect  ->  save the flow  ->  let the app validate it
//	->  fill in the blanks  ->  (where possible) run it and watch it work.
//
// A failure means a newcomer would be stuck at that step. The harness
// here is the shared plumbing; the journeys live in journey_test.go.
package journey

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"git.sr.ht/~klahr/dazyflow/auth"
	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/daemon"
	_ "git.sr.ht/~klahr/dazyflow/drops" // register every native drop
	"git.sr.ht/~klahr/dazyflow/drops/gmail"
	hfnet "git.sr.ht/~klahr/dazyflow/drops/net"
	rssdrop "git.sr.ht/~klahr/dazyflow/drops/rss"
	"git.sr.ht/~klahr/dazyflow/engine"
	"git.sr.ht/~klahr/dazyflow/engine/jobstore"
	"git.sr.ht/~klahr/dazyflow/pollstate"
)

// stack is a self-contained Dazyflow install: the HTTP API the web UI
// talks to, backed by an in-memory control plane and a real worker so
// saved flows actually run.
type stack struct {
	gw *daemon.HTTPGateway
}

func newStack(t *testing.T) *stack {
	t.Helper()

	ks := auth.NewMemKeyStore()
	users, err := auth.OpenJSONUserStore("") // "" => in-memory
	if err != nil {
		t.Fatalf("user store: %v", err)
	}
	sessions := auth.NewMemSessionStore()

	// AutoFSWorkspaces lazily provisions a workspace per signed-up
	// tenant, exactly like the self-serve daemon does. FSSandbox gives
	// filesystem-touching drops (and the Collections store) a place to
	// write under each workspace.
	wsRoot := t.TempDir()
	sandbox, err := daemon.NewFSSandbox(t.TempDir())
	if err != nil {
		t.Fatalf("sandbox: %v", err)
	}

	jobs := jobstore.NewMemory()
	bus := daemon.NewMemoryBus()
	eng := &engine.Engine{
		Resolver: &engine.NodeResolver{Native: engine.Default},
		Sandbox:  sandbox,
	}
	svc := &daemon.Service{
		Auth: auth.Chain{
			&auth.APIKeyAuthenticator{Store: ks},
			&auth.SessionAuthenticator{Store: sessions},
		},
		Workspaces: daemon.NewAutoFSWorkspaces(wsRoot),
		Jobs:       jobs,
		Engine:     eng,
		Bus:        bus,
		AdminKeys:  ks,
	}
	// The approval link. Without a signer the await-approval step hands out
	// no URL and POST /approve/ isn't registered, so the "someone taps the
	// link in the notification" half of every approval flow would be
	// untestable — and a harness that can't see it can't notice it breaking.
	signer := &daemon.HMACApprovalSigner{BaseURL: "http://localhost:8080", Secret: []byte("journey-approval-secret-0123456789")}
	eng.ApprovalSigner = signer

	gw := daemon.NewHTTPGateway(svc)
	gw.Approval = daemon.NewApprovalListener(svc, signer)
	gw.Users = users
	gw.Sessions = sessions
	gw.EnableSignup = true

	// Make the connectable accounts visible, like an install whose
	// admin has set up the OAuth providers. (A fresh self-host with no
	// OAuth configured returns 501 on /me/connections — a separate
	// onboarding gap noted in the friction report.)
	reg := daemon.NewOAuthRegistry("http://localhost:8080", nil)
	for _, def := range daemon.KnownOAuthProviderDefaults {
		reg.Register(daemon.OAuthProvider{
			Name:         def.Name,
			AuthorizeURL: def.AuthorizeURL,
			TokenURL:     def.TokenURL,
			Scopes:       def.Scopes,
		})
	}
	gw.OAuth = reg

	// A worker in the background so a fired flow makes progress, like
	// the real daemon. Fast poll so tests don't dawdle.
	workerCtx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	w := daemon.NewWorker(daemon.WorkerConfig{
		ID:              "journey-worker",
		PollInterval:    5 * time.Millisecond,
		LeaseDuration:   5 * time.Second,
		LeaseRenewEvery: 1 * time.Second,
	}, jobs, eng, bus)
	go func() { _ = w.Run(workerCtx) }()

	wireNodeState(t)
	return &stack{gw: gw}
}

// wireNodeState gives the drops that REMEMBER something between runs the
// store the real daemon gives them (cmd/dzd wires the same pairs against the
// encrypted secret store). Without it every run looks like a first run:
// "only new since last run" re-emits the whole mailbox, a feed re-fires every
// item, and an up/down watch alerts on every check. A harness that quietly
// disables the dedupe every scheduled flow depends on cannot test it, so it
// is wired here for every journey.
func wireNodeState(t *testing.T) {
	t.Helper()
	var mu sync.Mutex
	kv := map[string]string{}
	read := func(_ context.Context, tenant, name string) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		return kv[tenant+"/"+name], nil
	}
	write := func(_ context.Context, tenant, name, value string) error {
		mu.Lock()
		defer mu.Unlock()
		kv[tenant+"/"+name] = value
		return nil
	}
	gmail.SetCursorStore(read, write)
	rssdrop.SetCursorStore(read, write)
	hfnet.SetHTTPCacheStore(read, write)
	pollstate.SetStore(read, write)
	t.Cleanup(func() {
		gmail.SetCursorStore(nil, nil)
		rssdrop.SetCursorStore(nil, nil)
		hfnet.SetHTTPCacheStore(nil, nil)
		pollstate.SetStore(nil, nil)
	})
}

// resp is a tiny view over an HTTP response: status + raw body, with a
// helper to decode JSON.
type resp struct {
	t      *testing.T
	status int
	body   []byte
}

func (r resp) decode(v any) resp {
	r.t.Helper()
	if err := json.Unmarshal(r.body, v); err != nil {
		r.t.Fatalf("decode response (%s): %v", truncate(string(r.body), 300), err)
	}
	return r
}

// call issues one HTTP request through the real gateway. token may be
// empty (for unauthenticated calls like signup). bearerOverride lets the
// webhook trigger send its per-flow secret instead of the user token.
func (s *stack) call(t *testing.T, method, path, token string, body any) resp {
	t.Helper()
	var rdr *bytes.Buffer
	if body != nil {
		switch b := body.(type) {
		case []byte:
			rdr = bytes.NewBuffer(b)
		case string:
			rdr = bytes.NewBufferString(b)
		default:
			raw, _ := json.Marshal(b)
			rdr = bytes.NewBuffer(raw)
		}
	} else {
		rdr = bytes.NewBuffer(nil)
	}
	req := httptest.NewRequest(method, path, rdr)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/json")
	rw := httptest.NewRecorder()
	daemon.ServeForTest(s.gw, rw, req)
	return resp{t: t, status: rw.Code, body: rw.Body.Bytes()}
}

// --- the newcomer ----------------------------------------------------

// newcomer is a person who just found Dazyflow. They hold a session
// token and are bound to the tenant/workspace signup minted for them.
type newcomer struct {
	t         *testing.T
	s         *stack
	token     string
	tenant    string
	workspace string
}

// signUp is step one for everyone: create an account and get signed in.
func (s *stack) signUp(t *testing.T, email string) *newcomer {
	t.Helper()
	r := s.call(t, "POST", "/api/v1/auth/signup", "", map[string]any{
		"email": email, "password": "correct-horse-battery",
	})
	if r.status != http.StatusCreated {
		t.Fatalf("a newcomer could not sign up: status=%d body=%s", r.status, r.body)
	}
	var out struct {
		Token, Tenant, Workspace string
	}
	r.decode(&out)
	if out.Token == "" || out.Tenant == "" || out.Workspace == "" {
		t.Fatalf("signup did not return a usable session: %s", r.body)
	}
	return &newcomer{t: t, s: s, token: out.Token, tenant: out.Tenant, workspace: out.Workspace}
}

// flowPath builds the percent-encoded tenant/workspace/id the /me/flows
// routes expect (the web client does the same encoding).
func (n *newcomer) flowPath(id string) string {
	return "/api/v1/me/flows/" + n.tenant + "%2F" + n.workspace + "%2F" + id
}

// catalogModuleIDs returns the set of drop IDs the catalog offers — what
// a newcomer browsing the palette can actually find and drag in.
func (n *newcomer) catalogModuleIDs() map[string]bool {
	r := n.s.call(n.t, "GET", "/api/v1/catalog/drops", n.token, nil)
	if r.status != http.StatusOK {
		n.t.Fatalf("catalog did not load for a newcomer: status=%d", r.status)
	}
	var out struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	r.decode(&out)
	ids := make(map[string]bool, len(out.Items))
	for _, it := range out.Items {
		ids[it.ID] = true
	}
	return ids
}

// search mimics typing words into the catalog search box.
func (n *newcomer) search(query string) []string {
	r := n.s.call(n.t, "GET", "/api/v1/catalog/drops?q="+query, n.token, nil)
	var out struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	r.decode(&out)
	ids := make([]string, 0, len(out.Items))
	for _, it := range out.Items {
		ids = append(ids, it.ID)
	}
	return ids
}

// connectableProviders is what the Connections page shows: the accounts
// a newcomer can hook up.
func (n *newcomer) connectableProviders() []string {
	r := n.s.call(n.t, "GET", "/api/v1/me/connections", n.token, nil)
	if r.status != http.StatusOK {
		n.t.Fatalf("connections page did not load: status=%d", r.status)
	}
	var out struct {
		Providers []struct {
			Name string `json:"name"`
		} `json:"providers"`
	}
	r.decode(&out)
	names := make([]string, 0, len(out.Providers))
	for _, p := range out.Providers {
		names = append(names, p.Name)
	}
	return names
}

// saveFlow stores a flow exactly as the editor's Save button does.
func (n *newcomer) saveFlow(id string, graphJSON []byte) resp {
	return n.s.call(n.t, "PUT", n.flowPath(id), n.token, graphJSON)
}

// validateResult is the {ok, issues} the editor shows under a flow.
type validateResult struct {
	OK     bool `json:"ok"`
	Issues []struct {
		Code     string   `json:"code"`
		Severity string   `json:"severity"`
		Message  string   `json:"message"`
		NodeIDs  []string `json:"node_ids"`
	} `json:"issues"`
}

func (n *newcomer) validateFlow(id string) validateResult {
	r := n.s.call(n.t, "POST", n.flowPath(id)+"/validate", n.token, nil)
	if r.status != http.StatusOK {
		n.t.Fatalf("validate did not run: status=%d body=%s", r.status, r.body)
	}
	var out validateResult
	r.decode(&out)
	return out
}

func (n *newcomer) enableFlow(id string) {
	r := n.s.call(n.t, "POST", n.flowPath(id)+"/enable", n.token, nil)
	if r.status != http.StatusOK {
		n.t.Fatalf("could not turn the flow on: status=%d body=%s", r.status, r.body)
	}
}

// publishFlow makes the saved draft the live revision — what the editor's
// Publish button does. Nothing fires until this happens: the scheduler, the
// /trigger webhook, the hosted form and the provider-event fan-outs all
// refuse an unpublished flow. The journey covers it because a user who skips
// it has a flow that looks on and does nothing.
func (n *newcomer) publishFlow(id string) {
	r := n.s.call(n.t, "POST", n.flowPath(id)+"/publish", n.token, nil)
	if r.status != http.StatusOK {
		n.t.Fatalf("could not publish the flow: status=%d body=%s", r.status, r.body)
	}
}

// fireWebhook posts to the public trigger URL with the flow's secret,
// the way an inbound form/webhook would. Returns the run id.
func (n *newcomer) fireWebhook(id, secret string, payload any) string {
	path := "/trigger/" + n.tenant + "/" + n.workspace + "/" + id
	r := n.s.call(n.t, "POST", path, secret, payload)
	if r.status != http.StatusAccepted {
		n.t.Fatalf("webhook trigger rejected: status=%d body=%s", r.status, r.body)
	}
	var out struct {
		JobID string `json:"job_id"`
	}
	r.decode(&out)
	if out.JobID == "" {
		n.t.Fatalf("trigger returned no run id: %s", r.body)
	}
	return out.JobID
}

// eventually polls until cond holds, or fails with what was still wrong.
// Needed wherever a run's side effect happens CONCURRENTLY with the state the
// test can observe: a parked run publishes "awaiting" the moment it parks,
// while the notification carrying its approval link is dispatched just after.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(journeyWaitCeiling)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// tapApprovalLink follows the URL a notification carried, the way the person
// who received it would: an unauthenticated POST carrying the signature.
// Only the path+query are used — the run is served by this stack's own mux,
// not the public host the link names.
func (n *newcomer) tapApprovalLink(link, decision, approver string) {
	n.t.Helper()
	u, err := url.Parse(link)
	if err != nil {
		n.t.Fatalf("approval link %q is not a URL: %v", link, err)
	}
	q := u.Query()
	q.Set("decision", decision)
	q.Set("approver", approver)
	path := u.Path + "?" + q.Encode()
	if r := n.s.call(n.t, "POST", path, "", nil); r.status != http.StatusOK {
		n.t.Fatalf("tapping the approval link failed: status=%d body=%s", r.status, r.body)
	}
}

// waitForPending polls until the run parks on an approval, returning the node
// it is waiting on. A run that finishes without ever asking is a failure —
// that would mean the gate did not hold.
func (n *newcomer) waitForPending(runID string) string {
	n.t.Helper()
	deadline := time.Now().Add(journeyWaitCeiling)
	for time.Now().Before(deadline) {
		r := n.s.call(n.t, "GET", "/api/v1/approvals/pending", n.token, nil)
		if r.status == http.StatusOK {
			var out struct {
				Approvals []struct {
					RunID  string `json:"run_id"`
					NodeID string `json:"node_id"`
					URL    string `json:"url"`
				} `json:"approvals"`
			}
			r.decode(&out)
			for _, p := range out.Approvals {
				if p.RunID == runID {
					return p.NodeID
				}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	n.t.Fatalf("run %s never parked waiting for approval", runID)
	return ""
}

// runFlow triggers a manual run (the editor's Run button) and returns
// the run id.
func (n *newcomer) runFlow(id string) string {
	r := n.s.call(n.t, "POST", n.flowPath(id)+"/run", n.token, nil)
	if r.status != http.StatusAccepted {
		n.t.Fatalf("run was not accepted: status=%d body=%s", r.status, r.body)
	}
	var out struct {
		JobID string `json:"job_id"`
	}
	r.decode(&out)
	if out.JobID == "" {
		n.t.Fatalf("run returned no run id: %s", r.body)
	}
	return out.JobID
}

// waitForRun polls the run like the Run detail page does, until it
// reaches a terminal status or times out.
// journeyWaitCeiling is the generous ceiling for waiting on a run to reach
// a terminal status. waitForRun returns the instant the run finishes, so a
// large ceiling never slows a passing test — it only prevents a spurious
// failure when CPU contention under `go test -race ./...` (every package in
// parallel) starves the worker. The 8s it replaces lost that race under load.
const journeyWaitCeiling = 30 * time.Second

func (n *newcomer) waitForRun(runID string) string {
	deadline := time.Now().Add(journeyWaitCeiling)
	var last string
	for time.Now().Before(deadline) {
		r := n.s.call(n.t, "GET", "/api/v1/me/runs/"+runID, n.token, nil)
		if r.status == http.StatusOK {
			var rec struct {
				Status string `json:"status"`
			}
			r.decode(&rec)
			last = rec.Status
			switch last {
			case "succeeded", "failed", "canceled", "cancelled":
				return last
			}
		}
		time.Sleep(15 * time.Millisecond)
	}
	n.t.Fatalf("run %s never finished (last status %q)", runID, last)
	return last
}

// failedNodeReport lists the non-succeeded nodes of a run with their
// error, for when a run fails and we want to know which step and why.
func (n *newcomer) failedNodeReport(runID string) string {
	r := n.s.call(n.t, "GET", "/api/v1/me/runs/"+runID+"/nodes", n.token, nil)
	var out struct {
		Nodes []nodeView `json:"nodes"`
	}
	r.decode(&out)
	var b strings.Builder
	for _, rec := range out.Nodes {
		if string(rec.Status) == "succeeded" {
			continue
		}
		fmt.Fprintf(&b, "  - %s [%s]", rec.NodeID, rec.Status)
		if rec.Error != nil {
			fmt.Fprintf(&b, ": %s / %s", rec.Error.Code, rec.Error.Message)
		}
		b.WriteString("\n")
	}
	if b.Len() == 0 {
		return "  (no node-level failures recorded)"
	}
	return b.String()
}

// dumpRun returns every node's id, status, and outputs for a run, for
// debugging why a run did or didn't do what was expected.
func (n *newcomer) dumpRun(runID string) string {
	r := n.s.call(n.t, "GET", "/api/v1/me/runs/"+runID+"/nodes", n.token, nil)
	var out struct {
		Nodes []nodeView `json:"nodes"`
	}
	r.decode(&out)
	var b strings.Builder
	for _, rec := range out.Nodes {
		fmt.Fprintf(&b, "  %s [%s]", rec.NodeID, rec.Status)
		for port, ref := range rec.Outputs {
			bs, _ := json.Marshal(ref.Inline)
			fmt.Fprintf(&b, " %s=%s", port, truncate(string(bs), 200))
		}
		if rec.Error != nil {
			fmt.Fprintf(&b, " ERR=%s", rec.Error.Message)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// nodeView mirrors the daemon's node-run DTO (daemon.nodeRunView) — the
// shape the run-detail endpoints actually serialize. Node records are NOT
// returned as a raw core.JobRecord: the result is flattened into top-level
// `outputs`/`error` with no `result` envelope, so decoding into a
// core.JobRecord silently drops them. This is the harness's view of one
// executed node.
type nodeView struct {
	NodeID  string              `json:"node_id"`
	Status  core.JobStatus      `json:"status"`
	Inputs  map[string]core.Ref `json:"inputs"`
	Outputs map[string]core.Ref `json:"outputs"`
	Error   *core.JobError      `json:"error"`
}

// nodeRecord fetches one node's record within a run, as the Run detail
// page does when you click a node.
func (n *newcomer) nodeRecord(runID, nodeID string) nodeView {
	r := n.s.call(n.t, "GET", "/api/v1/me/runs/"+runID+"/nodes/"+nodeID, n.token, nil)
	if r.status != http.StatusOK {
		n.t.Fatalf("node %q not found in run %s: status=%d", nodeID, runID, r.status)
	}
	var v nodeView
	r.decode(&v)
	return v
}

// --- scenario files --------------------------------------------------

const scenarioDir = "../scenarios"

// scenarioFiles lists the NN-*.json graphs that back scenarios.md.
func scenarioFiles(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(scenarioDir, "*.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no scenario graphs found under %s: %v", scenarioDir, err)
	}
	return files
}

func readGraph(t *testing.T, file string) (raw []byte, g core.Graph) {
	t.Helper()
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read %s: %v", file, err)
	}
	if err := json.Unmarshal(raw, &g); err != nil {
		t.Fatalf("parse %s: %v", file, err)
	}
	return raw, g
}

// neededModules returns every drop a scenario depends on: the node
// modules plus any for_each step modules (which a newcomer must also
// find in the catalog).
func neededModules(g core.Graph) []string {
	seen := map[string]bool{}
	var out []string
	add := func(id string) {
		if id == "" || seen[id] {
			return
		}
		seen[id] = true
		out = append(out, id)
	}
	for _, node := range g.Nodes {
		// A for_each's per-item step is now an ordinary node wired to its
		// `body` pin, so collecting every node's module already covers it.
		add(node.Module)
	}
	return out
}

// fillBlanks replaces the REPLACE_WITH_… template placeholders with
// plausible values, standing in for a newcomer who filled the form
// fields in the editor.
func fillBlanks(raw []byte) []byte {
	repl := strings.NewReplacer(
		"REPLACE_WITH_SHEET_ID", "1AbcDEFghIJklmNOPqrsTUVwxyZ_0123456789abcd",
		"REPLACE_WITH_NOTION_DB_ID", "11111111-2222-3333-4444-555555555555",
		"REPLACE_WITH_WEBHOOK_SECRET", "journey-secret",
	)
	return []byte(repl.Replace(string(raw)))
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
