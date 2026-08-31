// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package gemini

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dazyflow/dazyflow/drops/internal/dropstest"
	"github.com/dazyflow/dazyflow/drops/internal/llmtask"
	"github.com/dazyflow/dazyflow/internal/llm"
)

func TestMain(m *testing.M) { dropstest.EgressTestMain(m) }

// capture serves one canned reply and records the request the provider made.
func capture(t *testing.T, status int, reply any) (*httptest.Server, *http.Request, *map[string]any) {
	t.Helper()
	var gotReq http.Request
	gotBody := map[string]any{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		gotReq = *r
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(reply)
	}))
	t.Cleanup(srv.Close)
	return srv, &gotReq, &gotBody
}

func textReply(text string) map[string]any {
	return map[string]any{"candidates": []any{map[string]any{
		"content": map[string]any{"role": "model", "parts": []any{map[string]any{"text": text}}},
	}}}
}

func TestCall_TextResponse(t *testing.T) {
	srv, req, body := capture(t, 200, textReply("Hello!"))

	res, jerr := provider{}.Call(context.Background(), "AIza-test", llmtask.Request{
		Model: "gemini-2.5-flash", System: "be brief", UserText: "Say hi", BaseURL: srv.URL,
	})
	if jerr != nil {
		t.Fatalf("err: %+v", jerr)
	}
	if res.Text != "Hello!" {
		t.Errorf("text = %q", res.Text)
	}
	// The key travels as a header, never in the URL — a URL lands in proxy logs.
	if got := req.Header.Get("x-goog-api-key"); got != "AIza-test" {
		t.Errorf("api key header = %q", got)
	}
	if strings.Contains(req.URL.RawQuery, "AIza") {
		t.Errorf("api key leaked into the query: %q", req.URL.RawQuery)
	}
	// The model is a path segment, not a body field.
	if want := "/v1beta/models/gemini-2.5-flash:generateContent"; req.URL.Path != want {
		t.Errorf("path = %q, want %q", req.URL.Path, want)
	}
	// System is its own instruction, not a turn.
	sys := (*body)["systemInstruction"].(map[string]any)
	if got := sys["parts"].([]any)[0].(map[string]any)["text"]; got != "be brief" {
		t.Errorf("systemInstruction = %+v", sys)
	}
	turns := (*body)["contents"].([]any)
	if len(turns) != 1 {
		t.Fatalf("contents = %+v", turns)
	}
	turn := turns[0].(map[string]any)
	if turn["role"] != "user" || turn["parts"].([]any)[0].(map[string]any)["text"] != "Say hi" {
		t.Errorf("turn = %+v", turn)
	}
}

// An empty model falls back to the package default rather than requesting
// "/models/:generateContent", which is a 404 with a confusing message.
func TestCall_DefaultsModel(t *testing.T) {
	srv, req, _ := capture(t, 200, textReply("ok"))
	if _, jerr := (provider{}).Call(context.Background(), "k", llmtask.Request{
		UserText: "hi", BaseURL: srv.URL,
	}); jerr != nil {
		t.Fatalf("err: %+v", jerr)
	}
	if !strings.Contains(req.URL.Path, defaultModel) {
		t.Errorf("path = %q, want the default model %q", req.URL.Path, defaultModel)
	}
}

// llmtask hands over OpenAI-shaped Messages. Gemini has no system turn and
// calls the assistant "model", so both have to be translated — and a system
// message must be hoisted rather than dropped.
func TestCall_MessagesTranslated(t *testing.T) {
	srv, _, body := capture(t, 200, textReply("ok"))

	_, jerr := provider{}.Call(context.Background(), "k", llmtask.Request{
		BaseURL: srv.URL,
		Messages: []any{
			map[string]any{"role": "system", "content": "you are terse"},
			map[string]any{"role": "user", "content": "first"},
			map[string]any{"role": "assistant", "content": "second"},
			map[string]any{"role": "user", "content": "third"},
		},
	})
	if jerr != nil {
		t.Fatalf("err: %+v", jerr)
	}
	sys := (*body)["systemInstruction"].(map[string]any)
	if got := sys["parts"].([]any)[0].(map[string]any)["text"]; got != "you are terse" {
		t.Errorf("system message was not hoisted: %+v", sys)
	}
	turns := (*body)["contents"].([]any)
	if len(turns) != 3 {
		t.Fatalf("want 3 turns (the system one hoisted), got %+v", turns)
	}
	roles := make([]string, len(turns))
	for i, tn := range turns {
		roles[i] = tn.(map[string]any)["role"].(string)
	}
	if strings.Join(roles, ",") != "user,model,user" {
		t.Errorf("roles = %v, want user,model,user", roles)
	}
}

// Every turn being a system message must still produce a request Gemini
// accepts: an empty contents list is rejected outright.
func TestCall_AllSystemMessagesStillSendsATurn(t *testing.T) {
	srv, _, body := capture(t, 200, textReply("ok"))
	if _, jerr := (provider{}).Call(context.Background(), "k", llmtask.Request{
		BaseURL: srv.URL, UserText: "fallback",
		Messages: []any{map[string]any{"role": "system", "content": "only this"}},
	}); jerr != nil {
		t.Fatalf("err: %+v", jerr)
	}
	turns := (*body)["contents"].([]any)
	if len(turns) != 1 {
		t.Fatalf("contents = %+v", turns)
	}
	if got := turns[0].(map[string]any)["parts"].([]any)[0].(map[string]any)["text"]; got != "fallback" {
		t.Errorf("turn text = %q, want the UserText fallback", got)
	}
}

