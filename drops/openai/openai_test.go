// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package openai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/dazyflow/dazyflow/drops/internal/llmtask"
	hfnet "github.com/dazyflow/dazyflow/drops/net"
	"github.com/dazyflow/dazyflow/internal/llm"
)

func TestMain(m *testing.M) {
	hfnet.SetAllowPrivateEgress(true)
	os.Exit(m.Run())
}

func TestCall_TextResponse(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		if r.Header.Get("authorization") != "Bearer sk-test" {
			t.Errorf("bad auth header: %q", r.Header.Get("authorization"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"content": "Hello!"}}},
		})
	}))
	defer srv.Close()

	res, jerr := provider{}.Call(context.Background(), "sk-test", llmtask.Request{
		System: "be brief", UserText: "Say hi", BaseURL: srv.URL,
	})
	if jerr != nil {
		t.Fatalf("err: %+v", jerr)
	}
	if res.Text != "Hello!" {
		t.Errorf("text = %q", res.Text)
	}
	// System becomes the first chat message; user second.
	msgs := gotBody["messages"].([]any)
	if msgs[0].(map[string]any)["role"] != "system" || msgs[1].(map[string]any)["content"] != "Say hi" {
		t.Errorf("messages = %+v", msgs)
	}
}

func TestCall_ToolCallArgs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{
				"tool_calls": []any{map[string]any{
					"function": map[string]any{"name": "extract", "arguments": `{"vendor":"Acme","amount":42}`},
				}},
			}}},
		})
	}))
	defer srv.Close()

	res, jerr := provider{}.Call(context.Background(), "k", llmtask.Request{
		UserText: "x", BaseURL: srv.URL,
		Tool: &llmtask.Tool{Name: "extract", Schema: map[string]any{"type": "object"}},
	})
	if jerr != nil {
		t.Fatalf("err: %+v", jerr)
	}
	if res.Tool["vendor"] != "Acme" || res.Tool["amount"] != 42.0 {
		t.Errorf("tool = %+v", res.Tool)
	}
}

func TestCall_RateLimited(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(429)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"type": "rate_limit", "message": "slow"}})
	}))
	defer srv.Close()
	_, jerr := provider{}.Call(context.Background(), "k", llmtask.Request{UserText: "x", BaseURL: srv.URL})
	if jerr == nil || jerr.Code != "openai_rate_limited" {
		t.Fatalf("jerr = %+v", jerr)
	}
}

func TestCall_SSRFGuardBlocksPrivate(t *testing.T) {
	hfnet.SetAllowPrivateEgress(false)
	defer hfnet.SetAllowPrivateEgress(true)
	_, jerr := provider{}.Call(context.Background(), "k", llmtask.Request{UserText: "x", BaseURL: "http://127.0.0.1:9"})
	if jerr == nil || !strings.Contains(jerr.Message, "ssrf_blocked") {
		t.Fatalf("want ssrf_blocked, got %+v", jerr)
	}
}

// TestCall_MessagesPassthrough exercises the branch where req.Messages is
// supplied directly (overriding System/UserText) and Temperature/Model are
// forwarded into the request body.
func TestCall_MessagesPassthrough(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"content": "ok"}}},
		})
	}))
	defer srv.Close()

	temp := 0.25
	_, jerr := provider{}.Call(context.Background(), "k", llmtask.Request{
		Model:       "gpt-4o",
		Temperature: &temp,
		MaxTokens:   77,
		Messages:    []any{map[string]any{"role": "user", "content": "pre-built"}},
		BaseURL:     srv.URL,
	})
	if jerr != nil {
		t.Fatalf("err: %+v", jerr)
	}
	if gotBody["model"] != "gpt-4o" {
		t.Errorf("model = %v", gotBody["model"])
	}
	if gotBody["temperature"] != 0.25 {
		t.Errorf("temperature = %v", gotBody["temperature"])
	}
	if gotBody["max_tokens"] != 77.0 {
		t.Errorf("max_tokens = %v", gotBody["max_tokens"])
	}
	msgs := gotBody["messages"].([]any)
	if len(msgs) != 1 || msgs[0].(map[string]any)["content"] != "pre-built" {
		t.Errorf("messages = %+v", msgs)
	}
}

