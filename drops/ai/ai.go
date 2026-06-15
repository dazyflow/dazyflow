// Package ai hosts the task-shaped "smart step" drops built on Claude's
// Messages API — Summarize, Extract fields, Classify, and Draft reply. They
// are deliberately narrower than the raw `claude` drop: a non-technical author
// sees plain fields ("how short?", "which categories?") instead of a prompt,
// model, and token budget.
//
// All four share the Claude app connection. Because their Integration is
// "Claude" (same as the claude drop), the engine injects conn.claude.api_key
// into the api_key param a node leaves unset — so a user connects Claude once
// and every smart step works, and they all group under one connection card.
//
// Every drop funnels through callClaude, a trimmed copy of the claude drop's
// Messages path: same x-api-key/anthropic-version headers, the same SSRF dial
// guard + egress allowlist (base_url is tenant-overridable, so the key must not
// be exfiltrable to an internal host), and the same 429 handling. The two
// structured drops (extract, classify) force a tool_use so the model returns
// validated JSON the canvas can wire, not prose.
package ai

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
)

const (
	apiVersion   = "2023-06-01"
	defaultBase  = "https://api.anthropic.com"
	defaultModel = "claude-sonnet-4-6"
	// maxResponseBytes caps how much of the API response we buffer, so a
	// hostile or buggy upstream (reachable via the base_url override) can't
	// OOM the daemon by streaming an unbounded body.
	maxResponseBytes = 64 << 20 // 64 MiB
)

// connectionFields is the shared Claude connection declaration. Every smart
// step embeds it so they read the same conn.claude.api_key the claude drop
// does — one connection, many drops.
var connectionFields = []core.ConnectionField{
	{Key: "api_key", Label: "API key", Secret: true, Required: true, Placeholder: "sk-ant-…"},
}

// toolDef is a forced Claude tool: the model must call it, so its input is
// guaranteed to match schema. Used by extract/classify for structured output.
type toolDef struct {
	name        string
	description string
	schema      map[string]any
}

// callOpts is one Messages request shaped by a smart-step drop.
type callOpts struct {
	system      string
	userText    string
	maxTokens   int
	temperature *float64
	tool        *toolDef // when set, force this tool and read its input
	model       string
	timeoutMS   int
}

// claudeOut is what callClaude returns: the concatenated text (for prose
// drops), the forced tool's input map (for structured drops), and the raw
// response (emitted as a non-pin "response" output for debugging).
type claudeOut struct {
	text string
	tool map[string]any
	raw  map[string]any
}

// callClaude sends one single-turn Messages request and parses the result.
// Returns a *core.JobError (never a Go error) so callers map it straight onto
// params.Err — matching how the connectors surface failures as failed jobs.
func callClaude(ctx context.Context, job core.Job, o callOpts) (claudeOut, *core.JobError) {
	apiKey, _ := params.StringOpt(job.Params, "api_key")
	if apiKey == "" {
		return claudeOut{}, &core.JobError{Code: "bad_param", Message: "no API key — connect Claude on the Apps page to set it"}
	}
	if strings.TrimSpace(o.userText) == "" {
		return claudeOut{}, &core.JobError{Code: "bad_input", Message: "no text — wire the Text input or fill the text field"}
	}

	model := o.model
	if model == "" {
		model = defaultModel
	}
	maxTokens := o.maxTokens
	if maxTokens <= 0 {
		maxTokens = 1024
	}

	body := map[string]any{
		"model":      model,
		"max_tokens": maxTokens,
		"messages":   []any{map[string]any{"role": "user", "content": o.userText}},
	}
	if o.system != "" {
		body["system"] = o.system
	}
	if o.temperature != nil {
		body["temperature"] = *o.temperature
	}
	if o.tool != nil {
		body["tools"] = []any{map[string]any{
			"name":         o.tool.name,
			"description":  o.tool.description,
			"input_schema": o.tool.schema,
		}}
		body["tool_choice"] = map[string]any{"type": "tool", "name": o.tool.name}
	}
	raw, _ := json.Marshal(body)

	base := strings.TrimRight(params.StringDefault(job.Params, "base_url", defaultBase), "/")
	endpoint := base + "/v1/messages"

	timeoutMS := o.timeoutMS
	if timeoutMS <= 0 {
		timeoutMS = 60000
	}
	timeout := time.Duration(timeoutMS) * time.Millisecond
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, "POST", endpoint, bytes.NewReader(raw))
	if err != nil {
		return claudeOut{}, &core.JobError{Code: "bad_param", Message: err.Error()}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", apiVersion)

	// base_url is tenant-overridable (x_advanced), so guard the dial like the
	// claude drop: EgressAllowed bounds which public hosts the key may reach,
	// and SafeHTTPClient blocks loopback/private/link-local (cloud metadata,
	// internal services) unless the operator opts in.
	if err := hfnet.EgressAllowed(endpoint); err != nil {
		return claudeOut{}, &core.JobError{Code: "egress_blocked", Message: err.Error()}
	}
	resp, err := hfnet.SafeHTTPClient(timeout, hfnet.PrivateEgressAllowed()).Do(req)
	if err != nil {
		return claudeOut{}, &core.JobError{Code: "claude_http_error", Message: err.Error()}
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if int64(len(respBody)) > maxResponseBytes {
		return claudeOut{}, &core.JobError{Code: "claude_http_error", Message: "response exceeds " + strconv.Itoa(maxResponseBytes) + " bytes"}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		code := "claude_api"
		if resp.StatusCode == 429 {
			code = "claude_rate_limited"
		}
		return claudeOut{}, &core.JobError{Code: code, Message: strconv.Itoa(resp.StatusCode) + " " + claudeError(respBody)}
	}

	var parsed map[string]any
	_ = json.Unmarshal(respBody, &parsed)
	out := claudeOut{raw: parsed, text: extractText(parsed)}
	if o.tool != nil {
		out.tool = extractToolInput(parsed, o.tool.name)
	}
	return out, nil
}

// resolveText reads the node's text: the "text" input port wins (so upstream
// data flows in), otherwise the "text" param the author typed inline.
func resolveText(job core.Job) string {
	if in, ok := job.Input["text"]; ok && in.Inline != nil {
		if s := coerceText(in.Inline); s != "" {
			return s
		}
	}
	return params.StringDefault(job.Params, "text", "")
}

// paramObjList reads an array-of-objects param (fields, categories) into a
// slice of maps, dropping non-object entries.
func paramObjList(p map[string]any, key string) []map[string]any {
	v, ok := p[key]
	if !ok {
		return nil
	}
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(arr))
	for _, it := range arr {
		if m, ok := it.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
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

// coerceText flattens whatever arrived on the text input into one string: a
// string passes through; a list joins with blank lines; a {value:…} wrapper
// recurses; any other object is JSON-encoded. Mirrors the claude drop.
func coerceText(v any) string {
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
			if s := coerceText(it); s != "" {
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, "\n\n")
	case map[string]any:
		if inner, ok := t["value"]; ok {
			return coerceText(inner)
		}
		b, _ := json.Marshal(t)
		return string(b)
	default:
		b, _ := json.Marshal(t)
		return string(b)
	}
}
