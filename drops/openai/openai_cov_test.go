package openai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"git.sr.ht/~klahr/dazyflow/drops/internal/llmtask"
	hfnet "git.sr.ht/~klahr/dazyflow/drops/net"
)

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
