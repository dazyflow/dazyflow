package io

import (
	"os"
	"path/filepath"
	"testing"

	"git.sr.ht/~klahr/hazy-flow/core"
)

func TestFileRead_OK(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hello.txt")
	if err := os.WriteFile(path, []byte("hi"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	res, err := executeFileRead(t.Context(), core.Job{
		Params: map[string]any{"path": path, "mime": "text/plain"},
	}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q, err=%+v", res.Status, res.Error)
	}
	if res.Output["out"].Ref != path {
		t.Errorf("Ref = %q, want %q", res.Output["out"].Ref, path)
	}
	if res.Output["out"].MIME != "text/plain" {
		t.Errorf("MIME = %q, want text/plain", res.Output["out"].MIME)
	}
}

func TestFileRead_Missing(t *testing.T) {
	res, _ := executeFileRead(t.Context(), core.Job{
		Params: map[string]any{"path": "/nonexistent/path/here"},
	}, nil)
	if res.Status != core.StatusError {
		t.Fatalf("status=%q, want error", res.Status)
	}
}

func TestFileWrite_FromInline(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "out.txt")
	res, err := executeFileWrite(t.Context(), core.Job{
		Params: map[string]any{"path": dst},
		Input:  map[string]core.Ref{"in": {Inline: "hello"}},
	}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q, err=%+v", res.Status, res.Error)
	}
	data, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(data) != "hello" {
		t.Errorf("contents = %q, want hello", data)
	}
}

func TestFileWrite_FromFileRef(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.bin")
	dst := filepath.Join(dir, "dst.bin")
	if err := os.WriteFile(src, []byte("payload"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	res, err := executeFileWrite(t.Context(), core.Job{
		Params: map[string]any{"path": dst},
		Input:  map[string]core.Ref{"in": {Ref: src}},
	}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q, err=%+v", res.Status, res.Error)
	}
	data, _ := os.ReadFile(dst)
	if string(data) != "payload" {
		t.Errorf("contents = %q, want payload", data)
	}
}

func TestFileWrite_Mkdirs(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "nested", "deep", "out.txt")
	res, err := executeFileWrite(t.Context(), core.Job{
		Params: map[string]any{"path": dst, "mkdirs": true},
		Input:  map[string]core.Ref{"in": {Inline: "x"}},
	}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q, err=%+v", res.Status, res.Error)
	}
}
