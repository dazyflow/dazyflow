package containerdrop

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"git.sr.ht/~klahr/hazy-flow/core"
)

// A drop that never returns a result and outlives its run budget is killed and
// surfaced as a structured "timeout", not left to pin the worker — the backstop
// the in-process executor used to provide.
func TestTransport_RunBudgetTimeout(t *testing.T) {
	// Runner blocks until ctx is cancelled (the budget firing) — the drop never
	// POSTs /result, so this exercises the !ok timeout branch.
	blocked := RunnerFunc(func(ctx context.Context, _ string, _ DropRef) error {
		<-ctx.Done()
		return ctx.Err()
	})
	tr := NewTransport(core.Manifest{ID: "slow"}, DropRef{ID: "slow"}, blocked, Host{})
	tr.MaxRunDuration = 50 * time.Millisecond

	start := time.Now()
	res, err := tr.Execute(context.Background(), core.Job{ID: "j"}, make(chan core.Progress, 1))
	if err != nil {
		t.Fatalf("infra error: %v", err)
	}
	if res.Status != core.StatusError || res.Error == nil || res.Error.Code != "timeout" {
		t.Fatalf("want timeout error, got status=%v err=%+v", res.Status, res.Error)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("budget did not fire promptly: took %s", elapsed)
	}
}

// A sooner caller deadline wins over the run budget and still yields a timeout.
func TestTransport_CallerDeadlineWins(t *testing.T) {
	blocked := RunnerFunc(func(ctx context.Context, _ string, _ DropRef) error {
		<-ctx.Done()
		return ctx.Err()
	})
	tr := NewTransport(core.Manifest{ID: "slow"}, DropRef{ID: "slow"}, blocked, Host{})
	tr.MaxRunDuration = time.Hour // budget far away; caller deadline should win

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	res, err := tr.Execute(ctx, core.Job{ID: "j"}, make(chan core.Progress, 1))
	if err != nil {
		t.Fatalf("infra error: %v", err)
	}
	if res.Status != core.StatusError || res.Error == nil || res.Error.Code != "timeout" {
		t.Fatalf("want timeout error, got status=%v err=%+v", res.Status, res.Error)
	}
}

// ProcessRunner captures a failed drop's stderr (when the caller wired none) so
// a crash-before-result is debuggable instead of an opaque exit code.
func TestProcessRunner_SurfacesStderr(t *testing.T) {
	r := ProcessRunner{}
	sock := filepath.Join(t.TempDir(), "x.sock")
	err := r.Run(context.Background(), sock, DropRef{
		ID:   "boomer",
		Argv: []string{"sh", "-c", "echo kaboom 1>&2; exit 3"},
	})
	if err == nil {
		t.Fatal("expected a non-zero exit error")
	}
	if !strings.Contains(err.Error(), "kaboom") {
		t.Errorf("stderr tail not surfaced in error: %v", err)
	}
}
