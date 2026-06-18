package io

import (
	"os"
	"path/filepath"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
)

// TestScratchScheme_WriteThenRead proves the scratch:// scheme end to
// end: file_write lands the bytes under ScratchRoot (not the persistent
// workspace), the output Ref preserves the scheme, and file_read resolves
// the same scheme back to the same bytes.
func TestScratchScheme_WriteThenRead(t *testing.T) {
	workspace := t.TempDir()
	scratch := t.TempDir()

	wres, err := executeFileWrite(t.Context(), core.Job{
		WorkspaceRoot: workspace,
		ScratchRoot:   scratch,
		Params:        map[string]any{"path": "scratch://tmp/note.txt", "mkdirs": true},
		Input:         map[string]core.Ref{"in": {Inline: "ephemeral", MIME: "text/plain"}},
	}, nil)
	if err != nil || wres.Status != core.StatusOK {
		t.Fatalf("write: status=%q err=%v (%+v)", wres.Status, err, wres.Error)
	}
	// Output Ref keeps the scheme so downstream nodes resolve it the same.
	if got := wres.Output["out"].Ref; got != "scratch://tmp/note.txt" {
		t.Errorf("output Ref = %q, want scratch://tmp/note.txt", got)
	}
	// Bytes landed under ScratchRoot, NOT the persistent workspace.
	if _, err := os.Stat(filepath.Join(scratch, "tmp", "note.txt")); err != nil {
		t.Errorf("expected file under scratch root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(workspace, "tmp", "note.txt")); !os.IsNotExist(err) {
		t.Errorf("scratch write leaked into the workspace root")
	}

	rres, err := executeFileRead(t.Context(), core.Job{
		WorkspaceRoot: workspace,
		ScratchRoot:   scratch,
		Params:        map[string]any{"path": "scratch://tmp/note.txt", "mime": "text/plain", "inline": true},
	}, nil)
	if err != nil || rres.Status != core.StatusOK {
		t.Fatalf("read: status=%q err=%v (%+v)", rres.Status, err, rres.Error)
	}
	if got := rres.Output["out"].Inline; got != "ephemeral" {
		t.Errorf("read back %v, want \"ephemeral\"", got)
	}
}

// TestScratchScheme_NoScratchRootConfigured verifies a scratch:// path
// fails clearly when the run has no scratch area, rather than silently
// falling back to the workspace.
func TestScratchScheme_NoScratchRootConfigured(t *testing.T) {
	res, _ := executeFileWrite(t.Context(), core.Job{
		WorkspaceRoot: t.TempDir(), // workspace present, scratch absent
		Params:        map[string]any{"path": "scratch://x.txt"},
		Input:         map[string]core.Ref{"in": {Inline: "data"}},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "no_sandbox" {
		t.Errorf("status=%q code=%q, want error/no_sandbox", res.Status, res.Error.Code)
	}
}

// TestScratchScheme_CopyWorkspaceToScratch covers a mixed-root copy: the
// source ref is workspace-relative, the destination is scratch://.
func TestScratchScheme_CopyWorkspaceToScratch(t *testing.T) {
	workspace := t.TempDir()
	scratch := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "src.txt"), []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := executeFileWrite(t.Context(), core.Job{
		WorkspaceRoot: workspace,
		ScratchRoot:   scratch,
		Params:        map[string]any{"path": "scratch://copy.txt"},
		Input:         map[string]core.Ref{"in": {Ref: "src.txt"}},
	}, nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("copy: status=%q err=%v (%+v)", res.Status, err, res.Error)
	}
	got, err := os.ReadFile(filepath.Join(scratch, "copy.txt"))
	if err != nil || string(got) != "payload" {
		t.Errorf("scratch copy = %q err=%v, want \"payload\"", got, err)
	}
}
