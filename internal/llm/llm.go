// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package llm is the shared LLM provider layer: the provider-neutral
// request/response types plus a process-global registry of backends
// (Claude, ChatGPT, …). Both the flow drops and editor/platform features
// (the render_template AI assist, future "draft a flow") go through it, so
// there's ONE place a provider lives and one place to add a new one.
//
// The registry is populated by the provider packages at init (the dzd
// binary imports them for drop registration), so a feature in the daemon
// can read the registry at run time WITHOUT importing the drops — it just
// asks the registry which providers exist and calls Generate.
package llm

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/dazyflow/dazyflow/core"
)

// Tool is a provider-neutral forced tool: the model must call it, so its
// returned input matches Schema.
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
	Text string
	Tool map[string]any
	Raw  map[string]any
}

// Provider is one LLM backend — the vendor API call + response parsing.
// Implementations live in the per-vendor drop packages and register here.
type Provider interface {
	Call(ctx context.Context, apiKey string, req Request) (Result, *core.JobError)
}

// ModelOption is one entry in a model picker.
type ModelOption struct {
	ID    string
	Label string
}

// ModelLister asks the vendor which models THIS credential can actually use.
//
// Optional. A provider without one keeps its compiled-in Models list, which is
// the honest answer when there is no catalog endpoint to ask. A provider WITH
// one is saying its catalog moves faster than our releases — which is the
// normal case, and which a static list gets wrong in both directions: it
// cannot offer a model published after the list was written, and it goes on
// offering one the vendor has since withdrawn. Only the vendor knows, and only
// per credential: availability varies by key, project and tier.
//
// Implementations must be read-only and free (a catalog GET, not a
// generation), because this runs without the user asking for it.
type ModelLister func(ctx context.Context, apiKey, baseURL string) ([]ModelOption, error)

// ProviderInfo is a registered backend: its stable id, the integration name
// that keys its connection secret (conn.<slug>.api_key), its models, and the
// Provider that makes the call.
type ProviderInfo struct {
	Name         string // stable id, e.g. "claude", "openai"
	Integration  string // "Claude" / "ChatGPT" — drives conn.<slug>.api_key
	DefaultModel string
	// Models is the fallback catalog: what the picker offers before
	// ListModels has answered, and what it keeps offering when there is no
	// ListModels, no connection, or the vendor is unreachable.
	Models     []ModelOption
	ListModels ModelLister
	Provider   Provider
}

var (
	mu        sync.RWMutex
	providers = map[string]ProviderInfo{}
	order     []string // registration order, for a stable default
)

// Register adds (or replaces) a provider. Called from each vendor package's
// init via llmtask.RegisterAll. Safe for concurrent use.
func Register(p ProviderInfo) {
	mu.Lock()
	defer mu.Unlock()
	if _, dup := providers[p.Name]; !dup {
		order = append(order, p.Name)
	}
	providers[p.Name] = p
}

// Get returns a provider by id.
func Get(name string) (ProviderInfo, bool) {
	mu.RLock()
	defer mu.RUnlock()
	p, ok := providers[name]
	return p, ok
}

// Registered lists providers in registration order.
func Registered() []ProviderInfo {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]ProviderInfo, 0, len(order))
	for _, n := range order {
		out = append(out, providers[n])
	}
	return out
}

// Generate runs one request against the named provider with the given API
// key, defaulting the model to the provider's default. Returns a plain error
// (the provider's friendly JobError message) so non-flow callers don't need
// the core.JobError type.
func Generate(ctx context.Context, name, apiKey string, req Request) (Result, error) {
	p, ok := Get(name)
	if !ok {
		return Result{}, fmt.Errorf("unknown LLM provider %q", name)
	}
	if req.Model == "" {
		req.Model = p.DefaultModel
	}
	res, jerr := p.Provider.Call(ctx, apiKey, req)
	if jerr != nil {
		return Result{}, fmt.Errorf("%s", jerr.Message)
	}
	return res, nil
}

// ByIntegration finds a provider by the integration name that keys its
// connection secret ("Gemini", "Ollama"). The daemon's catalog layer starts
// from a drop manifest, which carries the integration rather than the
// provider id. Case-insensitive: the manifest and the registration are written
// by hand in two different files.
func ByIntegration(integration string) (ProviderInfo, bool) {
	mu.RLock()
	defer mu.RUnlock()
	for _, n := range order {
		if strings.EqualFold(providers[n].Integration, integration) {
			return providers[n], true
		}
	}
	return ProviderInfo{}, false
}
