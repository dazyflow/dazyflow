// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package ollama is the local-model provider for the shared llmtask core —
// the sibling of drops/claude and drops/openai, and the first that talks to
// something the operator runs themselves.
//
// It exists so a self-hosted dazyflow is not obliged to hold an account with a
// US model vendor to use the AI steps at all. The task UX is unchanged: the
// same five steps, the same manifests, a different endpoint.
//
// Ollama serves an OpenAI-compatible /v1/chat/completions, so the request and
// response shapes here mirror drops/openai deliberately rather than using the
// native /api/chat. Two things genuinely differ, and both are why llmtask grew
// KeyOptional and BaseURLLabel:
//
//   - There is no API key. Ollama is a process on a machine the operator
//     controls; authentication is the network boundary, not a bearer token. A
//     key is still accepted, because a shared instance is often fronted by a
//     reverse proxy that wants one.
//   - There is no model catalog. Models are whatever the operator has pulled,
//     so the step's model field is free text rather than a picker, and the
//     default below is only a guess at the most likely one.
//
// Reaching a localhost Ollama needs the operator to have set
// DAZYFLOW_ALLOW_PRIVATE_EGRESS — the SSRF guard blocks private addresses by
// default and that default is right. Without it the connection test fails
// with a clear egress_blocked rather than a timeout.
package ollama

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/internal/chatcompletion"
	"github.com/dazyflow/dazyflow/drops/internal/llmtask"
	"github.com/dazyflow/dazyflow/internal/llm"
)

const (
	// defaultBase is where `ollama serve` listens out of the box. It is a
	// default rather than a requirement — the connection's Server URL field
	// points at a remote instance just as well.
	defaultBase = "http://localhost:11434"
	// defaultModel is a guess, not a catalog. llama3.1 is picked because it is
	// widely pulled AND supports tool calls, which the Extract fields and
	// Classify steps need; a text-only model runs the other three steps fine.
	defaultModel = "llama3.1"
)

type provider struct{}

// Call sends one Chat Completions request to Ollama's OpenAI-compatible
// endpoint and normalizes the response.
//
// Tool handling is where this diverges from drops/openai. Ollama accepts
// `tools` and returns `tool_calls` for models that implement them, but honors
// a forced `tool_choice` inconsistently across models — so a model may answer
// a forced tool call with ordinary prose containing the JSON instead. We ask
// for the tool the same way, then fall back to reading JSON out of the message
// content, which turns "the model ignored tool_choice" from a failed step into
// a working one. A model with no tool support at all still fails, and says so.
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
		user := map[string]any{"role": "user", "content": req.UserText}
		// Ollama's chat API takes images as bare base64 strings in an
		// `images` array on the message — not content parts, and not a data:
		// URI. Only images: llmtask refuses documents before we get here
		// (FilesImagesOnly), so anything arriving is a picture.
		if imgs := imageData(req.Files); len(imgs) > 0 {
			user["images"] = imgs
		}
		messages = append(messages, user)
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

	headers := map[string]string{"content-type": "application/json"}
	// Ollama itself ignores the header; a reverse proxy in front of a shared
	// instance may not. Sent only when the operator actually set a key, so the
	// common keyless case does not present an empty bearer.
	if apiKey != "" {
		headers["authorization"] = "Bearer " + apiKey
	}

	status, respBody, jerr := llmtask.PostJSON(ctx, baseOr(req.BaseURL)+"/v1/chat/completions", headers, raw, req.TimeoutMS)
	if jerr != nil {
		return llmtask.Result{}, jerr
	}
	if status < 200 || status >= 300 {
		return llmtask.Result{}, llmtask.HTTPError("ollama", "Ollama", status, ollamaError(respBody))
	}

	var parsed map[string]any
	_ = json.Unmarshal(respBody, &parsed)
	res := llmtask.Result{Raw: parsed, Text: chatcompletion.Text(parsed)}
	if req.Tool != nil {
		res.Tool = chatcompletion.ToolArgs(parsed)
		if res.Tool == nil {
			// The forced call was ignored — try the prose.
			res.Tool = jsonFromText(res.Text)
		}
		if res.Tool == nil {
			return llmtask.Result{}, &core.JobError{
				Code:    "ollama_no_tool_call",
				Message: fmt.Sprintf("the model %q did not return structured output — this step needs a tool-capable model (llama3.1, qwen2.5, mistral-nemo and similar). Pick one on the step, or use the Ask step for free text.", model),
			}
		}
	}
	return res, nil
}

func baseOr(base string) string {
	if b := strings.TrimRight(strings.TrimSpace(base), "/"); b != "" {
		return b
	}
	return defaultBase
}

func init() {
	llm.Register(llm.ProviderInfo{
		Name:         "ollama",
		Integration:  "Ollama",
		DefaultModel: defaultModel,
		// No Models: there is no catalog to compile in, because it is whatever
		// this operator pulled. ListModels asks the server instead, so a
		// reachable Ollama gets a real picker of exactly what it can run, and
		// an unreachable one falls back to free text rather than to a guess.
		ListModels: listModels,
		Provider:   provider{},
	})
	llmtask.RegisterAll(llmtask.Config{
		Provider:    provider{},
		FileSupport: llmtask.FilesImagesOnly,
		Integration: "Ollama",
		// Ollama's own llama mark, traced from the logo they serve at
		// ollama.com/public/ollama.png — see web/src/components/brand/OllamaIcon.tsx.
		// Color is the slate the static docs copy of the mark is baked in; the
		// in-app component inherits the text colour instead, so it stays legible
		// on both themes.
		Icon:         "ollama",
		Color:        "#4b5563",
		DefaultModel: defaultModel,
		AskID:        "ollama",
		TaskIDPrefix: "ollama",
		// Keyless by default, and the host lives on the connection: both are
		// the point of a local runtime. See the llmtask.Config docs.
		KeyOptional:        true,
		KeyPlaceholder:     "only if your instance is behind a proxy",
		BaseURLLabel:       "Server URL",
		BaseURLPlaceholder: defaultBase,
		BaseURLHelp:        "Where Ollama is listening. A localhost address also needs DAZYFLOW_ALLOW_PRIVATE_EGRESS set on the daemon.",
		VerifyKey:          verifyReachable,
	})
}

