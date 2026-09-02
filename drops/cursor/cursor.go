// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package cursor is the per-tenant key/value seam that polling drops use for
// watermarks and dedupe windows. The daemon wires it at startup to the
// encrypted secret store under a reserved "cursor." prefix; a drop only ever
// calls Read and Write.
package cursor

import (
	"context"
	"sync"
)

type (
	// Reader returns the stored value for an exact tenant/name, or ("", nil)
	// when nothing has been stored yet.
	Reader func(ctx context.Context, tenant, name string) (string, error)
	// Writer persists one value.
	Writer func(ctx context.Context, tenant, name, value string) error
)

var (
	mu     sync.RWMutex
	reader Reader
	writer Writer
)

// SetStore installs the read/write pair. nil, nil uninstalls it.
func SetStore(r Reader, w Writer) {
	mu.Lock()
	defer mu.Unlock()
	reader, writer = r, w
}

// Read returns the stored value, or "" when no store is wired, nothing is
// stored, or the read fails: every one of those means "start from the
// beginning".
func Read(ctx context.Context, tenant, name string) string {
	mu.RLock()
	r := reader
	mu.RUnlock()
	if r == nil {
		return ""
	}
	v, err := r(ctx, tenant, name)
	if err != nil {
		return ""
	}
	return v
}

// Write persists value. It is a no-op when no store is wired.
func Write(ctx context.Context, tenant, name, value string) error {
	mu.RLock()
	w := writer
	mu.RUnlock()
	if w == nil {
		return nil
	}
	return w(ctx, tenant, name, value)
}
