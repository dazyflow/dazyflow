// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

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

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/internal/params"
)

type Base struct {
	mu  sync.RWMutex
	url string
}

func New(defaultURL string) *Base {
	return &Base{url: defaultURL}
}

func (b *Base) Set(url string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.url = url
}

func (b *Base) Get() string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.url
}

func (b *Base) For(job core.Job) string {
	if u, _ := params.StringOpt(job.Params, "base_url"); u != "" {
		return strings.TrimRight(u, "/")
	}
	return b.Get()
}
