// Package shell hosts the `shell` drop — runs a command in a
// workspace-relative directory and streams its output. Used as the
// build/test step in CI-shaped pipelines and as a generic escape hatch
// when no first-class integration covers the operation a user wants.
package shell

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/creack/pty"

	"git.sr.ht/~klahr/hazyflow/core"
	"git.sr.ht/~klahr/hazyflow/engine"
	"git.sr.ht/~klahr/hazyflow/drops/internal/params"
)

func init() {
	// The shell drop runs arbitrary host commands as the daemon's user with
	// host filesystem + network access — it bypasses the scripted-drop
	// sandbox entirely. That's fine for a single-tenant CI box but is a full
	// RCE primitive on a multi-tenant deployment, where any user with
	// graph:run could read the host, the daemon env (master key!), and reach
	// internal services. So it is OFF unless the operator explicitly opts in
	// with HAZYFLOW_ENABLE_SHELL — and even then its env is scrubbed of
	// HAZYFLOW_* secrets (see executeShell).
	if !shellEnabled() {
		return
	}
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "shell",
			Version:     "1.0",
			Label:       "Shell",
			Subtitle:    "Run command",
			Color:       "#7f5af0",
			Icon:        "terminal",
			Category:    "io",
			Provider:    "internal",
			Tags:        []string{"build", "exec", "shell", "command", "ci"},
			Description: "Run a shell command inside a workspace-relative directory (commonly fed by git_checkout). Captures stdout/stderr and the exit code. Always returns ok so downstream notification nodes still fire on failure — branch on the 'Exit code' output (0 = success).",
			Summary:     "Run a command in a workspace directory and capture its output and exit code.",
			Examples: []core.ParamsExample{
				{
					Title:  "Run the test suite",
					Params: json.RawMessage(`{"command":"go","args":["test","./..."],"timeout_ms":600000}`),
					Notes:  "Wire git_checkout's path output into the 'path' input so the command runs against the freshly checked-out tree.",
				},
				{
					Title:  "List files in a subdirectory",
					Params: json.RawMessage(`{"path":"src","command":"ls","args":["-la"]}`),
				},
				{
					Title:  "Quick build with tight cap on output",
					Params: json.RawMessage(`{"command":"make","args":["build"],"timeout_ms":120000,"max_output_bytes":262144}`),
					Notes:  "Cap output when you only care about the tail; the daemon truncates silently beyond max_output_bytes.",
				},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				{Port: "path", Label: "Working directory (overrides params.path)"},
			},
			Outputs: []core.Port{
				// Only the friendly scalars are declared as ports; the full
				// structured result (command, args, path, success, duration_ms,
				// error) is still EMITTED under "meta" (see executeShell) so run
				// records keep it for debugging, but it's not a pin — undeclared
				// outputs can't be wired and don't clutter the card. Branch on
				// exit_code ("0" = success) for fail/notify paths.
				{Port: "stdout", Label: "Standard output", MIME: []string{"text/plain"}},
				{Port: "stderr", Label: "Standard error", MIME: []string{"text/plain"}},
				{Port: "exit_code", Label: "Exit code", MIME: []string{"text/plain"}},
			},
			ParamsSchema: json.RawMessage(
				`{
					"type":"object",
					"properties":{
						"path":{"type":"string","description":"Workspace-relative working directory. Overridden by the path input port if connected."},
						"command":{"type":"string","description":"Executable to run. Resolved via PATH unless absolute."},
						"args":{"type":"array","items":{"type":"string"},"description":"Argument vector. Defaults to []."},
						"timeout_ms":{"type":"integer","default":600000,"minimum":1,"description":"Hard deadline for the command, in milliseconds. Default 10 min."},
						"max_output_bytes":{"type":"integer","default":1048576,"minimum":0,"x_advanced":true,"description":"Truncate stdout/stderr beyond this. Default 1 MiB."}
					},
					"required":["command"]
				}`,
			),
			Idempotent: false,
		},
		Execute: executeShell,
	})
}

const (
	defaultTimeoutMs      = 10 * 60 * 1000
	defaultMaxOutputBytes = 1024 * 1024
)

// shellEnabled reports whether the operator opted into the shell drop. Any
// non-empty value other than "0"/"false" enables it.
func shellEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("HAZYFLOW_ENABLE_SHELL"))) {
	case "", "0", "false", "no", "off":
		return false
	default:
		return true
	}
}

// scrubbedEnv is the host environment minus every HAZYFLOW_* variable, so a
// command can't read the daemon's secrets (master key, Postgres DSN, webhook
// signing secrets, trusted signing keys, …) out of its own environment. CI
// ergonomics are preserved: PATH, HOME, GOPATH, language toolchain vars, etc.
// all pass through — only the app's own secret namespace is removed.
func scrubbedEnv() []string {
	src := os.Environ()
	out := make([]string, 0, len(src))
	for _, kv := range src {
		k, _, ok := strings.Cut(kv, "=")
		if ok && strings.HasPrefix(k, "HAZYFLOW_") {
			continue
		}
		out = append(out, kv)
	}
	return out
}

