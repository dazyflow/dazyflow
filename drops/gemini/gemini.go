// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// The Gemini provider for the shared llmtask core.
package gemini

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/internal/llmtask"
	"github.com/dazyflow/dazyflow/internal/llm"
)

var geminiModels = []llmtask.ModelOption{
	{ID: "gemini-flash-latest", Label: "Gemini Flash (latest)"},
	{ID: "gemini-flash-lite-latest", Label: "Gemini Flash-Lite (latest)"},
	{ID: "gemini-pro-latest", Label: "Gemini Pro (latest)"},
	{ID: "gemini-3.7-flash", Label: "Gemini 3.7 Flash"},
	{ID: "gemini-3.6-flash", Label: "Gemini 3.6 Flash"},
	{ID: "gemini-3.5-flash", Label: "Gemini 3.5 Flash"},
	{ID: "gemini-3.5-flash-lite", Label: "Gemini 3.5 Flash-Lite"},
	{ID: "gemini-3.1-flash-lite", Label: "Gemini 3.1 Flash-Lite"},
	{ID: "gemini-3.1-pro-preview", Label: "Gemini 3.1 Pro (preview)"},
	{ID: "gemini-2.5-flash", Label: "Gemini 2.5 Flash"},
	{ID: "gemini-2.5-flash-lite", Label: "Gemini 2.5 Flash-Lite"},
}

const (
	defaultBase = "https://generativelanguage.googleapis.com"
	// Flash rather than Pro on purpose: these drops run per-row, so cost compounds.
	defaultModel = "gemini-flash-latest"
)

type provider struct{}

func (provider) Call(ctx context.Context, apiKey string, req llmtask.Request) (llmtask.Result, *core.JobError) {
	model := req.Model
	if model == "" {
		model = defaultModel
	}
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 1024
	}

	contents, system := toContents(req)
	if system == "" {
		system = req.System
	}

	genCfg := map[string]any{"maxOutputTokens": maxTokens}
	if req.Temperature != nil {
		genCfg["temperature"] = *req.Temperature
	}
	body := map[string]any{"contents": contents, "generationConfig": genCfg}
	if system != "" {
		body["systemInstruction"] = map[string]any{"parts": []any{map[string]any{"text": system}}}
	}
	if req.Tool != nil {
		body["tools"] = []any{map[string]any{
			"functionDeclarations": []any{map[string]any{
				"name": req.Tool.Name, "description": req.Tool.Description, "parameters": req.Tool.Schema,
			}},
		}}
		body["toolConfig"] = map[string]any{"functionCallingConfig": map[string]any{
			"mode": "ANY", "allowedFunctionNames": []any{req.Tool.Name},
		}}
	}
	raw, _ := json.Marshal(body)

	endpoint := baseOr(req.BaseURL) + "/v1beta/models/" + url.PathEscape(model) + ":generateContent"
	status, respBody, jerr := llmtask.PostJSON(ctx, endpoint, map[string]string{
		"content-type": "application/json", "x-goog-api-key": apiKey,
	}, raw, req.TimeoutMS)
	if jerr != nil {
		return llmtask.Result{}, jerr
	}
	if status < 200 || status >= 300 {
		return llmtask.Result{}, llmtask.HTTPError("gemini", "Gemini", status, geminiError(respBody))
	}

	var parsed map[string]any
	_ = json.Unmarshal(respBody, &parsed)
	res := llmtask.Result{Raw: parsed, Text: extractText(parsed)}
	if req.Tool != nil {
		res.Tool = extractToolArgs(parsed, req.Tool.Name)
		if res.Tool == nil {
			return llmtask.Result{}, &core.JobError{
				Code:    "gemini_no_tool_call",
				Message: "Gemini returned no structured output" + finishHint(parsed) + " — try a shorter input, or use the Ask step for free text.",
			}
		}
	}
	return res, nil
}

