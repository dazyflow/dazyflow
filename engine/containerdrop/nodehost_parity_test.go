package containerdrop

import (
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"testing"

	"git.sr.ht/~klahr/hazyflow/core"
)

// The Node drop host's capability surface, exercised end-to-end through the
// broker. Originally a goja↔Node parity matrix; with goja retired as the
// executor it's the Node host's own regression test — each capability returns a
// known value under the `out` port. Covers params, secrets, env, crypto, fetch,
// files, auth, inputs, and DropError.

func nodeHostArgv(t *testing.T) []string {
	t.Helper()
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
	abs, err := filepath.Abs("nodehost/drophost.mjs")
	if err != nil {
		t.Fatal(err)
	}
	return []string{"node", abs}
}

func runDrop(t *testing.T, argv []string, source string, host Host, job core.Job) core.Result {
	t.Helper()
	tr := NewTransport(
		core.Manifest{ID: "p"},
		DropRef{ID: "p", Argv: argv, Source: []byte(source)},
		ProcessRunner{},
		host,
	)
	res, err := tr.Execute(context.Background(), job, make(chan core.Progress, 16))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	return res
}

func TestNodeHost_Capabilities(t *testing.T) {
	node := nodeHostArgv(t)
	const man = `manifest:{id:"p",version:"1.0.0",label:"p",summary:"s.",outputs:[{port:"out"}],examples:[{title:"x",params:{}}]}`

	cases := []struct {
		name   string
		run    string
		job    core.Job
		host   func() Host
		expect string // JSON of the `out` port's value
	}{
		{
			name:   "params",
			run:    `run(ctx){ return { out: { sum: ctx.params.x + 1, who: ctx.params.who } }; }`,
			job:    core.Job{ID: "j", Params: map[string]any{"x": 1, "who": "ada"}},
			expect: `{"sum":2,"who":"ada"}`,
		},
		{
			name:   "secrets",
			run:    `run(ctx){ return { out: { has: ctx.secrets.has("k"), got: ctx.secrets.get("k") } }; }`,
			job:    core.Job{ID: "j", Env: map[string]string{"secret:k": "s3cret"}},
			expect: `{"got":"s3cret","has":true}`,
		},
		{
			name:   "env",
			run:    `run(ctx){ return { out: { e: ctx.env.FOO } }; }`,
			job:    core.Job{ID: "j", Env: map[string]string{"FOO": "bar", "secret:k": "x"}},
			expect: `{"e":"bar"}`,
		},
		{
			name:   "crypto",
			run:    `run(ctx){ return { out: { b64: ctx.crypto.base64(ctx.crypto.utf8("hi")), b64url: ctx.crypto.base64(ctx.crypto.utf8("hello"), {url:true, pad:false}), hex: ctx.crypto.hex(ctx.crypto.hash("sha256","abc")), back: ctx.crypto.utf8Decode(ctx.crypto.base64Decode("aGk=")) } }; }`,
			job:    core.Job{ID: "j"},
			expect: `{"b64":"aGk=","b64url":"aGVsbG8","back":"hi","hex":"ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"}`,
		},
		{
			name:   "fetch",
			run:    `async run(ctx){ const r = await ctx.fetch("https://api.example.com/x", {method:"POST", body:{a:1}}); return { out: { status: r.status, ok: r.ok, text: await r.text() } }; }`,
			job:    core.Job{ID: "j"},
			host:   func() Host { return testHost(&stubDoer{status: 201, body: "DATA"}, &memFS{m: map[string][]byte{}}) },
			expect: `{"ok":true,"status":201,"text":"DATA"}`,
		},
		{
			name:   "files",
			run:    `async run(ctx){ await ctx.files.write("scratch://f.txt", "hello"); return { out: { ex: await ctx.files.exists("scratch://f.txt"), txt: await ctx.files.readText("scratch://f.txt"), missing: await ctx.files.exists("scratch://nope") } }; }`,
			job:    core.Job{ID: "j"},
			expect: `{"ex":true,"missing":false,"txt":"hello"}`,
		},
		{
			name:   "auth",
			run:    `async run(ctx){ return { out: { tok: await ctx.auth.token("google", "default") } }; }`,
			job:    core.Job{ID: "j"},
			expect: `{"tok":"tok-google-default"}`,
		},
		{
			name:   "inputs",
			run:    `run(ctx){ return { out: { got: ctx.inputs.get("in"), has: ctx.inputs.has("in"), missing: ctx.inputs.has("nope"), refp: ctx.inputs.ref("in").mime } }; }`,
			job:    core.Job{ID: "j", Input: map[string]core.Ref{"in": {MIME: "text/plain", Inline: "wired"}}},
			expect: `{"got":"wired","has":true,"missing":false,"refp":"text/plain"}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := testHost(&stubDoer{}, &memFS{m: map[string][]byte{}})
			if tc.host != nil {
				h = tc.host()
			}
			src := "export default { " + man + ", " + tc.run + " };"
			res := runDrop(t, node, src, h, tc.job)
			if res.Status != core.StatusOK {
				t.Fatalf("status=%v err=%+v", res.Status, res.Error)
			}
			if got := mustJSON(t, res.Output["out"].Inline); got != tc.expect {
				t.Errorf("out =\n  %s\nwant\n  %s", got, tc.expect)
			}
		})
	}

	// DropError → a typed node-error with the drop's code (async, as real drops are).
	t.Run("drop_error", func(t *testing.T) {
		src := "export default { " + man + ", async run(ctx){ throw new DropError(\"bad_param\", \"nope\"); } };"
		res := runDrop(t, node, src, testHost(&stubDoer{}, &memFS{m: map[string][]byte{}}), core.Job{ID: "j"})
		if res.Status != core.StatusError || res.Error == nil || res.Error.Code != "bad_param" {
			t.Errorf("want error code bad_param, got status=%v err=%+v", res.Status, res.Error)
		}
	})
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}
