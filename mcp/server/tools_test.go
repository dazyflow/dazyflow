// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

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

	"git.sr.ht/~klahr/dazyflow/mcp/server"
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
	c := server.NewDazydClient(srv.URL, "test-token")
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

// TestTool_HappyPaths drives every simple endpoint-hitting tool through
// the full framing stack, registering the matching fake-daemon handler
// and asserting the request method+path and a marker in the response.
// This covers the bulk of the per-tool handler bodies that the prior
// suite left at the success-path level only.
func TestTool_HappyPaths(t *testing.T) {
	tests := []struct {
		name       string
		tool       string
		args       map[string]any
		method     string
		path       string // decoded r.URL.Path the fake registers on
		respBody   string
		wantInBody string // substring expected in the tool result text
	}{
		{
			name:       "list_integrations",
			tool:       "list_integrations",
			args:       map[string]any{"q": "slack", "category": "io"},
			method:     "GET",
			path:       "/api/v1/catalog/integrations",
			respBody:   `{"items":[{"id":"Slack"}]}`,
			wantInBody: "Slack",
		},
		{
			name:       "describe_integration",
			tool:       "describe_integration",
			args:       map[string]any{"id": "Slack"},
			method:     "GET",
			path:       "/api/v1/catalog/integrations/Slack",
			respBody:   `{"id":"Slack","auth":"oauth"}`,
			wantInBody: "oauth",
		},
		{
			name:       "describe_drop",
			tool:       "describe_drop",
			args:       map[string]any{"id": "http_request"},
			method:     "GET",
			path:       "/api/v1/catalog/drops/http_request",
			respBody:   `{"id":"http_request","ports":{}}`,
			wantInBody: "http_request",
		},
		{
			name:       "describe_trigger_kinds",
			tool:       "describe_trigger_kinds",
			args:       map[string]any{},
			method:     "GET",
			path:       "/api/v1/catalog/trigger-kinds",
			respBody:   `{"kinds":["cron","webhook"]}`,
			wantInBody: "cron",
		},
		{
			name:       "list_connections",
			tool:       "list_connections",
			args:       map[string]any{},
			method:     "GET",
			path:       "/api/v1/me/connections",
			respBody:   `{"providers":[{"name":"slack"}]}`,
			wantInBody: "slack",
		},
		{
			name:       "start_connection",
			tool:       "start_connection",
			args:       map[string]any{"provider": "slack", "account": "work", "return_to": "/x"},
			method:     "POST",
			path:       "/api/v1/me/connections/slack/authorize",
			respBody:   `{"authorize_url":"https://auth"}`,
			wantInBody: "authorize_url",
		},
		{
			name:       "list_secrets",
			tool:       "list_secrets",
			args:       map[string]any{},
			method:     "GET",
			path:       "/api/v1/secrets",
			respBody:   `{"secrets":["A","B"]}`,
			wantInBody: "secrets",
		},
		{
			name:       "set_secret",
			tool:       "set_secret",
			args:       map[string]any{"name": "API_KEY", "value": "xyz"},
			method:     "PUT",
			path:       "/api/v1/secrets/API_KEY",
			respBody:   ``,
			wantInBody: `"saved": true`,
		},
		{
			name:       "delete_secret",
			tool:       "delete_secret",
			args:       map[string]any{"name": "API_KEY"},
			method:     "DELETE",
			path:       "/api/v1/secrets/API_KEY",
			respBody:   ``,
			wantInBody: `"deleted": true`,
		},
		{
			name:       "validate_cron",
			tool:       "validate_cron",
			args:       map[string]any{"expr": "0 9 * * 1"},
			method:     "POST",
			path:       "/api/v1/validate/cron",
			respBody:   `{"ok":true}`,
			wantInBody: `"ok": true`,
		},
		{
			name:       "list_flows",
			tool:       "list_flows",
			args:       map[string]any{},
			method:     "GET",
			path:       "/api/v1/me/flows",
			respBody:   `{"flows":["f1"]}`,
			wantInBody: "f1",
		},
		{
			name:       "get_flow",
			tool:       "get_flow",
			args:       map[string]any{"id": "f1"},
			method:     "GET",
			path:       "/api/v1/me/flows/t/ws/f1",
			respBody:   `{"id":"f1","nodes":[]}`,
			wantInBody: "f1",
		},
		{
			name:       "flow_references",
			tool:       "flow_references",
			args:       map[string]any{"id": "f1"},
			method:     "GET",
			path:       "/api/v1/me/flows/t/ws/f1/references",
			respBody:   `{"tokens":["trigger.body"]}`,
			wantInBody: "trigger.body",
		},
		{
			name:       "enable_flow",
			tool:       "enable_flow",
			args:       map[string]any{"id": "f1"},
			method:     "POST",
			path:       "/api/v1/me/flows/t/ws/f1/enable",
			respBody:   `{"enabled":true}`,
			wantInBody: "enabled",
		},
		{
			name:       "disable_flow",
			tool:       "disable_flow",
			args:       map[string]any{"id": "f1"},
			method:     "POST",
			path:       "/api/v1/me/flows/t/ws/f1/disable",
			respBody:   `{"disabled":true}`,
			wantInBody: "disabled",
		},
		{
			name:       "publish_flow",
			tool:       "publish_flow",
			args:       map[string]any{"id": "f1"},
			method:     "POST",
			path:       "/api/v1/me/flows/t/ws/f1/publish",
			respBody:   `{"published":true}`,
			wantInBody: "published",
		},
		{
			name:       "unpublish_flow",
			tool:       "unpublish_flow",
			args:       map[string]any{"id": "f1"},
			method:     "POST",
			path:       "/api/v1/me/flows/t/ws/f1/unpublish",
			respBody:   `{"unpublished":true}`,
			wantInBody: "unpublished",
		},
		{
			name:       "validate_flow",
			tool:       "validate_flow",
			args:       map[string]any{"id": "f1"},
			method:     "POST",
			path:       "/api/v1/me/flows/t/ws/f1/validate",
			respBody:   `{"ok":true,"issues":[]}`,
			wantInBody: `"ok": true`,
		},
		{
			name:       "patch_flow",
			tool:       "patch_flow",
			args:       map[string]any{"id": "f1", "patch": map[string]any{"name": "new"}},
			method:     "PATCH",
			path:       "/api/v1/me/flows/t/ws/f1",
			respBody:   `{"commit":"c2"}`,
			wantInBody: "c2",
		},
		{
			name:       "delete_flow",
			tool:       "delete_flow",
			args:       map[string]any{"id": "f1"},
			method:     "DELETE",
			path:       "/api/v1/me/flows/t/ws/f1",
			respBody:   ``,
			wantInBody: `"deleted": true`,
		},
		{
			name:       "validate_graph",
			tool:       "validate_graph",
			args:       map[string]any{"graph": map[string]any{"nodes": []any{}}},
			method:     "POST",
			path:       "/api/v1/validate/graph",
			respBody:   `{"ok":true}`,
			wantInBody: `"ok": true`,
		},
		{
			name:       "test_trigger_flow",
			tool:       "test_trigger_flow",
			args:       map[string]any{"id": "f1", "payload": map[string]any{"x": 1}},
			method:     "POST",
			path:       "/api/v1/me/flows/t/ws/f1/test-trigger",
			respBody:   `{"run_id":"r9"}`,
			wantInBody: "r9",
		},
		{
			name:       "sample_node",
			tool:       "sample_node",
			args:       map[string]any{"id": "f1", "node_id": "n1", "inputs": map[string]any{"a": 1}},
			method:     "POST",
			path:       "/api/v1/me/flows/t/ws/f1/nodes/n1/sample",
			respBody:   `{"output":{"ok":1}}`,
			wantInBody: "output",
		},
		{
			name:       "run_flow",
			tool:       "run_flow",
			args:       map[string]any{"id": "f1"},
			method:     "POST",
			path:       "/api/v1/me/flows/t/ws/f1/run",
			respBody:   `{"run_id":"r1"}`,
			wantInBody: "r1",
		},
		{
			name:       "cancel_run",
			tool:       "cancel_run",
			args:       map[string]any{"run_id": "r1", "reason": "stop"},
			method:     "POST",
			path:       "/api/v1/me/runs/r1/cancel",
			respBody:   `{"cancelled":true}`,
			wantInBody: "cancelled",
		},
		{
			name:       "get_run",
			tool:       "get_run",
			args:       map[string]any{"run_id": "r1"},
			method:     "GET",
			path:       "/api/v1/me/runs/r1",
			respBody:   `{"id":"r1","status":"running"}`,
			wantInBody: "running",
		},
		{
			name:       "list_runs_all",
			tool:       "list_runs",
			args:       map[string]any{"limit": 10},
			method:     "GET",
			path:       "/api/v1/me/runs",
			respBody:   `{"runs":[{"id":"r1"}]}`,
			wantInBody: "r1",
		},
		{
			name:       "list_runs_scoped",
			tool:       "list_runs",
			args:       map[string]any{"flow_id": "f1", "status": "failed"},
			method:     "GET",
			path:       "/api/v1/me/flows/t/ws/f1/runs",
			respBody:   `{"runs":[{"id":"r2"}]}`,
			wantInBody: "r2",
		},
		{
			name:       "list_pending_approvals",
			tool:       "list_pending_approvals",
			args:       map[string]any{},
			method:     "GET",
			path:       "/api/v1/approvals/pending",
			respBody:   `{"pending":[{"node":"n1"}]}`,
			wantInBody: "n1",
		},
		{
			name:       "configure_connection",
			tool:       "configure_connection",
			args:       map[string]any{"integration": "Email", "values": map[string]any{"host": "smtp", "port": 587}},
			method:     "PUT",
			path:       "/api/v1/catalog/integrations/email/connection",
			respBody:   ``,
			wantInBody: `"configured": true`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, fake, _ := fullStack(t)
			body := tc.respBody
			fake.on(tc.method, tc.path, func(w http.ResponseWriter, _ *http.Request) {
				if body != "" {
					_, _ = io.WriteString(w, body)
				}
			})
			res := runToolCall(t, s, tc.tool, tc.args)
			if res.IsError {
				t.Fatalf("isError: %s", res.Content[0].Text)
			}
			if !strings.Contains(res.Content[0].Text, tc.wantInBody) {
				t.Errorf("content = %q, want substring %q", res.Content[0].Text, tc.wantInBody)
			}
			if len(fake.requests) != 1 {
				t.Fatalf("requests = %+v", fake.requests)
			}
			if fake.requests[0].method != tc.method {
				t.Errorf("method = %s, want %s", fake.requests[0].method, tc.method)
			}
		})
	}
}

