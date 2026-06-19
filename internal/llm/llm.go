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
	"sort"
	"sync"

	"git.sr.ht/~klahr/dazyflow/core"
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

// ProviderInfo is a registered backend: its stable id, the integration name
// that keys its connection secret (conn.<slug>.api_key), its models, and the
// Provider that makes the call.
type ProviderInfo struct {
	Name         string // stable id, e.g. "claude", "openai"
	Integration  string // "Claude" / "ChatGPT" — drives conn.<slug>.api_key
	DefaultModel string
	Models       []ModelOption
	Provider     Provider
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

// RegisteredNames returns provider ids, sorted, for stable test/debug output.
func RegisteredNames() []string {
	mu.RLock()
	defer mu.RUnlock()
	out := append([]string(nil), order...)
	sort.Strings(out)
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
