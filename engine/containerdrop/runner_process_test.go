package containerdrop

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"

	"git.sr.ht/~klahr/hazy-flow/core"
)

// Proves the real cross-process path: a SEPARATE process (the testdrop binary)
// is the drop, reaching the broker over the unix socket and reporting its
// result — containers minus the isolation layer. The gVisor/Docker runners are
// the same shape with a different launch command.
func TestProcessRunner_RealSubprocess(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "testdrop")
	if out, err := exec.Command("go", "build", "-o", bin, "./internal/testdrop").CombinedOutput(); err != nil {
		t.Fatalf("build testdrop: %v\n%s", err, out)
	}

	tr := NewTransport(
		core.Manifest{ID: "t"},
		DropRef{ID: "t", Argv: []string{bin}},
		ProcessRunner{},
		Host{},
	)
	res, err := tr.Execute(context.Background(), core.Job{
		ID:     "j",
		Params: map[string]any{"p": "hi"},
	}, make(chan core.Progress, 1))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("status=%v error=%+v", res.Status, res.Error)
	}
	if got := res.Output["out"].Inline; got != "echo:hi" {
		t.Errorf("subprocess drop output = %#v, want %q", got, "echo:hi")
	}
}