// TestTool_RequiredFieldGuards exercises the in-handler required-field
// checks (no HTTP call should happen) across the tools that validate
// args before hitting the daemon.
func TestTool_RequiredFieldGuards(t *testing.T) {
	tests := []struct {
		name    string
		tool    string
		args    map[string]any
		wantMsg string
	}{
		{"describe_integration", "describe_integration", map[string]any{}, "id is required"},
		{"describe_drop", "describe_drop", map[string]any{}, "id is required"},
		{"start_connection", "start_connection", map[string]any{}, "provider is required"},
		{"set_secret_missing_value", "set_secret", map[string]any{"name": "X"}, "name and value are required"},
		{"delete_secret", "delete_secret", map[string]any{}, "name is required"},
		{"validate_cron", "validate_cron", map[string]any{}, "expr is required"},
		{"cancel_run", "cancel_run", map[string]any{}, "run_id is required"},
		{"get_run", "get_run", map[string]any{}, "run_id is required"},
		{"wait_for_run", "wait_for_run", map[string]any{}, "run_id is required"},
		{"approve_node", "approve_node", map[string]any{"run_id": "r"}, "run_id, node_id, and decision are required"},
		{"generate_flow", "generate_flow", map[string]any{}, "description is required"},
		{"configure_connection_missing_int", "configure_connection", map[string]any{"values": map[string]any{"a": "b"}}, "integration is required"},
		{"configure_connection_empty_values", "configure_connection", map[string]any{"integration": "Email"}, "values must be a non-empty object"},
		{"validate_graph_no_graph", "validate_graph", map[string]any{}, "graph must be a JSON object"},
		{"patch_flow_no_patch", "patch_flow", map[string]any{"id": "f1"}, "patch must be a JSON object"},
		{"save_flow_missing_id", "create_flow", map[string]any{"nodes": []any{}}, "id is required"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, fake, _ := fullStack(t)
			res := runToolCall(t, s, tc.tool, tc.args)
			if !res.IsError {
				t.Fatalf("expected tool-error, got success: %s", res.Content[0].Text)
			}
			if !strings.Contains(res.Content[0].Text, tc.wantMsg) {
				t.Errorf("message = %q, want substring %q", res.Content[0].Text, tc.wantMsg)
			}
			if len(fake.requests) != 0 {
				t.Errorf("expected no HTTP call, got %+v", fake.requests)
			}
		})
	}
}