// verifyReachable is the connection test. For a cloud vendor this asks "is
// this key valid"; for Ollama there is usually no key, so it asks the question
// that actually fails in practice — can the daemon reach this server at all,
// and does it have any models pulled. GET /api/tags is free and read-only.
func verifyReachable(ctx context.Context, apiKey, base string) error {
	headers := map[string]string{}
	if apiKey != "" {
		headers["authorization"] = "Bearer " + apiKey
	}
	status, body, err := llmtask.GetStatus(ctx, baseOr(base)+"/api/tags", headers)
	if err != nil {
		return fmt.Errorf("could not reach Ollama at %s: %w", baseOr(base), err)
	}
	switch {
	case status == 401 || status == 403:
		return errors.New("Ollama rejected the API key")
	case status < 200 || status >= 300:
		return fmt.Errorf("Ollama returned HTTP %d: %s", status, ollamaError(body))
	}
	if n := modelCount(body); n == 0 {
		return errors.New("Ollama is reachable but has no models pulled — run `ollama pull llama3.1` on that machine first")
	}
	return nil
}

// modelCount reads the length of /api/tags' models array. -1 when the body is
// not the shape we expect, which the caller treats as "don't complain".
func modelCount(body []byte) int {
	var tags struct {
		Models []json.RawMessage `json:"models"`
	}
	if err := json.Unmarshal(body, &tags); err != nil {
		return -1
	}
	return len(tags.Models)
}

// jsonFromText salvages a JSON object from an ordinary reply — the fallback
// for a model that ignored tool_choice and answered in prose. It takes the
// first balanced {...} span, so a fenced ```json block or a sentence of
// preamble around the object still yields the object.
//
// Deliberately only a brace scan: anything cleverer would be guessing at what
// the model meant, and a wrong guess here becomes wrong data in a flow.
func jsonFromText(text string) map[string]any {
	start := strings.IndexByte(text, '{')
	if start < 0 {
		return nil
	}
	depth, inStr, esc := 0, false, false
	for i := start; i < len(text); i++ {
		c := text[i]
		switch {
		case esc:
			esc = false
		case inStr && c == '\\':
			esc = true
		case c == '"':
			inStr = !inStr
		case inStr:
			// nothing: braces inside a string are not structure
		case c == '{':
			depth++
		case c == '}':
			depth--
			if depth == 0 {
				var out map[string]any
				if err := json.Unmarshal([]byte(text[start:i+1]), &out); err == nil {
					return out
				}
				return nil
			}
		}
	}
	return nil
}

// ollamaError pulls the message out of Ollama's error body ({"error":"..."}),
// falling back to the raw body. Note the shape differs from OpenAI's nested
// {"error":{"message":...}} even on the compatibility endpoint, so both are
// tried before giving up.
func ollamaError(body []byte) string {
	var flat struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &flat); err == nil && flat.Error != "" {
		return flat.Error
	}
	var nested struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &nested); err == nil && nested.Error.Message != "" {
		return nested.Error.Message
	}
	return string(body)
}

// listModels asks this Ollama which models are pulled. Backs the model picker.
//
// Ollama is the provider where a live list matters most, because there is no
// catalog to fall back to: defaultModel is a guess at what is commonly pulled,
// and on a machine without it every step fails with a 404 naming a model the
// operator never chose. /api/tags is the same free GET verifyReachable already
// makes, and its answer is exactly the set of models that can run here.
func listModels(ctx context.Context, apiKey, base string) ([]llm.ModelOption, error) {
	headers := map[string]string{}
	if apiKey != "" {
		headers["authorization"] = "Bearer " + apiKey
	}
	status, body, err := llmtask.GetStatus(ctx, baseOr(base)+"/api/tags", headers)
	if err != nil {
		return nil, fmt.Errorf("could not reach Ollama at %s: %w", baseOr(base), err)
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("Ollama returned HTTP %d: %s", status, ollamaError(body))
	}
	var tags struct {
		Models []struct {
			Name  string `json:"name"`
			Model string `json:"model"`
		} `json:"models"`
	}
	if err := json.Unmarshal(body, &tags); err != nil {
		return nil, fmt.Errorf("could not read Ollama's model list: %w", err)
	}
	out := make([]llm.ModelOption, 0, len(tags.Models))
	for _, m := range tags.Models {
		id := m.Name
		if id == "" {
			id = m.Model
		}
		if id == "" {
			continue
		}
		// The tag IS the name here — "qwen3-coder:30b" is what the operator
		// typed to pull it and what they will recognise. Nothing to prettify.
		out = append(out, llm.ModelOption{ID: id, Label: id})
	}
	if len(out) == 0 {
		return nil, errors.New("Ollama is reachable but has no models pulled")
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// imageData renders the request's images as the bare base64 strings Ollama
// wants. A non-image can't get this far — checkFileSupport refuses it with a
// message naming the provider — so it is skipped rather than mangled.
func imageData(files []llm.File) []any {
	out := make([]any, 0, len(files))
	for _, f := range files {
		if !f.IsImage() {
			continue
		}
		out = append(out, base64.StdEncoding.EncodeToString(f.Data))
	}
	return out
}
