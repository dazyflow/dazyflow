// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package claude

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"github.com/dazyflow/dazyflow/internal/llm"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/dazyflow/dazyflow/drops/internal/llmtask"
	hfnet "github.com/dazyflow/dazyflow/drops/net"
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
		if r.Header.Get("x-api-key") != "sk-ant-test" || r.Header.Get("anthropic-version") != apiVersion {
			t.Errorf("bad headers: %v", r.Header)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"content": []any{map[string]any{"type": "text", "text": "Hello!"}},
		})
	}))
	defer srv.Close()

	res, jerr := provider{}.Call(context.Background(), "sk-ant-test", llmtask.Request{
		UserText: "Say hi", BaseURL: srv.URL,
	})
	if jerr != nil {
		t.Fatalf("err: %+v", jerr)
	}
	if res.Text != "Hello!" {
		t.Errorf("text = %q", res.Text)
	}
	msgs := gotBody["messages"].([]any)
	if msgs[0].(map[string]any)["content"] != "Say hi" {
		t.Errorf("messages = %+v", msgs)
	}
}

func TestCall_ForcedTool(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"content": []any{map[string]any{"type": "tool_use", "name": "extract", "input": map[string]any{"vendor": "Acme"}}},
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
	if res.Tool["vendor"] != "Acme" {
		t.Errorf("tool = %+v", res.Tool)
	}
}

func TestCall_RateLimited(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(429)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"type": "rate_limit_error", "message": "slow down"}})
	}))
	defer srv.Close()
	_, jerr := provider{}.Call(context.Background(), "k", llmtask.Request{UserText: "x", BaseURL: srv.URL})
	if jerr == nil || jerr.Code != "claude_rate_limited" {
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

// TestCall_MessagesAndSystemPassthrough drives the pre-built Messages branch
// plus System and Temperature forwarding into the Anthropic request body.
func TestCall_MessagesAndSystemPassthrough(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"content": []any{map[string]any{"type": "text", "text": "ok"}},
		})
	}))
	defer srv.Close()

	temp := 0.5
	_, jerr := provider{}.Call(context.Background(), "k", llmtask.Request{
		Model:       "claude-opus-4-8",
		System:      "be terse",
		Temperature: &temp,
		MaxTokens:   55,
		Messages:    []any{map[string]any{"role": "user", "content": "pre-built"}},
		BaseURL:     srv.URL,
	})
	if jerr != nil {
		t.Fatalf("err: %+v", jerr)
	}
	if gotBody["model"] != "claude-opus-4-8" {
		t.Errorf("model = %v", gotBody["model"])
	}
	if gotBody["system"] != "be terse" {
		t.Errorf("system = %v", gotBody["system"])
	}
	if gotBody["temperature"] != 0.5 {
		t.Errorf("temperature = %v", gotBody["temperature"])
	}
	if gotBody["max_tokens"] != 55.0 {
		t.Errorf("max_tokens = %v", gotBody["max_tokens"])
	}
	msgs := gotBody["messages"].([]any)
	if len(msgs) != 1 || msgs[0].(map[string]any)["content"] != "pre-built" {
		t.Errorf("messages = %+v", msgs)
	}
}

func TestCall_ServerErrorClassified(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(503)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"type": "overloaded_error", "message": "busy"}})
	}))
	defer srv.Close()
	_, jerr := provider{}.Call(context.Background(), "k", llmtask.Request{UserText: "x", BaseURL: srv.URL})
	if jerr == nil || jerr.Code != "claude_upstream" {
		t.Fatalf("jerr = %+v", jerr)
	}
}