// Gemini has no system role: the system message becomes systemInstruction, and a
// tool result rides as a functionResponse part rather than a message.
func toContents(req llmtask.Request) (contents []any, system string) {
	if len(req.Messages) == 0 {
		if len(req.Files) > 0 {
			return []any{map[string]any{"role": "user", "parts": userParts(req)}}, ""
		}
		return []any{textTurn("user", req.UserText)}, ""
	}
	var systems []string
	for _, m := range req.Messages {
		msg, ok := m.(map[string]any)
		if !ok {
			continue
		}
		text := stringOf(msg["content"])
		role, _ := msg["role"].(string)
		switch role {
		case "system":
			if text != "" {
				systems = append(systems, text)
			}
		case "assistant", "model":
			contents = append(contents, textTurn("model", text))
		default:
			contents = append(contents, textTurn("user", text))
		}
	}
	if len(contents) == 0 {
		contents = append(contents, textTurn("user", req.UserText))
	}
	return contents, strings.Join(systems, "\n\n")
}

func textTurn(role, text string) map[string]any {
	return map[string]any{"role": role, "parts": []any{map[string]any{"text": text}}}
}

// Gemini's parts are typed, so a structured content block must be flattened.
func stringOf(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	default:
		b, _ := json.Marshal(t)
		return string(b)
	}
}

func baseOr(base string) string {
	if b := strings.TrimRight(strings.TrimSpace(base), "/"); b != "" {
		return b
	}
	return defaultBase
}

func init() {
	llm.Register(llm.ProviderInfo{
		Name:         "gemini",
		Integration:  "Gemini",
		DefaultModel: defaultModel,
		Models:       geminiModels,
		ListModels:   listModels,
		Provider:     provider{},
	})
	llmtask.RegisterAll(llmtask.Config{
		Provider:       provider{},
		FileSupport:    llmtask.FilesDocuments,
		Integration:    "Gemini",
		Icon:           "gemini",
		Color:          "#4893fc",
		DefaultModel:   defaultModel,
		Models:         geminiModels,
		KeyPlaceholder: "AIza…",
		AskID:          "gemini",
		TaskIDPrefix:   "gemini",
		VerifyKey:      verifyKey,
	})
}

func verifyKey(ctx context.Context, apiKey, base string) error {
	status, body, err := llmtask.GetStatus(ctx, baseOr(base)+"/v1beta/models", map[string]string{
		"x-goog-api-key": apiKey,
	})
	if err != nil {
		return fmt.Errorf("could not reach Gemini: %w", err)
	}
	switch {
	case status == 400 || status == 401 || status == 403:
		return errors.New("Gemini rejected the API key — check it in Google AI Studio")
	case status < 200 || status >= 300:
		return fmt.Errorf("Gemini returned HTTP %d: %s", status, geminiError(body))
	}
	return nil
}

func parts(parsed map[string]any) []any {
	candidates, ok := parsed["candidates"].([]any)
	if !ok || len(candidates) == 0 {
		return nil
	}
	c, ok := candidates[0].(map[string]any)
	if !ok {
		return nil
	}
	content, ok := c["content"].(map[string]any)
	if !ok {
		return nil
	}
	p, _ := content["parts"].([]any)
	return p
}

func extractText(parsed map[string]any) string {
	var out []string
	for _, p := range parts(parsed) {
		part, ok := p.(map[string]any)
		if !ok {
			continue
		}
		if s, ok := part["text"].(string); ok && s != "" {
			out = append(out, s)
		}
	}
	return strings.Join(out, "")
}

func extractToolArgs(parsed map[string]any, name string) map[string]any {
	for _, p := range parts(parsed) {
		part, ok := p.(map[string]any)
		if !ok {
			continue
		}
		call, ok := part["functionCall"].(map[string]any)
		if !ok {
			continue
		}
		// ANY mode naming one function forces the call rather than suggesting it.
		if n, _ := call["name"].(string); n != name {
			continue
		}
		if args, ok := call["args"].(map[string]any); ok {
			return args
		}
	}
	return nil
}

