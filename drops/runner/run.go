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
	Tenant string
	Runner string
	Label  string
	Script string
	// Shell names the interpreter the agent starts the script with — one of
	// Shells. Empty (and DefaultShell) mean the machine's own shell, which is
	// what a runner did before this was a choice.
	Shell   string
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

// DefaultShell means "whatever this machine runs scripts with" — /bin/sh on a
// unix box, cmd on Windows. It is the value a step carries when nobody chose,
// and it is what a runner did before choosing was possible, so an existing flow
// keeps behaving exactly as it did.
const DefaultShell = "default"

// Shells are the interpreters a step may ask for.
//
// A short, closed list rather than a free-text command, because the daemon has
// to be able to say "that is not a shell" while the author is still editing —
// and because the value crosses to another machine, where an arbitrary string
// would be an arbitrary program to start. Anything not on it belongs inside the
// script, behind a shebang or an explicit interpreter call.
//
// The agent maps each of these to the program it actually starts and picks the
// file extension the script is written to; see runner/dzrunner.py.
var Shells = []string{DefaultShell, "sh", "bash", "python", "powershell", "node"}

// knownShell reports whether s is one of Shells. The empty string is the same
// as DefaultShell: a task queued before this param existed carries no shell,
// and so does a step nobody has touched.
func knownShell(s string) bool {
	if s == "" {
		return true
	}
	for _, k := range Shells {
		if k == s {
			return true
		}
	}
	return false
}

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
				"or a network the built-in steps cannot reach. Pick a machine from the list, or a label " +
				"shared by several machines so any free one can take the job. Choose what runs the script " +
				"— the machine's own shell, sh, bash, Python, PowerShell or Node — and write it in the " +
				"box; or wire the script in on the 'script' input to build it in an earlier step. The " +
				"value wired into 'in' arrives on the script's standard input; whatever the script prints " +
				"comes back on 'out'. A non-zero exit fails the step, with the script's error output attached.",
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
				{
					Title:  "A Python script instead of a shell one",
					Params: json.RawMessage(`{"runner":"invoices-box","shell":"python","script":"import sys, json\nprint(json.load(sys.stdin)[\"total\"])"}`),
					Notes:  "The agent starts python3 with the script; standard input still carries the 'in' value.",
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
				{
					// The script itself, when an earlier step builds it —
					// picked from a table, filled into a template, written by
					// the AI step. Wired, it wins over the typed one; unwired,
					// the box on the step is the script, which is how nearly
					// every flow uses this.
					//
					// Separate from 'in' on purpose. One port carrying either
					// the program or its data would make "what did this run?"
					// depend on which upstream step happened to be connected.
					Port:       "script",
					Label:      "Script",
					MIME:       []string{"text/plain"},
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
      "title": "Machine",
      "format": "runner",
      "description": "The machine to run this on, chosen from the ones your organisation has registered. Leave it empty and set a label instead to use any machine in a pool."
    },
    "label": {
      "type": "string",
      "title": "Or any machine labelled",
      "format": "runner-label",
      "description": "Send the work to whichever machine carrying this label is free."
    },
    "shell": {
      "type": "string",
      "title": "Run it with",
      "default": "default",
      "enum": ["default", "sh", "bash", "python", "powershell", "node"],
      "enumNames": ["The machine's own shell", "sh (POSIX shell)", "bash", "Python 3", "PowerShell", "Node.js"],
      "description": "What starts the script on that machine. 'The machine's own shell' is /bin/sh on a unix box and cmd on Windows — the behaviour a runner has always had. Anything else writes the script to a temporary file and starts that interpreter with it, so choose Python and write Python. An agent older than this Dazyflow release does not know how to do that and will use the machine's shell regardless — re-run the install command on the machine to upgrade it."
    },
    "script": {
      "type": "string",
      "title": "Script",
      "format": "script",
      "description": "The script to run on that machine. It runs as the user the runner agent runs as, in the agent's working directory. Connect the 'script' input instead to have an earlier step supply it."
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
	// The 'script' input wins over the typed box, so an earlier step can build
	// the script — from a template, a table cell, the AI step. Only the
	// surrounding blank space goes: the inside of a script is significant, and
	// a Python one stops working if its indentation is rearranged.
	script, ok := params.TextInputOr(job, "script", params.StringDefault(job.Params, "script", ""))
	if !ok {
		return failed(job, "bad_input",
			"the 'script' input must carry text — wire a step that produces text, "+
				"or type the script on this step"), nil
	}
	script = strings.TrimSpace(script)
	if script == "" {
		return failed(job, "no_script", "this step has no script to run"), nil
	}
	shell := strings.ToLower(strings.TrimSpace(params.StringDefault(job.Params, "shell", "")))
	if !knownShell(shell) {
		// Refused here rather than on the machine: the daemon knows the list,
		// and a typo caught before the task is queued is a message about a
		// field instead of a script that never started.
		return failed(job, "bad_shell",
			"this step asks to run the script with "+shell+
				", which is not one of "+strings.Join(Shells, ", ")), nil
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
		Shell:  shell,
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