// TestTool_ErrorPaths confirms 4xx responses from the daemon become
// tool-error results (not RPC errors) across a representative spread of
// tools that route through errorResultOrErr.
func TestTool_ErrorPaths(t *testing.T) {
	tests := []struct {
		name   string
		tool   string
		args   map[string]any
		method string
		path   string
	}{
		{"list_integrations_500", "list_integrations", map[string]any{}, "GET", "/api/v1/catalog/integrations"},
		{"describe_drop_404", "describe_drop", map[string]any{"id": "nope"}, "GET", "/api/v1/catalog/drops/nope"},
		{"validate_cron_422", "validate_cron", map[string]any{"expr": "bad"}, "POST", "/api/v1/validate/cron"},
		{"get_run_404", "get_run", map[string]any{"run_id": "r1"}, "GET", "/api/v1/me/runs/r1"},
		{"cancel_run_409", "cancel_run", map[string]any{"run_id": "r1"}, "POST", "/api/v1/me/runs/r1/cancel"},
		{"approve_node_404", "approve_node", map[string]any{"run_id": "r1", "node_id": "n1", "decision": "approve"}, "POST", "/api/v1/approvals/r1/n1"},
		{"start_connection_501", "start_connection", map[string]any{"provider": "slack"}, "POST", "/api/v1/me/connections/slack/authorize"},
		{"configure_connection_400", "configure_connection", map[string]any{"integration": "Email", "values": map[string]any{"a": "b"}}, "PUT", "/api/v1/catalog/integrations/email/connection"},
		{"validate_graph_422", "validate_graph", map[string]any{"graph": map[string]any{}}, "POST", "/api/v1/validate/graph"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, fake, _ := fullStack(t)
			fake.on(tc.method, tc.path, func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "boom", http.StatusBadRequest)
			})
			res := runToolCall(t, s, tc.tool, tc.args)
			if !res.IsError {
				t.Fatalf("expected tool-error, got success: %s", res.Content[0].Text)
			}
			if !strings.Contains(res.Content[0].Text, "boom") {
				t.Errorf("error text lost: %q", res.Content[0].Text)
			}
		})
	}
}

