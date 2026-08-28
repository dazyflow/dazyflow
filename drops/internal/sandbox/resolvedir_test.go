// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package sandbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestResolveDir_RejectsSymlinkEscape is the reason ResolveDir exists. Rel()
// is a pure string check, so "link" — a symlink inside the workspace pointing
// outside it — passes cleaning and would then be followed by cmd.Dir/go-git,
// running the command outside the sandbox. Resolving through *os.Root makes
// the kernel refuse it.
func TestResolveDir_RejectsSymlinkEscape(t *testing.T) {
	outside := t.TempDir()
	ws := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(ws, "escape")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	// The string-cleaning check accepts it — that's the gap being closed.
	if _, err := Rel("escape"); err != nil {
		t.Fatalf("Rel rejected a plain name: %v", err)
	}
	// The root-confined resolve must not.
	if dir, _, err := ResolveDir(ws, "escape"); err == nil {
		t.Fatalf("ResolveDir followed a symlink out of the workspace, to %q", dir)
	} else if !strings.Contains(err.Error(), "escapes workspace") {
		t.Errorf("error = %v, want an escapes-workspace rejection", err)
	}

	// A nested path through the symlink is refused too.
	if _, _, err := ResolveDir(ws, "escape/deeper"); err == nil {
		t.Error("ResolveDir followed a path through an escaping symlink")
	}
}

func TestResolveDir_AllowsLegitimatePaths(t *testing.T) {
	ws := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ws, "a", "b"), 0o755); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct{ in, wantRel string }{
		{"", "."},
		{".", "."},
		{"a", "a"},
		{"a/b", filepath.Join("a", "b")},
		{"./a/b", filepath.Join("a", "b")},
	} {
		dir, rel, err := ResolveDir(ws, tc.in)
		if err != nil {
			t.Errorf("ResolveDir(%q) = %v", tc.in, err)
			continue
		}
		if rel != tc.wantRel {
			t.Errorf("ResolveDir(%q) rel = %q, want %q", tc.in, rel, tc.wantRel)
		}
		if want := filepath.Join(ws, tc.wantRel); dir != want {
			t.Errorf("ResolveDir(%q) dir = %q, want %q", tc.in, dir, want)
		}
	}
}

func TestResolveDir_RejectsTraversalAndFiles(t *testing.T) {
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "afile"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, in := range []string{"..", "../escape", "sub/../..", "/etc"} {
		if _, _, err := ResolveDir(ws, in); err == nil {
			t.Errorf("ResolveDir(%q) accepted a traversal", in)
		}
	}
	// A regular file is not a working directory.
	if _, _, err := ResolveDir(ws, "afile"); err == nil {
		t.Error("ResolveDir accepted a regular file as a directory")
	}
	// A missing directory is an error, not a silent pass-through.
	if _, _, err := ResolveDir(ws, "nope"); err == nil {
		t.Error("ResolveDir accepted a nonexistent directory")
	}
	// No workspace configured at all.
	if _, _, err := ResolveDir("", "a"); err == nil {
		t.Error("ResolveDir accepted an empty root")
	}
}
