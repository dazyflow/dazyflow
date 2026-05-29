// Package ai contains modules that call LLM APIs. Per the design
// argued for in our roadmap: each LLM provider is its own one-shot
// module, with the agent loop expressed in the graph rather than baked
// into a "generic agent" node. That keeps every model call visible in
// audit, retry/skip/fallback policies applicable per call, and budget
// control tractable.
package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"git.sr.ht/~klahr/hazy-flow/core"
	"git.sr.ht/~klahr/hazy-flow/engine"
	"git.sr.ht/~klahr/hazy-flow/integrations/internal/params"
)

const (
	claudeDefaultModel     = "claude-sonnet-4-6"
	claudeDefaultMaxTokens = 1024
	claudeDefaultTimeoutMS = 60_000
	claudeAPIVersion       = "2023-06-01"
	claudeDefaultBaseURL   = "https://api.anthropic.com"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:             "claude",
			Version:        "1.0",
			Label:          "Claude",
			Color:          "#cc7755",
			Icon:           "claude",
			Category:       "ai",
			Provider:       "anthropic",
			Integration:    "Claude",
			Tags:           []string{"llm", "claude", "anthropic", "messages"},
			Description:    "Send a prompt to Claude and get a response back. Useful for summarising upstream text, classifying inputs, generating responses, or any step where you want a language model in the loop. The graph itself is your agent loop — combine with branch nodes for multi-turn flows.",
			Summary:        "Send a prompt to Claude and get a single-turn text response back.",
			Examples: []core.ParamsExample{
				{
					Title:  "One-shot summary",
					Params: json.RawMessage(`{"model":"claude-sonnet-4-6","prompt":"Summarize the upstream text in one sentence.","max_tokens":256,"api_key":"${secret:ANTHROPIC_API_KEY}"}`),
					Notes:  "Wire the text to be summarised into the 'prompt' input port; params.prompt is just the instruction.",
				},
				{
					Title:  "System-prompted classifier",
					Params: json.RawMessage(`{"model":"claude-sonnet-4-6","system":"You classify emails as spam or not. Reply with exactly 'spam' or 'ham'.","prompt":"Your bank account has been compromised, click here","max_tokens":4,"temperature":0,"api_key":"${secret:ANTHROPIC_API_KEY}"}`),
					Notes:  "Lock behaviour into the system prompt so the user prompt can be raw upstream input.",
				},
				{
					Title:  "Multi-turn conversation",
					Params: json.RawMessage(`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"What's the capital of France?"},{"role":"assistant","content":"Paris."},{"role":"user","content":"And of Germany?"}],"max_tokens":128,"api_key":"${secret:ANTHROPIC_API_KEY}"}`),
					Notes:  "Pass a full messages array when you need conversation history; this overrides params.prompt.",
				},
			},
			RequiresConnections: []core.ConnectionRequirement{
				{Kind: "secret", Name: "ANTHROPIC_API_KEY", Note: "Anthropic API key (sk-ant-...); any secret name works at param level, this is the conventional one."},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{{
				Port:  "prompt",
				Label: "Optional user message text (overrides params.messages if set)",
			}},
			Outputs: []core.Port{
				{Port: "text", Label: "Assistant response text"},
				{Port: "response", Label: "Full response object (usage, stop_reason, ...)"},
			},
			// Defaults mirror the Go constants in the package header so the
			// SchemaForm pre-fills sensible values; the executor still
			// applies the same defaults if params arrive without them.
			//
			// prompt + system get format:"multiline" so the UI gives
			// them a textarea — the natural shape for LLM prompt text.
			// The executor's precedence (prompt input port → messages
			// → params.prompt) is unchanged; this just makes the
			// "single-turn prompt" case discoverable in the inspector.
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"model":{"type":"string","default":"claude-sonnet-4-6","description":"Model alias or full name. Defaults to claude-sonnet-4-6."},
					"prompt":{"type":"string","format":"multiline","description":"Single user message. Used when no 'prompt' input port is wired AND params.messages is empty — the simplest path for one-shot calls."},
					"system":{"type":"string","format":"multiline","description":"Optional system prompt — gives the model its persona / task framing."},
					"messages":{"type":"array","description":"Full conversation history ({role, content}). When set, overrides params.prompt. For multi-turn calls."},
					"max_tokens":{"type":"integer","minimum":1,"default":1024,"description":"Maximum tokens the assistant may produce."},
					"temperature":{"type":"number","minimum":0,"maximum":1,"description":"Sampling temperature (0=deterministic, 1=creative). Leave unset to use the model's default."},
					"stop_sequences":{"type":"array","items":{"type":"string"},"description":"Optional list of strings that stop generation when emitted."},
					"api_key":{"type":"string","description":"Anthropic API key. Use ${secret:NAME} — never paste sk-ant-... literals into the graph. (Ignored when hzd is in --claude-cli mode.)"},
					"base_url":{"type":"string","default":"https://api.anthropic.com","description":"Override the API host (proxy, sandbox)."},
					"timeout_ms":{"type":"integer","minimum":1,"default":60000,"description":"HTTP timeout in milliseconds."}
				}
			}`),
			// One-shot LLM calls are safe to retry — retries happen only
			// when the network/HTTP layer failed before we received a
			// response, never when we've already observed an answer.
			Idempotent:  true,
			RetryPolicy: core.RetryExponentialBackoff,
		},
		Execute: executeClaude,
	})
}

type claudeRequest struct {
	Model         string          `json:"model"`
	System        string          `json:"system,omitempty"`
	Messages      []claudeMessage `json:"messages"`
	MaxTokens     int             `json:"max_tokens"`
	Temperature   *float64        `json:"temperature,omitempty"`
	StopSequences []string        `json:"stop_sequences,omitempty"`
}

type claudeMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type claudeContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

type claudeUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

type claudeResponse struct {
	ID         string               `json:"id"`
	Type       string               `json:"type"`
	Role       string               `json:"role"`
	Content    []claudeContentBlock `json:"content"`
	Model      string               `json:"model"`
	StopReason string               `json:"stop_reason"`
	Usage      claudeUsage          `json:"usage"`
}

type claudeAPIError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

type claudeAPIErrorEnvelope struct {
	Type  string         `json:"type"`
	Error claudeAPIError `json:"error"`
}

func executeClaude(ctx context.Context, job core.Job, progress chan<- core.Progress) (core.Result, error) {
	// claude-cli mode: hzd was started with -claude-cli, so route
	// through the local Claude Code CLI (OAuth-based auth, no
	// Anthropic API key needed). Same toggle the in-app chat agent
	// uses, exposed to this drop via env so the integrations
	// package doesn't have to import daemon.
	if isClaudeCLIMode() {
		return executeClaudeViaCLI(ctx, job, progress)
	}

	apiKey, err := params.String(job.Params, "api_key")
	if err != nil {
		return params.Err(job, "bad_param", err.Error()), nil
	}
	if apiKey == "" {
		return params.Err(job, "bad_param", "api_key is required (use a secret reference like env://ANTHROPIC_API_KEY)"), nil
	}

	body, err := buildClaudeRequest(job)
	if err != nil {
		return params.Err(job, "bad_param", err.Error()), nil
	}
	if len(body.Messages) == 0 {
		return params.Err(job, "bad_input", "no messages — provide params.messages or the prompt input port"), nil
	}

	baseURL := params.StringDefault(job.Params, "base_url", claudeDefaultBaseURL)
	timeoutMs := params.IntDefault(job.Params, "timeout_ms", claudeDefaultTimeoutMS)

	emitProgress(progress, job, 0.1, fmt.Sprintf("calling %s/v1/messages (model=%s)", baseURL, body.Model))

	reqJSON, err := json.Marshal(body)
	if err != nil {
		return params.Err(job, "marshal", err.Error()), nil
	}

	req, err := http.NewRequestWithContext(ctx, "POST",
		strings.TrimRight(baseURL, "/")+"/v1/messages",
		bytes.NewReader(reqJSON))
	if err != nil {
		return params.Err(job, "bad_url", err.Error()), nil
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", claudeAPIVersion)

	client := &http.Client{Timeout: time.Duration(timeoutMs) * time.Millisecond}
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return params.Err(job, "cancelled", ctx.Err().Error()), ctx.Err()
		}
		return params.Err(job, "http", err.Error()), nil
	}
	defer resp.Body.Close()

	rawResp, err := io.ReadAll(resp.Body)
	if err != nil {
		return params.Err(job, "io", err.Error()), nil
	}

	if resp.StatusCode >= 400 {
		var env claudeAPIErrorEnvelope
		if err := json.Unmarshal(rawResp, &env); err == nil && env.Error.Message != "" {
			code := "claude_api"
			if resp.StatusCode == 429 {
				code = "claude_rate_limited"
			}
			return params.Err(job, code, fmt.Sprintf("%d %s: %s", resp.StatusCode, env.Error.Type, env.Error.Message)), nil
		}
		return params.Err(job, "claude_api", fmt.Sprintf("HTTP %d: %s", resp.StatusCode, string(rawResp))), nil
	}

	var parsed claudeResponse
	if err := json.Unmarshal(rawResp, &parsed); err != nil {
		return params.Err(job, "unmarshal", err.Error()), nil
	}

	emitProgress(progress, job, 0.9, fmt.Sprintf("in=%d out=%d tokens",
		parsed.Usage.InputTokens, parsed.Usage.OutputTokens))

	text := concatText(parsed.Content)

	// Re-marshal the response into a generic map so downstream branch
	// nodes can navigate it without needing the claude-specific struct.
	var responseMap map[string]any
	if err := json.Unmarshal(rawResp, &responseMap); err != nil {
		// Should not fail since we just parsed it once; fall back to nil.
		responseMap = nil
	}

	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"text":     {MIME: "text/plain", Inline: text},
			"response": {MIME: "application/json", Inline: responseMap},
		},
	}, nil
}

// buildClaudeRequest assembles the API request from params and the
// optional prompt input port. Precedence:
//   - input.prompt (string, list, or object — coerced to text) →
//     single user message, overrides params.messages
//   - params.messages → used as-is
//   - params.prompt (a string, fallback) → single user message
//
// The prompt input is coerced because upstream nodes commonly emit
// non-string shapes — Merge publishes `[]core.Ref`, structured
// transformers publish maps, etc. — and silently dropping those
// would leave the user wondering why their wired node produced no
// messages.
func buildClaudeRequest(job core.Job) (claudeRequest, error) {
	req := claudeRequest{
		Model:     params.StringDefault(job.Params, "model", claudeDefaultModel),
		System:    params.StringDefault(job.Params, "system", ""),
		MaxTokens: params.IntDefault(job.Params, "max_tokens", claudeDefaultMaxTokens),
	}

	if t, ok := paramFloat(job.Params, "temperature"); ok {
		req.Temperature = &t
	}
	if stops, ok := paramStringSlice(job.Params, "stop_sequences"); ok {
		req.StopSequences = stops
	}

	// Resolve messages with documented precedence.
	if input, ok := job.Input["prompt"]; ok {
		if s := coercePromptText(input.Inline); s != "" {
			req.Messages = []claudeMessage{{Role: "user", Content: s}}
		}
	}
	if len(req.Messages) == 0 {
		if m, err := paramMessages(job.Params, "messages"); err != nil {
			return req, err
		} else if len(m) > 0 {
			req.Messages = m
		}
	}
	if len(req.Messages) == 0 {
		if s, ok := params.StringOpt(job.Params, "prompt"); ok && s != "" {
			req.Messages = []claudeMessage{{Role: "user", Content: s}}
		}
	}

	return req, nil
}

// coercePromptText flattens whatever shape arrived on the prompt
// input port into a single user-message string.
//
// Handled shapes:
//   - string / []byte                          → use verbatim
//   - []core.Ref (Merge fan-in)                → recurse into each
//     ref's Inline and join with blank lines
//   - []any (JSON array from a transform node) → same flattening
//   - map / struct (structured object)         → JSON-encode
//   - nil                                       → empty
//
// Blank-line separators between merged items match what an operator
// would naturally write if combining several blurbs into one
// prompt; they also keep markdown-style sections distinct so the
// model doesn't read them as one run-on paragraph.
func coercePromptText(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case []byte:
		return string(x)
	case []core.Ref:
		parts := make([]string, 0, len(x))
		for _, r := range x {
			if s := coercePromptText(r.Inline); s != "" {
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, "\n\n")
	case []any:
		parts := make([]string, 0, len(x))
		for _, item := range x {
			if s := coercePromptText(item); s != "" {
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, "\n\n")
	case map[string]any:
		b, err := json.Marshal(x)
		if err != nil {
			return ""
		}
		return string(b)
	}
	// Last resort — JSON-stringify whatever it is. Catches custom
	// structs, numbers, bools, etc.
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

func concatText(blocks []claudeContentBlock) string {
	var b strings.Builder
	for _, blk := range blocks {
		if blk.Type == "text" {
			b.WriteString(blk.Text)
		}
	}
	return b.String()
}
