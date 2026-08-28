// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package limits centralizes the resource ceilings that keep a single drop
// from exhausting the daemon's memory when a flow author hands it (or a join
// amplifies it into) an unbounded amount of data.
//
// A transform node holds its entire input in memory, and a few drops multiply
// it — join_rows can emit up to left×right rows on a many-to-many key, and
// for_each allocates a result slot and goroutine per item. Without a ceiling,
// a flow that feeds one of these a giant list is an out-of-memory vector that
// no per-call timeout catches (the allocation happens before any I/O). MaxRows
// is the cap; drops reject inputs/outputs that exceed it with a structured
// error rather than trying to process them.
package limits

import (
	"os"
	"strconv"
	"sync"
)

// DefaultMaxRows is the row/item ceiling when DAZYFLOW_MAX_ROWS is unset. It's
// generous for genuine batch work (a million rows) while still bounding memory
// to something a daemon can hold.
const DefaultMaxRows = 1_000_000

var (
	mu      sync.RWMutex
	maxRows = envInt("DAZYFLOW_MAX_ROWS", DefaultMaxRows)
)

// MaxRows is the most rows (or list items, or joined output rows) a single
// drop will accept before failing fast. Operators with genuinely larger
// batches raise it via the DAZYFLOW_MAX_ROWS environment variable; a value
// that isn't a positive integer is ignored.
func MaxRows() int {
	mu.RLock()
	defer mu.RUnlock()
	return maxRows
}

// SetMaxRows overrides the ceiling and returns a function that restores the
// previous value. Intended for tests that must trip the limit without
// allocating millions of rows; not used by production code.
func SetMaxRows(n int) (restore func()) {
	mu.Lock()
	prev := maxRows
	maxRows = n
	mu.Unlock()
	return func() {
		mu.Lock()
		maxRows = prev
		mu.Unlock()
	}
}

func envInt(key string, def int) int {
	if s := os.Getenv(key); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			return n
		}
	}
	return def
}
