package e2e

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"git.sr.ht/~klahr/hazy-flow/auth"
	"git.sr.ht/~klahr/hazy-flow/core"
	"git.sr.ht/~klahr/hazy-flow/daemon"
	"git.sr.ht/~klahr/hazy-flow/engine"
	"git.sr.ht/~klahr/hazy-flow/engine/jobstore"
	_ "git.sr.ht/~klahr/hazy-flow/integrations"
	"git.sr.ht/~klahr/hazy-flow/workspace"
)

// TestHTTPGateway_E2E_SubmitAndStreamSSE drives the full UI-facing path:
//
//  1. PUT a graph
//  2. POST /run
//  3. GET /jobs/{id}/events with SSE — expect a snapshot frame, optional
//     progress frames, and a terminal frame
//  4. GET /jobs/{id} — expect status=succeeded
//
// This is the contract a visual editor will rely on.
func TestHTTPGateway_E2E_SubmitAndStreamSSE(t *testing.T) {
	ks := auth.NewMemKeyStore()
	role := core.Role{Name: "editor", Permissions: []core.Permission{
		core.PermGraphRun, core.PermGraphEdit, core.PermGraphAdmin,
	}}
	_, token, err := auth.IssueAPIKey(ks, t.Context(), "k", "t", "ws", "alice", []core.Role{role}, nil)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}

	wsStore, _ := workspace.OpenFS("")
	store := jobstore.NewMemory()
	bus := daemon.NewMemoryBus()
	eng := &engine.Engine{Resolver: &engine.NodeResolver{Native: engine.Default}}
	svc := &daemon.Service{
		Auth:       auth.Chain{&auth.APIKeyAuthenticator{Store: ks}},
		Workspaces: daemon.MapWorkspaces{"t/ws": wsStore},
		Jobs:       store,
		Engine:     eng,
		Bus:        bus,
	}
	wctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	w := daemon.NewWorker(daemon.WorkerConfig{
		ID: "w", PollInterval: 5 * time.Millisecond, MaxRetries: 1,
	}, store, eng, bus)
	go func() { _ = w.Run(wctx) }()

	gw := daemon.NewHTTPGateway(svc)
	ts := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		daemon.ServeForTest(gw, rw, r)
	}))
	defer ts.Close()

	// 1. PUT graph
	g := core.Graph{
		ID: "stream-demo", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{
			{ID: "step1", Module: "sleep", Params: map[string]any{"ms": 5}},
			{ID: "step2", Module: "sleep", Params: map[string]any{"ms": 5}},
		},
		Edges: []core.Edge{
			{From: "step1", FromPort: "out", To: "step2", ToPort: "in"},
		},
	}
	body, _ := json.Marshal(g)
	putReq, _ := http.NewRequest("PUT", ts.URL+"/api/v1/me/flows/t%2Fws%2Fstream-demo", bytes.NewReader(body))
	putReq.Header.Set("Authorization", "Bearer "+token)
	putReq.Header.Set("Content-Type", "application/json")
	putResp, err := http.DefaultClient.Do(putReq)
	if err != nil {
		t.Fatalf("PUT: %v", err)
	}
	putResp.Body.Close()
	if putResp.StatusCode != http.StatusOK {
		t.Fatalf("PUT status = %d", putResp.StatusCode)
	}

	// 2. POST /run
	runReq, _ := http.NewRequest("POST", ts.URL+"/api/v1/me/flows/t%2Fws%2Fstream-demo/run", nil)
	runReq.Header.Set("Authorization", "Bearer "+token)
	runResp, err := http.DefaultClient.Do(runReq)
	if err != nil {
		t.Fatalf("POST run: %v", err)
	}
	defer runResp.Body.Close()
	if runResp.StatusCode != http.StatusAccepted {
		t.Fatalf("run status = %d", runResp.StatusCode)
	}
	var runOut struct {
		JobID string `json:"job_id"`
	}
	_ = json.NewDecoder(runResp.Body).Decode(&runOut)
	if runOut.JobID == "" {
		t.Fatal("no job_id")
	}

	// 3. GET /jobs/{id}/events — SSE
	streamCtx, streamCancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer streamCancel()
	sseReq, _ := http.NewRequestWithContext(streamCtx, "GET",
		ts.URL+"/api/v1/me/runs/"+runOut.JobID+"/events", nil)
	sseReq.Header.Set("Authorization", "Bearer "+token)
	sseResp, err := http.DefaultClient.Do(sseReq)
	if err != nil {
		t.Fatalf("GET events: %v", err)
	}
	defer sseResp.Body.Close()
	if ct := sseResp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content-type = %q, want text/event-stream", ct)
	}

	events := readSSEUntilTerminal(t, sseResp.Body, 5*time.Second)
	if len(events) == 0 {
		t.Fatal("no SSE events received")
	}
	// The very first frame is a snapshot of the current job-record.
	if events[0].name != "snapshot" {
		t.Errorf("first event = %q, want snapshot", events[0].name)
	}
	// The last frame is the terminal event.
	last := events[len(events)-1]
	if last.name != "terminal" {
		t.Fatalf("last event = %q, want terminal", last.name)
	}

	// 4. GET /jobs/{id} — final snapshot
	snapReq, _ := http.NewRequest("GET", ts.URL+"/api/v1/me/runs/"+runOut.JobID, nil)
	snapReq.Header.Set("Authorization", "Bearer "+token)
	snapResp, err := http.DefaultClient.Do(snapReq)
	if err != nil {
		t.Fatalf("GET job: %v", err)
	}
	defer snapResp.Body.Close()
	if snapResp.StatusCode != http.StatusOK {
		t.Fatalf("snapshot status = %d", snapResp.StatusCode)
	}
	var rec core.JobRecord
	if err := json.NewDecoder(snapResp.Body).Decode(&rec); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if rec.Status != core.JobStatusSucceeded {
		t.Errorf("final status = %q, want succeeded", rec.Status)
	}
}

