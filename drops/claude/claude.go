// Package claude is the Claude (Anthropic Messages API) provider for the
// shared llmtask core. It implements one Provider — the vendor API call +
// response parsing — and registers the five task-shaped drops (Ask,
// Summarize, Extract, Classify, Draft reply) under the "Claude" integration
// via llmtask.RegisterAll. The task UX + manifests live in llmtask; only the
// Anthropic-specific bits live here. ChatGPT is the sibling package
// drops/openai, sharing the same core.
package claude

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/drops/internal/llmtask"
	"git.sr.ht/~klahr/dazyflow/internal/llm"
)

// claudeModels is the model picker, shared by the task drops (llmtask) and
// the shared LLM registry (internal/llm) so both list the same set.
var claudeModels = []llmtask.ModelOption{
	{ID: "claude-opus-4-8", Label: "Claude Opus 4.8"},
	{ID: "claude-sonnet-4-6", Label: "Claude Sonnet 4.6"},
	{ID: "claude-haiku-4-5-20251001", Label: "Claude Haiku 4.5"},
}

const (
	apiVersion   = "2023-06-01"
	defaultBase  = "https://api.anthropic.com"
	defaultModel = "claude-sonnet-4-6"
)

type provider struct{}

// Call sends one single-turn Anthropic Messages request and normalizes the
// response into an llmtask.Result. Forced tools map to Anthropic's
// tools + tool_choice; the response's tool_use block carries the input.
func (provider) Call(ctx context.Context, apiKey string, req llmtask.Request) (llmtask.Result, *core.JobError) {
	model := req.Model
	if model == "" {
		model = defaultModel
	}
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 1024
	}

	var messages []any
	if len(req.Messages) > 0 {
		messages = req.Messages
	} else {
		messages = []any{map[string]any{"role": "user", "content": req.UserText}}
	}

	body := map[string]any{"model": model, "max_tokens": maxTokens, "messages": messages}
	if req.System != "" {
		body["system"] = req.System
	}
	if req.Temperature != nil {
		body["temperature"] = *req.Temperature
	}
	if req.Tool != nil {
		body["tools"] = []any{map[string]any{
			"name": req.Tool.Name, "description": req.Tool.Description, "input_schema": req.Tool.Schema,
		}}
		body["tool_choice"] = map[string]any{"type": "tool", "name": req.Tool.Name}
	}
	raw, _ := json.Marshal(body)

	base := strings.TrimRight(req.BaseURL, "/")
	if base == "" {
		base = defaultBase
	}
	status, respBody, jerr := llmtask.PostJSON(ctx, base+"/v1/messages", map[string]string{
		"content-type": "application/json", "x-api-key": apiKey, "anthropic-version": apiVersion,
	}, raw, req.TimeoutMS)
	if jerr != nil {
		return llmtask.Result{}, jerr
	}
	if status < 200 || status >= 300 {
		return llmtask.Result{}, llmtask.HTTPError("claude", "Claude", status, claudeError(respBody))
	}

	var parsed map[string]any
	_ = json.Unmarshal(respBody, &parsed)
	res := llmtask.Result{Raw: parsed, Text: extractText(parsed)}
	if req.Tool != nil {
		res.Tool = extractToolInput(parsed, req.Tool.Name)
	}
	return res, nil
}

func init() {
	// Shared LLM registry: makes this provider available to editor/platform
	// features (the render_template AI assist) — not just the flow drops.
	llm.Register(llm.ProviderInfo{
		Name:         "claude",
		Integration:  "Claude",
		DefaultModel: defaultModel,
		Models:       claudeModels,
		Provider:     provider{},
	})
	llmtask.RegisterAll(llmtask.Config{
		Provider:       provider{},
		Integration:    "Claude",
		Icon:           "claude",
		Color:          "#cc7755",
		DefaultModel:   defaultModel,
		Models:         claudeModels,
		KeyPlaceholder: "sk-ant-…",
		AskID:          "claude",
		TaskIDPrefix:   "claude",
		VerifyKey:      verifyKey,
	})
}

// verifyKey checks an Anthropic API key by listing models — a free,
// read-only GET (no tokens spent, nothing generated). 200 means the key is
// valid; 401/403 means it was rejected. Used by the Apps page to test the
// connection before saving it.
func verifyKey(ctx context.Context, apiKey, base string) error {
	base = strings.TrimRight(base, "/")
	if base == "" {
		base = defaultBase
	}
	status, body, err := llmtask.GetStatus(ctx, base+"/v1/models", map[string]string{
		"x-api-key": apiKey, "anthropic-version": apiVersion,
	})
	if err != nil {
		return fmt.Errorf("could not reach Claude: %w", err)
	}
	switch {
	case status == 401 || status == 403:
		return errors.New("Claude rejected the API key")
	case status < 200 || status >= 300:
		return fmt.Errorf("Claude returned HTTP %d: %s", status, claudeError(body))
	}
	return nil
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

// extractToolInput returns the input map of the first tool_use block matching
// name (the forced tool), or nil if the model didn't call it.
func extractToolInput(parsed map[string]any, name string) map[string]any {
	content, ok := parsed["content"].([]any)
	if !ok {
		return nil
	}
	for _, blk := range content {
		m, ok := blk.(map[string]any)
		if !ok || m["type"] != "tool_use" {
			continue
		}
		if name != "" && m["name"] != name {
			continue
		}
		if in, ok := m["input"].(map[string]any); ok {
			return in
		}
	}
	return nil
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
