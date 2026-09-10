// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

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

	// Files are documents and images sent alongside UserText — a PDF invoice,
	// a scanned receipt, a screenshot. Each provider renders them into its own
	// content-block shape, because there is no common wire format: Anthropic
	// takes a `document` block, OpenAI a `file` part, Gemini `inline_data`,
	// and Ollama only images. A provider that cannot carry a file at all says
	// so rather than dropping it silently — a summary of a PDF the model never
	// saw is the worst possible outcome here.
	//
	// Ignored when Messages is set: a caller supplying raw multi-turn messages
	// is already speaking the provider's own shape.
	Files []File
}

type File struct {
	Name string
	// MIME is the content type — "application/pdf", "image/png". Required:
	// every provider needs it on the wire, and guessing from the extension is
	// how a JPEG ends up labelled as a PDF.
	MIME string
	Data []byte
}

func (f File) IsImage() bool { return strings.HasPrefix(strings.ToLower(f.MIME), "image/") }

func (f File) IsPDF() bool {
	return strings.EqualFold(strings.TrimSpace(strings.SplitN(f.MIME, ";", 2)[0]), "application/pdf")
}

type Result struct {
	Text string
	Tool map[string]any
	Raw  map[string]any
}

type Provider interface {
	Call(ctx context.Context, apiKey string, req Request) (Result, *core.JobError)
}

type ModelOption struct {
	ID    string
	Label string
}

// ModelLister asks the vendor which models THIS credential can actually use.
//
// Optional. A provider without one keeps its compiled-in Models list, which is
// the honest answer when there is no catalog endpoint to ask. A provider WITH
// one is saying its catalog moves faster than our releases, and a static list
// then gets it wrong in both directions: it cannot offer a model published after
// the list was written, and it goes on offering one the vendor has withdrawn.
// Only the vendor knows, and only per credential — availability varies by key,
// project and tier.
//
// Implementations must be read-only and free (a catalog GET, not a generation),
// because this runs without the user asking for it.
type ModelLister func(ctx context.Context, apiKey, baseURL string) ([]ModelOption, error)

// ProviderInfo is a registered backend: its stable id, the integration name
// that keys its connection secret (conn.<slug>.api_key), its models, and the
// Provider that makes the call.
type ProviderInfo struct {
	Name         string // stable id, e.g. "claude", "openai"
	Integration  string // "Claude" / "ChatGPT" — drives conn.<slug>.api_key
	DefaultModel string
	Models       []ModelOption
	ListModels   ModelLister
	Provider     Provider

	// Embedder is set only by a provider that also sells embeddings; see
	// embed.go. DefaultEmbedModel and EmbedModels are its own catalogue,
	// separate from the chat models above — the two lists never overlap.
	Embedder          Embedder
	DefaultEmbedModel string
	EmbedModels       []ModelOption
}

var (
	mu        sync.RWMutex
	providers = map[string]ProviderInfo{}
	order     []string // registration order, for a stable default
)

func Register(p ProviderInfo) {
	mu.Lock()
	defer mu.Unlock()
	if _, dup := providers[p.Name]; !dup {
		order = append(order, p.Name)
	}
	providers[p.Name] = p
}

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
