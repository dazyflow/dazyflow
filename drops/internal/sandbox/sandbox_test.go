// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package sandbox

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/dazyflow/dazyflow/core"
)

func TestResolve(t *testing.T) {
	t.Run("workspace-relative path", func(t *testing.T) {
		job := core.Job{WorkspaceRoot: "/ws"}
		root, rel, err := Resolve(job, "sub/file.txt")
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if root != "/ws" || rel != "sub/file.txt" {
			t.Errorf("Resolve = (%q, %q), want (/ws, sub/file.txt)", root, rel)
		}
	})
	// The redundant workspace:// spelling several step examples carried. It
	// used to pass through unresolved, so the path cleaned to
	// "workspace:/sub/file.txt" and the step wrote into a directory literally
	// named "workspace:" while reporting success.
	t.Run("legacy workspace:// prefix resolves to the workspace", func(t *testing.T) {
		job := core.Job{WorkspaceRoot: "/ws"}
		root, rel, err := Resolve(job, WorkspaceScheme+"sub/file.txt")
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if root != "/ws" || rel != "sub/file.txt" {
			t.Errorf("Resolve = (%q, %q), want (/ws, sub/file.txt)", root, rel)
		}
		// And it lands in exactly the same place as the canonical spelling.
		bareRoot, bareRel, err := Resolve(job, "sub/file.txt")
		if err != nil {
			t.Fatalf("bare err = %v", err)
		}
		if root != bareRoot || rel != bareRel {
			t.Errorf("prefixed (%q,%q) != bare (%q,%q)", root, rel, bareRoot, bareRel)
		}
	})
	// Only a LEADING prefix is a scheme. A file whose name happens to contain
	// the text is just a file.
	t.Run("the prefix is only stripped at the front", func(t *testing.T) {
		job := core.Job{WorkspaceRoot: "/ws"}
		_, rel, err := Resolve(job, "notes/workspace://odd.txt")
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if rel != "notes/workspace://odd.txt" {
			t.Errorf("rel = %q, want it untouched", rel)
		}
	})
	// scratch:// still wins: it is a real second root, not a spelling.
	t.Run("scratch path resolves against ScratchRoot", func(t *testing.T) {
		job := core.Job{WorkspaceRoot: "/ws", ScratchRoot: "/scratch"}
		root, rel, err := Resolve(job, Scheme+"a/b.json")
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if root != "/scratch" || rel != "a/b.json" {
			t.Errorf("Resolve = (%q, %q), want (/scratch, a/b.json)", root, rel)
		}
	})
	t.Run("scratch path without scratch root errors", func(t *testing.T) {
		job := core.Job{WorkspaceRoot: "/ws"} // no ScratchRoot
		if _, _, err := Resolve(job, Scheme+"x"); err == nil {
			t.Error("want error for scratch:// path with no scratch root")
		}
	})
	t.Run("no workspace configured errors", func(t *testing.T) {
		if _, _, err := Resolve(core.Job{}, "file.txt"); err == nil {
			t.Error("want error when no workspace sandbox is configured")
		}
	})
}

func TestOpenRoot(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "inside.txt"), []byte("hi"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Run("opens and confines to workspace", func(t *testing.T) {
		root, rel, err := OpenRoot(core.Job{WorkspaceRoot: dir}, "inside.txt")
		if err != nil {
			t.Fatalf("OpenRoot: %v", err)
		}
		defer root.Close()
		if rel != "inside.txt" {
			t.Errorf("rel = %q, want inside.txt", rel)
		}
		f, err := root.Open(rel)
		if err != nil {
			t.Fatalf("open confined file: %v", err)
		}
		f.Close()
	})

	t.Run("traversal outside the root is rejected", func(t *testing.T) {
		root, _, err := OpenRoot(core.Job{WorkspaceRoot: dir}, ".")
		if err != nil {
			t.Fatalf("OpenRoot: %v", err)
		}
		defer root.Close()
		// os.Root must refuse a path that climbs out of the tree, and the
		// resulting error must be classified as an escape.
		_, err = root.Open("../../../etc/passwd")
		if err == nil {
			t.Fatal("expected traversal to be rejected")
		}
		if !IsEscape(err) {
			t.Errorf("IsEscape(%v) = false, want true", err)
		}
	})

	t.Run("missing root directory errors", func(t *testing.T) {
		_, _, err := OpenRoot(core.Job{WorkspaceRoot: filepath.Join(dir, "does-not-exist")}, "x")
		if err == nil {
			t.Error("want error opening a non-existent root")
		}
	})
}

func TestIsEscape(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"os.ErrInvalid", os.ErrInvalid, true},
		{"wrapped os.ErrInvalid", fmt.Errorf("op: %w", os.ErrInvalid), true},
		{"path escapes message", errors.New("openat: path escapes from parent"), true},
		{"outside root message", errors.New("resolves to a location outside root"), true},
		{"invalid argument message", errors.New("readlinkat: invalid argument"), true},
		{"unrelated error", errors.New("permission denied"), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := IsEscape(c.err); got != c.want {
				t.Errorf("IsEscape(%v) = %v, want %v", c.err, got, c.want)
			}
		})
	}
}

func TestOpenRoot_MissingDirErrors(t *testing.T) {
	// ScratchRoot points at a path that doesn't exist, so os.OpenRoot fails
	// and OpenRoot wraps it with "open root".
	job := core.Job{ScratchRoot: "/nonexistent-sandbox-root-xyz"}
	if _, _, err := OpenRoot(job, "scratch://file.txt"); err == nil {
		t.Fatal("OpenRoot on a missing root: want error, got nil")
	}
}

func TestOpenRoot_ResolveErrorPropagates(t *testing.T) {
	// A scratch:// path with no scratch root configured fails in Resolve,
	// before any os.OpenRoot attempt.
	if _, _, err := OpenRoot(core.Job{}, "scratch://x"); err == nil {
		t.Fatal("OpenRoot with no scratch root: want resolve error, got nil")
	}
}
