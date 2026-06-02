package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"git.sr.ht/~klahr/hazyflow/mcp/server"
)

// fakeHzd stands in for the daemon's /api/v1 surface. Tests register
// handlers per "METHOD /path" so each scenario controls exactly what
// the client sees.
type fakeHzd struct {
	mu       map[string]http.HandlerFunc
	requests []recordedRequest
}

type recordedRequest struct {
	method string
	path   string
	auth   string
	body   []byte
}

func newFakeHzd() (*fakeHzd, *httptest.Server) {
	f := &fakeHzd{mu: map[string]http.HandlerFunc{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.requests = append(f.requests, recordedRequest{
			method: r.Method, path: r.URL.RequestURI(),
			auth: r.Header.Get("Authorization"), body: body,
		})
		key := r.Method + " " + r.URL.Path
		if h, ok := f.mu[key]; ok {
			r.Body = io.NopCloser(bytes.NewReader(body))
			h(w, r)
			return
		}
		http.Error(w, "no handler for "+key, http.StatusNotFound)
	}))
	return f, srv
}

func (f *fakeHzd) on(method, path string, h http.HandlerFunc) {
	f.mu[method+" "+path] = h
}

// runToolCall executes a single tools/call through the framing layer
// so tests exercise the same code path Claude Desktop hits.
func runToolCall(t *testing.T, s *server.Server, toolName string, args any) server.ToolCallResult {
	t.Helper()
	argsRaw, _ := json.Marshal(args)
	req := map[string]any{
		"jsonrpc": "2.0",
		"id":      99,
		"method":  "tools/call",
		"params":  map[string]any{"name": toolName, "arguments": json.RawMessage(argsRaw)},
	}
	b, _ := json.Marshal(req)
	in := strings.NewReader(string(b) + "\n")
	out := &bytes.Buffer{}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if err := s.Serve(ctx, in, out); err != nil && err != io.EOF {
		t.Fatalf("Serve: %v", err)
	}
	var resp struct {
		Result server.ToolCallResult `json:"result"`
		Error  *server.RPCError      `json:"error"`
	}
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v\nbody=%q", err, out.String())
	}
	if resp.Error != nil {
		t.Fatalf("rpc error: %v", resp.Error)
	}
	return resp.Result
}

// fullStack builds a Server with all the real tools registered
// against the fake daemon. Lets us assert "the LLM hits this tool
// and the right HTTP request lands at the daemon."
func fullStack(t *testing.T) (*server.Server, *fakeHzd, *httptest.Server) {
	t.Helper()
	fake, srv := newFakeHzd()
	t.Cleanup(srv.Close)
	c := server.NewHazydClient(srv.URL, "test-token")
	s := &server.Server{Name: "test", Version: "1.0"}
	for _, tool := range server.BuildTools(c, server.Defaults{Tenant: "t", Workspace: "ws"}) {
		s.Register(tool)
	}
	return s, fake, srv
}

func TestTool_ListDrops_HitsRightEndpoint(t *testing.T) {
	s, fake, _ := fullStack(t)
	// list_drops now hits the new catalog endpoint (the LLM-friendly
	// surface with Summary + Examples) rather than the legacy
	// /api/v1/drops shape.
	fake.on("GET", "/api/v1/catalog/drops", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"items":[{"id":"http_request","label":"HTTP request"}]}`)
	})

	res := runToolCall(t, s, "list_drops", map[string]any{})
	if res.IsError {
		t.Fatalf("isError: %s", res.Content[0].Text)
	}
	if !strings.Contains(res.Content[0].Text, "http_request") {
		t.Errorf("content = %q", res.Content[0].Text)
	}
	if len(fake.requests) != 1 || fake.requests[0].auth != "Bearer test-token" {
		t.Errorf("requests = %+v", fake.requests)
	}
}

// create_flow must thread tenant/workspace from the server defaults
// into the URL path, NOT just into the body — the gateway uses the
// path values as the source of truth.
func TestTool_CreateFlow_UsesDefaultsInPath(t *testing.T) {
	s, fake, _ := fullStack(t)
	// flow_id is the percent-encoded composite tenant/workspace/id;
	// the fake's net/http server normalizes %2F back to / in r.URL.Path,
	// so we register the decoded shape.
	fake.on("PUT", "/api/v1/me/flows/t/ws/my-flow", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"commit":"abc","flow_id":"t/ws/my-flow","graph_id":"my-flow"}`)
	})

	res := runToolCall(t, s, "create_flow", map[string]any{
		"id":    "my-flow",
		"nodes": []map[string]any{{"id": "n1", "module": "noop"}},
	})
	if res.IsError {
		t.Fatalf("isError: %s", res.Content[0].Text)
	}
	if len(fake.requests) != 1 {
		t.Fatalf("requests = %+v", fake.requests)
	}
	r := fake.requests[0]
	// r.path is the raw RequestURI (encoded), so slashes inside the
	// flow_id composite show up as %2F here. The fake's handler-lookup
	// table keys on decoded r.URL.Path, which is why "on" above matched.
	if r.method != "PUT" || r.path != "/api/v1/me/flows/t%2Fws%2Fmy-flow" {
		t.Errorf("request = %s %s", r.method, r.path)
	}
	var body map[string]any
	_ = json.Unmarshal(r.body, &body)
	if body["tenant"] != "t" || body["workspace"] != "ws" || body["id"] != "my-flow" {
		t.Errorf("body = %+v", body)
	}
}

