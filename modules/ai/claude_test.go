package ai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"git.sr.ht/~klahr/hazy-flow/core"
)

// claudeRequestSeen captures everything the module sent to the
// (mocked) Anthropic endpoint so tests can assert on each piece.
type claudeRequestSeen struct {
	method    string
	apiKey    string
	version   string
	body      claudeRequest
	rawBody   []byte
}

func mockClaude(t *testing.T, response any, status int) (*httptest.Server, *claudeRequestSeen) {
	t.Helper()
	seen := &claudeRequestSeen{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.method = r.Method
		seen.apiKey = r.Header.Get("x-api-key")
		seen.version = r.Header.Get("anthropic-version")
		body, _ := io.ReadAll(r.Body)
		seen.rawBody = body
		_ = json.Unmarshal(body, &seen.body)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(response)
	}))
	return srv, seen
}

func okResponse(text string) claudeResponse {
	return claudeResponse{
		ID:    "msg_test",
		Type:  "message",
		Role:  "assistant",
		Model: "claude-sonnet-4-6",
		Content: []claudeContentBlock{
			{Type: "text", Text: text},
		},
		StopReason: "end_turn",
		Usage:      claudeUsage{InputTokens: 12, OutputTokens: 8},
	}
}

func TestClaude_HappyPath(t *testing.T) {
	srv, seen := mockClaude(t, okResponse("hello, world!"), 200)
	defer srv.Close()

	res, err := executeClaude(t.Context(), core.Job{
		Params: map[string]any{
			"api_key":  "test-key",
			"base_url": srv.URL,
			"system":   "You are concise.",
			"messages": []any{
				map[string]any{"role": "user", "content": "hi"},
			},
			"max_tokens": 50,
		},
	}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q (%+v)", res.Status, res.Error)
	}

	text, _ := res.Output["text"].Inline.(string)
	if text != "hello, world!" {
		t.Errorf("text = %q", text)
	}
	resp, ok := res.Output["response"].Inline.(map[string]any)
	if !ok {
		t.Fatalf("response port is %T, want map", res.Output["response"].Inline)
	}
	if usage, _ := resp["usage"].(map[string]any); int(usage["output_tokens"].(float64)) != 8 {
		t.Errorf("usage output_tokens = %v", usage)
	}

	// Wire-level assertions
	if seen.method != "POST" {
		t.Errorf("method = %q", seen.method)
	}
	if seen.apiKey != "test-key" {
		t.Errorf("x-api-key = %q", seen.apiKey)
	}
	if seen.version != claudeAPIVersion {
		t.Errorf("anthropic-version = %q, want %q", seen.version, claudeAPIVersion)
	}
	if seen.body.Model == "" {
		t.Error("model not sent")
	}
	if seen.body.System != "You are concise." {
		t.Errorf("system = %q", seen.body.System)
	}
	if len(seen.body.Messages) != 1 || seen.body.Messages[0].Content != "hi" {
		t.Errorf("messages = %+v", seen.body.Messages)
	}
	if seen.body.MaxTokens != 50 {
		t.Errorf("max_tokens = %d", seen.body.MaxTokens)
	}
}

func TestClaude_DefaultModel(t *testing.T) {
	srv, seen := mockClaude(t, okResponse(""), 200)
	defer srv.Close()

	_, _ = executeClaude(t.Context(), core.Job{
		Params: map[string]any{
			"api_key":  "k",
			"base_url": srv.URL,
			"prompt":   "go",
		},
	}, nil)
	if seen.body.Model != claudeDefaultModel {
		t.Errorf("model = %q, want default %q", seen.body.Model, claudeDefaultModel)
	}
}

func TestClaude_PromptInputPortOverridesParams(t *testing.T) {
	srv, seen := mockClaude(t, okResponse(""), 200)
	defer srv.Close()

	_, _ = executeClaude(t.Context(), core.Job{
		Params: map[string]any{
			"api_key":  "k",
			"base_url": srv.URL,
			"messages": []any{
				map[string]any{"role": "user", "content": "from params"},
			},
		},
		Input: map[string]core.Ref{
			"prompt": {Inline: "from input port"},
		},
	}, nil)
	if len(seen.body.Messages) != 1 {
		t.Fatalf("got %d messages", len(seen.body.Messages))
	}
	if seen.body.Messages[0].Content != "from input port" {
		t.Errorf("content = %q (input port should win)", seen.body.Messages[0].Content)
	}
}

