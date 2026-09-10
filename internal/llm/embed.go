// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package llm

import (
	"context"
	"fmt"

	"github.com/dazyflow/dazyflow/core"
)

// Embedding is the other half of what an AI provider sells: instead of writing
// text, it turns text into a vector whose direction is what the text is about.
// Two vectors close together mean two pieces of text about the same thing, and
// that is the whole trick behind answering a question out of a pile of
// documents.
//
// Not every provider has one — Anthropic sells no embeddings API — so this is
// an optional half of ProviderInfo rather than part of Provider. A provider
// without an Embedder simply never appears where embeddings are offered.
//
// Vectors are float32 because that is what the vendors return and what the
// store keeps: the precision beyond it is noise, and doubling the width would
// double both the file and the time to scan it.

type EmbedRequest struct {
	Model string
	// Texts is a batch. Every provider here charges and rate-limits per
	// request rather than per text, so embedding a document's chunks in one
	// call is both faster and cheaper than a call each.
	Texts     []string
	BaseURL   string // tenant override; "" = provider default
	TimeoutMS int
}

type EmbedResult struct {
	// Vectors comes back in the order Texts went in — the callers here match
	// them up by position, so a provider that reorders must sort before
	// returning.
	Vectors [][]float32
	Model   string
}

type Embedder interface {
	Embed(ctx context.Context, apiKey string, req EmbedRequest) (EmbedResult, *core.JobError)
}

// Embed runs one embedding request against a registered provider, filling in
// that provider's default model when the caller named none.
func Embed(ctx context.Context, name, apiKey string, req EmbedRequest) (EmbedResult, *core.JobError) {
	p, ok := Get(name)
	if !ok || p.Embedder == nil {
		return EmbedResult{}, &core.JobError{
			Code:    "bad_param",
			Message: fmt.Sprintf("%q cannot make embeddings — choose one that can: %v", name, EmbedderNames()),
		}
	}
	if req.Model == "" {
		req.Model = p.DefaultEmbedModel
	}
	res, jerr := p.Embedder.Embed(ctx, apiKey, req)
	if jerr != nil {
		return EmbedResult{}, jerr
	}
	if len(res.Vectors) != len(req.Texts) {
		return EmbedResult{}, &core.JobError{
			Code: "llm_api",
			Message: fmt.Sprintf("%s returned %d vectors for %d pieces of text",
				name, len(res.Vectors), len(req.Texts)),
		}
	}
	if res.Model == "" {
		res.Model = req.Model
	}
	return res, nil
}

// Embedders lists the registered providers that can make embeddings, in
// registration order.
func Embedders() []ProviderInfo {
	out := make([]ProviderInfo, 0, 4)
	for _, p := range Registered() {
		if p.Embedder != nil {
			out = append(out, p)
		}
	}
	return out
}

func EmbedderNames() []string {
	var out []string
	for _, p := range Embedders() {
		out = append(out, p.Name)
	}
	return out
}

// EmbedderByIntegration finds an embedding provider by the integration name a
// connection is keyed on ("ChatGPT", "Ollama"), which is what a person picks
// from a dropdown rather than the internal provider id.
func EmbedderByIntegration(integration string) (ProviderInfo, bool) {
	p, ok := ByIntegration(integration)
	if !ok || p.Embedder == nil {
		return ProviderInfo{}, false
	}
	return p, true
}

// EmbeddingIntegrations names the connectable integrations that can embed —
// the choices a Knowledge connection offers.
func EmbeddingIntegrations() []string {
	var out []string
	for _, p := range Embedders() {
		out = append(out, p.Integration)
	}
	return out
}
