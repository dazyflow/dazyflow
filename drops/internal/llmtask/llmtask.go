// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

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

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/internal/params"
	hfnet "github.com/dazyflow/dazyflow/drops/net"
	"github.com/dazyflow/dazyflow/engine"
	"github.com/dazyflow/dazyflow/internal/llm"
)

// maxResponseBytes caps how much of a response we buffer, so a hostile or
// buggy upstream (reachable via the base_url override) can't OOM the daemon.
const maxResponseBytes = 64 << 20

// The provider-neutral request/response vocabulary now lives in internal/llm
// (the shared LLM layer used by both these drops and editor features like the
// render_template AI assist). These aliases keep the provider packages and
// their tests referring to llmtask.Request/Result/Tool/Provider unchanged.
type (
	Tool        = llm.Tool
	Request     = llm.Request
	Result      = llm.Result
	Provider    = llm.Provider
	ModelOption = llm.ModelOption
)

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
	// KeyOptional marks a provider that needs no API key — a local runtime
	// (Ollama) rather than a metered cloud API. The connection's api_key field
	// stops being required, an empty key stops being a run-time error, and the
	// connection verifier checks reachability instead of credentials.
	KeyOptional bool
	// BaseURLLabel, when set, ALSO puts the API host on the connection instead
	// of only on the step. For a cloud vendor the host is a rare override and
	// the advanced per-step base_url is the right home; for a local runtime the
	// host IS the configuration, and retyping it on every step is unusable.
	// The field is plain (not secret) so it stays visible in node output, and
	// injectConnectionDefaults leaves an author's per-step override alone.
	BaseURLLabel       string
	BaseURLPlaceholder string
	BaseURLHelp        string
	// VerifyKey, when set, checks that an API key is usable WITHOUT a
	// token-costing generation — typically a GET to the provider's models
	// endpoint (see GetStatus). RegisterAll wires it into the connection
	// verifier for cfg.Integration so the Apps page can test the key before
	// saving it. baseURL is "" for the provider default.
	VerifyKey func(ctx context.Context, apiKey, baseURL string) error
}

// RegisterAll registers all five task drops for a provider, plus (when the
// provider supplies cfg.VerifyKey) the connection verifier that backs the
// Apps page's "Test connection" / verify-before-save for this integration.
func RegisterAll(cfg Config) {
	engine.Register(askDrop(cfg))
	engine.Register(summarizeDrop(cfg))
	engine.Register(extractDrop(cfg))
	engine.Register(classifyDrop(cfg))
	engine.Register(draftReplyDrop(cfg))
	if cfg.VerifyKey != nil {
		verify := cfg.VerifyKey
		integration := cfg.Integration
		engine.RegisterConnectionVerifier(integration, func(ctx context.Context, conn map[string]string) error {
			key := strings.TrimSpace(conn["api_key"])
			if key == "" && !cfg.KeyOptional {
				return fmt.Errorf("no API key — paste your %s API key to connect", integration)
			}
			return verify(ctx, key, strings.TrimSpace(conn["base_url"]))
		})
	}
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
	if err := hfnet.EgressAllowedFor(ctx, endpoint); err != nil {
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

// GetStatus runs one guarded GET (SSRF dial guard + egress allowlist +
// timeout) and returns the HTTP status and a capped body. It's the read-only
// sibling of PostJSON, used by a provider's VerifyKey to validate an API key
// against a cheap read endpoint (e.g. GET /v1/models) — no token-costing
// generation, no mutation. Endpoints are tenant-overridable, so the same
// egress guard applies: the key must not be exfiltrable to an internal host.
func GetStatus(ctx context.Context, endpoint string, headers map[string]string) (int, []byte, error) {
	timeout := 10 * time.Second
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, "GET", endpoint, nil)
	if err != nil {
		return 0, nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if err := hfnet.EgressAllowedFor(ctx, endpoint); err != nil {
		return 0, nil, err
	}
	resp, err := hfnet.SafeHTTPClient(timeout, hfnet.PrivateEgressAllowed()).Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, body, nil
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
	if k == "" && !cfg.KeyOptional {
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
	fields := []core.ConnectionField{
		{Key: "api_key", Label: "API key", Secret: true, Required: !cfg.KeyOptional, Placeholder: cfg.KeyPlaceholder},
	}
	if cfg.BaseURLLabel != "" {
		fields = append(fields, core.ConnectionField{
			Key: "base_url", Label: cfg.BaseURLLabel, Required: true,
			Placeholder: cfg.BaseURLPlaceholder, Help: cfg.BaseURLHelp,
		})
	}
	return fields
}

// connNote and unsetHint are the "where do credentials come from" line on a
// params example. They differ per provider because the mistake each one heads
// off differs: a keyed provider's reader is about to paste an API key onto the
// step, while a keyless one has no key to paste and instead needs to know the
// server address is not a per-step setting either.
func connNote(cfg Config) string {
	if cfg.KeyOptional {
		return "The server URL comes from the connection — leave base_url unset."
	}
	return "The API key comes from the connection — leave api_key unset."
}

// unsetHint is the same fact as a trailing clause, for examples that run it on
// after a semicolon rather than as its own sentence.
func unsetHint(cfg Config) string {
	if cfg.KeyOptional {
		return "leave base_url unset."
	}
	return "leave api_key unset."
}

func taskID(cfg Config, task string) string { return cfg.TaskIDPrefix + "_" + task }

// baseProps returns the params every drop shares: the model picker plus the
// advanced api_key / base_url / timeout knobs.
func baseProps(cfg Config) map[string]any {
	// A vendor with a published catalog gets a picker. A local runtime serves
	// whatever the operator has pulled, so it gets a free-text field instead —
	// an enum there would be a guess that hides every model the user has.
	modelProp := map[string]any{"type": "string", "title": "Model", "x_advanced": true, "default": cfg.DefaultModel}
	if len(cfg.Models) > 0 {
		enum := make([]any, len(cfg.Models))
		names := make([]any, len(cfg.Models))
		for i, m := range cfg.Models {
			enum[i] = m.ID
			names[i] = m.Label
		}
		modelProp["enum"] = enum
		modelProp["enumNames"] = names
	}
	return map[string]any{
		"model":      modelProp,
		"api_key":    map[string]any{"type": "string", "x_advanced": true, "description": "Injected from the " + cfg.Integration + " connection — leave unset."},
		"base_url":   map[string]any{"type": "string", "x_advanced": true, "description": "Override the API host."},
		"timeout_ms": map[string]any{"type": "integer", "default": 60000, "minimum": 1, "description": "Hard deadline for the AI request, in milliseconds."},
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