func TestClaude_PromptParamsFallback(t *testing.T) {
	srv, seen := mockClaude(t, okResponse(""), 200)
	defer srv.Close()

	_, _ = executeClaude(t.Context(), core.Job{
		Params: map[string]any{
			"api_key":  "k",
			"base_url": srv.URL,
			"prompt":   "via params.prompt",
		},
	}, nil)
	if len(seen.body.Messages) != 1 {
		t.Fatalf("got %d messages", len(seen.body.Messages))
	}
	if seen.body.Messages[0].Content != "via params.prompt" {
		t.Errorf("content = %q", seen.body.Messages[0].Content)
	}
}

func TestClaude_MissingAPIKey(t *testing.T) {
	res, _ := executeClaude(t.Context(), core.Job{
		Params: map[string]any{"prompt": "hi"},
	}, nil)
	if res.Status != core.StatusError {
		t.Fatalf("status = %q, want error", res.Status)
	}
	if res.Error.Code != "bad_param" {
		t.Errorf("code = %q, want bad_param", res.Error.Code)
	}
}

func TestClaude_EmptyAPIKey(t *testing.T) {
	res, _ := executeClaude(t.Context(), core.Job{
		Params: map[string]any{"api_key": "", "prompt": "hi"},
	}, nil)
	if res.Status != core.StatusError {
		t.Fatalf("status = %q, want error", res.Status)
	}
}

func TestClaude_NoMessagesAtAll(t *testing.T) {
	srv, _ := mockClaude(t, okResponse(""), 200)
	defer srv.Close()

	res, _ := executeClaude(t.Context(), core.Job{
		Params: map[string]any{
			"api_key":  "k",
			"base_url": srv.URL,
			// no messages, no prompt, no input port
		},
	}, nil)
	if res.Status != core.StatusError {
		t.Fatalf("status=%q, want error", res.Status)
	}
	if res.Error.Code != "bad_input" {
		t.Errorf("code=%q, want bad_input", res.Error.Code)
	}
}

func TestClaude_APIErrorBecomesNodeFailure(t *testing.T) {
	errBody := map[string]any{
		"type": "error",
		"error": map[string]any{
			"type":    "invalid_request_error",
			"message": "bad model parameter",
		},
	}
	srv, _ := mockClaude(t, errBody, 400)
	defer srv.Close()

	res, _ := executeClaude(t.Context(), core.Job{
		Params: map[string]any{
			"api_key":  "k",
			"base_url": srv.URL,
			"prompt":   "hi",
		},
	}, nil)
	if res.Status != core.StatusError {
		t.Fatalf("status=%q", res.Status)
	}
	if res.Error.Code != "claude_api" {
		t.Errorf("code=%q, want claude_api", res.Error.Code)
	}
	if !strings.Contains(res.Error.Message, "bad model parameter") {
		t.Errorf("message=%q; expected to mention API error message", res.Error.Message)
	}
}

func TestClaude_RateLimitGetsDistinctCode(t *testing.T) {
	errBody := map[string]any{
		"type":  "error",
		"error": map[string]any{"type": "rate_limit_error", "message": "slow down"},
	}
	srv, _ := mockClaude(t, errBody, 429)
	defer srv.Close()

	res, _ := executeClaude(t.Context(), core.Job{
		Params: map[string]any{"api_key": "k", "base_url": srv.URL, "prompt": "hi"},
	}, nil)
	if res.Error.Code != "claude_rate_limited" {
		t.Errorf("code=%q, want claude_rate_limited", res.Error.Code)
	}
}