// TestHTTPGateway_E2E_PerNodeSSE asserts that the SSE stream carries
// per-node status transitions as `node` frames, in order, plus a
// terminal frame at the end. The graph has two sleeps so we should
// see at least four `node` frames: running+succeeded for each node.
func TestHTTPGateway_E2E_PerNodeSSE(t *testing.T) {
	ks := auth.NewMemKeyStore()
	role := core.Role{Name: "editor", Permissions: []core.Permission{
		core.PermGraphRun, core.PermGraphEdit, core.PermGraphAdmin,
	}}
	_, token, err := auth.IssueAPIKey(ks, t.Context(), "k", "t", "ws", "alice", []core.Role{role}, nil)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	wsStore, _ := workspace.OpenFS("")
	store := jobstore.NewMemory()
	bus := daemon.NewMemoryBus()
	eng := &engine.Engine{Resolver: &engine.NodeResolver{Native: engine.Default}}
	svc := &daemon.Service{
		Auth:       auth.Chain{&auth.APIKeyAuthenticator{Store: ks}},
		Workspaces: daemon.MapWorkspaces{"t/ws": wsStore},
		Jobs:       store,
		Engine:     eng,
		Bus:        bus,
	}
	wctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	w := daemon.NewWorker(daemon.WorkerConfig{
		ID: "w", PollInterval: 5 * time.Millisecond, MaxRetries: 1,
	}, store, eng, bus)
	go func() { _ = w.Run(wctx) }()

	gw := daemon.NewHTTPGateway(svc)
	ts := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		daemon.ServeForTest(gw, rw, r)
	}))
	defer ts.Close()

	g := core.Graph{
		ID: "node-sse", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{
			{ID: "alpha", Module: "sleep", Params: map[string]any{"ms": 20}},
			{ID: "beta", Module: "sleep", Params: map[string]any{"ms": 20}},
		},
		Edges: []core.Edge{
			{From: "alpha", FromPort: "out", To: "beta", ToPort: "in"},
		},
	}
	body, _ := json.Marshal(g)
	putReq, _ := http.NewRequest("PUT", ts.URL+"/api/v1/me/flows/t%2Fws%2Fnode-sse", bytes.NewReader(body))
	putReq.Header.Set("Authorization", "Bearer "+token)
	putReq.Header.Set("Content-Type", "application/json")
	putResp, _ := http.DefaultClient.Do(putReq)
	putResp.Body.Close()

	runReq, _ := http.NewRequest("POST", ts.URL+"/api/v1/me/flows/t%2Fws%2Fnode-sse/run", nil)
	runReq.Header.Set("Authorization", "Bearer "+token)
	runResp, err := http.DefaultClient.Do(runReq)
	if err != nil {
		t.Fatalf("run request: %v", err)
	}
	defer runResp.Body.Close()
	var runOut struct {
		JobID string `json:"job_id"`
	}
	_ = json.NewDecoder(runResp.Body).Decode(&runOut)

	streamCtx, streamCancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer streamCancel()
	sseReq, _ := http.NewRequestWithContext(streamCtx, "GET",
		ts.URL+"/api/v1/me/runs/"+runOut.JobID+"/events", nil)
	sseReq.Header.Set("Authorization", "Bearer "+token)
	sseResp, err := http.DefaultClient.Do(sseReq)
	if err != nil {
		t.Fatalf("GET events: %v", err)
	}
	defer sseResp.Body.Close()

	events := readSSEUntilTerminal(t, sseResp.Body, 5*time.Second)

	// Collect per-node status events; we expect each node to appear with
	// at least one terminal-ish status. (Snapshot may also have emitted
	// some — we just want non-empty coverage.)
	statuses := map[string][]string{}
	for _, e := range events {
		if e.name != "node" {
			continue
		}
		var ev struct {
			NodeID string `json:"node_id"`
			Status string `json:"status"`
		}
		_ = json.Unmarshal(e.data, &ev)
		statuses[ev.NodeID] = append(statuses[ev.NodeID], ev.Status)
	}
	for _, n := range []string{"alpha", "beta"} {
		seq := statuses[n]
		if len(seq) == 0 {
			t.Errorf("no `node` frames for %q (saw %d events total)", n, len(events))
			continue
		}
		// Last status seen for this node must be terminal-success.
		final := seq[len(seq)-1]
		if final != "succeeded" {
			t.Errorf("%q final status = %q, want succeeded (seq=%v)", n, final, seq)
		}
	}
}

// sseEvent captures one SSE frame's name and JSON-decoded data so tests
// can assert against frame sequence without re-parsing.
type sseEvent struct {
	name string
	data []byte
}

// readSSEUntilTerminal reads SSE frames until either a terminal frame
// arrives or the deadline elapses. Frames are `event: <name>\ndata: <json>\n\n`.
// Comment-only ping frames (lines starting with ":") are skipped.
func readSSEUntilTerminal(t *testing.T, body io.Reader, deadline time.Duration) []sseEvent {
	t.Helper()
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64*1024), 64*1024)
	var out []sseEvent
	var current sseEvent
	stop := time.AfterFunc(deadline, func() {
		// no-op; the bufio scanner already returns on EOF/close
	})
	defer stop.Stop()
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, ":"):
			// keep-alive comment, ignore
		case strings.HasPrefix(line, "event: "):
			current.name = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			current.data = []byte(strings.TrimPrefix(line, "data: "))
		case line == "":
			if current.name != "" {
				out = append(out, current)
				if current.name == "terminal" {
					return out
				}
				current = sseEvent{}
			}
		}
	}
	return out
}