// 409 from the gateway (lock check on edit-while-running) should
// surface as a tool-error result with the message intact — the LLM
// reads it and can tell the user to wait for the run to finish.
func TestTool_CreateFlow_ServerConflictBecomesToolError(t *testing.T) {
	s, fake, _ := fullStack(t)
	fake.on("PUT", "/api/v1/me/flows/t/ws/locked", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `flow "locked" has an active run`, http.StatusConflict)
	})

	res := runToolCall(t, s, "create_flow", map[string]any{
		"id":    "locked",
		"nodes": []map[string]any{},
	})
	if !res.IsError {
		t.Fatalf("expected tool-error, got success: %s", res.Content[0].Text)
	}
	if !strings.Contains(res.Content[0].Text, "active run") {
		t.Errorf("message lost: %q", res.Content[0].Text)
	}
}

// TestTool_StructuredErrorEnvelope_PropagatesCode verifies that when
// the daemon returns the new spec-aligned ErrorEnvelope shape, the
// MCP tool surfaces it as structured JSON — so an LLM can branch on
// `code` (e.g. "flow_locked") instead of parsing English.
func TestTool_StructuredErrorEnvelope_PropagatesCode(t *testing.T) {
	s, fake, _ := fullStack(t)
	fake.on("POST", "/api/v1/me/flows/t/ws/wedged/run", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_, _ = io.WriteString(w, `{"error":{"code":"flow_locked","message":"another run in flight","doc":"/api/v1/openapi.json#flow"}}`)
	})

	res := runToolCall(t, s, "run_flow", map[string]any{"id": "wedged"})
	if !res.IsError {
		t.Fatalf("expected tool-error, got success: %s", res.Content[0].Text)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(res.Content[0].Text), &got); err != nil {
		t.Fatalf("error result is not JSON: %v\n%s", err, res.Content[0].Text)
	}
	if got["code"] != "flow_locked" {
		t.Errorf("code = %v, want flow_locked", got["code"])
	}
	if got["message"] != "another run in flight" {
		t.Errorf("message = %v", got["message"])
	}
	if got["doc"] != "/api/v1/openapi.json#flow" {
		t.Errorf("doc = %v", got["doc"])
	}
	if got["status"].(float64) != 409 {
		t.Errorf("status = %v, want 409", got["status"])
	}
}

// wait_for_run polls until a terminal status appears. Verifies the
// polling shape (multiple GETs) plus the "wait_timed_out" sentinel
// when the run is still in flight at deadline.
func TestTool_WaitForRun_ReturnsTerminal(t *testing.T) {
	s, fake, _ := fullStack(t)
	calls := 0
	fake.on("GET", "/api/v1/me/runs/r1", func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls < 2 {
			_, _ = io.WriteString(w, `{"id":"r1","status":"running"}`)
			return
		}
		_, _ = io.WriteString(w, `{"id":"r1","status":"succeeded"}`)
	})

	res := runToolCall(t, s, "wait_for_run", map[string]any{
		"run_id":          "r1",
		"timeout_seconds": 5,
	})
	if res.IsError {
		t.Fatalf("isError: %s", res.Content[0].Text)
	}
	if !strings.Contains(res.Content[0].Text, `"status": "succeeded"`) {
		t.Errorf("content = %q", res.Content[0].Text)
	}
	if calls < 2 {
		t.Errorf("expected at least 2 polls, got %d", calls)
	}
}

func TestTool_ApproveNode_QueryParams(t *testing.T) {
	s, fake, _ := fullStack(t)
	fake.on("POST", "/api/v1/approvals/r1/n1", func(w http.ResponseWriter, r *http.Request) {
		// The decision and comment ride in the query string, not the
		// body — match how the gateway's approveAuthed parses them.
		if r.URL.Query().Get("decision") != "approve" {
			http.Error(w, "missing decision", http.StatusBadRequest)
			return
		}
		if r.URL.Query().Get("comment") != "looks good" {
			http.Error(w, "missing comment", http.StatusBadRequest)
			return
		}
		_, _ = io.WriteString(w, `{"status":"resumed","decision":"approve"}`)
	})

	res := runToolCall(t, s, "approve_node", map[string]any{
		"run_id":   "r1",
		"node_id":  "n1",
		"decision": "approve",
		"comment":  "looks good",
	})
	if res.IsError {
		t.Fatalf("isError: %s", res.Content[0].Text)
	}
}

func TestTool_MissingRequiredArg_ReturnsToolError(t *testing.T) {
	s, _, _ := fullStack(t)
	res := runToolCall(t, s, "get_flow", map[string]any{})
	if !res.IsError {
		t.Fatal("expected tool-error for missing id")
	}
	if !strings.Contains(res.Content[0].Text, "id is required") {
		t.Errorf("message = %q", res.Content[0].Text)
	}
}
