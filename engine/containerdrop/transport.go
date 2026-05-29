package containerdrop

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"git.sr.ht/~klahr/hazy-flow/core"
	"git.sr.ht/~klahr/hazy-flow/engine/jsdrop"
)

// DefaultMaxRunDuration is the hard wall-clock ceiling a drop execution gets
// when neither the caller's context nor the Transport sets a tighter one. It is
// the backstop the in-process executor used to provide (goja's watchdog): a
// runaway loop or a never-settling await can't pin a worker slot forever. It's
// generous enough to clear a max-length fetch (maxFetchTimeout) plus overhead;
// operators tune it via HAZYFLOW_SCRIPTED_DROP_TIMEOUT, and a per-node
// TimeoutSeconds (whichever is sooner) still applies.
const DefaultMaxRunDuration = 5 * time.Minute

// DropRef identifies the drop a Runner launches. Different runners use different
// fields — a gVisor/Docker runner uses Image (an OCI image by digest); a
// subprocess runner uses Argv. ID is for logs.
type DropRef struct {
	ID string
	// Argv is the launcher command for the ProcessRunner (e.g. ["node",
	// drophost.mjs]). The DockerRunner supplies its own entrypoint and ignores it.
	Argv []string
	// Source is the drop's bundled JS. The Transport materializes it into the
	// per-execution dir as SourceFileName (next to the broker socket, so a
	// container runner bind-mounts one dir); each Runner points the drop host at
	// it in its own path namespace. Drops are always source — there is no image form.
	Source []byte
	// RestrictEgress, when true, makes the broker enforce Egress as the drop's
	// allowed outbound hosts (on top of the global SSRF guard). An empty Egress
	// under RestrictEgress denies all fetch — the least-privilege default for an
	// untrusted drop that declared no destinations. When false the broker
	// applies no per-drop egress restriction.
	RestrictEgress bool
	// Egress is the allowed outbound host allowlist (hostnames; "*.example.com"
	// matches subdomains). Only consulted when RestrictEgress is true.
	Egress []string
}

// Runner launches the drop process, handing it the unix socket the broker is
// listening on, and blocks until the drop exits. This is the ONLY runtime-
// specific seam: gVisor, Docker, and a plain subprocess are all just Runners.
type Runner interface {
	Run(ctx context.Context, socketPath string, drop DropRef) error
}

// RunnerFunc adapts a function to a Runner (used by tests as an in-process drop,
// and by trivial runners).
type RunnerFunc func(ctx context.Context, socketPath string, drop DropRef) error

func (f RunnerFunc) Run(ctx context.Context, socketPath string, drop DropRef) error {
	return f(ctx, socketPath, drop)
}

// Host bundles the capability implementations the broker mediates — the daemon
// wires one set, reused across every isolation tier.
type Host struct {
	HTTP  jsdrop.HTTPDoer                     // guarded HTTP; nil → http.DefaultClient
	Token jsdrop.TokenResolver                // OAuth resolver; nil → no auth capability
	Files func(job core.Job) jsdrop.FileStore // per-job sandboxed FS; nil → no files
}

// Transport adapts a containerized drop to core.Transport: it stands up a
// per-execution broker on a private unix socket, launches the drop via the
// Runner, and turns the drop's reported result into a core.Result. Identical
// Manifest()/Execute() contract as a native drop, so the engine resolves and
// runs a scripted drop with no special-casing.
type Transport struct {
	manifest core.Manifest
	drop     DropRef
	runner   Runner
	host     Host
	// MaxRunDuration is the hard wall-clock ceiling for one execution; <= 0 uses
	// DefaultMaxRunDuration. A sooner caller deadline wins. The daemon sets it
	// from HAZYFLOW_SCRIPTED_DROP_TIMEOUT.
	MaxRunDuration time.Duration
}

func NewTransport(manifest core.Manifest, drop DropRef, runner Runner, host Host) *Transport {
	return &Transport{manifest: manifest, drop: drop, runner: runner, host: host}
}

func (t *Transport) Manifest() core.Manifest { return t.manifest }

