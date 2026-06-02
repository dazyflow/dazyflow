package claude

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"git.sr.ht/~klahr/hazyflow/core"
)

func TestClaude_PromptInputAndText(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		if r.Header.Get("x-api-key") != "sk-ant-test" || r.Header.Get("anthropic-version") != apiVersion {
			t.Errorf("headers: %v", r.Header)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"content": []any{map[string]any{"type": "text", "text": "Hello!"}},
			"usage":   map[string]any{"output_tokens": 2},
		})
	}))
	defer srv.Close()

	res, err := executeClaude(context.Background(), core.Job{
		Params: map[string]any{"api_key": "sk-ant-test", "base_url": srv.URL, "max_tokens": 64},
		Input:  map[string]core.Ref{"prompt": {Inline: "Say hi"}},
	}, nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	if res.Output["text"].Inline != "Hello!" {
		t.Errorf("text = %v", res.Output["text"].Inline)
	}
	msgs := gotBody["messages"].([]any)
	if len(msgs) != 1 || msgs[0].(map[string]any)["content"] != "Say hi" {
		t.Errorf("messages = %+v", msgs)
	}
}

func TestClaude_MissingAPIKey(t *testing.T) {
	res, _ := executeClaude(context.Background(), core.Job{
		Params: map[string]any{"prompt": "hi"},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("status=%q code=%v", res.Status, res.Error)
	}
}

func TestClaude_NoMessages(t *testing.T) {
	res, _ := executeClaude(context.Background(), core.Job{
		Params: map[string]any{"api_key": "k"},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_input" {
		t.Errorf("status=%q code=%v", res.Status, res.Error)
	}
}

func TestClaude_RateLimited(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(429)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"type": "rate_limit_error", "message": "slow down"}})
	}))
	defer srv.Close()
	res, _ := executeClaude(context.Background(), core.Job{
		Params: map[string]any{"api_key": "k", "base_url": srv.URL, "prompt": "hi"},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "claude_rate_limited" {
		t.Fatalf("status=%q code=%v", res.Status, res.Error)
	}
}

func TestCoercePromptText(t *testing.T) {
	if coercePromptText([]any{"a", "b"}) != "a\n\nb" {
		t.Errorf("list join failed")
	}
	if coercePromptText(map[string]any{"value": "wrapped"}) != "wrapped" {
		t.Errorf("value-wrapper failed")
	}
}
