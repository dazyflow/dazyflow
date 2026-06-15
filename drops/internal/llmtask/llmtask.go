// Package llmtask is the shared core behind the per-provider LLM drops
// (Claude, ChatGPT, …). It owns the task-shaped UX — Ask, Summarize, Extract
// fields, Classify, Draft reply — as a SINGLE implementation; each provider
// package supplies a Provider (the vendor API call + response parsing) and a
// Config (branding, models, ids) and calls RegisterAll.
//
// This mirrors how Sheets and Excel stay separate integrations while sharing
// the rows+headers contract: the task logic + manifests live here once, and
// the vendor specifics (endpoint, auth header, tool-call shape) live in the
// provider packages. Adding a provider is one new package + a RegisterAll —
// no duplicated drops.
package llmtask

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"git.sr.ht/~klahr/hazyflow/core"
	"git.sr.ht/~klahr/hazyflow/drops/internal/params"
	hfnet "git.sr.ht/~klahr/hazyflow/drops/net"
	"git.sr.ht/~klahr/hazyflow/engine"
)

// maxResponseBytes caps how much of a response we buffer, so a hostile or
// buggy upstream (reachable via the base_url override) can't OOM the daemon.
const maxResponseBytes = 64 << 20

// Tool is a provider-neutral forced tool: the model must call it, so its
// returned input matches Schema. Providers map it to their own shape
// (Anthropic input_schema / OpenAI function parameters).
type Tool struct {
	Name        string
	Description string
	Schema      map[string]any
}

// Request is one single-turn generation, provider-neutral.
type Request struct {
	Model       string
	System      string
	UserText    string
	Messages    []any // optional multi-turn ({role,content}); overrides System+UserText
	MaxTokens   int
	Temperature *float64
	Tool        *Tool
	BaseURL     string // tenant override; "" = provider default
	TimeoutMS   int
}

// Result is the normalized provider response.
type Result struct {
	Text string         // concatenated text output
	Tool map[string]any // forced-tool input, when Request.Tool was set
	Raw  map[string]any // raw decoded response (emitted for debugging)
}

// Provider is one LLM backend. Implementations live in the per-vendor packages.
type Provider interface {
	Call(ctx context.Context, apiKey string, req Request) (Result, *core.JobError)
}

// ModelOption is one entry in a drop's model picker.
type ModelOption struct {
	ID    string
	Label string
}

// Config is a provider's branding + model set, supplied to RegisterAll.
type Config struct {
	Provider       Provider
	Integration    string // "Claude" / "ChatGPT" — drives grouping + conn.<slug>.api_key
	Icon           string
	Color          string
	BrandLogo      string
	DefaultModel   string
	Models         []ModelOption
	KeyPlaceholder string
	AskID          string // "claude" / "chatgpt"
	TaskIDPrefix   string // "claude" / "gpt" → <prefix>_summarize
}

// RegisterAll registers all five task drops for a provider.
func RegisterAll(cfg Config) {
	engine.Register(askDrop(cfg))
	engine.Register(summarizeDrop(cfg))
	engine.Register(extractDrop(cfg))
	engine.Register(classifyDrop(cfg))
	engine.Register(draftReplyDrop(cfg))
}

