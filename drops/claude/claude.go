// Package claude hosts the native Claude (Anthropic Messages API)
// connector, migrated from the scripted TS drop. It authenticates with an
// api_key param supplied by the per-tenant Claude app connection — the
// engine injects conn.claude.api_key into the param when the node leaves
// it unset (see ConnectionFields below); there's no OAuth.
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
	hfnet "git.sr.ht/~klahr/hazyflow/drops/net"
	"git.sr.ht/~klahr/hazyflow/engine"
)

const (
	apiVersion  = "2023-06-01"
	defaultBase = "https://api.anthropic.com"
	// maxResponseBytes caps how much of the API response we buffer, so a
	// hostile or buggy upstream (reachable via the base_url override) can't
	// OOM the daemon by streaming an unbounded body.
	maxResponseBytes = 64 << 20 // 64 MiB
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
				{Title: "One-shot summary", Params: json.RawMessage(`{"model":"claude-sonnet-4-6","prompt":"Summarize the upstream text in one sentence.","max_tokens":256}`), Notes: "Wire the text to summarise into the 'prompt' input; params.prompt or params.system is the instruction. The API key comes from the Claude app connection — leave api_key unset."},
				{Title: "System-prompted classifier", Params: json.RawMessage(`{"model":"claude-sonnet-4-6","system":"Reply with exactly 'spam' or 'ham'.","prompt":"Your bank account has been compromised","max_tokens":4,"temperature":0}`)},
			},
			// The Anthropic API key is a per-tenant connection set once on
			// the Claude app page; the engine injects it into the api_key
			// param so a flow author never pastes the key on a node.
			ConnectionFields: []core.ConnectionField{
				{Key: "api_key", Label: "API key", Secret: true, Required: true, Placeholder: "sk-ant-…"},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				{Port: "prompt", Label: "Prompt"},
			},
			Outputs: []core.Port{
				{Port: "text", Label: "Text", MIME: []string{"text/plain"}},
				{Port: "response", Label: "Full response", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"model":{"type":"string","description":"Model id, e.g. claude-sonnet-4-6."},
					"prompt":{"type":"string","format":"multiline","description":"Single user message (used when no 'prompt' input and no params.messages)."},
					"system":{"type":"string","format":"multiline","description":"Optional system prompt."},
					"messages":{"type":"array","items":{},"x_advanced":true,"description":"Full conversation history ({role, content}); overrides params.prompt."},
					"max_tokens":{"type":"integer","default":1024,"minimum":1},
					"temperature":{"type":"number"},
					"stop_sequences":{"type":"array","items":{"type":"string"},"x_advanced":true},
					"api_key":{"type":"string","x_advanced":true,"description":"Anthropic API key. Configured once on the Claude app connection and injected automatically — leave unset on the node."},
					"base_url":{"type":"string","x_advanced":true,"description":"Override the API host."},
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
		return params.Err(job, "bad_param", "no API key — connect Claude on the Apps page to set it"), nil
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

	// base_url is a tenant-overridable param (x_advanced), so the endpoint
	// is NOT fixed — guard the dial like every other connector: the SSRF
	// client blocks loopback/private/link-local targets (cloud metadata,
	// internal services) and the egress allowlist (when the operator sets
	// one) bounds which public hosts the x-api-key may be sent to.
	if err := hfnet.EgressAllowed(endpoint); err != nil {
		return params.Err(job, "egress_blocked", err.Error()), nil
	}
	resp, err := hfnet.SafeHTTPClient(timeout, hfnet.PrivateEgressAllowed()).Do(req)
	if err != nil {
		return params.Err(job, "claude_http_error", err.Error()), nil
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if int64(len(respBody)) > maxResponseBytes {
		return params.Err(job, "claude_http_error", "response exceeds "+strconv.Itoa(maxResponseBytes)+" bytes"), nil
	}
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
