package containerdrop

import (
	"bytes"
	"context"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"git.sr.ht/~klahr/hazy-flow/core"
)

// probeDrop exercises several broker-backed capabilities (secret, fetch, files,
// params) from inside a real drop — the fixture for the gVisor round-trip.
const probeDrop = `export default {
  manifest: {
    id: "probe", version: "1.0.0", label: "probe",
    summary: "exercises broker capabilities.",
    outputs: [{ port: "out" }],
    examples: [{ title: "x", params: {} }],
  },
  async run(ctx) {
    const key = ctx.secrets.get("api_key");
    const res = await ctx.fetch("https://example.test/data");
    const body = await res.text();
    await ctx.files.write("scratch://note.txt", "hello");
    const wrote = await ctx.files.exists("scratch://note.txt");
    return { out: { secret: key, fetched: body, status: res.status, wrote: wrote, param: ctx.params.p } };
  },
};`

const nodeImage = "node:22-alpine"

func requireNodeImage(t *testing.T) {
	t.Helper()
	if err := exec.Command("docker", "image", "inspect", nodeImage).Run(); err != nil {
		t.Skipf("%s not available locally", nodeImage)
	}
}

// TestNodeHost_GVisor is the Phase B gate: a real drop runs the Node drop host
// (drophost.mjs) inside a gVisor sandbox — stock node image, our drophost.mjs
// bind-mounted, the drop reaching the broker only over the socket. The Node
// analog of TestDockerRunner_GVisor; proves the broker contract is runtime- AND
// substrate-agnostic (goja-process, goja-gVisor, node-gVisor all hold).
func TestNodeHost_GVisor(t *testing.T) {
	requireGVisor(t)
	requireNodeImage(t)

	drophost, err := filepath.Abs("nodehost/drophost.mjs")
	if err != nil {
		t.Fatal(err)
	}

	var stderr bytes.Buffer
	doer := &stubDoer{body: "DATA"}
	fs := &memFS{m: map[string][]byte{}}
	tr := NewTransport(
		core.Manifest{ID: "probe"},
		DropRef{ID: "probe", Source: []byte(probeDrop)},
		DockerRunner{
			Image:   nodeImage,
			Command: []string{"node", "/node-drophost.mjs"},
			Mounts:  map[string]string{drophost: "/node-drophost.mjs"},
			Limits:  Limits{MemoryBytes: 256 << 20, CPUSeconds: 30, OpenFiles: 256},
			Stderr:  &stderr,
		},
		testHost(doer, fs),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	res, err := tr.Execute(ctx, core.Job{
		ID:     "j",
		Params: map[string]any{"p": "hi"},
		Env:    map[string]string{"secret:api_key": "sekret"},
	}, make(chan core.Progress, 4))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusOK {
		skipIfHostUDSDenied(t, stderr.String())
		t.Fatalf("status=%v error=%+v\nstderr:\n%s", res.Status, res.Error, stderr.String())
	}
	out, ok := res.Output["out"].Inline.(map[string]any)
	if !ok {
		t.Fatalf("output 'out' not an object: %#v", res.Output["out"])
	}
	if out["secret"] != "sekret" || out["fetched"] != "DATA" || out["wrote"] != true || out["param"] != "hi" {
		t.Errorf("capability round-trip through Node+gVisor wrong: %#v", out)
	}
}
