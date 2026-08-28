// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package gemini is the Google Gemini provider for the shared llmtask core —
// the sibling of drops/claude, drops/openai and drops/ollama. It implements
// one Provider (the vendor API call + response parsing) and registers the five
// task-shaped drops under the "Gemini" integration via llmtask.RegisterAll.
// The task UX and manifests are shared; only Gemini's request/response shape
// lives here.
//
// It is the odd one out of the four, because Gemini is the only vendor here
// that does NOT speak the OpenAI chat shape. Three things have to be
// translated, and they are the whole of this file's substance:
//
//   - The model is in the URL, not the body: POST /v1beta/models/<model>:generateContent.
//     So an unknown model is a 404 on the path rather than a 400 about a field.
//   - Turns are `contents[{role,parts[{text}]}]` with the assistant's role
//     spelled "model", and the system prompt is a separate `systemInstruction`
//     rather than a turn. llmtask hands us OpenAI-shaped Messages, so
//     toContents does that conversion — see its comment for why a stray
//     "system" turn is hoisted rather than dropped.
//   - A forced tool is `tools[].functionDeclarations` plus a toolConfig in ANY
//     mode naming the one function, and the call comes back as a
//     `functionCall` PART of the reply rather than a separate field. The
//     schemas llmtask builds (type/properties/description/enum/required) are
//     already inside the OpenAPI subset Gemini accepts, so they pass through
//     untranslated.
//
// The API key rides the x-goog-api-key header rather than ?key=, so it stays
// out of URLs — which is where a key ends up in a proxy access log.
package gemini

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/drops/internal/llmtask"
	"git.sr.ht/~klahr/dazyflow/internal/llm"
)

// geminiModels is shared by the task drops and the shared LLM registry.
//
// A picker rather than free text, matching Claude and ChatGPT: Google publishes
// a catalog, so a typo should be impossible rather than a 404 at run time. The
// cost is that a model released after this list was written cannot be selected
// until the list grows — adding an entry here is the whole change.
var geminiModels = []llmtask.ModelOption{
	{ID: "gemini-2.5-pro", Label: "Gemini 2.5 Pro"},
	{ID: "gemini-2.5-flash", Label: "Gemini 2.5 Flash"},
	{ID: "gemini-2.5-flash-lite", Label: "Gemini 2.5 Flash-Lite"},
}

const (
	defaultBase = "https://generativelanguage.googleapis.com"
	// defaultModel is Flash rather than Pro on purpose: the five task drops are
	// short, high-volume calls (summarize a row, classify an email), which is
	// exactly what Flash is priced and tuned for. An author who wants Pro picks
	// it on the step.
	defaultModel = "gemini-2.5-flash"
)

type provider struct{}

// Call sends one generateContent request and normalizes the response.
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
		// ANY + allowedFunctionNames is Gemini's spelling of "you must call
		// this one function" — the equivalent of OpenAI's tool_choice.
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
			// A blocked or truncated reply is the usual cause, and both are
			// stated on the candidate — so say which rather than "no output".
			return llmtask.Result{}, &core.JobError{
				Code:    "gemini_no_tool_call",
				Message: "Gemini returned no structured output" + finishHint(parsed) + " — try a shorter input, or use the Ask step for free text.",
			}
		}
	}
	return res, nil
}

// toContents converts llmtask's OpenAI-shaped Messages into Gemini's contents
// list, returning the turns and any system text found among them.
//
// Gemini has no "system" turn — it has a separate systemInstruction — and it
// spells the assistant's role "model". A caller that put a system message in
// the transcript (the shape every other provider here accepts) would otherwise
// have it silently rejected as an unknown role, so it is HOISTED out to the
// system instruction instead of dropped: losing the instructions that frame a
// conversation is the kind of failure that shows up as a subtly worse answer
// rather than an error.
//
// With no Messages at all this is the simple case llmtask uses for the task
// drops: one user turn carrying req.UserText, and req.System handled by Call.
func toContents(req llmtask.Request) (contents []any, system string) {
	if len(req.Messages) == 0 {
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
		// Every turn was a system message. Gemini rejects an empty contents
		// list, and the instructions still have to reach the model.
		contents = append(contents, textTurn("user", req.UserText))
	}
	return contents, strings.Join(systems, "\n\n")
}

func textTurn(role, text string) map[string]any {
	return map[string]any{"role": role, "parts": []any{map[string]any{"text": text}}}
}

// stringOf flattens a message's content. Gemini's parts are typed, so a
// non-string content (a caller passing OpenAI's content-parts array) is
// rendered as its JSON rather than silently becoming an empty turn.
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
		Provider:     provider{},
	})
	llmtask.RegisterAll(llmtask.Config{
		Provider:    provider{},
		Integration: "Gemini",
		// Google's Gemini spark. Color is the gradient's starting blue — the
		// static docs copy of the mark keeps the full gradient, the in-app
		// component redraws it; see web/src/components/brand/GeminiIcon.tsx.
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

// verifyKey checks a Gemini API key by listing models — a free, read-only GET
// (no tokens spent). Backs the Apps page's connection test / verify-before-save
// for Gemini.
//
// Gemini answers a bad key with 400 INVALID_ARGUMENT as often as 401/403, so
// the status alone does not separate "your key is wrong" from "your request was
// wrong" — and on a request with no body but a key, there is nothing else to be
// wrong. Hence the 400 is read as a rejected key here, which is the message the
// person pasting one needs.
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

// parts returns candidates[0].content.parts, or nil.
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

// extractText joins every text part of the reply.
//
// Joined rather than "first one wins": a reply that also called a tool, or one
// the model split across parts, has its prose spread over several, and taking
// only parts[0] would truncate it to whatever happened to come first.
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

// extractToolArgs reads the args of the first functionCall part matching name,
// or nil if the model answered without calling the tool.
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
		// A toolConfig in ANY mode naming one function should make a
		// mismatched name impossible; checked anyway, because accepting
		// another function's arguments would silently fill the step's fields
		// from the wrong schema.
		if n, _ := call["name"].(string); n != name {
			continue
		}
		if args, ok := call["args"].(map[string]any); ok {
			return args
		}
	}
	return nil
}

// finishHint names the reason a candidate stopped, when it was not a normal
// stop. MAX_TOKENS and SAFETY are the two that produce an empty or partial
// reply, and both are actionable — unlike "no output", which is not.
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

// geminiError pulls the human message out of Gemini's error envelope
// ({"error":{"message":…,"status":…}}), falling back to the raw body.
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