// TestTool_GenerateFlow_NeedConnect verifies the in-band {error} shape
// the generate endpoint returns when no AI provider is connected gets
// surfaced as a tool error even on an HTTP 200.
func TestTool_GenerateFlow_NeedConnect(t *testing.T) {
	s, fake, _ := fullStack(t)
	fake.on("POST", "/api/v1/tools/flow/generate", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"error":"no AI provider connected","need_connect":true}`)
	})
	res := runToolCall(t, s, "generate_flow", map[string]any{"description": "do a thing", "provider": "claude", "tz": "UTC"})
	if !res.IsError {
		t.Fatalf("expected tool-error, got success: %s", res.Content[0].Text)
	}
	if !strings.Contains(res.Content[0].Text, "no AI provider connected") {
		t.Errorf("message = %q", res.Content[0].Text)
	}
}

// TestTool_GenerateFlow_Success covers the success epilogue of the
// generator tool.
func TestTool_GenerateFlow_Success(t *testing.T) {
	s, fake, _ := fullStack(t)
	fake.on("POST", "/api/v1/tools/flow/generate", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"graph":{"nodes":[]},"issues":[]}`)
	})
	res := runToolCall(t, s, "generate_flow", map[string]any{"description": "do a thing"})
	if res.IsError {
		t.Fatalf("isError: %s", res.Content[0].Text)
	}
	if !strings.Contains(res.Content[0].Text, "graph") {
		t.Errorf("content = %q", res.Content[0].Text)
	}
}

