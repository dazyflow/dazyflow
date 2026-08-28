// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
)

// jsonFileStore is the shared backing for this package's dev JSON-file
// stores (JSONUserStore, JSONInvitationStore). It holds the records in a
// map keyed by K, guards them with a mutex, and rewrites the whole file
// atomically (.tmp + rename) on flush. Records are persisted as a JSON
// array of V; on load each element's key is derived via the keyOf
// extractor, so the map key never has to live in the serialized form.
//
// Embedders own the public API (Get/Put/List/...) and reach into mu,
// items, and flushLocked directly — the embedded fields and methods are
// promoted, so a JSONUserStore can lock s.mu and call s.flushLocked()
// exactly as it did when the machinery was inlined.
type jsonFileStore[K comparable, V any] struct {
	mu        sync.RWMutex
	path      string
	items     map[K]V
	keyOf     func(V) K
	normalize func(V) V
}

// newJSONFileStore constructs the embedded store and loads any existing
// file. keyOf extracts the map key from a record. normalize canonicalizes
// a record on load (e.g. lower-casing an email) before it's keyed and
// stored — pass nil for identity. An empty path means in-memory only
// (load is a no-op, flush is skipped).
func newJSONFileStore[K comparable, V any](path string, keyOf func(V) K, normalize func(V) V) (*jsonFileStore[K, V], error) {
	if normalize == nil {
		normalize = func(v V) V { return v }
	}
	s := &jsonFileStore[K, V]{path: path, items: make(map[K]V), keyOf: keyOf, normalize: normalize}
	if path == "" {
		return s, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return s, nil
		}
		return nil, fmt.Errorf("read %q: %w", path, err)
	}
	var slice []V
	if err := json.Unmarshal(data, &slice); err != nil {
		return nil, fmt.Errorf("parse %q: %w", path, err)
	}
	for _, v := range slice {
		v = normalize(v)
		s.items[keyOf(v)] = v
	}
	return s, nil
}

// flushLocked rewrites the file from the current map. Callers must hold
// s.mu (write). A zero path skips the write (in-memory mode).
func (s *jsonFileStore[K, V]) flushLocked() error {
	if s.path == "" {
		return nil
	}
	slice := make([]V, 0, len(s.items))
	for _, v := range s.items {
		slice = append(slice, v)
	}
	data, err := json.MarshalIndent(slice, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
