// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package workspace

import (
	"os"
	"path/filepath"
	"testing"
)

func TestListGraphsDistinguishesEmptyFromGone(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ws")
	store, err := OpenFS(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	ids, err := store.ListGraphs()
	if err != nil {
		t.Fatalf("empty workspace errored: %v", err)
	}
	if len(ids) != 0 {
		t.Fatalf("empty workspace listed %v", ids)
	}

	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ListGraphs(); err == nil {
		t.Error("a workspace whose directory is gone reported no flows and no error")
	}
}

// The in-memory backend has no directory to lose and must stay usable.
func TestListGraphsMemoryBackendHasNoRoot(t *testing.T) {
	store, err := OpenFS("")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := store.ListGraphs(); err != nil {
		t.Fatalf("memory workspace errored: %v", err)
	}
}