func executeShell(ctx context.Context, job core.Job, progress chan<- core.Progress) (core.Result, error) {
	cmdName, err := params.String(job.Params, "command")
	if err != nil {
		return params.Err(job, "bad_param", err.Error()), nil
	}
	if job.WorkspaceRoot == "" {
		return params.Err(job, "no_sandbox", "shell requires a workspace sandbox"), nil
	}
	relPath := params.StringDefault(job.Params, "path", "")
	if input, ok := job.Input["path"]; ok {
		switch v := input.Inline.(type) {
		case string:
			if v != "" {
				relPath = v
			}
		}
		if relPath == "" && input.Ref != "" {
			relPath = input.Ref
		}
	}
	cleanRel, err := sandboxRel(relPath)
	if err != nil {
		return params.Err(job, "sandbox_escape", err.Error()), nil
	}
	workdir := filepath.Join(job.WorkspaceRoot, cleanRel)

	args := paramStringSlice(job.Params, "args")
	timeoutMs := params.IntDefault(job.Params, "timeout_ms", defaultTimeoutMs)
	maxBytes := params.IntDefault(job.Params, "max_output_bytes", defaultMaxOutputBytes)

	runCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutMs)*time.Millisecond)
	defer cancel()

	cmd := exec.CommandContext(runCtx, cmdName, args...)
	cmd.Dir = workdir
	// Never expose the daemon's HAZYFLOW_* secrets (master key, DSN, webhook
	// secrets) to the command — see scrubbedEnv.
	cmd.Env = scrubbedEnv()
	combined := &boundedBuffer{limit: maxBytes}

	emitProgress(progress, job, 0.1, "exec "+cmdName)
	started := time.Now()

	// Spawn the command attached to a PTY so build tools that switch to
	// block buffering when stdout is a pipe (make, gcc, cargo, …) flush
	// line-by-line as if a user were watching. Tradeoff: stdout and
	// stderr arrive merged, so we route everything to stdout and leave
	// stderr empty.
	ptmx, err := pty.Start(cmd)
	if err != nil {
		return params.Err(job, "start", err.Error()), nil
	}
	defer func() { _ = ptmx.Close() }()

	doneRead := make(chan struct{})
	go func() {
		pumpStream(ptmx, combined, progress, job, "stdout")
		close(doneRead)
	}()
	runErr := cmd.Wait()
	<-doneRead
	duration := time.Since(started)

	if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		return params.Err(job, "timeout",
			fmt.Sprintf("command exceeded %dms", timeoutMs)), nil
	}

	exitCode := -1
	success := false
	switch {
	case runErr == nil:
		exitCode = 0
		success = true
	default:
		var ee *exec.ExitError
		if errors.As(runErr, &ee) {
			exitCode = ee.ExitCode()
		}
	}

	meta := map[string]any{
		"command":     cmdName,
		"args":        args,
		"path":        cleanRel,
		"exit_code":   exitCode,
		"success":     success,
		"duration_ms": duration.Milliseconds(),
	}
	if runErr != nil && !success {
		meta["error"] = runErr.Error()
	}
	emitProgress(progress, job, 1.0, fmt.Sprintf("exit %d", exitCode))

	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"stdout":    {MIME: "text/plain", Inline: combined.String()},
			"stderr":    {MIME: "text/plain", Inline: ""},
			"exit_code": {MIME: "text/plain", Inline: strconv.Itoa(exitCode)},
			"meta":      {MIME: "application/json", Inline: meta},
		},
	}, nil
}

func pumpStream(src io.Reader, dst *boundedBuffer, progress chan<- core.Progress, job core.Job, stream string) {
	scanner := bufio.NewScanner(src)
	scanner.Buffer(make([]byte, 4096), 64*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		dst.Write(line)
		dst.Write([]byte{'\n'})
		emitLogProgress(progress, job, stream, string(line))
	}
}

// boundedBuffer captures output up to limit bytes, silently discarding
// the remainder so a runaway command can't OOM the daemon. A zero or
// negative limit disables the cap.
type boundedBuffer struct {
	bytes.Buffer
	limit int
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if b.limit <= 0 {
		return b.Buffer.Write(p)
	}
	remaining := b.limit - b.Buffer.Len()
	if remaining <= 0 {
		return len(p), nil
	}
	if len(p) > remaining {
		b.Buffer.Write(p[:remaining])
		return len(p), nil
	}
	return b.Buffer.Write(p)
}
