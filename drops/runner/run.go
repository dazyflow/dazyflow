// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package runner hosts the step that runs a script on one of the
// organisation's own machines.
//
// The machine runs a small agent that asks the daemon for work, so nothing has
// to reach it: it can sit behind NAT, on a laptop, inside a network the daemon
// has never heard of. Installing one is a single command with a token.
//
// This step is deliberately the ONLY one in this package. A runner is not a way
// to add typed steps to the catalog — it is a way to run a command somewhere
// else. Everything about what the command does lives in the script, on the
// org's machine, under the org's control.
package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/drops/internal/params"
	"git.sr.ht/~klahr/dazyflow/engine"
)

// Dispatcher sends a script to a runner and waits for the result. The daemon
// installs one at boot; without it this step reports that runners are not
// configured rather than failing obscurely.
type Dispatcher interface {
	Dispatch(ctx context.Context, req Request, onProgress func(string)) (Result, error)
}

// Request is one script to run.
type Request struct {
	Tenant  string
	Runner  string
	Label   string
	Script  string
	Env     map[string]string
	Stdin   string
	Timeout time.Duration
}

// Result is what came back.
type Result struct {
	ExitCode int
	Stdout   string
	Stderr   string
	Error    string
}

var (
	mu         sync.RWMutex
	dispatcher Dispatcher
)

// SetDispatcher installs the daemon's dispatcher. Follows the same injection
// shape as the other daemon-backed drops (io.SetQuotaReserver, and so on) so
// this package does not import the daemon.
func SetDispatcher(d Dispatcher) {
	mu.Lock()
	defer mu.Unlock()
	dispatcher = d
}

func current() Dispatcher {
	mu.RLock()
	defer mu.RUnlock()
	return dispatcher
}

