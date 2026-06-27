// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package server_test

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

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