func (t *Transport) Execute(ctx context.Context, job core.Job, progress chan<- core.Progress) (core.Result, error) {
	// Hard run budget — the backstop a hung or never-settling drop can't escape.
	// A sooner caller deadline (e.g. a per-node TimeoutSeconds) wins; cancelling
	// it on return kills a still-running child (exec.CommandContext) and unblocks
	// the broker's in-flight fetch/token calls (they hang off this ctx).
	budget := t.MaxRunDuration
	if budget <= 0 {
		budget = DefaultMaxRunDuration
	}
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()

	dir, err := os.MkdirTemp("", "hz-broker-")
	if err != nil {
		return core.Result{}, err // infra failure
	}
	defer os.RemoveAll(dir)
	sockPath := filepath.Join(dir, "broker.sock")
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		return core.Result{}, err
	}
	defer ln.Close()

	var files jsdrop.FileStore
	if t.host.Files != nil {
		files = t.host.Files(job)
	}
	deps := BrokerDeps{
		Secrets:        secretsFromEnv(job.Env),
		HTTP:           t.host.HTTP,
		Token:          t.host.Token,
		Files:          files,
		RestrictEgress: t.drop.RestrictEgress,
		Egress:         t.drop.Egress,
		OnLog: func(_, msg string) {
			if msg == "" {
				return
			}
			select {
			case progress <- core.Progress{JobID: job.ID, NodeID: job.NodeID, Message: msg}:
			default:
			}
		},
	}
	broker := newBroker(ctx, deps, jobContextOf(job))
	stop := broker.serveOn(ln)
	defer stop()

	// Materialize the drop's ESM source as SourceFileName next to the socket, so every
	// runner finds it in the same shared dir. The runner injects the actual
	// --source path in ITS namespace (the host path for ProcessRunner, the
	// in-container mount path for a container runner) — the Transport stays
	// runtime-agnostic and never bakes a host path into the launch command.
	if len(t.drop.Source) > 0 {
		if err := os.WriteFile(filepath.Join(dir, SourceFileName), t.drop.Source, 0o600); err != nil {
			return core.Result{}, err // infra failure
		}
	}

	runErr := t.runner.Run(ctx, sockPath, t.drop)

	out, derr, ok := broker.result()
	if !ok {
		// The drop never POSTed /result. Distinguish the cause so dashboards can
		// react: our run budget firing (or a caller deadline) is a timeout, not a
		// crash; otherwise surface the runner's error (with any captured stderr).
		if ctx.Err() == context.DeadlineExceeded {
			return errorResult(job, "timeout", fmt.Sprintf("drop exceeded its %s run budget", budget)), nil
		}
		if runErr != nil {
			return errorResult(job, "runner_error", runErr.Error()), nil
		}
		return errorResult(job, "no_result", "drop exited without returning a result"), nil
	}
	if derr != nil {
		code := derr.Code
		if code == "" {
			code = "drop_error"
		}
		return errorResult(job, code, derr.Message), nil
	}
	return core.Result{JobID: job.ID, Status: core.StatusOK, Output: toRefs(out)}, nil
}

func errorResult(job core.Job, code, msg string) core.Result {
	return core.Result{JobID: job.ID, Status: core.StatusError, Error: &core.JobError{Code: code, Message: msg}}
}

// jobContextOf builds the context handed to the drop via GET /job.
func jobContextOf(job core.Job) JobContext {
	inputs := make(map[string]InputRefJSON, len(job.Input))
	for k, r := range job.Input {
		inputs[k] = InputRefJSON{MIME: r.MIME, Value: r.Inline, Path: r.Ref}
	}
	return JobContext{Params: job.Params, Inputs: inputs, Env: nonSecretEnv(job.Env), Secrets: secretsFromEnv(job.Env)}
}

// secretsFromEnv lifts "secret:NAME" entries into a plain name→value map.
func secretsFromEnv(env map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range env {
		if name, ok := strings.CutPrefix(k, "secret:"); ok {
			out[name] = v
		}
	}
	return out
}

// nonSecretEnv is every Job.Env entry except the secret: ones (those flow
// through the secret capability, never the general environment).
func nonSecretEnv(env map[string]string) map[string]string {
	if len(env) == 0 {
		return nil
	}
	out := map[string]string{}
	for k, v := range env {
		if strings.HasPrefix(k, "secret:") {
			continue
		}
		out[k] = v
	}
	return out
}

// toRefs maps the drop's output values to core.Refs, honoring an explicit
// { mime, value } / { mime, path } shape (mirrors jsdrop.toRefs).
func toRefs(out map[string]any) map[string]core.Ref {
	refs := make(map[string]core.Ref, len(out))
	for port, v := range out {
		if r, ok := asRef(v); ok {
			refs[port] = r
			continue
		}
		refs[port] = core.Ref{MIME: inferMIME(v), Inline: v}
	}
	return refs
}

func asRef(v any) (core.Ref, bool) {
	m, ok := v.(map[string]any)
	if !ok {
		return core.Ref{}, false
	}
	mime, ok := m["mime"].(string)
	if !ok || len(m) != 2 {
		return core.Ref{}, false
	}
	if val, ok := m["value"]; ok {
		return core.Ref{MIME: mime, Inline: val}, true
	}
	if path, ok := m["path"].(string); ok {
		return core.Ref{MIME: mime, Ref: path}, true
	}
	return core.Ref{}, false
}

func inferMIME(v any) string {
	switch v.(type) {
	case string:
		return "text/plain"
	case []byte:
		return "application/octet-stream"
	default:
		return "application/json"
	}
}

var _ core.Transport = (*Transport)(nil)

// tailBuffer is an io.Writer that retains only the last max bytes written — a
// bounded capture of a drop process's stderr, so a crash-before-result can be
// explained without risking unbounded memory from a chatty or hostile drop.
type tailBuffer struct {
	max int
	buf []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf = t.buf[len(t.buf)-t.max:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string { return strings.TrimSpace(string(t.buf)) }

// stderrTailBytes bounds the captured stderr tail surfaced in a runner error.
const stderrTailBytes = 4 << 10
