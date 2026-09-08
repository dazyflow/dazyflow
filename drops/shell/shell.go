// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package shell runs a command in a workspace-relative directory and streams its
// output: the build/test step in CI-shaped pipelines, and the escape hatch when no
// first-class integration covers what a user wants.
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
				// Only the friendly scalars are declared as ports. The full structured result
				// is still EMITTED under "meta", so run records keep it for debugging, but an
				// undeclared output cannot be wired and does not clutter the card. Branch on
				// exit_code for fail/notify paths.
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

// shellEnabled is FAIL-CLOSED: only an explicit affirmative turns it on, so
// every other value — including anything unrecognized like "disabled" or a typo —
// leaves this host-RCE primitive OFF. The earlier "anything non-negative enables"
// logic failed OPEN, silently arming remote code execution for an operator who
// wrote DAZYFLOW_ENABLE_SHELL=disabled.
func shellEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("DAZYFLOW_ENABLE_SHELL"))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// scrubbedEnv always removes every DAZYFLOW_* variable, so a command cannot read
// the daemon's own secrets out of its environment. CI ergonomics survive: PATH,
// HOME, toolchain vars all pass through — only the app's own namespace goes.
//
// DAZYFLOW_SHELL_ENV_ALLOW opts into least privilege instead: the command sees
// ONLY the named variables plus a minimal safe base. That is for boxes whose
// daemon environment also holds THIRD-PARTY secrets (AWS_*,
// GOOGLE_APPLICATION_CREDENTIALS) that the prefix scrub would not catch.
func scrubbedEnv() []string {
	allow := parseShellEnvAllow(os.Getenv("DAZYFLOW_SHELL_ENV_ALLOW"))
	src := os.Environ()
	out := make([]string, 0, len(src))
	for _, kv := range src {
		k, _, ok := strings.Cut(kv, "=")
		if ok && strings.HasPrefix(k, "DAZYFLOW_") {
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
	// Resolve the working directory THROUGH an os.Root handle rather than by
	// string-cleaning: cleaning never touches the filesystem, so a symlink planted
	// inside the workspace and pointing out of it was accepted and then followed by
	// cmd.Dir, running the command outside the sandbox.
	workdir, cleanRel, err := sandbox.ResolveDir(job.WorkspaceRoot, relPath)
	if err != nil {
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
	cmd.Env = scrubbedEnv()
	// On timeout, tear down the WHOLE process group: pty.Start makes the command a
	// session leader, so killing the group reaps grandchildren it backgrounded that a
	// bare Process.Kill would orphan to keep running on the host after the node
	// "finished". The pgid==pid guard is a hard interlock — signal a group ONLY when
	// the child genuinely leads its own — so a negative-PID kill can never escape to
	// the daemon's own group. WaitDelay backstops a child that ignores the signal.
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

	ptmx, err := pty.Start(cmd)
	if err != nil {
		return params.Err(job, "start", err.Error()), nil
	}
	defer func() { _ = ptmx.Close() }()

	doneRead := make(chan struct{})
	go func() {
		// Deferred so a panic in the pump cannot leave Execute blocked forever on
		// <-doneRead, and the recover keeps a pump panic from killing the daemon — the
		// engine's recover only covers the calling goroutine.
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

// maxLogLineBytes: a longer line is split into consecutive chunks rather than
// dropped, so memory stays bounded without losing output.
const maxLogLineBytes = 64 * 1024

// pumpStream uses bufio.Reader.ReadSlice rather than bufio.Scanner: a Scanner
// stops permanently on a token over its max size, silently dropping the rest of
// the output while the exit code reports success. ReadSlice reports that as a
// non-terminal ErrBufferFull, so an over-long line is emitted in chunks and
// reading continues. Lines arrive CRLF-terminated off the pty.
func pumpStream(src io.Reader, dst *boundedBuffer, progress chan<- core.Progress, job core.Job, stream string) {
	r := bufio.NewReaderSize(src, maxLogLineBytes)
	for {
		chunk, err := r.ReadSlice('\n')
		if errors.Is(err, bufio.ErrBufferFull) {
			if len(chunk) > 0 {
				dst.Write(chunk)
				emitLogProgress(progress, job, stream, string(chunk))
			}
			continue
		}
		if line := trimEOL(chunk); len(line) > 0 || err == nil {
			dst.Write(line)
			dst.Write([]byte{'\n'})
			emitLogProgress(progress, job, stream, string(line))
		}
		if err != nil {
			return
		}
	}
}

func trimEOL(b []byte) []byte {
	b = bytes.TrimSuffix(b, []byte{'\n'})
	return bytes.TrimSuffix(b, []byte{'\r'})
}

const maxTimeoutMs = int(math.MaxInt64 / int64(time.Millisecond))

// resolveTimeoutMs is the real enforcement: the ParamsSchema's "minimum" is
// advisory, since nothing validates a job's params against it before Execute.
//
// Non-positive falls back to the default — context.WithTimeout with a zero
// duration is already expired, killing the command the instant it starts, and
// unlike the daemon's secondsToDuration "no timeout" is not an option here, a
// shell step without a deadline pinning a worker indefinitely.
//
// Over-large clamps: time.Duration is int64 NANOSECONDS, so a big millisecond
// count wraps NEGATIVE, turning a request for an enormous timeout into an
// immediate kill. Reachable by typing a long run of digits.
//
// No ceiling beyond the overflow bound: the per-run policy limit is the graph
// timeout, and duplicating a lower cap here would break legitimately long
// builds.
func resolveTimeoutMs(n int) int {
	if n <= 0 {
		return defaultTimeoutMs
	}
	if n > maxTimeoutMs {
		return maxTimeoutMs
	}
	return n
}

// resolveMaxOutputBytes has to be enforced HERE, not by the ParamsSchema's
// "minimum": the schema drives the UI form, the docs and flowgen, but nothing
// validates a job's params against it before Execute — there is no JSON-schema
// validator in the daemon at all.
//
// It matters because a non-positive value reaches boundedBuffer as "no limit" and
// hands a runaway command an unbounded in-memory buffer, exactly the OOM the cap
// prevents. Someone writing 0 far more likely means "leave it alone".
func resolveMaxOutputBytes(n int) int {
	if n <= 0 {
		return defaultMaxOutputBytes
	}
	return n
}

// boundedBuffer discards the remainder so a runaway command cannot OOM the
// daemon. A non-positive limit disables the cap, a convenience for tests;
// executeShell never passes one.
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
