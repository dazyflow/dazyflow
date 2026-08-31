// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/internal/llm"
)

// Live model catalogs for the AI steps.
//
// A model picker built from a list compiled into the binary is wrong twice
// over: it cannot offer a model the vendor published after the release, and it
// goes on offering one the vendor has withdrawn. The second is the worse half,
// because it fails at run time inside someone's flow rather than at the moment
// they pick. Gemini did exactly that — a model stayed in the catalog while
// being closed to new keys.
//
// So a provider that can be asked (llm.ProviderInfo.ListModels) is asked, per
// tenant, because availability follows the credential and not the deployment.
// Three properties matter more than freshness here:
//
//   - The palette never waits. listDrops runs on every editor render; a vendor
//     round trip on that path would put someone else's outage in the way of
//     opening a flow. A miss serves the compiled-in list and refreshes behind
//     the request, so the answer is right from the second render on.
//   - A failure is cached too, briefly. Without that, a tenant with a dead
//     connection re-asks on every keystroke of the palette search.
//   - A failure is never louder than the fallback it replaces. Nothing here
//     surfaces an error: the picker simply shows what it showed before.
const (
	modelCacheTTL    = 15 * time.Minute
	modelCacheErrTTL = 2 * time.Minute
	modelFetchBudget = 20 * time.Second
)

type modelCatalogEntry struct {
	models   []llm.ModelOption
	at       time.Time
	failed   bool
	inflight bool
}

type modelCatalog struct {
	mu      sync.Mutex
	entries map[string]*modelCatalogEntry
}

func (s *Service) modelCatalog() *modelCatalog {
	s.modelsOnce.Do(func() {
		s.modelsCache = &modelCatalog{entries: map[string]*modelCatalogEntry{}}
	})
	return s.modelsCache
}

// liveModels returns the cached catalog for (tenant, provider), kicking off a
// refresh when it is missing or stale. Never blocks and never errors: an empty
// return means "use the compiled-in list", which is always a valid answer.
func (s *Service) liveModels(tenant string, info llm.ProviderInfo) []llm.ModelOption {
	if info.ListModels == nil || s.EncryptedSecrets == nil || tenant == "" {
		return nil
	}
	c := s.modelCatalog()
	key := info.Name + "\x00" + tenant

	c.mu.Lock()
	e, ok := c.entries[key]
	if !ok {
		e = &modelCatalogEntry{}
		c.entries[key] = e
	}
	ttl := modelCacheTTL
	if e.failed {
		ttl = modelCacheErrTTL
	}
	stale := e.at.IsZero() || time.Since(e.at) > ttl
	if stale && !e.inflight {
		e.inflight = true
		go s.refreshModels(key, tenant, info)
	}
	models := e.models
	c.mu.Unlock()
	return models
}

// refreshModels does the vendor round trip off the request path and stores the
// result. Runs detached: the request that triggered it has long since answered.
func (s *Service) refreshModels(key, tenant string, info llm.ProviderInfo) {
	ctx, cancel := context.WithTimeout(context.Background(), modelFetchBudget)
	defer cancel()

	models, err := s.fetchModels(ctx, tenant, info)

	c := s.modelCatalog()
	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.entries[key]
	if e == nil {
		return
	}
	e.inflight = false
	e.at = time.Now()
	e.failed = err != nil || len(models) == 0
	if !e.failed {
		e.models = models
	}
	// A failed refresh keeps the models it already had. A vendor blip should
	// not empty a picker that was working a minute ago — the stale list is
	// still a better answer than none, and the next attempt is minutes away.
}

// fetchModels resolves this tenant's connection and asks the vendor. Returns
// nil when the tenant has not connected the integration at all, which is not a
// failure: there is simply nothing to ask with.
func (s *Service) fetchModels(ctx context.Context, tenant string, info llm.ProviderInfo) ([]llm.ModelOption, error) {
	apiKey, _ := s.EncryptedSecrets.GetExact(ctx, tenant, core.ConnectionSecretKey(info.Integration, "api_key"))
	baseURL, _ := s.EncryptedSecrets.GetExact(ctx, tenant, core.ConnectionSecretKey(info.Integration, "base_url"))
	if apiKey == "" && baseURL == "" {
		// No connection. Asking anyway would mean every tenant probing the
		// provider's default host — for a local runtime that is a knock on
		// localhost:11434 on behalf of someone who never mentioned Ollama.
		return nil, nil
	}
	return info.ListModels(ctx, apiKey, baseURL)
}

// overlayLiveModels rewrites the model picker in each AI drop's params schema
// to the models this tenant's credential can actually call.
//
// Applied in listDrops rather than in the HTTP catalog handler so every reader
// gets the same answer: the editor's palette, the MCP describe_drop an agent
// builds a flow from, and flow generation. A picker that is honest in the UI
// and stale over MCP would just move the failure.
func (s *Service) overlayLiveModels(p core.Principal, out map[string]core.Manifest) {
	if s.EncryptedSecrets == nil || p.Tenant == "" {
		return
	}
	// One lookup per integration, not per drop: each provider registers five
	// task drops that share a catalog.
	byIntegration := map[string][]llm.ModelOption{}
	resolved := map[string]bool{}

	for id, m := range out {
		if m.Integration == "" || len(m.ParamsSchema) == 0 {
			continue
		}
		if !resolved[m.Integration] {
			resolved[m.Integration] = true
			if info, ok := llm.ByIntegration(m.Integration); ok {
				byIntegration[m.Integration] = s.liveModels(p.Tenant, info)
			}
		}
		models := byIntegration[m.Integration]
		if len(models) == 0 {
			continue
		}
		if patched, ok := withModelEnum(m.ParamsSchema, models); ok {
			m.ParamsSchema = patched
			out[id] = m
		}
	}
}

// withModelEnum replaces the model property's enum with the live list. Reports
// false and changes nothing when the schema is not the shape it expects, so an
// unrelated drop that happens to share an integration name is left alone.
//
// It also repairs the default. A default outside the offered list is how the
// Ollama steps failed before this existed: llama3.1 is a reasonable guess at
// what an operator has pulled and a 404 on the machines where they haven't,
// and it reached the model field of every step nobody had configured. When the
// compiled-in default is not something this credential can call, the first
// live model stands in — chosen by the vendor's own ordering, which puts the
// current generation first.
func withModelEnum(schema json.RawMessage, models []llm.ModelOption) (json.RawMessage, bool) {
	var doc map[string]any
	if err := json.Unmarshal(schema, &doc); err != nil {
		return nil, false
	}
	props, ok := doc["properties"].(map[string]any)
	if !ok {
		return nil, false
	}
	modelProp, ok := props["model"].(map[string]any)
	if !ok {
		return nil, false
	}
	ids := make([]any, len(models))
	labels := make([]any, len(models))
	offered := make(map[string]bool, len(models))
	for i, m := range models {
		ids[i], labels[i] = m.ID, m.Label
		offered[m.ID] = true
	}
	modelProp["enum"] = ids
	modelProp["enumNames"] = labels
	if def, _ := modelProp["default"].(string); def == "" || !offered[def] {
		modelProp["default"] = models[0].ID
	}
	patched, err := json.Marshal(doc)
	if err != nil {
		return nil, false
	}
	return patched, true
}
