package io

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.sr.ht/~klahr/hazyflow/core"
)

func TestFileRead_OK(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "hello.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	res, err := executeFileRead(t.Context(), core.Job{
		WorkspaceRoot: root,
		Params:        map[string]any{"path": "hello.txt", "mime": "text/plain"},
	}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q, err=%+v", res.Status, res.Error)
	}
	if res.Output["out"].Ref != "hello.txt" {
		t.Errorf("Ref = %q, want workspace-relative 'hello.txt'", res.Output["out"].Ref)
	}
}

func TestFileRead_MissingSandbox(t *testing.T) {
	res, _ := executeFileRead(t.Context(), core.Job{
		Params: map[string]any{"path": "x"},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "no_sandbox" {
		t.Errorf("status=%q code=%q, want no_sandbox", res.Status, res.Error.Code)
	}
}

func TestFileRead_PathTraversalBlocked(t *testing.T) {
	root := t.TempDir()
	// Put a victim file outside the sandbox.
	victim := filepath.Join(filepath.Dir(root), "secret.txt")
	if err := os.WriteFile(victim, []byte("hidden"), 0o644); err != nil {
		t.Fatalf("seed victim: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(victim) })

	for _, attempt := range []string{
		"../secret.txt",
		"../../etc/passwd",
		"/etc/passwd",
	} {
		t.Run(attempt, func(t *testing.T) {
			res, _ := executeFileRead(t.Context(), core.Job{
				WorkspaceRoot: root,
				Params:        map[string]any{"path": attempt},
			}, nil)
			if res.Status != core.StatusError {
				t.Fatalf("status=%q (read succeeded — sandbox bypassed?)", res.Status)
			}
			if res.Error == nil || !strings.Contains(res.Error.Code, "sandbox") {
				// Some attempts may surface as plain "io" (file not found
				// when os.Root resolves the path inside the sandbox); the
				// important thing is the read DIDN'T succeed.
				t.Logf("blocked via %q: %s", res.Error.Code, res.Error.Message)
			}
		})
	}
}

func TestFileRead_SymlinkEscapeBlocked(t *testing.T) {
	root := t.TempDir()
	victim := filepath.Join(filepath.Dir(root), "secret-symlinked.txt")
	if err := os.WriteFile(victim, []byte("hidden"), 0o644); err != nil {
		t.Fatalf("seed victim: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(victim) })

	// Plant a symlink inside the sandbox pointing outside.
	link := filepath.Join(root, "escape")
	if err := os.Symlink(victim, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	res, _ := executeFileRead(t.Context(), core.Job{
		WorkspaceRoot: root,
		Params:        map[string]any{"path": "escape"},
	}, nil)
	if res.Status == core.StatusOK {
		t.Fatalf("symlink read should not have succeeded")
	}
}

func TestFileWrite_FromInline(t *testing.T) {
	root := t.TempDir()
	res, err := executeFileWrite(t.Context(), core.Job{
		WorkspaceRoot: root,
		Params:        map[string]any{"path": "out.txt"},
		Input:         map[string]core.Ref{"in": {Inline: "hello"}},
	}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q, err=%+v", res.Status, res.Error)
	}
	data, err := os.ReadFile(filepath.Join(root, "out.txt"))
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(data) != "hello" {
		t.Errorf("contents = %q, want hello", data)
	}
}

func TestFileWrite_FromFileRef(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "src.bin"), []byte("payload"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	res, err := executeFileWrite(t.Context(), core.Job{
		WorkspaceRoot: root,
		Params:        map[string]any{"path": "dst.bin"},
		Input:         map[string]core.Ref{"in": {Ref: "src.bin"}},
	}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q, err=%+v", res.Status, res.Error)
	}
	data, _ := os.ReadFile(filepath.Join(root, "dst.bin"))
	if string(data) != "payload" {
		t.Errorf("contents = %q, want payload", data)
	}
}

func TestFileWrite_Mkdirs(t *testing.T) {
	root := t.TempDir()
	res, err := executeFileWrite(t.Context(), core.Job{
		WorkspaceRoot: root,
		Params:        map[string]any{"path": "nested/deep/out.txt", "mkdirs": true},
		Input:         map[string]core.Ref{"in": {Inline: "x"}},
	}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q, err=%+v", res.Status, res.Error)
	}
}

func TestFileWrite_PathTraversalBlocked(t *testing.T) {
	root := t.TempDir()
	for _, attempt := range []string{
		"../escape.txt",
		"/tmp/absolute-escape.txt",
		"../../etc/passwd",
	} {
		t.Run(attempt, func(t *testing.T) {
			res, _ := executeFileWrite(t.Context(), core.Job{
				WorkspaceRoot: root,
				Params:        map[string]any{"path": attempt},
				Input:         map[string]core.Ref{"in": {Inline: "evil"}},
			}, nil)
			if res.Status == core.StatusOK {
				t.Fatalf("write to %q should not have succeeded", attempt)
			}
		})
	}
}

func TestFileWrite_QuotaCheck(t *testing.T) {
	cases := []struct {
		name     string
		limit    int64
		used     int64
		payload  string
		wantOK   bool
		wantCode string
	}{
		{name: "under limit", limit: 100, used: 0, payload: "hello", wantOK: true},
		{name: "exactly at limit", limit: 5, used: 0, payload: "hello", wantOK: true},
		{name: "over limit", limit: 5, used: 0, payload: "hello!", wantOK: false, wantCode: "quota_exceeded"},
		{name: "used pushes over", limit: 10, used: 8, payload: "abcd", wantOK: false, wantCode: "quota_exceeded"},
		{name: "unlimited", limit: 0, used: 1 << 30, payload: "anything", wantOK: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			res, _ := executeFileWrite(t.Context(), core.Job{
				ID:            "j",
				WorkspaceRoot: root,
				Tenant:        "acme",
				QuotaLimit:    tc.limit,
				QuotaUsed:     tc.used,
				Params:        map[string]any{"path": "out.txt"},
				Input:         map[string]core.Ref{"in": {Inline: tc.payload}},
			}, nil)
			if tc.wantOK {
				if res.Status != core.StatusOK {
					t.Fatalf("status = %q (%+v); want ok", res.Status, res.Error)
				}
				if _, err := os.Stat(filepath.Join(root, "out.txt")); err != nil {
					t.Errorf("expected file written: %v", err)
				}
				return
			}
			if res.Status != core.StatusError {
				t.Fatalf("status = %q, want error", res.Status)
			}
			if res.Error == nil || res.Error.Code != tc.wantCode {
				t.Errorf("error code = %q, want %q", res.Error.Code, tc.wantCode)
			}
			if _, err := os.Stat(filepath.Join(root, "out.txt")); !os.IsNotExist(err) {
				t.Errorf("blocked write left file on disk: %v", err)
			}
		})
	}
}

func TestFileWrite_QuotaCountsRefInputSize(t *testing.T) {
	root := t.TempDir()
	// Source file is 200 bytes; tenant limit is 100. Even though
	// QuotaUsed = 0, the engine snapshot can't see the input size
	// (only the module can), so this is the choke point.
	src := filepath.Join(root, "src.bin")
	if err := os.WriteFile(src, make([]byte, 200), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	res, _ := executeFileWrite(t.Context(), core.Job{
		ID:            "j",
		WorkspaceRoot: root,
		Tenant:        "acme",
		QuotaLimit:    100,
		QuotaUsed:     0,
		Params:        map[string]any{"path": "dst.bin"},
		Input:         map[string]core.Ref{"in": {Ref: "src.bin"}},
	}, nil)
	if res.Status != core.StatusError {
		t.Fatalf("status = %q, want error (200 > 100)", res.Status)
	}
	if res.Error == nil || res.Error.Code != "quota_exceeded" {
		t.Errorf("error code = %q, want quota_exceeded", res.Error.Code)
	}
}

func TestFileWrite_RefFromOutsideSandboxBlocked(t *testing.T) {
	root := t.TempDir()
	// Attacker hand-crafts an input Ref pointing at a path that resolves
	// outside the workspace. os.Root.Open must reject it.
	res, _ := executeFileWrite(t.Context(), core.Job{
		WorkspaceRoot: root,
		Params:        map[string]any{"path": "captured.bin"},
		Input:         map[string]core.Ref{"in": {Ref: "../../../etc/passwd"}},
	}, nil)
	if res.Status == core.StatusOK {
		t.Fatalf("read-from-outside-sandbox should not succeed")
	}
}
