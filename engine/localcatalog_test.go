package engine

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"git.sr.ht/~klahr/hazyflow/core"
)

func TestLocalCatalog_LoadDir_MissingDir(t *testing.T) {
	c := NewLocalCatalog()
	if err := c.LoadDir(filepath.Join(t.TempDir(), "does-not-exist")); err == nil {
		t.Error("LoadDir(missing dir): want error")
	}
}

func TestLocalCatalog_LoadDir_CollectsErrorsSkipsNonJSON(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("ignore.txt", "not a descriptor")                                    // non-.json: skipped silently
	write("bad.json", "{ this is not json")                                    // parse error: collected
	write("badruntime.json", `{"id":"x","runtime":"wasm","path":"/bin/true"}`) // Register error: collected

	c := NewLocalCatalog()
	err := c.LoadDir(dir)
	if err == nil {
		t.Fatal("LoadDir with bad descriptors: want joined error")
	}
	// Nothing valid registered.
	if m := c.Manifests(); len(m) != 0 {
		t.Errorf("Manifests = %v, want empty", m)
	}
	if _, ok := c.Get("x"); ok {
		t.Error("Get(x): should not be registered after a runtime error")
	}
}

func TestLocalCatalog_Register_BadRuntime(t *testing.T) {
	c := NewLocalCatalog()
	err := c.Register(LocalDescriptor{ID: "x", Runtime: "wasm", Path: "/bin/true"})
	if err == nil {
		t.Error("Register(unsupported runtime): want error")
	}
}

func TestLocalCatalog_Register_HandshakeFailsOnMissingBinary(t *testing.T) {
	c := NewLocalCatalog()
	c.HandshakeTimeout = time.Second
	err := c.Register(LocalDescriptor{ID: "x", Runtime: "executable", Path: "/nonexistent/hazyflow-binary"})
	if err == nil {
		t.Error("Register(missing binary): want handshake error")
	}
}

func TestLocalErr(t *testing.T) {
	r := localErr(core.Job{ID: "j1"}, "spawn", errors.New("boom"))
	if r.Status != core.StatusError || r.JobID != "j1" {
		t.Fatalf("localErr = %+v", r)
	}
	if r.Error == nil || r.Error.Code != "spawn" || r.Error.Message != "boom" {
		t.Errorf("localErr.Error = %+v", r.Error)
	}
}
