package claude

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
