// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"context"
	"sync"
	"time"

	"git.sr.ht/~klahr/dazyflow/core"
)

// memoryWriteDedupe is a process-local, bounded, TTL'd implementation of
// core.WriteDedupeStore. It protects single-node deployments: every worker is
// a goroutine in one dzd sharing one Engine (and thus one store), so a worker
// that reclaims another's expired-lease job sees the recorded write and skips
// re-firing it.
//
// Scope NOT here: cross-PROCESS dedupe. In a multi-node cluster a reclaim by a
// DIFFERENT dzd won't see this node's in-memory record, so a cross-node lease
// steal can still double-fire. A shared (Postgres-backed) implementation of
// the same interface closes that gap; the engine takes the interface so it can
// be swapped without touching connectors.
//
// The TTL only needs to outlive the re-execution window — an expired lease is
// reclaimed within a lease duration (tens of seconds) and crash recovery
// within minutes — so an hour is generous. The entry cap bounds memory under
// write-heavy load with FIFO eviction; an evicted entry just means a (rare)
// re-execution past the cap re-fires, which is the at-least-once contract.
const (
	writeDedupeTTL      = time.Hour
	writeDedupeMaxItems = 50_000
)

type dedupeEntry struct {
	result   core.Result
	storedAt time.Time
}

type memoryWriteDedupe struct {
	mu      sync.Mutex
	entries map[string]dedupeEntry
	order   []string
	now     func() time.Time // injectable for tests
}

// NewMemoryWriteDedupe builds an in-process write-dedupe store. cmd/dzd wires
// it onto the shared Engine; tests can construct their own.
func NewMemoryWriteDedupe() core.WriteDedupeStore {
	return &memoryWriteDedupe{
		entries: make(map[string]dedupeEntry),
		now:     time.Now,
	}
}

func (m *memoryWriteDedupe) Get(_ context.Context, key string) (core.Result, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[key]
	if !ok {
		return core.Result{}, false
	}
	// Treat a stale entry as absent (and drop it) so a recorded write can't
	// suppress a legitimate re-run forever.
	if m.now().Sub(e.storedAt) > writeDedupeTTL {
		delete(m.entries, key)
		m.removeFromOrderLocked(key)
		return core.Result{}, false
	}
	return e.result, true
}

func (m *memoryWriteDedupe) Put(_ context.Context, key string, result core.Result) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.entries[key]; !exists {
		m.order = append(m.order, key)
	}
	m.entries[key] = dedupeEntry{result: result, storedAt: m.now()}
	for len(m.entries) > writeDedupeMaxItems {
		drop := m.order[0]
		m.order = m.order[1:]
		delete(m.entries, drop)
	}
}

func (m *memoryWriteDedupe) removeFromOrderLocked(key string) {
	for i, k := range m.order {
		if k == key {
			m.order = append(m.order[:i], m.order[i+1:]...)
			return
		}
	}
}
