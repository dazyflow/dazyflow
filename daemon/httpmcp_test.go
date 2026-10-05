// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/dazyflow/dazyflow/mcp/server"
)

func (h *gatewayHarness) mcp(t *testing.T, token, body string, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rw := httptest.NewRecorder()
	ServeForTest(h.gw, rw, req)
	return rw
}

type mcpReply struct {
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func decodeMCP(t *testing.T, rw *httptest.ResponseRecorder) mcpReply {
	t.Helper()
	if rw.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rw.Code, rw.Body)
	}
	var r mcpReply
	if err := json.Unmarshal(rw.Body.Bytes(), &r); err != nil {
		t.Fatalf("decode: %v; body = %s", err, rw.Body)
	}
	if r.Error != nil {
		t.Fatalf("rpc error %d: %s", r.Error.Code, r.Error.Message)
	}
	return r
}

func callTool(t *testing.T, h *gatewayHarness, name string, args any) (text string, isError bool) {
	t.Helper()
	params, _ := json.Marshal(map[string]any{"name": name, "arguments": args})
	r := decodeMCP(t, h.mcp(t, h.token, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":`+string(params)+`}`, nil))
	var res struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(r.Result, &res); err != nil || len(res.Content) == 0 {
		t.Fatalf("decode tool result: %v; %s", err, r.Result)
	}
	return res.Content[0].Text, res.IsError
}

func TestMCPEndpoint_InitializeNegotiatesVersion(t *testing.T) {
	t.Parallel()
	h := newGatewayHarness(t)
	r := decodeMCP(t, h.mcp(t, h.token,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"c","version":"1"}}}`, nil))
	var init struct {
		ProtocolVersion string `json:"protocolVersion"`
		ServerInfo      struct {
			Name string `json:"name"`
		} `json:"serverInfo"`
	}
	if err := json.Unmarshal(r.Result, &init); err != nil {
		t.Fatal(err)
	}
	if init.ProtocolVersion != "2025-06-18" || init.ServerInfo.Name != "dazyflow" {
		t.Errorf("initialize = %+v", init)
	}
}

func TestMCPEndpoint_UnauthenticatedGetsResourceMetadataChallenge(t *testing.T) {
	t.Parallel()
	h := newGatewayHarness(t)
	for _, tok := range []string{"", "dz_not_a_real_key"} {
		rw := h.mcp(t, tok, `{"jsonrpc":"2.0","id":1,"method":"ping"}`, nil)
		if rw.Code != http.StatusUnauthorized {
			t.Fatalf("token %q: status = %d", tok, rw.Code)
		}
		if c := rw.Header().Get("WWW-Authenticate"); !strings.Contains(c, "/.well-known/oauth-protected-resource/mcp") {
			t.Errorf("token %q: WWW-Authenticate = %q", tok, c)
		}
	}
}

func TestMCPEndpoint_RefusesSessionCookieAlone(t *testing.T) {
	t.Parallel()
	h := newGatewayHarness(t)
	rw := h.mcp(t, "", `{"jsonrpc":"2.0","id":1,"method":"ping"}`, map[string]string{"Cookie": sessionCookieName + "=" + h.token})
	if rw.Code != http.StatusUnauthorized && rw.Code != http.StatusForbidden {
		t.Fatalf("status = %d", rw.Code)
	}
}

func TestMCPEndpoint_NotificationIsAccepted(t *testing.T) {
	t.Parallel()
	h := newGatewayHarness(t)
	rw := h.mcp(t, h.token, `{"jsonrpc":"2.0","method":"notifications/initialized"}`, nil)
	if rw.Code != http.StatusAccepted || rw.Body.Len() != 0 {
		t.Fatalf("status = %d, body = %q", rw.Code, rw.Body)
	}
}

func TestMCPEndpoint_RejectsForeignOriginAndUnknownVersion(t *testing.T) {
	t.Parallel()
	h := newGatewayHarness(t)
	ping := `{"jsonrpc":"2.0","id":1,"method":"ping"}`
	if rw := h.mcp(t, h.token, ping, map[string]string{"Origin": "https://evil.example"}); rw.Code != http.StatusForbidden {
		t.Errorf("foreign origin: status = %d", rw.Code)
	}
	if rw := h.mcp(t, h.token, ping, map[string]string{"MCP-Protocol-Version": "1999-01-01"}); rw.Code != http.StatusBadRequest {
		t.Errorf("unknown version: status = %d", rw.Code)
	}
}

// The tools reach the gateway's own routes in-process with the caller's
// credential: a flow created over /mcp is the caller's flow on the REST API.
func TestMCPEndpoint_ToolsRunAgainstTheGatewayInProcess(t *testing.T) {
	t.Parallel()
	h := newGatewayHarness(t)
	text, isErr := callTool(t, h, "create_flow", map[string]any{
		"id":    "from-mcp",
		"nodes": []map[string]any{{"id": "a", "module": "noop"}},
	})
	if isErr {
		t.Fatalf("create_flow: %s", text)
	}
	if rw := h.do(t, http.MethodGet, "/api/v1/me/flows/t%2Fws%2Ffrom-mcp", nil); rw.Code != http.StatusOK {
		t.Fatalf("GET flow: status = %d, body = %s", rw.Code, rw.Body)
	}
	text, isErr = callTool(t, h, "list_flows", map[string]any{})
	if isErr || !strings.Contains(text, "from-mcp") {
		t.Fatalf("list_flows: isError=%v %s", isErr, text)
	}
}

// The server never hands a handler a nil Body, and handlers rely on it; a
// body-less client request (a GET, a DELETE) has one.
func TestInProcessTransport_BodylessRequestGetsNoBody(t *testing.T) {
	t.Parallel()
	var sawNil bool
	tr := inProcessTransport{
		handler: http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) { sawNil = r.Body == nil }),
		outer:   httptest.NewRequest(http.MethodPost, "/mcp", nil),
	}
	req, _ := http.NewRequest(http.MethodDelete, "http://example.com/api/v1/me/flows/x", nil)
	if _, err := tr.RoundTrip(req); err != nil {
		t.Fatal(err)
	}
	if sawNil {
		t.Error("handler saw a nil Body")
	}
}

// The worked example in get_authoring_guide is what the model copies; it must
// apply cleanly to the real catalog, through the real tool.
func TestMCPEndpoint_AuthoringGuideExampleBuildsAFlow(t *testing.T) {
	t.Parallel()
	h := newGatewayHarness(t)
	if text, isErr := callTool(t, h, "create_flow", map[string]any{"id": "guide", "nodes": []map[string]any{}}); isErr {
		t.Fatalf("create_flow: %s", text)
	}
	var ops []map[string]any
	if err := json.Unmarshal(server.GuideExampleOps(), &ops); err != nil {
		t.Fatalf("guide example is not JSON: %v", err)
	}
	text, isErr := callTool(t, h, "edit_flow", map[string]any{"id": "guide", "ops": ops, "note": "From the guide"})
	if isErr {
		t.Fatalf("guide example: %s", text)
	}
	for _, want := range []string{`"contact: form_input → tell_team"`, `"etag"`, `"undo_ref"`} {
		if !strings.Contains(text, want) {
			t.Errorf("reply lacks %s: %s", want, text)
		}
	}
}

// Every snake_case word in the instructions names a real tool, or is one of
// the reply fields they point at — a renamed tool must not leave the model
// calling one that is gone.
func TestMCPEndpoint_InstructionsNameRealTools(t *testing.T) {
	t.Parallel()
	h := newGatewayHarness(t)
	r := decodeMCP(t, h.mcp(t, h.token, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`, nil))
	var init struct {
		Instructions string `json:"instructions"`
	}
	_ = json.Unmarshal(r.Result, &init)
	if init.Instructions != server.Instructions {
		t.Fatal("initialize does not carry the instructions")
	}
	tools := map[string]bool{"canvas_url": true, "undo_ref": true}
	for _, tl := range server.BuildTools(nil, server.Defaults{}) {
		tools[tl.Name] = true
	}
	for _, w := range regexp.MustCompile(`\b[a-z]+(?:_[a-z]+)+\b`).FindAllString(init.Instructions, -1) {
		if !tools[w] {
			t.Errorf("instructions mention %q, which is not a tool", w)
		}
	}
}