func TestVerifyKey_OK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if r.Header.Get("x-api-key") != "sk-ant-good" || r.Header.Get("anthropic-version") != apiVersion {
			t.Errorf("headers = %v", r.Header)
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()
	if err := verifyKey(context.Background(), "sk-ant-good", srv.URL); err != nil {
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
		_, _ = w.Write([]byte(`{"error":{"type":"api_error","message":"down"}}`))
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
	if err == nil || !strings.Contains(err.Error(), "could not reach Claude") {
		t.Fatalf("err = %v", err)
	}
}

// TestExtractText_EdgeCases drives the no-content / non-text-block / multi-block
// branches of extractText through a real Call.
func TestExtractText_EdgeCases(t *testing.T) {
	cases := []struct {
		name string
		resp map[string]any
		want string
	}{
		{"no content", map[string]any{}, ""},
		{"content wrong type", map[string]any{"content": "nope"}, ""},
		{"block not map", map[string]any{"content": []any{"x"}}, ""},
		{"non-text block skipped", map[string]any{"content": []any{map[string]any{"type": "tool_use"}}}, ""},
		{"text not string", map[string]any{"content": []any{map[string]any{"type": "text", "text": 9}}}, ""},
		{"two text blocks joined", map[string]any{"content": []any{
			map[string]any{"type": "text", "text": "ab"},
			map[string]any{"type": "image"},
			map[string]any{"type": "text", "text": "cd"},
		}}, "abcd"},
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

// TestExtractToolInput_EdgeCases drives every nil-return / skip branch of
// extractToolInput with a forced Tool.
func TestExtractToolInput_EdgeCases(t *testing.T) {
	cases := []struct {
		name   string
		resp   map[string]any
		wantOK bool
	}{
		{"no content", map[string]any{}, false},
		{"content wrong type", map[string]any{"content": "x"}, false},
		{"block not map", map[string]any{"content": []any{"x"}}, false},
		{"not tool_use", map[string]any{"content": []any{map[string]any{"type": "text", "text": "hi"}}}, false},
		{"name mismatch", map[string]any{"content": []any{map[string]any{"type": "tool_use", "name": "other", "input": map[string]any{"a": 1}}}}, false},
		{"input wrong type", map[string]any{"content": []any{map[string]any{"type": "tool_use", "name": "extract", "input": "x"}}}, false},
		{"good", map[string]any{"content": []any{map[string]any{"type": "tool_use", "name": "extract", "input": map[string]any{"a": 1}}}}, true},
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

// TestClaudeError_PlainBodyFallback drives the branch where the body is not a
// structured error object.
func TestClaudeError_PlainBodyFallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(400)
		_, _ = w.Write([]byte("plain failure"))
	}))
	defer srv.Close()
	_, jerr := provider{}.Call(context.Background(), "k", llmtask.Request{UserText: "x", BaseURL: srv.URL})
	if jerr == nil || !strings.Contains(jerr.Message, "plain failure") {
		t.Fatalf("jerr = %+v", jerr)
	}
}

// A PDF must ride as an Anthropic `document` block, before the text — and the
// base64 must not be line-wrapped, which the API rejects. Asserted on the
// BODY WE SEND, because that is the half of this we control: a wrong block
// type fails at Anthropic with an error about our request.
func TestCall_PDFBecomesADocumentBlock(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"ok"}]}`))
	}))
	defer srv.Close()

	pdf := []byte("%PDF-1.4\nnot really a pdf but the bytes don't matter here\n")
	_, jerr := provider{}.Call(context.Background(), "k", llmtask.Request{
		Model: "m", UserText: "What is the total?", BaseURL: srv.URL,
		Files: []llm.File{{Name: "invoice.pdf", MIME: "application/pdf", Data: pdf}},
	})
	if jerr != nil {
		t.Fatalf("Call: %+v", jerr)
	}

	msgs, _ := got["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("want 1 message, got %d: %v", len(msgs), got["messages"])
	}
	content, ok := msgs[0].(map[string]any)["content"].([]any)
	if !ok {
		t.Fatalf("content is not a block array: %#v", msgs[0])
	}
	if len(content) != 2 {
		t.Fatalf("want document + text blocks, got %d: %#v", len(content), content)
	}

	doc, _ := content[0].(map[string]any)
	if doc["type"] != "document" {
		t.Errorf("first block is %v, want a document block before the question", doc["type"])
	}
	src, _ := doc["source"].(map[string]any)
	if src["type"] != "base64" || src["media_type"] != "application/pdf" {
		t.Errorf("source = %#v", src)
	}
	data, _ := src["data"].(string)
	if strings.ContainsAny(data, "\r\n") {
		t.Error("base64 is line-wrapped — the API rejects that")
	}
	if decoded, err := base64.StdEncoding.DecodeString(data); err != nil {
		t.Errorf("data isn't valid base64: %v", err)
	} else if string(decoded) != string(pdf) {
		t.Error("the PDF bytes didn't survive the round trip")
	}

	txt, _ := content[1].(map[string]any)
	if txt["type"] != "text" || txt["text"] != "What is the total?" {
		t.Errorf("second block = %#v, want the question", txt)
	}
}

// An image is an `image` block, not a document, and its media_type must carry
// no parameters.
func TestCall_ImageBecomesAnImageBlock(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"ok"}]}`))
	}))
	defer srv.Close()

	_, jerr := provider{}.Call(context.Background(), "k", llmtask.Request{
		Model: "m", UserText: "what is this", BaseURL: srv.URL,
		Files: []llm.File{{Name: "shot.png", MIME: "image/png; charset=binary", Data: []byte("\x89PNG\r\n")}},
	})
	if jerr != nil {
		t.Fatalf("Call: %+v", jerr)
	}
	msgs, _ := got["messages"].([]any)
	content, _ := msgs[0].(map[string]any)["content"].([]any)
	blk, _ := content[0].(map[string]any)
	if blk["type"] != "image" {
		t.Fatalf("block type = %v, want image", blk["type"])
	}
	src, _ := blk["source"].(map[string]any)
	if src["media_type"] != "image/png" {
		t.Errorf("media_type = %v, want the parameters stripped", src["media_type"])
	}
}

// No files means the old plain-string content, unchanged. This is the
// regression guard: every existing flow sends a string, and turning that into
// a block array for no reason would be a wire change nobody asked for.
func TestCall_NoFilesKeepsPlainStringContent(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"ok"}]}`))
	}))
	defer srv.Close()

	_, jerr := provider{}.Call(context.Background(), "k", llmtask.Request{
		Model: "m", UserText: "hello", BaseURL: srv.URL,
	})
	if jerr != nil {
		t.Fatalf("Call: %+v", jerr)
	}
	msgs, _ := got["messages"].([]any)
	if content, _ := msgs[0].(map[string]any)["content"].(string); content != "hello" {
		t.Errorf("content = %#v, want the plain string form", msgs[0].(map[string]any)["content"])
	}
}
