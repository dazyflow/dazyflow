// Package apibase holds the tiny "API root" test seam every HTTP connector
// carried as copy-pasted boilerplate: a mutex-guarded default base URL, a
// Set to swap it (tests point it at an httptest server), a Get to read it,
// and For(job) to honor a per-job base_url override.
//
// It lives under drops/internal/ so only sibling connector packages import
// it. Each connector kept its own `httpBaseMu sync.RWMutex` + default +
// SetHTTPBase + baseURL helper; the bodies never diverged, so they live here
// once. Connectors keep their package-level SetHTTPBase as a thin forwarder
// (tests and daemon wiring call those by name).
package apibase

import (
	"strings"
	"sync"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/drops/internal/params"
)

// Base is a concurrency-safe holder for a connector's API root. The zero
// value is not usable — construct with New so the default is set.
type Base struct {
	mu  sync.RWMutex
	url string
}

// New returns a Base seeded with the connector's production default URL.
func New(defaultURL string) *Base {
	return &Base{url: defaultURL}
}

// Set swaps the base URL. Tests point it at an httptest server; the daemon
// can repoint it for self-hosted deployments. The package-level SetHTTPBase
// functions forward here.
func (b *Base) Set(url string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.url = url
}

// Get returns the current base URL (default or whatever Set last stored).
// Use this for connectors that don't accept a per-job base_url param.
func (b *Base) Get() string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.url
}

// For resolves the base URL for one job: an explicit `base_url` param wins
// (trailing slash trimmed so callers can concatenate paths), else Get(). This
// matches the historical baseURL(job) helper of the connectors that exposed a
// base_url param (stripe, twilio, slack).
func (b *Base) For(job core.Job) string {
	if u, _ := params.StringOpt(job.Params, "base_url"); u != "" {
		return strings.TrimRight(u, "/")
	}
	return b.Get()
}