func finishHint(parsed map[string]any) string {
	candidates, ok := parsed["candidates"].([]any)
	if !ok || len(candidates) == 0 {
		return ""
	}
	c, ok := candidates[0].(map[string]any)
	if !ok {
		return ""
	}
	switch reason, _ := c["finishReason"].(string); reason {
	case "", "STOP":
		return ""
	case "MAX_TOKENS":
		return " (it ran out of output budget — raise max_tokens on the step)"
	case "SAFETY", "PROHIBITED_CONTENT":
		return " (it stopped on a safety filter)"
	default:
		return " (it stopped early: " + reason + ")"
	}
}

func geminiError(body []byte) string {
	var e struct {
		Error struct {
			Status  string `json:"status"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &e); err == nil && e.Error.Message != "" {
		if e.Error.Status != "" {
			return e.Error.Status + ": " + e.Error.Message
		}
		return e.Error.Message
	}
	return string(body)
}

// Models that answer generateContent but cannot do text, so they must be hidden.
var nonTextFamilies = []string{
	"-tts", "tts-", "image", "nano-banana", "transcribe", "robotics",
	"computer-use", "lyria", "embedding", "aqa",
}

// What this key may actually call, which is not the same as what exists.
func listModels(ctx context.Context, apiKey, base string) ([]llm.ModelOption, error) {
	var out []llm.ModelOption
	seen := map[string]bool{}
	token := ""
	for fetched := 0; fetched < 8; fetched++ {
		u := baseOr(base) + "/v1beta/models?pageSize=200"
		if token != "" {
			u += "&pageToken=" + url.QueryEscape(token)
		}
		status, body, err := llmtask.GetStatus(ctx, u, map[string]string{"x-goog-api-key": apiKey})
		if err != nil {
			return nil, fmt.Errorf("could not reach Gemini: %w", err)
		}
		if status < 200 || status >= 300 {
			return nil, fmt.Errorf("Gemini returned HTTP %d: %s", status, geminiError(body))
		}
		var resp struct {
			Models []struct {
				Name        string   `json:"name"`
				DisplayName string   `json:"displayName"`
				Methods     []string `json:"supportedGenerationMethods"`
			} `json:"models"`
			NextPageToken string `json:"nextPageToken"`
		}
		if err := json.Unmarshal(body, &resp); err != nil {
			return nil, fmt.Errorf("could not read Gemini's model list: %w", err)
		}
		for _, m := range resp.Models {
			id := strings.TrimPrefix(m.Name, "models/")
			if id == "" || seen[id] || !generates(m.Methods) || nonText(id) {
				continue
			}
			seen[id] = true
			label := m.DisplayName
			if label == "" {
				label = id
			}
			out = append(out, llm.ModelOption{ID: id, Label: label})
		}
		if resp.NextPageToken == "" {
			break
		}
		token = resp.NextPageToken
	}
	if len(out) == 0 {
		return nil, errors.New("Gemini listed no models this key can generate with")
	}
	// Aliases first: they stay correct as Google rolls versions forward.
	sort.SliceStable(out, func(i, j int) bool {
		return strings.HasSuffix(out[i].ID, "-latest") && !strings.HasSuffix(out[j].ID, "-latest")
	})
	return out, nil
}

func generates(methods []string) bool {
	for _, m := range methods {
		if m == "generateContent" {
			return true
		}
	}
	return false
}

func nonText(id string) bool {
	for _, f := range nonTextFamilies {
		if strings.Contains(id, f) {
			return true
		}
	}
	return false
}

func userParts(req llmtask.Request) []any {
	parts := make([]any, 0, len(req.Files)+1)
	for _, f := range req.Files {
		parts = append(parts, map[string]any{
			"inline_data": map[string]any{
				"mime_type": mediaType(f.MIME),
				"data":      base64.StdEncoding.EncodeToString(f.Data),
			},
		})
	}
	if strings.TrimSpace(req.UserText) != "" {
		parts = append(parts, map[string]any{"text": req.UserText})
	}
	return parts
}

func mediaType(mime string) string {
	return strings.TrimSpace(strings.SplitN(mime, ";", 2)[0])
}
