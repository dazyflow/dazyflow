// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package ollama

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"git.sr.ht/~klahr/dazyflow/drops/internal/llmtask"
	hfnet "git.sr.ht/~klahr/dazyflow/drops/net"
)

func TestMain(m *testing.M) {
	// httptest listens on 127.0.0.1, which the SSRF guard blocks by default —
	// the same guard a real localhost Ollama meets. See the package doc.
	hfnet.SetAllowPrivateEgress(true)
	os.Exit(m.Run())
}

// chatServer replies to /v1/chat/completions with resp and records the request.
func chatServer(t *testing.T, resp map[string]any) (*httptest.Server, *map[string]any, *http.Header) {
	t.Helper()
	var body map[string]any
	var hdr http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		hdr = r.Header.Clone()
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)
	return srv, &body, &hdr
}

func textReply(content string) map[string]any {
	return map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": content}}}}
}

// TestCall_KeylessSendsNoAuthHeader is the case that separates this provider
// from its cloud siblings: no key configured must mean no Authorization header
// at all, not an empty bearer.
func TestCall_KeylessSendsNoAuthHeader(t *testing.T) {
	srv, _, hdr := chatServer(t, textReply("Hej!"))

	res, jerr := provider{}.Call(context.Background(), "", llmtask.Request{
		UserText: "Say hi", BaseURL: srv.URL,
	})
	if jerr != nil {
		t.Fatalf("err: %+v", jerr)
	}
	if res.Text != "Hej!" {
		t.Errorf("text = %q", res.Text)
	}
	if got := hdr.Get("authorization"); got != "" {
		t.Errorf("keyless call sent authorization %q, want none", got)
	}
}

// A key is still forwarded — a shared instance behind an authenticating proxy.
func TestCall_KeyIsForwardedWhenSet(t *testing.T) {
	srv, _, hdr := chatServer(t, textReply("ok"))

	_, jerr := provider{}.Call(context.Background(), "proxy-token", llmtask.Request{
		UserText: "x", BaseURL: srv.URL,
	})
	if jerr != nil {
		t.Fatalf("err: %+v", jerr)
	}
	if got := hdr.Get("authorization"); got != "Bearer proxy-token" {
		t.Errorf("authorization = %q", got)
	}
}

// Ollama returns tool arguments as a JSON object where OpenAI returns a JSON
// string. Both must decode — the provider cannot know which build it is on.
func TestCall_ToolArgsObjectAndStringForms(t *testing.T) {
	for _, tc := range []struct {
		name string
		args any
	}{
		{"object", map[string]any{"vendor": "Acme", "amount": float64(42)}},
		{"string", `{"vendor":"Acme","amount":42}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, _, _ := chatServer(t, map[string]any{"choices": []any{map[string]any{"message": map[string]any{
				"tool_calls": []any{map[string]any{
					"function": map[string]any{"name": "extract", "arguments": tc.args},
				}},
			}}}})

			res, jerr := provider{}.Call(context.Background(), "", llmtask.Request{
				UserText: "x", BaseURL: srv.URL,
				Tool: &llmtask.Tool{Name: "extract", Schema: map[string]any{"type": "object"}},
			})
			if jerr != nil {
				t.Fatalf("err: %+v", jerr)
			}
			if res.Tool["vendor"] != "Acme" || res.Tool["amount"] != float64(42) {
				t.Errorf("tool args = %+v", res.Tool)
			}
		})
	}
}

// The reason this provider has a fallback at all: Ollama honours a forced
// tool_choice only on some models. A model that answers in prose still has to
// produce a working Extract fields step.
func TestCall_FallsBackToJSONInProse(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content string
	}{
		{"bare object", `{"vendor":"Acme"}`},
		{"fenced", "Here you go:\n```json\n{\"vendor\":\"Acme\"}\n```"},
		{"preamble", `Sure! {"vendor":"Acme"} — hope that helps.`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, _, _ := chatServer(t, textReply(tc.content))

			res, jerr := provider{}.Call(context.Background(), "", llmtask.Request{
				UserText: "x", BaseURL: srv.URL,
				Tool: &llmtask.Tool{Name: "extract", Schema: map[string]any{"type": "object"}},
			})
			if jerr != nil {
				t.Fatalf("err: %+v", jerr)
			}
			if res.Tool["vendor"] != "Acme" {
				t.Errorf("tool args = %+v", res.Tool)
			}
		})
	}
}

