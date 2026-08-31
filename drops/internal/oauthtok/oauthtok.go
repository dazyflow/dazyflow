// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package oauthtok holds the per-account OAuth token plumbing shared by the
// connectors that each ride their own OAuth provider (slack, github, notion).
// The mutex-guarded lookup hook and the resolve sequence — honor an explicit
// `token` param (the test/seam injection point), else look up the connected
// account's token — were identical copies in each package, differing only in
// the provider name woven into the error messages. A connector now holds one
// Hook instead of re-declaring the boilerplate.
//
// The Google connectors do NOT use this: they share a single provider and have
// their own richer plumbing in drops/internal/google.
package oauthtok

import (
	"context"
	"fmt"
	"sync"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/internal/params"
)

// Lookup resolves a per-account access token. The daemon wires one (against its
// OAuth registry) at startup; nil means "no lookup" — the connector then
// requires an explicit `token` param.
type Lookup = func(ctx context.Context, account string) (string, error)

// Hook is a connector's OAuth token resolver: the wired Lookup plus the
// identity strings used in its error messages.
type Hook struct {
	mu sync.RWMutex
	fn Lookup

	// display is the connector's human name ("Slack", "GitHub", "Notion") and
	// slug is its OAuth provider slug in the authorize URL ("slack"); both feed
	// the "not connected" guidance. noun is the name used in the
	// "<noun> account %q is not connected" message — some connectors capitalize
	// it (e.g. "GitHub"), so it is supplied separately rather than derived.
	display, slug, noun string
}

// New builds a Hook for a connector. display/slug/noun feed its error messages.
func New(display, slug, noun string) *Hook {
	return &Hook{display: display, slug: slug, noun: noun}
}

// Set wires (or, with nil, clears) the daemon's token lookup. Called once at
// dzd startup.
func (h *Hook) Set(fn Lookup) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.fn = fn
}

// Resolve returns the access token for a job: an explicit `token` param when
// present (the integration-test seam), else the connected account's token via
// the wired lookup. account defaults to "default".
func (h *Hook) Resolve(ctx context.Context, job core.Job) (string, error) {
	if t, _ := params.StringOpt(job.Params, "token"); t != "" {
		return t, nil
	}
	account, _ := params.StringOpt(job.Params, "account")
	if account == "" {
		account = "default"
	}
	h.mu.RLock()
	fn := h.fn
	h.mu.RUnlock()
	if fn == nil {
		return "", fmt.Errorf("no %s token: pass `token` directly or connect a %s account via /api/v1/oauth/%s/authorize", h.display, h.display, h.slug)
	}
	tok, err := fn(ctx, account)
	if err != nil {
		return "", fmt.Errorf("lookup token for account %q: %w", account, err)
	}
	if tok == "" {
		return "", fmt.Errorf("%s account %q is not connected", h.noun, account)
	}
	return tok, nil
}
