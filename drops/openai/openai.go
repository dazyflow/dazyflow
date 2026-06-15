// Package openai is the ChatGPT (OpenAI Chat Completions API) provider for the
// shared llmtask core — the sibling of drops/claude. It implements one
// Provider (the OpenAI API call + response parsing) and registers the five
// task-shaped drops under the "ChatGPT" integration via llmtask.RegisterAll.
// All the task UX + manifests are shared from llmtask; only the OpenAI-specific
// request/response shape (chat messages, function tool-calls) lives here.
package openai

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"git.sr.ht/~klahr/hazyflow/core"
	"git.sr.ht/~klahr/hazyflow/drops/internal/llmtask"
)

const (
	defaultBase  = "https://api.openai.com"
	defaultModel = "gpt-4o-mini"
)

type provider struct{}

// Call sends one Chat Completions request and normalizes the response. Forced
// tools map to OpenAI's tools (type:function) + tool_choice; the chosen
// tool_call's arguments (a JSON string) decode into the result's Tool map.
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
		messages = []any{}
		if req.System != "" {
			messages = append(messages, map[string]any{"role": "system", "content": req.System})
		}
		messages = append(messages, map[string]any{"role": "user", "content": req.UserText})
	}

	body := map[string]any{"model": model, "messages": messages, "max_tokens": maxTokens}
	if req.Temperature != nil {
		body["temperature"] = *req.Temperature
	}
	if req.Tool != nil {
		body["tools"] = []any{map[string]any{
			"type": "function",
			"function": map[string]any{
				"name": req.Tool.Name, "description": req.Tool.Description, "parameters": req.Tool.Schema,
			},
		}}
		body["tool_choice"] = map[string]any{"type": "function", "function": map[string]any{"name": req.Tool.Name}}
	}
	raw, _ := json.Marshal(body)

	base := strings.TrimRight(req.BaseURL, "/")
	if base == "" {
		base = defaultBase
	}
	status, respBody, jerr := llmtask.PostJSON(ctx, base+"/v1/chat/completions", map[string]string{
		"content-type": "application/json", "authorization": "Bearer " + apiKey,
	}, raw, req.TimeoutMS)
	if jerr != nil {
		return llmtask.Result{}, jerr
	}
	if status < 200 || status >= 300 {
		code := "openai_api"
		if status == 429 {
			code = "openai_rate_limited"
		}
		return llmtask.Result{}, &core.JobError{Code: code, Message: strconv.Itoa(status) + " " + openaiError(respBody)}
	}

	var parsed map[string]any
	_ = json.Unmarshal(respBody, &parsed)
	res := llmtask.Result{Raw: parsed, Text: extractText(parsed)}
	if req.Tool != nil {
		res.Tool = extractToolArgs(parsed)
	}
	return res, nil
}

func init() {
	llmtask.RegisterAll(llmtask.Config{
		Provider:     provider{},
		Integration:  "ChatGPT",
		Icon:         "openai",
		Color:        "#10a37f",
		DefaultModel: defaultModel,
		Models: []llmtask.ModelOption{
			{ID: "gpt-4o", Label: "GPT-4o"},
			{ID: "gpt-4o-mini", Label: "GPT-4o mini"},
		},
		KeyPlaceholder: "sk-…",
		AskID:          "chatgpt",
		TaskIDPrefix:   "gpt",
		// New provider — no legacy ids to alias.
	})
}

// message returns choices[0].message, or nil.
func message(parsed map[string]any) map[string]any {
	choices, ok := parsed["choices"].([]any)
	if !ok || len(choices) == 0 {
		return nil
	}
	c, ok := choices[0].(map[string]any)
	if !ok {
		return nil
	}
	m, _ := c["message"].(map[string]any)
	return m
}

// extractText reads the assistant message content.
func extractText(parsed map[string]any) string {
	m := message(parsed)
	if m == nil {
		return ""
	}
	if s, ok := m["content"].(string); ok {
		return s
	}
	return ""
}

// extractToolArgs decodes the first tool_call's function.arguments (a JSON
// string) into a map, or nil if the model didn't call the tool.
func extractToolArgs(parsed map[string]any) map[string]any {
	m := message(parsed)
	if m == nil {
		return nil
	}
	calls, ok := m["tool_calls"].([]any)
	if !ok || len(calls) == 0 {
		return nil
	}
	call, ok := calls[0].(map[string]any)
	if !ok {
		return nil
	}
	fn, ok := call["function"].(map[string]any)
	if !ok {
		return nil
	}
	argStr, ok := fn["arguments"].(string)
	if !ok || argStr == "" {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(argStr), &out); err != nil {
		return nil
	}
	return out
}

func openaiError(body []byte) string {
	var e struct {
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &e); err == nil && e.Error.Message != "" {
		if e.Error.Type != "" {
			return e.Error.Type + ": " + e.Error.Message
		}
		return e.Error.Message
	}
	return string(body)
}