// A model with no tool support and no JSON must fail with an error that names
// the actual fix, not a nil map that silently becomes empty output downstream.
func TestCall_NoStructuredOutputIsAnActionableError(t *testing.T) {
	srv, _, _ := chatServer(t, textReply("I'm afraid I can't do that."))

	_, jerr := provider{}.Call(context.Background(), "", llmtask.Request{
		UserText: "x", Model: "tinyllama", BaseURL: srv.URL,
		Tool: &llmtask.Tool{Name: "extract", Schema: map[string]any{"type": "object"}},
	})
	if jerr == nil {
		t.Fatal("no error for a model that returned neither a tool call nor JSON")
	}
	if jerr.Code != "ollama_no_tool_call" {
		t.Errorf("code = %q", jerr.Code)
	}
	// The message has to carry the model name and a way forward.
	if !strings.Contains(jerr.Message, "tinyllama") || !strings.Contains(jerr.Message, "llama3.1") {
		t.Errorf("message is not actionable: %q", jerr.Message)
	}
}

func TestCall_HTTPErrorIsClassified(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"model 'nope' not found"}`))
	}))
	defer srv.Close()

	_, jerr := provider{}.Call(context.Background(), "", llmtask.Request{
		UserText: "x", BaseURL: srv.URL,
	})
	if jerr == nil {
		t.Fatal("404 produced no error")
	}
	if jerr.Code != "ollama_not_found" {
		t.Errorf("code = %q, want ollama_not_found", jerr.Code)
	}
	if !strings.Contains(jerr.Message, "not found") {
		t.Errorf("vendor detail lost: %q", jerr.Message)
	}
}

// jsonFromText must not be fooled by braces inside strings, and must decline
// rather than guess when there is no object to find.
func TestJSONFromText(t *testing.T) {
	if got := jsonFromText(`{"note":"a } inside a string","ok":true}`); got == nil || got["ok"] != true {
		t.Errorf("brace inside a string ended the scan early: %+v", got)
	}
	if got := jsonFromText("no object here at all"); got != nil {
		t.Errorf("invented %+v from prose", got)
	}
	if got := jsonFromText(`{"unterminated": `); got != nil {
		t.Errorf("accepted truncated JSON: %+v", got)
	}
	if got := jsonFromText(`{"a":1} {"b":2}`); got == nil || got["a"] != float64(1) {
		t.Errorf("wanted the first object, got %+v", got)
	}
}

// The connection test asks the question that actually fails for a local
// runtime — reachability and whether anything is pulled — not key validity.
func TestVerifyReachable(t *testing.T) {
	t.Run("no models pulled", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"models":[]}`))
		}))
		defer srv.Close()
		err := verifyReachable(context.Background(), "", srv.URL)
		if err == nil || !strings.Contains(err.Error(), "ollama pull") {
			t.Errorf("err = %v, want the pull-a-model hint", err)
		}
	})

	t.Run("reachable with a model", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/tags" {
				t.Errorf("probed %q, want /api/tags", r.URL.Path)
			}
			_, _ = w.Write([]byte(`{"models":[{"name":"llama3.1:8b"}]}`))
		}))
		defer srv.Close()
		if err := verifyReachable(context.Background(), "", srv.URL); err != nil {
			t.Errorf("err = %v", err)
		}
	})

	t.Run("unreachable", func(t *testing.T) {
		// A closed port on an address the guard permits under this test's
		// SetAllowPrivateEgress — the operator's real "wrong URL" case.
		srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		url := srv.URL
		srv.Close()
		err := verifyReachable(context.Background(), "", url)
		if err == nil || !strings.Contains(err.Error(), "could not reach Ollama") {
			t.Errorf("err = %v, want a reachability message naming the URL", err)
		}
	})
}

func TestOllamaError(t *testing.T) {
	// Ollama's own flat shape.
	if got := ollamaError([]byte(`{"error":"model not found"}`)); got != "model not found" {
		t.Errorf("flat: %q", got)
	}
	// The OpenAI-compatible nested shape, which the same server also emits.
	if got := ollamaError([]byte(`{"error":{"message":"bad request"}}`)); got != "bad request" {
		t.Errorf("nested: %q", got)
	}
	// Anything else comes back verbatim rather than as an empty string.
	if got := ollamaError([]byte(`upstream exploded`)); got != "upstream exploded" {
		t.Errorf("raw: %q", got)
	}
}

func TestBaseOr(t *testing.T) {
	if got := baseOr(""); got != defaultBase {
		t.Errorf("empty base = %q, want %q", got, defaultBase)
	}
	if got := baseOr("  http://box.local:11434/  "); got != "http://box.local:11434" {
		t.Errorf("trim = %q", got)
	}
}
