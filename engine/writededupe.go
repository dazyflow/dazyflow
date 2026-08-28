// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"context"
	"encoding/json"
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

// dedupePutTimeout bounds the post-success dedupe record write. The engine
// detaches it from the (possibly already-cancelled) execution context so a lost
// lease can't suppress the record, but a shared/Postgres store must still not
// block the worker forever if its backend hangs.
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
	// Return a deep copy, NOT the stored value: the caller (engine) mutates the
	// result after a dedupe hit (ApplyPassthrough adds a port, redactResult
	// reassigns ports AND mutates some slice shapes in place), which would
	// otherwise corrupt this stored entry and race other readers of the same
	// key. A JSON round-trip is a guaranteed-safe deep copy and mirrors exactly
	// what the Postgres store does on every Get (it unmarshals a fresh Result),
	// so replay behaviour is uniform across stores. Caveat: the round-trip is
	// lossless for JSON VALUES but coerces Go TYPES on replay (int→float64,
	// []byte→base64 string) — same as the Postgres path. Dedupe-eligible drops
	// (external writes: SMS/email/HTTP send) emit JSON-native status outputs, so
	// this doesn't bite in practice; a drop emitting a non-JSON-native Output
	// that a downstream node type-asserts should not opt into DedupeWrites.
	clone, err := cloneResult(e.result)
	if err != nil {
		// A result that won't round-trip can't have been persisted either;
		// treat the entry as absent so the caller re-runs rather than replaying
		// a corrupt value.
		return core.Result{}, false
	}
	return clone, true
}

// cloneResult deep-copies a Result by JSON round-trip so a dedupe replay can't
// alias the stored entry's maps. Matches the Postgres store's marshal/unmarshal.
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
	// Store a deep copy so a later mutation of the caller's `result` (it still
	// holds the same Output map we'd otherwise share) can't reach back into the
	// stored entry. Get also clones on the way out; cloning on both edges makes
	// the entry fully isolated. A clone failure falls back to storing as-is —
	// no worse than before, and Get's clone still protects readers.
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
