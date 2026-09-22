// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package apibase holds the tiny "API root" test seam every HTTP connector
// carried as copy-pasted boilerplate: a mutex-guarded default base URL, a
// Set to swap it (tests point it at an httptest server), a Get to read it,
// and For(job) to honor a per-job base_url override — only one on the vendor's
// own host outside tests (see Override).
//
// It lives under drops/internal/ so only sibling connector packages import
// it. Each connector kept its own `httpBaseMu sync.RWMutex` + default +
// SetHTTPBase + baseURL helper; the bodies never diverged, so they live here
// once. Connectors keep their package-level SetHTTPBase as a thin forwarder
// (tests and daemon wiring call those by name).
package apibase

import (
	"net/url"
	"strings"
	"sync"
	"testing"

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

func (b *Base) For(job core.Job) string { return b.ForParam(job, "base_url") }

// ForParam is For with the override read from another param (e.g. upload_url).
func (b *Base) ForParam(job core.Job, key string) string {
	def := b.Get()
	if u := OverrideParam(job, key, def); u != "" {
		return u
	}
	return def
}

// anyOverride lets a per-job base_url point anywhere. Only under `go test`,
// where connector tests aim a job at an httptest server; apibase's own tests
// flip it to exercise the production rule.
var anyOverride = testing.Testing()

// Override returns the job's base_url (trailing slash trimmed) when it may be
// used, or "" when it is unset or not allowed. base_url is an ordinary job
// param, so in production it is honoured only when its scheme and host match
// one of the vendor's own roots in allowed: the connection's credentials and
// OAuth token ride on every request, and an arbitrary host would receive them.
// A disallowed override is ignored, so the request goes to the vendor.
func Override(job core.Job, allowed ...string) string {
	return OverrideParam(job, "base_url", allowed...)
}

func OverrideParam(job core.Job, key string, allowed ...string) string {
	u, _ := params.StringOpt(job.Params, key)
	u = strings.TrimRight(strings.TrimSpace(u), "/")
	if u == "" {
		return ""
	}
	if anyOverride || sameOrigin(u, allowed...) {
		return u
	}
	return ""
}

func sameOrigin(u string, allowed ...string) bool {
	pu, err := url.Parse(u)
	if err != nil || pu.Host == "" || pu.User != nil {
		return false
	}
	for _, a := range allowed {
		pa, err := url.Parse(a)
		if err != nil {
			continue
		}
		if strings.EqualFold(pu.Scheme, pa.Scheme) && strings.EqualFold(pu.Host, pa.Host) {
			return true
		}
	}
	return false
}
