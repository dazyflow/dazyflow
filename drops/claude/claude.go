// Package claude hosts the native Claude (Anthropic Messages API)
// connector, migrated from the scripted TS drop. It authenticates with an
// api_key param (typically ${tenant:ANTHROPIC_API_KEY}); there's no OAuth.
package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"git.sr.ht/~klahr/hazyflow/core"
	"git.sr.ht/~klahr/hazyflow/drops/internal/params"
	"git.sr.ht/~klahr/hazyflow/engine"
)

const (
	apiVersion  = "2023-06-01"
	defaultBase = "https://api.anthropic.com"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "claude",
			Version:     "1.0",
			Label:       "Claude",
			Summary:     "Send a prompt to Claude and get a single-turn text response back.",
			Description: "Send a prompt to Claude and get a response back — summarise upstream text, classify inputs, or generate responses. The graph itself is your agent loop.",
			Integration: "Claude",
			Category:    "ai",
			Icon:        "claude",
			Color:       "#cc7755",
			Provider:    "internal",
			Tags:        []string{"claude", "anthropic", "ai", "llm", "prompt"},
			Examples: []core.ParamsExample{
				{Title: "One-shot summary", Params: json.RawMessage(`{"model":"claude-sonnet-4-6","prompt":"Summarize the upstream text in one sentence.","max_tokens":256,"api_key":"${tenant:ANTHROPIC_API_KEY}"}`), Notes: "Wire the text to summarise into the 'prompt' input; params.prompt or params.system is the instruction."},
				{Title: "System-prompted classifier", Params: json.RawMessage(`{"model":"claude-sonnet-4-6","system":"Reply with exactly 'spam' or 'ham'.","prompt":"Your bank account has been compromised","max_tokens":4,"temperature":0,"api_key":"${tenant:ANTHROPIC_API_KEY}"}`)},
			},
			RequiresConnections: []core.ConnectionRequirement{
				{Kind: "secret", Name: "ANTHROPIC_API_KEY", Note: "Anthropic API key (sk-ant-…)."},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				{Port: "prompt", Label: "Optional user message text (overrides params.messages if set)"},
			},
			Outputs: []core.Port{
				{Port: "text", Label: "Assistant response text"},
				{Port: "response", Label: "Full response object (usage, stop_reason, …)"},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"model":{"type":"string","description":"Model id, e.g. claude-sonnet-4-6."},
					"prompt":{"type":"string","format":"multiline","description":"Single user message (used when no 'prompt' input and no params.messages)."},
					"system":{"type":"string","format":"multiline","description":"Optional system prompt."},
					"messages":{"type":"array","items":{},"description":"Full conversation history ({role, content}); overrides params.prompt."},
					"max_tokens":{"type":"integer","default":1024,"minimum":1},
					"temperature":{"type":"number"},
					"stop_sequences":{"type":"array","items":{"type":"string"}},
					"api_key":{"type":"string","description":"Anthropic API key. Use ${tenant:ANTHROPIC_API_KEY}."},
					"base_url":{"type":"string","description":"Override the API host."},
					"timeout_ms":{"type":"integer","default":60000,"minimum":1}
				}
			}`),
			Idempotent:  true,
			RetryPolicy: core.RetryExponentialBackoff,
		},
		Execute: executeClaude,
	})
}

func executeClaude(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	apiKey, _ := params.StringOpt(job.Params, "api_key")
	if apiKey == "" {
		return params.Err(job, "bad_param", "api_key is required (use ${tenant:ANTHROPIC_API_KEY})"), nil
	}

	// Message precedence: prompt input → params.messages → params.prompt.
	var messages []any
	if in, ok := job.Input["prompt"]; ok && in.Inline != nil {
		if text := coercePromptText(in.Inline); text != "" {
			messages = []any{map[string]any{"role": "user", "content": text}}
		}
	}
	if messages == nil {
		if m, ok := job.Params["messages"].([]any); ok && len(m) > 0 {
			messages = m
		}
	}
	if messages == nil {
		if pr, _ := params.StringOpt(job.Params, "prompt"); pr != "" {
			messages = []any{map[string]any{"role": "user", "content": pr}}
		}
	}
	if len(messages) == 0 {
		return params.Err(job, "bad_input", "no messages — provide params.messages or the prompt input port"), nil
	}

	body := map[string]any{
		"model":      params.StringDefault(job.Params, "model", "claude-sonnet-4-6"),
		"messages":   messages,
		"max_tokens": params.IntDefault(job.Params, "max_tokens", 1024),
	}
	if s, _ := params.StringOpt(job.Params, "system"); s != "" {
		body["system"] = s
	}
	if t, ok := job.Params["temperature"].(float64); ok {
		body["temperature"] = t
	}
	if ss, ok := job.Params["stop_sequences"].([]any); ok && len(ss) > 0 {
		body["stop_sequences"] = ss
	}
	raw, _ := json.Marshal(body)

	base := strings.TrimRight(params.StringDefault(job.Params, "base_url", defaultBase), "/")
	endpoint := base + "/v1/messages"

	timeout := time.Duration(params.IntDefault(job.Params, "timeout_ms", 60000)) * time.Millisecond
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, "POST", endpoint, bytes.NewReader(raw))
	if err != nil {
		return params.Err(job, "bad_param", err.Error()), nil
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", apiVersion)

	// Claude hits a fixed vendor endpoint (api.anthropic.com), so no SSRF
	// guard — same posture as the slack/gmail/sheets connectors.
	resp, err := (&http.Client{Timeout: timeout}).Do(req)
	if err != nil {
		return params.Err(job, "claude_http_error", err.Error()), nil
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		code := "claude_api"
		if resp.StatusCode == 429 {
			code = "claude_rate_limited"
		}
		return params.Err(job, code, strconv.Itoa(resp.StatusCode)+" "+claudeError(respBody)), nil
	}

	var parsed map[string]any
	_ = json.Unmarshal(respBody, &parsed)
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"text":     {MIME: "text/plain", Inline: extractText(parsed)},
			"response": {MIME: "application/json", Inline: parsed},
		},
	}, nil
}

// extractText concatenates the text blocks of a Messages API response.
func extractText(parsed map[string]any) string {
	content, ok := parsed["content"].([]any)
	if !ok {
		return ""
	}
	var b strings.Builder
	for _, blk := range content {
		m, ok := blk.(map[string]any)
		if !ok || m["type"] != "text" {
			continue
		}
		if t, ok := m["text"].(string); ok {
			b.WriteString(t)
		}
	}
	return b.String()
}

func claudeError(body []byte) string {
	var e struct {
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &e); err == nil && e.Error.Message != "" {
		return e.Error.Type + ": " + e.Error.Message
	}
	return string(body)
}

// coercePromptText flattens whatever arrived on the prompt input into one
// string: a string passes through; a list joins with blank lines; a
// {value:…} wrapper recurses; any other object is JSON-encoded.
func coercePromptText(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case []byte:
		return string(t)
	case []any:
		var parts []string
		for _, it := range t {
			if s := coercePromptText(it); s != "" {
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, "\n\n")
	case map[string]any:
		if inner, ok := t["value"]; ok {
			return coercePromptText(inner)
		}
		b, _ := json.Marshal(t)
		return string(b)
	default:
		b, _ := json.Marshal(t)
		return string(b)
	}
}
