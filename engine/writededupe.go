// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/dazyflow/dazyflow/core"
)

// memoryWriteDedupe protects single-node deployments: every worker is a
// goroutine in one dzd sharing one store, so a worker reclaiming another's
// expired-lease job sees the recorded write and skips re-firing it. NOT
// cross-PROCESS — in a cluster a reclaim by a different dzd won't see this
// node's record, which a shared implementation of the same interface closes.
//
// The TTL only has to outlive the re-execution window — an expired lease is
// reclaimed within tens of seconds, crash recovery within minutes — so an hour
// is generous. The entry cap bounds memory with FIFO eviction, and an evicted
// entry just means a rare re-execution re-fires, which is the at-least-once
// contract.
const (
	writeDedupeTTL      = time.Hour
	writeDedupeMaxItems = 50_000
)

// dedupePutTimeout: the engine detaches the record write from the possibly
// cancelled execution context so a lost lease cannot suppress it, but a shared
// store must still not block the worker forever if its backend hangs.
const dedupePutTimeout = 5 * time.Second

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
	// A stale entry is absent, so a recorded write cannot suppress a legitimate
	// re-run forever.
	if m.now().Sub(e.storedAt) > writeDedupeTTL {
		delete(m.entries, key)
		m.removeFromOrderLocked(key)
		return core.Result{}, false
	}
	// Return a deep copy, NOT the stored value: the engine mutates the result after
	// a dedupe hit — ApplyPassthrough adds a port, redactResult reassigns ports and
	// mutates some slices in place — which would corrupt this entry and race other
	// readers. A JSON round-trip mirrors what the Postgres store does on every Get, so
	// replay behaviour is uniform.
	//
	// The round-trip is lossless for JSON VALUES but coerces Go TYPES on replay
	// (int→float64), same as the Postgres path. Dedupe-eligible drops emit JSON-native
	// status outputs, so a drop emitting a non-JSON-native Output that a downstream
	// node type-asserts should not opt into DedupeWrites.
	clone, err := cloneResult(e.result)
	if err != nil {
		// A result that won't round-trip can't have been persisted either, so treat it
		// as absent and re-run rather than replay a corrupt value.
		return core.Result{}, false
	}
	return clone, true
}

func cloneResult(r core.Result) (core.Result, error) {
	blob, err := json.Marshal(r)
	if err != nil {
		return core.Result{}, err
	}
	var out core.Result
	if err := json.Unmarshal(blob, &out); err != nil {
		return core.Result{}, err
	}
	return out, nil
}

func (m *memoryWriteDedupe) Put(_ context.Context, key string, result core.Result) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.entries[key]; !exists {
		m.order = append(m.order, key)
	}
	if clone, err := cloneResult(result); err == nil {
		result = clone
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