// TestTool_SaveFlow_NextStepHint verifies the draft/publish hint is
// appended when the save response advertises trigger endpoints.
func TestTool_SaveFlow_NextStepHint(t *testing.T) {
	s, fake, _ := fullStack(t)
	fake.on("PUT", "/api/v1/me/flows/t/ws/f1", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"commit":"c1","endpoints":["https://hook"]}`)
	})
	res := runToolCall(t, s, "create_flow", map[string]any{"id": "f1", "nodes": []any{}})
	if res.IsError {
		t.Fatalf("isError: %s", res.Content[0].Text)
	}
	if !strings.Contains(res.Content[0].Text, "next_step") {
		t.Errorf("expected publish hint, got %q", res.Content[0].Text)
	}
}

// TestTool_WaitForRun_TimesOut covers the deadline branch: the run
// never reaches terminal within the budget, so the last snapshot is
// returned with wait_timed_out set.
func TestTool_WaitForRun_TimesOut(t *testing.T) {
	s, fake, _ := fullStack(t)
	fake.on("GET", "/api/v1/me/runs/r1", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"id":"r1","status":"running"}`)
	})
	res := runToolCall(t, s, "wait_for_run", map[string]any{"run_id": "r1", "timeout_seconds": 1})
	if res.IsError {
		t.Fatalf("isError: %s", res.Content[0].Text)
	}
	if !strings.Contains(res.Content[0].Text, "wait_timed_out") {
		t.Errorf("expected wait_timed_out, got %q", res.Content[0].Text)
	}
}

// TestTool_WaitForRun_ClampsTimeout exercises the clamp branches for an
// out-of-range timeout, then returns immediately on a terminal run.
func TestTool_WaitForRun_ClampsTimeout(t *testing.T) {
	s, fake, _ := fullStack(t)
	fake.on("GET", "/api/v1/me/runs/r1", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"id":"r1","status":"failed"}`)
	})
	res := runToolCall(t, s, "wait_for_run", map[string]any{"run_id": "r1", "timeout_seconds": 9999})
	if res.IsError {
		t.Fatalf("isError: %s", res.Content[0].Text)
	}
	if !strings.Contains(res.Content[0].Text, "failed") {
		t.Errorf("content = %q", res.Content[0].Text)
	}
}

// TestTool_WaitForRun_TerminalViaCapitalStatus covers isTerminal's
// belt-and-braces capital "Status" branch.
func TestTool_WaitForRun_TerminalViaCapitalStatus(t *testing.T) {
	s, fake, _ := fullStack(t)
	fake.on("GET", "/api/v1/me/runs/r1", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"id":"r1","Status":"succeeded"}`)
	})
	res := runToolCall(t, s, "wait_for_run", map[string]any{"run_id": "r1", "timeout_seconds": 5})
	if res.IsError {
		t.Fatalf("isError: %s", res.Content[0].Text)
	}
}

// TestTool_WaitForRun_HTTPError covers the error branch inside the poll
// loop.
func TestTool_WaitForRun_HTTPError(t *testing.T) {
	s, fake, _ := fullStack(t)
	fake.on("GET", "/api/v1/me/runs/r1", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "gone", http.StatusNotFound)
	})
	res := runToolCall(t, s, "wait_for_run", map[string]any{"run_id": "r1", "timeout_seconds": 5})
	if !res.IsError {
		t.Fatalf("expected tool-error, got success: %s", res.Content[0].Text)
	}
}

// TestTool_DecodeArgsError covers the malformed-arguments branch shared
// by handlers: a non-object JSON value fails to decode into the args
// map.
func TestTool_DecodeArgsError(t *testing.T) {
	s, _, _ := fullStack(t)
	// runToolCall marshals args as-is; a JSON array is valid JSON but
	// won't unmarshal into map[string]any, hitting decodeArgs's error.
	// describe_drop decodes its args, so it surfaces the failure.
	res := runToolCall(t, s, "describe_drop", []any{1, 2, 3})
	if !res.IsError {
		t.Fatalf("expected tool-error, got success: %s", res.Content[0].Text)
	}
	if !strings.Contains(res.Content[0].Text, "decode arguments") {
		t.Errorf("message = %q", res.Content[0].Text)
	}
}
