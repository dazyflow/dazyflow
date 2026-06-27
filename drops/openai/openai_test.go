// SPDX-FileCopyrightText: 2026 Joachim Klahr
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

	"git.sr.ht/~klahr/dazyflow/drops/internal/llmtask"
	hfnet "git.sr.ht/~klahr/dazyflow/drops/net"
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
