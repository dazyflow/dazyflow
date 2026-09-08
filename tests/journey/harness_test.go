// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package journey runs the scenarios in tests/scenarios/README.md as
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

	"github.com/dazyflow/dazyflow/auth"
	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/daemon"
	_ "github.com/dazyflow/dazyflow/drops" // register every native drop
	"github.com/dazyflow/dazyflow/drops/cursor"
	hfnet "github.com/dazyflow/dazyflow/drops/net"
	"github.com/dazyflow/dazyflow/engine"
	"github.com/dazyflow/dazyflow/engine/jobstore"
	"github.com/dazyflow/dazyflow/pollstate"
)

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
	cursor.SetStore(read, write)
	hfnet.SetHTTPCacheStore(read, write)
	pollstate.SetStore(read, write)
	t.Cleanup(func() {
		cursor.SetStore(nil, nil)
		hfnet.SetHTTPCacheStore(nil, nil)
		pollstate.SetStore(nil, nil)
	})
}

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

type newcomer struct {
	t         *testing.T
	s         *stack
	token     string
	tenant    string
	workspace string
}

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

func (n *newcomer) flowPath(id string) string {
	return "/api/v1/me/flows/" + n.tenant + "%2F" + n.workspace + "%2F" + id
}

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

func (n *newcomer) saveFlow(id string, graphJSON []byte) resp {
	return n.s.call(n.t, "PUT", n.flowPath(id), n.token, graphJSON)
}

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

func (n *newcomer) publishFlow(id string) {
	r := n.s.call(n.t, "POST", n.flowPath(id)+"/publish", n.token, nil)
	if r.status != http.StatusOK {
		n.t.Fatalf("could not publish the flow: status=%d body=%s", r.status, r.body)
	}
}

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

// fireForm submits the flow's hosted form the way a visitor would: no token,
// possession of the URL is the whole credential. The form answers with a page
// rather than a run id, so the id comes from the flow's own runs list — which
// is also how the owner would find it.
func (n *newcomer) fireForm(id string, fields map[string]any) string {
	path := "/form/" + n.tenant + "/" + n.workspace + "/" + id
	r := n.s.call(n.t, "POST", path, "", fields)
	if r.status != http.StatusOK {
		n.t.Fatalf("form submission rejected: status=%d body=%s", r.status, r.body)
	}
	return n.latestRun(id)
}

func (n *newcomer) latestRun(id string) string {
	deadline := time.Now().Add(journeyWaitCeiling)
	for time.Now().Before(deadline) {
		r := n.s.call(n.t, "GET", n.flowPath(id)+"/runs?limit=1", n.token, nil)
		if r.status == http.StatusOK {
			var out struct {
				Runs []struct {
					ID string `json:"id"`
				} `json:"runs"`
			}
			r.decode(&out)
			if len(out.Runs) > 0 && out.Runs[0].ID != "" {
				return out.Runs[0].ID
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	n.t.Fatalf("the form submission started no run of flow %q", id)
	return ""
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

func (n *newcomer) nodeRecord(runID, nodeID string) nodeView {
	r := n.s.call(n.t, "GET", "/api/v1/me/runs/"+runID+"/nodes/"+nodeID, n.token, nil)
	if r.status != http.StatusOK {
		n.t.Fatalf("node %q not found in run %s: status=%d", nodeID, runID, r.status)
	}
	var v nodeView
	r.decode(&v)
	return v
}

const scenarioDir = "../scenarios"

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
		add(node.Module)
	}
	return out
}

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
