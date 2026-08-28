// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package pollstate lets poll-driven fetcher nodes report whether their last
// fire found new data, so the scheduler can adapt a flow's poll cadence —
// widening the interval for a poller that keeps coming up empty and snapping
// back when data reappears. On a hosted shared-fleet deployment this cuts the
// dominant cost of polling: the calls that find nothing.
//
// The marker is keyed by the FLOW (graph), not the reporting node, because the
// scheduler fires the whole graph and the node that knows "empty" (the fetcher
// — homeassistant_state_changed, google_form_trigger, a conditional
// http_request) is often DOWNSTREAM of the scheduler-fired trigger node, so
// their node IDs differ. Graph scoping lets any fetcher in the run speak for
// the run. A flow with several independent pollers shares one marker; the
// failure mode is benign (less-aggressive backoff), so the approximation is
// acceptable.
//
// Persistence mirrors the cursor store (drops/trigger/gform): the daemon wires
// SetStore to the encrypted secret store under the reserved "pollstate."
// prefix (hidden from the Credentials UI). When unwired (tests, in-process
// Engine.Run) every call is a no-op.
package pollstate

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"git.sr.ht/~klahr/dazyflow/core"
)

// Reader returns the stored marker for an exact tenant/name, or ("", nil)
// when nothing is stored yet. Writer persists one. Both match the cursor
// store's shape so the daemon can back them with the same secret store.
type (
	Reader func(ctx context.Context, tenant, name string) (string, error)
	Writer func(ctx context.Context, tenant, name, value string) error
)

var (
	mu     sync.RWMutex
	reader Reader
	writer Writer
)

// SetStore wires the persistence backend. cmd/dzd points it at the encrypted
// secret store under the "pollstate." prefix.
func SetStore(r Reader, w Writer) {
	mu.Lock()
	defer mu.Unlock()
	reader, writer = r, w
}

// Marker is the persisted per-flow poll outcome: whether the most recent fire
// found no new data, and when it was recorded. The scheduler reads At to tell
// a fresh outcome from one it already counted (it owns the empty STREAK in
// memory; the marker only carries the latest single outcome, so concurrent
// fetchers in one run are last-writer-wins without corrupting a shared count).
type Marker struct {
	Empty bool   `json:"empty"`
	At    string `json:"at"` // RFC3339
}

// Name is the tenant-scoped secret name a graph's poll marker is stored under.
// Exported so the scheduler builds the identical key it reads.
func Name(graphID string) string {
	return "pollstate." + graphID
}

// Report records whether the current poll run found new data, keyed by the
// running flow. foundData=false marks the fire empty (a no-op observation, a
// 304, zero new rows); true marks it active and lets the scheduler tighten
// back to the base interval. A no-op when the store is unwired or the job
// carries no tenant/graph.
func Report(ctx context.Context, job core.Job, foundData bool) {
	mu.RLock()
	w := writer
	mu.RUnlock()
	if w == nil || job.Tenant == "" || job.GraphID == "" {
		return
	}
	b, err := json.Marshal(Marker{Empty: !foundData, At: time.Now().UTC().Format(time.RFC3339)})
	if err != nil {
		return
	}
	// Best-effort: a failed write just means the scheduler keeps the current
	// cadence — never affects the flow's own outcome.
	_ = w(ctx, job.Tenant, Name(job.GraphID), string(b))
}

// Read returns the current marker for a graph, or nil when unset/unwired/
// unparseable. Used by the scheduler.
func Read(ctx context.Context, tenant, graphID string) *Marker {
	mu.RLock()
	r := reader
	mu.RUnlock()
	if r == nil || tenant == "" || graphID == "" {
		return nil
	}
	raw, err := r(ctx, tenant, Name(graphID))
	if err != nil || raw == "" {
		return nil
	}
	var m Marker
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return nil
	}
	return &m
}

// ParseAt parses a marker's At timestamp, returning the zero time when empty
// or malformed.
func (m *Marker) ParseAt() time.Time {
	if m == nil {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, m.At)
	if err != nil {
		return time.Time{}
	}
	return t
}
