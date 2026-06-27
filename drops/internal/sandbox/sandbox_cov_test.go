package sandbox

import (
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
)

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
