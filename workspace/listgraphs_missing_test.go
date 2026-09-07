// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package workspace

import (
	"os"
	"path/filepath"
	"testing"
)

// TestListGraphsDistinguishesEmptyFromGone pins the difference a caller that
// deletes on absence depends on: a workspace with no flows reports none, a
// workspace whose directory has vanished reports an error. Both resolve no
// HEAD, so without the root check the second reads as the first — and the
// schedule reconcile prunes every schedule the workspace owned.
func TestListGraphsDistinguishesEmptyFromGone(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ws")
	store, err := OpenFS(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	// Present but empty: no flows, no error.
	ids, err := store.ListGraphs()
	if err != nil {
		t.Fatalf("empty workspace errored: %v", err)
	}
	if len(ids) != 0 {
		t.Fatalf("empty workspace listed %v", ids)
	}

	// The volume goes away underneath it.
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
