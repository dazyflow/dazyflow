// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package io

import (
	"os"
	"path/filepath"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
)

func TestFilePicker_EmitsPathAndFile(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "data.csv"), []byte("a,b\n1,2\n"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	res, err := executeFilePicker(t.Context(), core.Job{
		WorkspaceRoot: root,
		Params:        map[string]any{"path": "data.csv"},
	}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}

	if p, _ := res.Output["path"].Inline.(string); p != "data.csv" {
		t.Errorf("path output = %q, want data.csv", p)
	}
	file := res.Output["file"]
	if file.Ref != "data.csv" {
		t.Errorf("file.Ref = %q, want data.csv", file.Ref)
	}
	// MIME guess from the .csv extension — the table in
	// guessMIMEByExt maps that to text/csv.
	if file.MIME != "text/csv" {
		t.Errorf("file.MIME = %q, want text/csv", file.MIME)
	}
	// Bytes must NOT be inlined by default — that's the whole point
	// of decoupling the picker from the read.
	if file.Inline != nil {
		t.Errorf("file.Inline = %v, want nil (inline default off)", file.Inline)
	}
}

func TestFilePicker_InlineModeReadsBytes(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "note.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	res, _ := executeFilePicker(t.Context(), core.Job{
		WorkspaceRoot: root,
		Params:        map[string]any{"path": "note.txt", "inline": true},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	file := res.Output["file"]
	// Text MIME → string inline so the value survives gRPC's JSON
	// wrapping (same convention file_read uses).
	if got, _ := file.Inline.(string); got != "hello" {
		t.Errorf("inline = %v, want %q", file.Inline, "hello")
	}
	// And in inline mode Ref.Ref is cleared — the locator and the
	// content shouldn't both claim to be the source of truth.
	if file.Ref != "" {
		t.Errorf("file.Ref = %q, want empty in inline mode", file.Ref)
	}
}

func TestFilePicker_DirectoryIsAnError(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	res, _ := executeFilePicker(t.Context(), core.Job{
		WorkspaceRoot: root,
		Params:        map[string]any{"path": "sub"},
	}, nil)
	if res.Status != core.StatusError || res.Error == nil || res.Error.Code != "io" {
		t.Errorf("status=%q error=%+v, want io error for directory", res.Status, res.Error)
	}
}

func TestFilePicker_SandboxEscapeRejected(t *testing.T) {
	root := t.TempDir()
	res, _ := executeFilePicker(t.Context(), core.Job{
		WorkspaceRoot: root,
		Params:        map[string]any{"path": "../escape.txt"},
	}, nil)
	if res.Status != core.StatusError || res.Error == nil {
		t.Fatalf("status=%q, want error for sandbox escape", res.Status)
	}
}