// DefaultTimeout bounds a script that never returns. Ten minutes is long
// enough for real work and short enough that a hung script does not hold a run
// open for the rest of the day.
const DefaultTimeout = 10 * time.Minute

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:       "run_on_runner",
			Version:  "1.0",
			Label:    "Run on your machine",
			Subtitle: "A script, on a runner you host",
			Color:    "#9f83fe",
			Icon:     "terminal",
			Category: "system",
			Provider: "internal",
			Tags: []string{
				"runner", "script", "shell", "self-hosted", "remote", "command", "own machine",
			},
			Description: "Runs a script on one of your organisation's own machines — a server, a laptop, " +
				"anything running the Dazyflow runner agent. Use it when the work needs a library, a tool, " +
				"or a network the built-in steps cannot reach. Point it at a runner by name, or at a label " +
				"shared by several machines so any free one can take the job. The value wired into 'in' " +
				"arrives on the script's standard input; whatever the script prints comes back on 'out'. " +
				"A non-zero exit fails the step, with the script's error output attached.",
			Summary: "Run a script on a machine you host, and use what it prints.",
			Examples: []core.ParamsExample{
				{
					Title:  "Run a script on one machine",
					Params: json.RawMessage(`{"runner":"invoices-box","script":"./fetch-invoices.sh"}`),
					Notes:  "The simplest form: a named runner and a command it is allowed to run.",
				},
				{
					Title:  "Any machine in a pool",
					Params: json.RawMessage(`{"label":"linux","script":"./report.sh --month 03"}`),
					Notes:  "Targets a label instead of a name, so whichever labelled runner is free takes it.",
				},
				{
					Title:  "Pipe a value in, read the output back",
					Params: json.RawMessage(`{"runner":"tools","script":"jq -r .total","timeout_seconds":30}`),
					Notes:  "Whatever is wired into 'in' arrives on standard input.",
				},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				{
					Port:  "in",
					Label: "Input",
					MIME:  []string{"text/plain", "application/json"},
					// A runner is on another machine, so a file path from the
					// daemon's disk means nothing there. Values only.
					InlineOnly: true,
				},
			},
			Outputs: []core.Port{
				{Port: "out", Label: "Output", MIME: []string{"text/plain"}},
			},
			ParamsSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "runner": {
      "type": "string",
      "title": "Runner",
      "description": "The machine to run this on, by name. Leave empty and set a label instead to use any machine in a pool."
    },
    "label": {
      "type": "string",
      "title": "Or any runner labelled",
      "description": "Send the work to whichever machine carrying this label is free."
    },
    "script": {
      "type": "string",
      "title": "Command",
      "format": "textarea",
      "description": "The command to run on that machine. It runs as the user the runner agent runs as, in the agent's working directory."
    },
    "timeout_seconds": {
      "type": "integer",
      "title": "Give up after (seconds)",
      "x_advanced": true,
      "description": "Defaults to 600. The runner kills the script when this elapses."
    }
  },
  "required": ["script"]
}`),
			// Not idempotent, and this is the one manifest flag worth being
			// careful about: the daemon has no idea what the script does, so it
			// must never assume running it twice is harmless.
			Idempotent: false,
		},
		Execute: execute,
	})
}

func execute(ctx context.Context, job core.Job, progress chan<- core.Progress) (core.Result, error) {
	d := current()
	if d == nil {
		return failed(job, "not_configured",
			"runners are not set up on this Dazyflow deployment"), nil
	}
	tenant, _ := core.TenantFromContext(ctx)
	if tenant == "" {
		// Without a tenant there is no way to know whose runners these are, and
		// guessing would mean running a script on someone else's machine.
		return failed(job, "no_tenant", "this step has no organisation to run against"), nil
	}

	// Normalized the same way registration normalizes them: lower-cased and
	// trimmed. A runner installed with `--labels Linux,Build` is stored (and
	// listed in the admin page) as linux and build, so a step targeting "Linux"
	// — or "build " with a trailing space from a paste — matched nothing and
	// failed with 'no runner is labelled "Linux"' while the page plainly showed
	// one. Same for the name, which validRunnerName only ever allows in
	// lower-case.
	runnerName := normalizeTarget(params.StringDefault(job.Params, "runner", ""))
	label := normalizeTarget(params.StringDefault(job.Params, "label", ""))
	script := strings.TrimSpace(params.StringDefault(job.Params, "script", ""))
	if script == "" {
		return failed(job, "no_script", "this step has no command to run"), nil
	}
	if runnerName == "" && label == "" {
		return failed(job, "no_target",
			"choose a runner, or a label shared by the machines that may take this"), nil
	}
	if runnerName != "" && label != "" {
		// Both would be ambiguous, and silently preferring one would make the
		// other look honoured.
		return failed(job, "two_targets",
			"set either a runner or a label, not both"), nil
	}

	timeout := DefaultTimeout
	if s := params.IntDefault(job.Params, "timeout_seconds", 0); s > 0 {
		timeout = time.Duration(s) * time.Second
	}

	res, err := d.Dispatch(ctx, Request{
		Tenant: tenant,
		Runner: runnerName,
		Label:  label,
		Script: script,
		// The node's own environment, with ${secret.…} already resolved by the
		// engine. Plumbed all the way to the agent — which merges it over its
		// own environment — but never populated here until now, so a script
		// that read $MONTH got nothing and the whole path was decoration.
		Env:     job.Env,
		Stdin:   stdinFrom(job),
		Timeout: timeout,
	}, func(msg string) {
		emit(job, progress, msg)
	})
	if err != nil {
		return failed(job, "dispatch_failed", err.Error()), nil
	}
	if res.Error != "" {
		return failed(job, "runner_error", res.Error), nil
	}
	if res.ExitCode != 0 {
		// The script's own stderr is the useful part — it is the author's
		// message about what went wrong, and burying it would leave them with
		// only a number.
		msg := fmt.Sprintf("the command exited with status %d", res.ExitCode)
		if trimmed := strings.TrimSpace(res.Stderr); trimmed != "" {
			msg += ": " + trimmed
		}
		return failed(job, "nonzero_exit", msg), nil
	}
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"out": {MIME: "text/plain", Inline: res.Stdout},
		},
	}, nil
}

// normalizeTarget matches daemon.normalizeLabels' rule for one value. Kept
// here rather than imported because a drop must not depend on the daemon; the
// rule is two operations and the tests on both sides pin it.
func normalizeTarget(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// stdinFrom renders the wired input as the text the script reads.
//
// A file reference cannot reach here: the port is InlineOnly and the engine
// refuses such a job before the step runs (engine.refuseInlineOnlyFileRefs).
// The nil case below is therefore "nothing wired in", not "a file was wired
// in" — which is what it used to be, and why a file-producing step upstream
// made the script run with empty stdin and report SUCCESS.
func stdinFrom(job core.Job) string {
	ref, ok := job.Input["in"]
	if !ok {
		return ""
	}
	switch v := ref.Inline.(type) {
	case nil:
		return ""
	case string:
		return v
	default:
		// Anything structured goes across as JSON, which is what a script can
		// actually parse — jq, python, a Go program all read it the same way.
		b, err := json.Marshal(v)
		if err != nil {
			return fmt.Sprint(v)
		}
		return string(b)
	}
}

func emit(job core.Job, progress chan<- core.Progress, msg string) {
	if progress == nil {
		return
	}
	select {
	case progress <- core.Progress{JobID: job.ID, NodeID: job.NodeID, Message: msg}:
	default:
		// A full progress channel must not stall the step; the message is
		// advisory and the result is what matters.
	}
}

func failed(job core.Job, code, msg string) core.Result {
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusError,
		Error:  &core.JobError{Code: code, Message: msg},
	}
}
