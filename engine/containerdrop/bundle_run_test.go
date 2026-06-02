package containerdrop

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"git.sr.ht/~klahr/hazyflow/core"
)

// TestAuthoringBundle_RunsInNode proves the Phase C authoring path end to end:
// a MULTI-FILE drop (drop.ts importing a helper module) is bundled by
// `hz-drops bundle` into one self-contained ESM file, then runs through the Node
// drop host with the imported code inlined and executing. This is the same
// mechanism that inlines an npm dependency — esbuild resolves node_modules and
// relative imports identically — so it stands in for "use any pure-JS npm pkg".
func TestAuthoringBundle_RunsInNode(t *testing.T) {
	node := nodeHostArgv(t) // skips if node absent

	// Build the hz-drops tool and bundle a two-file drop.
	tool := filepath.Join(t.TempDir(), "hz-drops")
	if out, err := exec.Command("go", "build", "-o", tool, "../../cmd/hz-drops").CombinedOutput(); err != nil {
		t.Fatalf("build hz-drops: %v\n%s", err, out)
	}
	src := t.TempDir()
	mustWrite(t, filepath.Join(src, "greeting.ts"), `export function greet(n){ return "hello " + n; }`)
	mustWrite(t, filepath.Join(src, "drop.ts"), `
import { greet } from "./greeting";
export default {
  manifest: { id: "greeter", version: "1.0.0", label: "Greeter", summary: "greets.",
    outputs: [{ port: "out" }], examples: [{ title: "x", params: {} }] },
  run(ctx) { return { out: greet(ctx.params.who || "world") }; },
};`)

	bundlePath := filepath.Join(src, "drop.bundle.js")
	if out, err := exec.Command(tool, "bundle", "-o", bundlePath, filepath.Join(src, "drop.ts")).CombinedOutput(); err != nil {
		t.Fatalf("bundle: %v\n%s", err, out)
	}
	bundle, err := os.ReadFile(bundlePath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(bundle), `from "./greeting"`) {
		t.Fatal("bundle still has the relative import — esbuild did not inline it")
	}

	// --emit-manifest reads the manifest from the bundle WITHOUT a broker (the
	// install-time inspection path that replaces goja's in-process Inspect).
	man, err := exec.Command(node[0], node[1], "--source", bundlePath, "--emit-manifest").Output()
	if err != nil || !strings.Contains(string(man), `"greeter"`) {
		t.Fatalf("emit-manifest = %q err=%v", man, err)
	}

	// Run the bundle through the Node host over the broker; the imported helper
	// must have executed.
	tr := NewTransport(
		core.Manifest{ID: "greeter"},
		DropRef{ID: "greeter", Argv: node, Source: bundle},
		ProcessRunner{},
		testHost(&stubDoer{}, &memFS{m: map[string][]byte{}}),
	)
	res, err := tr.Execute(context.Background(), core.Job{ID: "j", Params: map[string]any{"who": "ada"}}, make(chan core.Progress, 4))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("status=%v err=%+v", res.Status, res.Error)
	}
	if got := res.Output["out"].Inline; got != "hello ada" {
		t.Errorf("out = %#v, want \"hello ada\" (imported helper didn't run)", got)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
