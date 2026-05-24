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
)

const (
	claudeDefaultModel     = "claude-sonnet-4-6"
	claudeDefaultMaxTokens = 1024
	claudeDefaultTimeoutMS = 60_000
	claudeAPIVersion       = "2023-06-01"
	claudeDefaultBaseURL   = "https://api.anthropic.com"
)

func init() {
	engine.Register(engine.NativeNode{
		Manifest: core.Manifest{
			ID:             "claude",
			Version:        "1.0",
			Label:          "Claude (Anthropic Messages API)",
			Color:          "#cc7755",
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
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"model":{"type":"string"},
					"system":{"type":"string"},
					"messages":{"type":"array"},
					"max_tokens":{"type":"integer","minimum":1},
					"temperature":{"type":"number","minimum":0,"maximum":1},
					"stop_sequences":{"type":"array","items":{"type":"string"}},
					"api_key":{"type":"string"},
					"base_url":{"type":"string"},
					"timeout_ms":{"type":"integer","minimum":1}
				},
				"required":["api_key"]
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
	apiKey, err := paramString(job.Params, "api_key")
	if err != nil {
		return errResult(job, "bad_param", err.Error()), nil
	}
	if apiKey == "" {
		return errResult(job, "bad_param", "api_key is required (use a secret reference like env://ANTHROPIC_API_KEY)"), nil
	}

	body, err := buildClaudeRequest(job)
	if err != nil {
		return errResult(job, "bad_param", err.Error()), nil
	}
	if len(body.Messages) == 0 {
		return errResult(job, "bad_input", "no messages — provide params.messages or the prompt input port"), nil
	}

	baseURL := paramStringDefault(job.Params, "base_url", claudeDefaultBaseURL)
	timeoutMs := paramIntDefault(job.Params, "timeout_ms", claudeDefaultTimeoutMS)

	emitProgress(progress, job, 0.1, fmt.Sprintf("calling %s/v1/messages (model=%s)", baseURL, body.Model))

	reqJSON, err := json.Marshal(body)
	if err != nil {
		return errResult(job, "marshal", err.Error()), nil
	}

	req, err := http.NewRequestWithContext(ctx, "POST",
		strings.TrimRight(baseURL, "/")+"/v1/messages",
		bytes.NewReader(reqJSON))
	if err != nil {
		return errResult(job, "bad_url", err.Error()), nil
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", claudeAPIVersion)

	client := &http.Client{Timeout: time.Duration(timeoutMs) * time.Millisecond}
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return errResult(job, "cancelled", ctx.Err().Error()), ctx.Err()
		}
		return errResult(job, "http", err.Error()), nil
	}
	defer resp.Body.Close()

	rawResp, err := io.ReadAll(resp.Body)
	if err != nil {
		return errResult(job, "io", err.Error()), nil
	}

	if resp.StatusCode >= 400 {
		var env claudeAPIErrorEnvelope
		if err := json.Unmarshal(rawResp, &env); err == nil && env.Error.Message != "" {
			code := "claude_api"
			if resp.StatusCode == 429 {
				code = "claude_rate_limited"
			}
			return errResult(job, code, fmt.Sprintf("%d %s: %s", resp.StatusCode, env.Error.Type, env.Error.Message)), nil
		}
		return errResult(job, "claude_api", fmt.Sprintf("HTTP %d: %s", resp.StatusCode, string(rawResp))), nil
	}

	var parsed claudeResponse
	if err := json.Unmarshal(rawResp, &parsed); err != nil {
		return errResult(job, "unmarshal", err.Error()), nil
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
//   - input.prompt (a string) → single user message, overrides params.messages
//   - params.messages → used as-is
//   - params.prompt (a string, fallback) → single user message
func buildClaudeRequest(job core.Job) (claudeRequest, error) {
	req := claudeRequest{
		Model:     paramStringDefault(job.Params, "model", claudeDefaultModel),
		System:    paramStringDefault(job.Params, "system", ""),
		MaxTokens: paramIntDefault(job.Params, "max_tokens", claudeDefaultMaxTokens),
	}

	if t, ok := paramFloat(job.Params, "temperature"); ok {
		req.Temperature = &t
	}
	if stops, ok := paramStringSlice(job.Params, "stop_sequences"); ok {
		req.StopSequences = stops
	}

	// Resolve messages with documented precedence.
	if input, ok := job.Input["prompt"]; ok {
		if s, ok := input.Inline.(string); ok && s != "" {
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
		if s, ok := paramStringOpt(job.Params, "prompt"); ok && s != "" {
			req.Messages = []claudeMessage{{Role: "user", Content: s}}
		}
	}

	return req, nil
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