// PostJSON runs one guarded JSON POST: SSRF dial guard + egress allowlist
// (endpoints are tenant-overridable, so the key must not be exfiltrable to an
// internal host), a response cap, and returns status + body for the caller to
// classify (vendor error shapes differ). Providers build the body/headers and
// parse the result.
func PostJSON(ctx context.Context, endpoint string, headers map[string]string, body []byte, timeoutMS int) (int, []byte, *core.JobError) {
	if timeoutMS <= 0 {
		timeoutMS = 60000
	}
	timeout := time.Duration(timeoutMS) * time.Millisecond
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, "POST", endpoint, bytes.NewReader(body))
	if err != nil {
		return 0, nil, &core.JobError{Code: "bad_param", Message: err.Error()}
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if err := hfnet.EgressAllowed(endpoint); err != nil {
		return 0, nil, &core.JobError{Code: "egress_blocked", Message: err.Error()}
	}
	resp, err := hfnet.SafeHTTPClient(timeout, hfnet.PrivateEgressAllowed()).Do(req)
	if err != nil {
		// A deadline here is almost always "the model took too long", not a
		// bug — say so and point at the knob, instead of a raw context error.
		if errors.Is(err, context.DeadlineExceeded) || reqCtx.Err() == context.DeadlineExceeded {
			return 0, nil, &core.JobError{Code: "llm_timeout", Message: fmt.Sprintf("the AI request timed out after %ds — try again, raise timeout_ms on the step, or shorten the input", timeoutMS/1000)}
		}
		return 0, nil, &core.JobError{Code: "llm_http_error", Message: "could not reach the AI service: " + err.Error()}
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if int64(len(respBody)) > maxResponseBytes {
		return resp.StatusCode, nil, &core.JobError{Code: "llm_http_error", Message: "response exceeds limit"}
	}
	return resp.StatusCode, respBody, nil
}

// HTTPError turns a non-2xx LLM API status into an actionable JobError so
// the user reads "ChatGPT is rate-limited; try again shortly" instead of
// an opaque "llm_http_error: 429 …". codePrefix keeps each provider's
// stable error codes ("claude"/"openai" → claude_rate_limited, …); label
// is the human integration name woven into the message; detail is the
// vendor's own extracted error text, appended for debugging. Shared so
// every provider classifies the common statuses identically.
func HTTPError(codePrefix, label string, status int, detail string) *core.JobError {
	detail = strings.TrimSpace(detail)
	var code, msg string
	switch {
	case status == 401 || status == 403:
		code, msg = codePrefix+"_auth", fmt.Sprintf("%s rejected the API key — check it on the Apps page and re-connect %s.", label, label)
	case status == 429:
		code, msg = codePrefix+"_rate_limited", fmt.Sprintf("%s is rate-limited right now — wait a moment and try again, or run this step less often.", label)
	case status == 400 || status == 422:
		code, msg = codePrefix+"_bad_request", fmt.Sprintf("%s rejected the request — usually the model name or an over-long prompt. Check the model picker and shorten the input.", label)
	case status == 404:
		code, msg = codePrefix+"_not_found", fmt.Sprintf("%s couldn't find that model — pick a different model on the step.", label)
	case status >= 500:
		code, msg = codePrefix+"_upstream", fmt.Sprintf("%s is having a temporary problem (server error %d) — try again shortly.", label, status)
	default:
		code, msg = codePrefix+"_api", fmt.Sprintf("%s returned an error (HTTP %d).", label, status)
	}
	if detail != "" {
		msg += fmt.Sprintf(" (%s said: %s)", label, detail)
	}
	return &core.JobError{Code: code, Message: msg}
}

// --- shared param + key resolution -----------------------------------------

func resolveKey(job core.Job, cfg Config) (string, *core.JobError) {
	k, _ := params.StringOpt(job.Params, "api_key")
	if k == "" {
		return "", &core.JobError{Code: "bad_param", Message: "no API key — connect " + cfg.Integration + " on the Apps page to set it"}
	}
	return k, nil
}

func model(job core.Job, cfg Config) string {
	return params.StringDefault(job.Params, "model", cfg.DefaultModel)
}

func timeoutMS(job core.Job) int  { return params.IntDefault(job.Params, "timeout_ms", 60000) }
func baseURL(job core.Job) string { return params.StringDefault(job.Params, "base_url", "") }

// resolveText reads the node's text: the "text" input port wins, else the
// "text" param the author typed inline.
func resolveText(job core.Job) string {
	if in, ok := job.Input["text"]; ok && in.Inline != nil {
		if s := coerceText(in.Inline); s != "" {
			return s
		}
	}
	return params.StringDefault(job.Params, "text", "")
}

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

// coerceText flattens whatever arrived on an input into one string.
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

// --- manifest helpers -------------------------------------------------------

func connFields(cfg Config) []core.ConnectionField {
	return []core.ConnectionField{
		{Key: "api_key", Label: "API key", Secret: true, Required: true, Placeholder: cfg.KeyPlaceholder},
	}
}

func taskID(cfg Config, task string) string { return cfg.TaskIDPrefix + "_" + task }

// baseProps returns the params every drop shares: the model picker plus the
// advanced api_key / base_url / timeout knobs.
func baseProps(cfg Config) map[string]any {
	enum := make([]any, len(cfg.Models))
	names := make([]any, len(cfg.Models))
	for i, m := range cfg.Models {
		enum[i] = m.ID
		names[i] = m.Label
	}
	return map[string]any{
		"model":      map[string]any{"type": "string", "title": "Model", "x_advanced": true, "enum": enum, "enumNames": names, "default": cfg.DefaultModel},
		"api_key":    map[string]any{"type": "string", "x_advanced": true, "description": "Injected from the " + cfg.Integration + " connection — leave unset."},
		"base_url":   map[string]any{"type": "string", "x_advanced": true, "description": "Override the API host."},
		"timeout_ms": map[string]any{"type": "integer", "default": 60000, "minimum": 1},
	}
}

func schemaJSON(props map[string]any, required []string) json.RawMessage {
	s := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		s["required"] = required
	}
	b, _ := json.Marshal(s)
	return b
}

func tags(cfg Config, extra ...string) []string {
	return append([]string{"ai", strings.ToLower(cfg.Integration)}, extra...)
}
