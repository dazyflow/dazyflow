package io

import (
	"os"
	"path/filepath"
	"testing"

	"git.sr.ht/~klahr/hazy-flow/core"
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

// excel_read must accept a "path" wired from upstream (file_picker)
// — the headline composability win. Verifies the input port wins
// over params.path so the user doesn't have to remove the path
// param when wiring the picker.
func TestExcelRead_PathFromInputPort(t *testing.T) {
	root := t.TempDir()
	seedXLSX(t, root, "input.xlsx", map[string][][]string{
		"Sheet1": {{"id"}, {"1"}, {"2"}},
	})

	res, err := executeExcelRead(t.Context(), core.Job{
		WorkspaceRoot: root,
		Params: map[string]any{
			// Wrong path on purpose — input port must override.
			"path": "nope.xlsx",
		},
		Input: map[string]core.Ref{
			"path": {MIME: "text/plain", Inline: "input.xlsx"},
		},
	}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v (input port should have overridden params.path)", res.Status, res.Error)
	}
	rows := res.Output["rows"].Inline.([]map[string]string)
	if len(rows) != 2 {
		t.Errorf("rows = %d, want 2", len(rows))
	}
}

// And the path-via-Ref form: when an old graph wires a file_read or
// custom upstream that publishes a Ref locator (Ref.Ref = path,
// Inline = nil), pickPath must still find the path.
func TestExcelRead_PathFromRefLocator(t *testing.T) {
	root := t.TempDir()
	seedXLSX(t, root, "input.xlsx", map[string][][]string{
		"Sheet1": {{"id"}, {"7"}},
	})

	res, _ := executeExcelRead(t.Context(), core.Job{
		WorkspaceRoot: root,
		Input: map[string]core.Ref{
			"path": {MIME: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", Ref: "input.xlsx"},
		},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
}

// Neither params.path nor an input port → clear error pointing at
// both knobs so the user knows what to do.
func TestExcelRead_MissingPathErrorIsHelpful(t *testing.T) {
	res, _ := executeExcelRead(t.Context(), core.Job{
		WorkspaceRoot: t.TempDir(),
	}, nil)
	if res.Status != core.StatusError || res.Error == nil || res.Error.Code != "bad_param" {
		t.Fatalf("status=%q error=%+v, want bad_param", res.Status, res.Error)
	}
}