func TestClaude_TemperatureAndStopForwarded(t *testing.T) {
	srv, seen := mockClaude(t, okResponse(""), 200)
	defer srv.Close()

	_, _ = executeClaude(t.Context(), core.Job{
		Params: map[string]any{
			"api_key":        "k",
			"base_url":       srv.URL,
			"prompt":         "hi",
			"temperature":    0.3,
			"stop_sequences": []any{"###", "STOP"},
		},
	}, nil)

	if seen.body.Temperature == nil || *seen.body.Temperature != 0.3 {
		t.Errorf("temperature = %v", seen.body.Temperature)
	}
	if len(seen.body.StopSequences) != 2 || seen.body.StopSequences[0] != "###" {
		t.Errorf("stop_sequences = %v", seen.body.StopSequences)
	}
}

func TestClaude_TimeoutEnforced(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(500 * time.Millisecond)
	}))
	defer srv.Close()

	start := time.Now()
	res, _ := executeClaude(t.Context(), core.Job{
		Params: map[string]any{
			"api_key":    "k",
			"base_url":   srv.URL,
			"prompt":     "hi",
			"timeout_ms": 50,
		},
	}, nil)
	elapsed := time.Since(start)
	if res.Status != core.StatusError {
		t.Fatalf("status=%q, want error", res.Status)
	}
	if elapsed > 400*time.Millisecond {
		t.Errorf("took %v; timeout should have cut earlier", elapsed)
	}
}

func TestClaude_ContextCancellation(t *testing.T) {
	// Hand the module an already-cancelled context. The HTTP client
	// should reject the request immediately rather than dial out.
	srv, _ := mockClaude(t, okResponse(""), 200)
	defer srv.Close()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	res, _ := executeClaude(ctx, core.Job{
		Params: map[string]any{
			"api_key":  "k",
			"base_url": srv.URL,
			"prompt":   "hi",
		},
	}, nil)
	if res.Status != core.StatusError {
		t.Fatalf("status=%q, want error", res.Status)
	}
}

func TestClaude_MultiBlockResponseConcatenates(t *testing.T) {
	// A response with two text blocks should produce one combined string
	// on the text port.
	resp := claudeResponse{
		ID: "x", Type: "message", Role: "assistant", Model: "m",
		Content: []claudeContentBlock{
			{Type: "text", Text: "first "},
			{Type: "text", Text: "second"},
		},
		StopReason: "end_turn",
	}
	srv, _ := mockClaude(t, resp, 200)
	defer srv.Close()

	res, _ := executeClaude(t.Context(), core.Job{
		Params: map[string]any{"api_key": "k", "base_url": srv.URL, "prompt": "hi"},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q", res.Status)
	}
	text, _ := res.Output["text"].Inline.(string)
	if text != "first second" {
		t.Errorf("text = %q, want concatenation 'first second'", text)
	}
}

func TestClaude_MalformedResponseFailsCleanly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte("not json at all"))
	}))
	defer srv.Close()
	res, _ := executeClaude(t.Context(), core.Job{
		Params: map[string]any{"api_key": "k", "base_url": srv.URL, "prompt": "hi"},
	}, nil)
	if res.Status != core.StatusError {
		t.Fatalf("status=%q", res.Status)
	}
	if res.Error.Code != "unmarshal" {
		t.Errorf("code=%q, want unmarshal", res.Error.Code)
	}
}

// TestClaude_LiveSmoke is opt-in: if ANTHROPIC_API_KEY is in the env we
// run a real call to the real API. Skipped otherwise so the test suite
// doesn't burn tokens unattended.
func TestClaude_LiveSmoke(t *testing.T) {
	key := os.Getenv("ANTHROPIC_API_KEY")
	if key == "" {
		t.Skip("ANTHROPIC_API_KEY not set; skipping live smoke test")
	}
	res, err := executeClaude(t.Context(), core.Job{
		Params: map[string]any{
			"api_key":    key,
			"prompt":     "Reply with exactly the word: OK",
			"max_tokens": 10,
		},
	}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q (%+v)", res.Status, res.Error)
	}
	text, _ := res.Output["text"].Inline.(string)
	if !strings.Contains(strings.ToUpper(text), "OK") {
		t.Errorf("text=%q; expected OK", text)
	}
}
