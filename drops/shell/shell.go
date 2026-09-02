// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

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
	"log"
	"math"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/creack/pty"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/internal/params"
	"github.com/dazyflow/dazyflow/drops/internal/sandbox"
	"github.com/dazyflow/dazyflow/engine"
)

func init() {
	// The shell drop runs arbitrary host commands as the daemon's user with
	// host filesystem + network access — it bypasses the scripted-drop
	// sandbox entirely. That's fine for a single-tenant CI box but is a full
	// RCE primitive on a multi-tenant deployment, where any user with
	// graph:run could read the host, the daemon env (master key!), and reach
	// internal services. So it is OFF unless the operator explicitly opts in
	// with DAZYFLOW_ENABLE_SHELL — and even then its env is scrubbed of
	// DAZYFLOW_* secrets (see executeShell).
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
			Description: "Run a shell command inside a workspace-relative directory (commonly fed by git_checkout). Captures stdout/stderr and the exit code. Always returns ok so later notification steps still fire on failure — branch on the 'Exit code' output (0 = success).",
			Summary:     "Run a command in a workspace directory and capture its output and exit code.",
			Examples: []core.ParamsExample{
				{
					Title:  "Run the test suite",
					Params: json.RawMessage(`{"command":"go","args":["test","./..."],"timeout_ms":600000}`),
					Notes:  "Connect git_checkout's path output into the 'path' input so the command runs against the freshly checked-out tree.",
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
				{Port: "path", Label: "Working directory"},
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
				{Port: "meta", Label: "Details", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(
				`{
					"type":"object",
					"properties":{
						"path":{"type":"string","description":"Workspace-relative working directory. Overridden by the path input port if connected."},
						"command":{"type":"string","description":"Executable to run. Resolved via PATH unless absolute."},
						"args":{"type":"array","items":{"type":"string"},"description":"Argument vector. Defaults to []."},
						"timeout_ms":{"type":"integer","default":600000,"minimum":1,"description":"Hard deadline for the command, in milliseconds. Default 10 min. A non-positive value falls back to the default — there is no 'run forever' setting."},
						"max_output_bytes":{"type":"integer","default":1048576,"minimum":1,"x_advanced":true,"description":"Truncate stdout/stderr beyond this. Default 1 MiB. There is no unlimited setting — output is buffered in memory, so raise the number rather than trying to disable the cap."}
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

// shellEnabled reports whether the operator opted into the shell drop.
// FAIL-CLOSED: only an explicit affirmative ("1"/"true"/"yes"/"on")
// turns it on; every other value — empty, "0"/"false"/"no"/"off", AND
// anything unrecognized like "disabled", "none", or a typo — leaves this
// host-RCE primitive OFF. This matches cmd/dzd's envBool convention. The
// earlier "anything non-negative enables" logic failed OPEN: an operator
// who wrote DAZYFLOW_ENABLE_SHELL=disabled (reasonably expecting it off)
// would have silently armed remote code execution. A security-critical
// toggle must never enable on a value the operator didn't clearly mean
// as "yes".
func shellEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("DAZYFLOW_ENABLE_SHELL"))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// scrubbedEnv builds the environment handed to the command.
//
// Floor (always): every DAZYFLOW_* variable is removed, so a command can't
// read the daemon's own secrets (master key, Postgres DSN, webhook signing
// secrets, trusted signing keys, …) out of its environment. CI ergonomics
// are preserved by default: PATH, HOME, GOPATH, language toolchain vars,
// etc. all pass through — only the app's own secret namespace is removed.
//
// Least-privilege (opt-in): when DAZYFLOW_SHELL_ENV_ALLOW is set (a
// comma-separated list of variable names), the command instead sees ONLY
// those variables plus a minimal safe base (PATH, HOME) — nothing else.
// This is for boxes whose daemon environment also holds THIRD-PARTY secrets
// (AWS_*, GOOGLE_APPLICATION_CREDENTIALS, generic API keys) that the
// prefix scrub above wouldn't catch: the operator names exactly what a
// command may see, and everything unlisted is withheld.
func scrubbedEnv() []string {
	allow := parseShellEnvAllow(os.Getenv("DAZYFLOW_SHELL_ENV_ALLOW"))
	src := os.Environ()
	out := make([]string, 0, len(src))
	for _, kv := range src {
		k, _, ok := strings.Cut(kv, "=")
		if ok && strings.HasPrefix(k, "DAZYFLOW_") {
			// The app's own secrets are withheld in every mode.
			continue
		}
		if allow != nil {
			if _, want := allow[k]; !want {
				continue
			}
		}
		out = append(out, kv)
	}
	return out
}

// parseShellEnvAllow turns the DAZYFLOW_SHELL_ENV_ALLOW list into a set, or
// returns nil when unset (signalling "no allowlist — pass the full scrubbed
// env"). PATH and HOME are always included so commands still resolve and
// run; the operator need only name the extras a command genuinely needs.
func parseShellEnvAllow(s string) map[string]struct{} {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	allow := map[string]struct{}{"PATH": {}, "HOME": {}}
	for _, name := range strings.Split(s, ",") {
		if name = strings.TrimSpace(name); name != "" {
			allow[name] = struct{}{}
		}
	}
	return allow
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
	// Resolve the working directory THROUGH an os.Root handle rather than
	// string-cleaning it. Cleaning alone never touches the filesystem, so a
	// symlink planted inside the workspace and pointing outside it was
	// accepted and then followed by cmd.Dir — the command would run outside
	// the sandbox. The io drops already resolve through a root; this brings
	// the shell drop up to the same standard.
	workdir, cleanRel, err := sandbox.ResolveDir(job.WorkspaceRoot, relPath)
	if err != nil {
		// Separate "you pointed outside the sandbox" from "that folder isn't
		// there" — same rejection, very different thing for a user to fix.
		if errors.Is(err, os.ErrNotExist) {
			return params.Err(job, "bad_param",
				fmt.Sprintf("working folder %q doesn't exist in the workspace", relPath)), nil
		}
		return params.Err(job, "sandbox_escape", err.Error()), nil
	}

	args := params.StringSlice(job.Params, "args")
	timeoutMs := resolveTimeoutMs(params.IntDefault(job.Params, "timeout_ms", defaultTimeoutMs))
	maxBytes := resolveMaxOutputBytes(params.IntDefault(job.Params, "max_output_bytes", defaultMaxOutputBytes))

	runCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutMs)*time.Millisecond)
	defer cancel()

	cmd := exec.CommandContext(runCtx, cmdName, args...)
	cmd.Dir = workdir
	// Never expose the daemon's DAZYFLOW_* secrets (master key, DSN, webhook
	// secrets) to the command — see scrubbedEnv.
	cmd.Env = scrubbedEnv()
	// On timeout/cancel, tear down the WHOLE process group, not just the
	// direct child. pty.Start (below) makes the command a session leader, so
	// its PID doubles as its process-group ID; killing the group reaps
	// grandchildren the command backgrounded (`thing &`, a fork bomb) that a
	// bare Process.Kill would orphan to keep running on the host after the
	// node "finished". The pgid==pid guard is a hard safety interlock: we
	// signal a group ONLY when the child genuinely leads its own group, so a
	// negative-PID kill can never escape to the daemon's own process group.
	// WaitDelay backstops a child that ignores the signal or holds the pty.
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		pid := cmd.Process.Pid
		if pgid, err := syscall.Getpgid(pid); err == nil && pgid == pid {
			if syscall.Kill(-pgid, syscall.SIGKILL) == nil {
				return nil
			}
		}
		return cmd.Process.Kill()
	}
	cmd.WaitDelay = 5 * time.Second
	combined := &boundedBuffer{limit: maxBytes}

	params.EmitProgress(progress, job, 0.1, "exec "+cmdName)
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
		// close(doneRead) is deferred so a panic in the pump can't leave the
		// Execute goroutine blocked forever on <-doneRead, and the recover
		// keeps a pump panic from killing the daemon (the engine's recover
		// only covers the calling goroutine).
		defer close(doneRead)
		defer func() {
			if r := recover(); r != nil {
				log.Printf("shell: recovered while pumping command output: %v", r)
			}
		}()
		pumpStream(ptmx, combined, progress, job, "stdout")
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
	params.EmitProgress(progress, job, 1.0, fmt.Sprintf("exit %d", exitCode))

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

// maxLogLineBytes bounds how much of a single output line is buffered
// before it is flushed as one progress event. A line longer than this is
// split into consecutive chunks rather than dropped, so memory stays
// bounded without losing output.
const maxLogLineBytes = 64 * 1024

// pumpStream forwards src to dst (the captured stdout) and to the progress
// channel, one line at a time.
//
// It uses bufio.Reader.ReadSlice rather than bufio.Scanner: a Scanner stops
// permanently on a token over its max size, silently dropping the rest of the
// output while the exit code reports success. ReadSlice reports that as a
// non-terminal ErrBufferFull, so an over-long line is emitted in
// maxLogLineBytes chunks and reading continues. The command runs on a pty, so
// complete lines arrive CRLF-terminated; those become "\n" in the captured
// output and are stripped from the progress message. A chunk flushed mid-line
// is written through verbatim.
func pumpStream(src io.Reader, dst *boundedBuffer, progress chan<- core.Progress, job core.Job, stream string) {
	r := bufio.NewReaderSize(src, maxLogLineBytes)
	for {
		chunk, err := r.ReadSlice('\n')
		if errors.Is(err, bufio.ErrBufferFull) {
			// Mid-line flush: the line is longer than the buffer. Emit what
			// we have and keep reading — this is the case the old Scanner
			// treated as fatal.
			if len(chunk) > 0 {
				dst.Write(chunk)
				emitLogProgress(progress, job, stream, string(chunk))
			}
			continue
		}
		// Either a complete line (err == nil) or the trailing unterminated
		// remainder at EOF. Both are emitted as one newline-terminated line,
		// which is what the Scanner produced for them.
		if line := trimEOL(chunk); len(line) > 0 || err == nil {
			dst.Write(line)
			dst.Write([]byte{'\n'})
			emitLogProgress(progress, job, stream, string(line))
		}
		if err != nil {
			// io.EOF, or the read error a closed pty surfaces once the
			// command exits. Everything read so far has been emitted.
			return
		}
	}
}

// trimEOL strips one trailing line terminator — "\r\n", "\n", or a bare
// "\r" — from a complete line. Mirrors bufio.ScanLines, which dropped the
// pty's CR along with the LF.
func trimEOL(b []byte) []byte {
	b = bytes.TrimSuffix(b, []byte{'\n'})
	return bytes.TrimSuffix(b, []byte{'\r'})
}

// maxTimeoutMs is the largest millisecond count that fits in an int64-ns
// time.Duration without overflow (~292 years). Mirrors the daemon's
// maxDurationSeconds, in the unit this drop's param uses.
const maxTimeoutMs = int(math.MaxInt64 / int64(time.Millisecond))

// resolveTimeoutMs turns the untrusted timeout_ms param into a usable
// deadline. Like resolveMaxOutputBytes, this is the real enforcement — the
// ParamsSchema's "minimum" is advisory, since nothing validates a job's
// params against it before Execute.
//
// Two hostile shapes to absorb:
//
//   - Non-positive. context.WithTimeout with a zero or negative duration is
//     already expired, so the command is killed the instant it starts (or
//     never starts at all, surfacing as a confusing "start" error rather
//     than a timeout). Fall back to the default, matching
//     resolveMaxOutputBytes: unlike the daemon's secondsToDuration, "no
//     timeout" is not an option here — a shell step without a deadline pins
//     a worker indefinitely.
//
//   - Over-large. time.Duration is int64 NANOSECONDS, so a big
//     millisecond count overflows and wraps NEGATIVE — turning a request for
//     an enormous timeout into an immediate kill, the exact inversion of
//     what was asked for. This is reachable by typing a long run of digits.
//     Clamp to the max representable instead.
//
// No practical ceiling is imposed beyond the overflow bound: the per-run
// policy limit is the graph timeout (effectiveGraphTimeout, which clamps to
// the tenant's MaxTimeoutSeconds), and duplicating a lower cap here would
// silently break legitimately long builds.
func resolveTimeoutMs(n int) int {
	if n <= 0 {
		return defaultTimeoutMs
	}
	if n > maxTimeoutMs {
		return maxTimeoutMs
	}
	return n
}

// resolveMaxOutputBytes turns the untrusted max_output_bytes param into a
// usable positive cap.
//
// The cap has to be enforced HERE, not by the ParamsSchema's "minimum": the
// schema drives the UI form, the docs and flowgen, but nothing validates a
// job's params against it before Execute — there is no JSON-schema validator
// in the daemon at all. So a schema constraint is documentation, and this is
// the check.
//
// It matters because a non-positive value reaches boundedBuffer as "no
// limit" and hands a runaway command an unbounded in-memory buffer — exactly
// the OOM the cap exists to prevent. Falling back to the default is the
// fail-safe reading: someone writing 0 far more likely means "leave it
// alone" than "buffer without bound", and unbounded is not a setting this
// drop offers. To capture more output, raise the number.
func resolveMaxOutputBytes(n int) int {
	if n <= 0 {
		return defaultMaxOutputBytes
	}
	return n
}

// boundedBuffer captures output up to limit bytes, silently discarding
// the remainder so a runaway command can't OOM the daemon. A zero or
// negative limit disables the cap — a primitive convenience for tests that
// want everything; executeShell never passes one, because a caller-supplied
// limit goes through resolveMaxOutputBytes first.
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