// TestCall_DefaultsApplied checks the empty-model / non-positive-maxTokens
// defaulting branches and that an empty BaseURL falls through to defaultBase
// (verified indirectly via the SSRF guard rejecting the public host dial when
// private egress is disabled).
func TestCall_ServerErrorClassified(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(500)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"type": "server_error", "message": "boom"}})
	}))
	defer srv.Close()
	_, jerr := provider{}.Call(context.Background(), "k", llmtask.Request{UserText: "x", BaseURL: srv.URL})
	if jerr == nil || jerr.Code != "openai_upstream" {
		t.Fatalf("jerr = %+v", jerr)
	}
}

func TestVerifyKey_OK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if r.Header.Get("authorization") != "Bearer sk-good" {
			t.Errorf("auth = %q", r.Header.Get("authorization"))
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()
	if err := verifyKey(context.Background(), "sk-good", srv.URL); err != nil {
		t.Fatalf("verifyKey = %v", err)
	}
}

func TestVerifyKey_Rejected(t *testing.T) {
	for _, status := range []int{401, 403} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
		}))
		err := verifyKey(context.Background(), "bad", srv.URL)
		srv.Close()
		if err == nil || !strings.Contains(err.Error(), "rejected the API key") {
			t.Fatalf("status %d: err = %v", status, err)
		}
	}
}

func TestVerifyKey_OtherHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(500)
		_, _ = w.Write([]byte(`{"error":{"message":"down"}}`))
	}))
	defer srv.Close()
	err := verifyKey(context.Background(), "k", srv.URL)
	if err == nil || !strings.Contains(err.Error(), "HTTP 500") || !strings.Contains(err.Error(), "down") {
		t.Fatalf("err = %v", err)
	}
}

func TestVerifyKey_Unreachable(t *testing.T) {
	hfnet.SetAllowPrivateEgress(false)
	defer hfnet.SetAllowPrivateEgress(true)
	err := verifyKey(context.Background(), "k", "http://127.0.0.1:9")
	if err == nil || !strings.Contains(err.Error(), "could not reach ChatGPT") {
		t.Fatalf("err = %v", err)
	}
}

// TestExtractText_EdgeCases drives the nil/wrong-type branches of message and
// extractText through a real Call so the parser is exercised end to end.
func TestExtractText_EdgeCases(t *testing.T) {
	cases := []struct {
		name string
		resp map[string]any
		want string
	}{
		{"no choices", map[string]any{}, ""},
		{"empty choices", map[string]any{"choices": []any{}}, ""},
		{"choice not map", map[string]any{"choices": []any{"nope"}}, ""},
		{"no message", map[string]any{"choices": []any{map[string]any{}}}, ""},
		{"content not string", map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": 5}}}}, ""},
		{"content string", map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": "hi"}}}}, "hi"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_ = json.NewEncoder(w).Encode(c.resp)
			}))
			defer srv.Close()
			res, jerr := provider{}.Call(context.Background(), "k", llmtask.Request{UserText: "x", BaseURL: srv.URL})
			if jerr != nil {
				t.Fatalf("err: %+v", jerr)
			}
			if res.Text != c.want {
				t.Errorf("text = %q, want %q", res.Text, c.want)
			}
		})
	}
}

// TestExtractToolArgs_EdgeCases drives every nil-return branch of
// extractToolArgs. Each calls with a forced Tool so extractToolArgs runs.
func TestExtractToolArgs_EdgeCases(t *testing.T) {
	tc := func(message map[string]any) map[string]any {
		return map[string]any{"choices": []any{map[string]any{"message": message}}}
	}
	cases := []struct {
		name   string
		resp   map[string]any
		wantOK bool
	}{
		{"no tool_calls", tc(map[string]any{}), false},
		{"empty tool_calls", tc(map[string]any{"tool_calls": []any{}}), false},
		{"call not map", tc(map[string]any{"tool_calls": []any{"x"}}), false},
		{"no function", tc(map[string]any{"tool_calls": []any{map[string]any{}}}), false},
		{"function not map", tc(map[string]any{"tool_calls": []any{map[string]any{"function": "x"}}}), false},
		{"empty arguments", tc(map[string]any{"tool_calls": []any{map[string]any{"function": map[string]any{"arguments": ""}}}}), false},
		{"missing arguments", tc(map[string]any{"tool_calls": []any{map[string]any{"function": map[string]any{}}}}), false},
		{"bad json arguments", tc(map[string]any{"tool_calls": []any{map[string]any{"function": map[string]any{"arguments": "{not json"}}}}), false},
		{"no message at all", map[string]any{}, false},
		{"good", tc(map[string]any{"tool_calls": []any{map[string]any{"function": map[string]any{"arguments": `{"a":1}`}}}}), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_ = json.NewEncoder(w).Encode(c.resp)
			}))
			defer srv.Close()
			res, jerr := provider{}.Call(context.Background(), "k", llmtask.Request{
				UserText: "x", BaseURL: srv.URL,
				Tool: &llmtask.Tool{Name: "extract", Schema: map[string]any{"type": "object"}},
			})
			if jerr != nil {
				t.Fatalf("err: %+v", jerr)
			}
			if c.wantOK {
				if res.Tool == nil || res.Tool["a"] != 1.0 {
					t.Errorf("tool = %+v", res.Tool)
				}
			} else if res.Tool != nil {
				t.Errorf("expected nil tool, got %+v", res.Tool)
			}
		})
	}
}

