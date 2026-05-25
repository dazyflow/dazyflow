package git

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/creack/pty"

	"git.sr.ht/~klahr/hazy-flow/core"
	"git.sr.ht/~klahr/hazy-flow/engine"
)

func init() {
	engine.Register(engine.NativeNode{
		Manifest: core.Manifest{
			ID:             "git_build",
			Version:        "1.0",
			Label:          "Build",
			Color:          "#7f5af0",
			Icon:           "hammer",
			Category:       "io",
			Provider:       "internal",
			Tags:           []string{"build", "exec", "shell", "git"},
			Description:    "Run a build command inside a workspace-relative directory (typically a checked-out repo). Captures stdout/stderr and the exit code. Always returns ok so downstream notification nodes still fire on failure — branch on meta.success / meta.exit_code.",
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				{Port: "path", Label: "Working directory (overrides params.path)"},
			},
			Outputs: []core.Port{
				{Port: "stdout", Label: "Standard output"},
				{Port: "stderr", Label: "Standard error"},
				{Port: "meta", Label: "Build metadata (JSON)"},
			},
			ParamsSchema: json.RawMessage(
				`{
					"type":"object",
					"properties":{
						"path":{"type":"string","description":"Workspace-relative working directory. Overridden by the path input port if connected."},
						"command":{"type":"string","description":"Executable to run. Resolved via PATH unless absolute."},
						"args":{"type":"array","items":{"type":"string"},"description":"Argument vector. Defaults to []."},
						"timeout_ms":{"type":"integer","default":600000,"minimum":1,"description":"Hard deadline for the build, in milliseconds. Default 10 min."},
						"max_output_bytes":{"type":"integer","default":1048576,"minimum":0,"description":"Truncate stdout and stderr individually beyond this. Default 1 MiB."}
					},
					"required":["command"]
				}`,
			),
			Idempotent: false,
		},
		Execute: executeGitBuild,
	})
}

const (
	defaultBuildTimeoutMs = 10 * 60 * 1000
	defaultMaxOutputBytes = 1024 * 1024
)

func executeGitBuild(ctx context.Context, job core.Job, progress chan<- core.Progress) (core.Result, error) {
	cmdName, err := paramString(job.Params, "command")
	if err != nil {
		return errResult(job, "bad_param", err.Error()), nil
	}
	if job.WorkspaceRoot == "" {
		return errResult(job, "no_sandbox", "git_build requires a workspace sandbox"), nil
	}
	relPath := paramStringDefault(job.Params, "path", "")
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
		return errResult(job, "sandbox_escape", err.Error()), nil
	}
	workdir := filepath.Join(job.WorkspaceRoot, cleanRel)

	args := paramStringSlice(job.Params, "args")
	timeoutMs := paramIntDefault(job.Params, "timeout_ms", defaultBuildTimeoutMs)
	maxBytes := paramIntDefault(job.Params, "max_output_bytes", defaultMaxOutputBytes)

	runCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutMs)*time.Millisecond)
	defer cancel()

	cmd := exec.CommandContext(runCtx, cmdName, args...)
	cmd.Dir = workdir
	combined := &boundedBuffer{limit: maxBytes}

	emitProgress(progress, job, 0.1, "exec "+cmdName)
	started := time.Now()

	// Spawn the command attached to a PTY so build tools that switch
	// to block-buffering when stdout is a pipe (make, gcc, cargo, …)
	// instead flush line-by-line as if a user were watching. The
	// tradeoff is that stdout and stderr arrive merged — separate
	// capture is no longer available, so we route everything into the
	// stdout output port and leave stderr empty.
	ptmx, err := pty.Start(cmd)
	if err != nil {
		return errResult(job, "start", err.Error()), nil
	}
	defer func() { _ = ptmx.Close() }()

	// pumpStream returns when the PTY closes (EOF), which the kernel
	// signals once the child fully exits and its tty side is closed.
	// We run it synchronously after Wait — but Wait blocks on the
	// child, so the read happens on its own goroutine first.
	doneRead := make(chan struct{})
	go func() {
		pumpStream(ptmx, combined, progress, job, "stdout")
		close(doneRead)
	}()
	runErr := cmd.Wait()
	<-doneRead
	duration := time.Since(started)

	if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		return errResult(job, "timeout",
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
			"stdout": {MIME: "text/plain", Inline: combined.String()},
			"stderr": {MIME: "text/plain", Inline: ""},
			"meta":   {MIME: "application/json", Inline: meta},
		},
	}, nil
}

// pumpStream copies lines from src into both the bounded buffer (for
// the final outputs) and the progress channel (for live SSE consumers).
// scanner.Buffer caps individual lines at 64 KiB; longer lines are
// silently split.
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

// boundedBuffer captures output up to limit bytes, silently discarding the
// remainder so a runaway build can't OOM the daemon. A zero or negative
// limit disables the cap.
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