func TestCall_ToolCallArgs(t *testing.T) {
	srv, _, body := capture(t, 200, map[string]any{"candidates": []any{map[string]any{
		"content": map[string]any{"parts": []any{map[string]any{
			"functionCall": map[string]any{"name": "extract", "args": map[string]any{"vendor": "Acme", "amount": 42}},
		}}},
	}}})

	res, jerr := provider{}.Call(context.Background(), "k", llmtask.Request{
		UserText: "x", BaseURL: srv.URL,
		Tool: &llmtask.Tool{Name: "extract", Description: "d", Schema: map[string]any{"type": "object"}},
	})
	if jerr != nil {
		t.Fatalf("err: %+v", jerr)
	}
	if res.Tool["vendor"] != "Acme" || res.Tool["amount"] != 42.0 {
		t.Errorf("tool = %+v", res.Tool)
	}
	// A forced tool is functionDeclarations + a toolConfig in ANY mode.
	decls := (*body)["tools"].([]any)[0].(map[string]any)["functionDeclarations"].([]any)
	if decls[0].(map[string]any)["name"] != "extract" {
		t.Errorf("functionDeclarations = %+v", decls)
	}
	cfg := (*body)["toolConfig"].(map[string]any)["functionCallingConfig"].(map[string]any)
	if cfg["mode"] != "ANY" || cfg["allowedFunctionNames"].([]any)[0] != "extract" {
		t.Errorf("toolConfig = %+v", cfg)
	}
}

// A functionCall for a different function must not fill the step's fields from
// the wrong schema.
func TestCall_IgnoresMismatchedToolName(t *testing.T) {
	srv, _, _ := capture(t, 200, map[string]any{"candidates": []any{map[string]any{
		"content": map[string]any{"parts": []any{map[string]any{
			"functionCall": map[string]any{"name": "somethingElse", "args": map[string]any{"x": 1}},
		}}},
	}}})

	_, jerr := provider{}.Call(context.Background(), "k", llmtask.Request{
		UserText: "x", BaseURL: srv.URL,
		Tool: &llmtask.Tool{Name: "extract", Schema: map[string]any{"type": "object"}},
	})
	if jerr == nil || jerr.Code != "gemini_no_tool_call" {
		t.Fatalf("want gemini_no_tool_call, got %+v", jerr)
	}
}

// A truncated reply is the common cause of a missing tool call, and the knob
// that fixes it has to be in the message.
func TestCall_NoToolCallNamesTheFinishReason(t *testing.T) {
	srv, _, _ := capture(t, 200, map[string]any{"candidates": []any{map[string]any{
		"content":      map[string]any{"parts": []any{}},
		"finishReason": "MAX_TOKENS",
	}}})

	_, jerr := provider{}.Call(context.Background(), "k", llmtask.Request{
		UserText: "x", BaseURL: srv.URL,
		Tool: &llmtask.Tool{Name: "extract", Schema: map[string]any{"type": "object"}},
	})
	if jerr == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(jerr.Message, "max_tokens") {
		t.Errorf("message does not name the knob: %q", jerr.Message)
	}
}

// Prose split across parts is joined, not truncated to the first one.
func TestCall_JoinsTextParts(t *testing.T) {
	srv, _, _ := capture(t, 200, map[string]any{"candidates": []any{map[string]any{
		"content": map[string]any{"parts": []any{
			map[string]any{"text": "one "},
			map[string]any{"text": "two"},
		}},
	}}})
	res, jerr := provider{}.Call(context.Background(), "k", llmtask.Request{UserText: "x", BaseURL: srv.URL})
	if jerr != nil {
		t.Fatalf("err: %+v", jerr)
	}
	if res.Text != "one two" {
		t.Errorf("text = %q", res.Text)
	}
}

func TestCall_RateLimited(t *testing.T) {
	srv, _, _ := capture(t, 429, map[string]any{"error": map[string]any{
		"status": "RESOURCE_EXHAUSTED", "message": "quota exceeded",
	}})
	_, jerr := provider{}.Call(context.Background(), "k", llmtask.Request{UserText: "x", BaseURL: srv.URL})
	if jerr == nil || jerr.Code != "gemini_rate_limited" {
		t.Fatalf("want gemini_rate_limited, got %+v", jerr)
	}
	if !strings.Contains(jerr.Message, "quota exceeded") {
		t.Errorf("vendor detail dropped: %q", jerr.Message)
	}
}

// Gemini answers a bad key with 400 as readily as 401, so verifyKey has to
// read both as "your key is wrong" — that is the only thing a keys-only
// request can have got wrong.
func TestVerifyKey(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  int
		wantErr string
	}{
		{"valid", 200, ""},
		{"bad key 400", 400, "rejected the API key"},
		{"bad key 403", 403, "rejected the API key"},
		{"upstream", 500, "HTTP 500"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, req, _ := capture(t, tc.status, map[string]any{"models": []any{}})
			err := verifyKey(context.Background(), "AIza-x", srv.URL)
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("want ok, got %v", err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Fatalf("want %q, got %v", tc.wantErr, err)
			}
			if got := req.URL.Path; got != "/v1beta/models" {
				t.Errorf("verify path = %q", got)
			}
		})
	}
}

// The provider has to be in the shared registry, not just the drop catalog:
// the flow generator and the editor's AI assists read it from there.
func TestRegisteredInSharedRegistry(t *testing.T) {
	info, ok := llm.Get("gemini")
	if !ok {
		t.Fatal("gemini is not registered with internal/llm")
	}
	if info.Integration != "Gemini" || info.DefaultModel != defaultModel {
		t.Errorf("info = %+v", info)
	}
	if len(info.Models) == 0 {
		t.Error("no models exposed to the picker")
	}
}