// TestOpenaiError_PlainBodyFallback checks the branch where the body is not a
// structured {error:{message}} object: openaiError returns the raw body.
func TestOpenaiError_PlainBodyFallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(400)
		_, _ = w.Write([]byte("plain text failure"))
	}))
	defer srv.Close()
	_, jerr := provider{}.Call(context.Background(), "k", llmtask.Request{UserText: "x", BaseURL: srv.URL})
	if jerr == nil || !strings.Contains(jerr.Message, "plain text failure") {
		t.Fatalf("jerr = %+v", jerr)
	}
}

// OpenAI wants two different shapes for what is conceptually one thing: an
// image as an `image_url` part holding a data: URI, a PDF as a `file` part
// holding the same encoding under `file_data` with a filename. Getting either
// wrong fails at OpenAI with an error about our request, so the assertion is
// on the body we send.
func TestCall_FilesBecomeContentParts(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer srv.Close()

	_, jerr := provider{}.Call(context.Background(), "k", llmtask.Request{
		Model: "m", UserText: "totals?", BaseURL: srv.URL,
		Files: []llm.File{
			{Name: "shot.png", MIME: "image/png", Data: []byte("\x89PNG\r\n")},
			{Name: "invoice.pdf", MIME: "application/pdf", Data: []byte("%PDF-1.4\n")},
		},
	})
	if jerr != nil {
		t.Fatalf("Call: %+v", jerr)
	}

	msgs, _ := got["messages"].([]any)
	last, _ := msgs[len(msgs)-1].(map[string]any)
	parts, ok := last["content"].([]any)
	if !ok {
		t.Fatalf("content is not a parts array: %#v", last["content"])
	}
	if len(parts) != 3 {
		t.Fatalf("want image + file + text, got %d: %#v", len(parts), parts)
	}

	img, _ := parts[0].(map[string]any)
	if img["type"] != "image_url" {
		t.Errorf("image part type = %v, want image_url", img["type"])
	}
	url, _ := img["image_url"].(map[string]any)["url"].(string)
	if !strings.HasPrefix(url, "data:image/png;base64,") {
		t.Errorf("image url = %q, want a data: URI", url)
	}

	doc, _ := parts[1].(map[string]any)
	if doc["type"] != "file" {
		t.Errorf("pdf part type = %v, want file", doc["type"])
	}
	fileObj, _ := doc["file"].(map[string]any)
	if fileObj["filename"] != "invoice.pdf" {
		t.Errorf("filename = %v — inline file_data needs one", fileObj["filename"])
	}
	if fd, _ := fileObj["file_data"].(string); !strings.HasPrefix(fd, "data:application/pdf;base64,") {
		t.Errorf("file_data = %q, want a data: URI", fd)
	}

	if txt, _ := parts[2].(map[string]any); txt["text"] != "totals?" {
		t.Errorf("last part = %#v, want the question after the files", txt)
	}
}

// No files means the plain-string content every existing flow sends.
func TestCall_NoFilesKeepsPlainStringContent(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer srv.Close()

	if _, jerr := (provider{}).Call(context.Background(), "k", llmtask.Request{
		Model: "m", UserText: "hello", BaseURL: srv.URL,
	}); jerr != nil {
		t.Fatalf("Call: %+v", jerr)
	}
	msgs, _ := got["messages"].([]any)
	last, _ := msgs[len(msgs)-1].(map[string]any)
	if s, _ := last["content"].(string); s != "hello" {
		t.Errorf("content = %#v, want the plain string form", last["content"])
	}
}
