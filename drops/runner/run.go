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
	"strconv"
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
	// Tags is where to run: a machine carrying ALL of them. A machine's own name
	// is one of its tags, so pinning a step to one machine is a single-tag list.
	Tags   []string
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

// What a non-zero exit means for the step. Two words rather than a boolean
// because they say what happens, and a `"on_nonzero_exit": "continue"` in a
// saved flow reads without a lookup — where `"ignore_exit_code": true` would
// claim the code is ignored when it is in fact handed to the flow.
const (
	// ExitFail is the default and the long-standing behaviour: a script that
	// exits non-zero has failed, and so has the step.
	ExitFail = "fail"
	// ExitContinue succeeds the step and puts the code on an output for the
	// flow to branch on. Covers ONLY a script that ran and returned a code —
	// see the param's description for the cases it deliberately does not.
	ExitContinue = "continue"
)

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
				"or a network the built-in steps cannot reach. Say WHERE with tags: the job goes to a " +
				"machine carrying every tag you list, so 'linux' means any of your linux machines and " +
				"'linux + gpu' means one that is both. Every machine's own name is also a tag, so listing " +
				"a name pins the step to that one machine. Choose what runs the script " +
				"— the machine's own shell, sh, bash, Python, PowerShell or Node — and write it in the " +
				"box; or wire the script in on the 'script' input to build it in an earlier step. Pass " +
				"values in as environment variables — ${secret.NAME} for a credential, which reaches the " +
				"machine without ever being written into the flow. The " +
				"value wired into 'in' arrives on the script's standard input; whatever the script prints " +
				"comes back on 'out'. A non-zero exit fails the step, with the script's error output " +
				"attached — or set 'If the script exits non-zero' to carry on, and branch on the " +
				"'Exit code' output instead so the flow handles its own failures.",
			Summary: "Run a script on a machine you host, and use what it prints.",
			Examples: []core.ParamsExample{
				{
					Title:  "Run a script on one particular machine",
					Params: json.RawMessage(`{"tags":["invoices-box"],"script":"./fetch-invoices.sh"}`),
					Notes:  "A machine's name is one of its tags, so a single name pins the step to it.",
				},
				{
					Title:  "Any machine in a pool",
					Params: json.RawMessage(`{"tags":["build"],"script":"./report.sh --month 03"}`),
					Notes:  "Whichever machine tagged 'build' is free takes the job.",
				},
				{
					Title:  "Narrow the pool with a second tag",
					Params: json.RawMessage(`{"tags":["linux","gpu"],"script":"./render.sh"}`),
					Notes:  "Every tag must match, so this runs on a machine that is both — not on either.",
				},
				{
					Title:  "Give the script a credential and a parameter",
					Params: json.RawMessage(`{"tags":["invoices-box"],"env":{"API_TOKEN":"${secret.BILLING_TOKEN}","MONTH":"03"},"script":"./fetch-invoices.sh"}`),
					Notes:  "The secret is resolved on the way out and never stored in the flow; the script reads $API_TOKEN.",
				},
				{
					Title:  "Let the flow decide what a failure means",
					Params: json.RawMessage(`{"tags":["build"],"script":"./sync.sh","on_nonzero_exit":"continue"}`),
					Notes:  "The step succeeds whatever the script returns; branch on 'Exit code' (\"0\" is success) to take a different path per code.",
				},
				{
					Title:  "A Python script instead of a shell one",
					Params: json.RawMessage(`{"tags":["invoices-box"],"shell":"python","script":"import sys, json\nprint(json.load(sys.stdin)[\"total\"])"}`),
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
				// The script's own report on how it went. Emitted on every run
				// that actually reached the machine, success or not — a script
				// that succeeds can still have written warnings to stderr, and
				// a flow handling its own failures needs the number.
				{Port: "exit_code", Label: "Exit code", MIME: []string{"text/plain"}},
				{Port: "stderr", Label: "Error output", MIME: []string{"text/plain"}},
			},
			ParamsSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "tags": {
      "type": "array",
      "title": "Where to run it",
      "format": "runner-tags",
      "items": { "type": "string" },
      "description": "Tags the machine must carry — ALL of them. One tag sends the work to whichever machine carrying it is free; adding a second narrows that to machines carrying both. Every machine's own name is also a tag, so picking a name pins this step to that one machine."
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
    "on_nonzero_exit": {
      "type": "string",
      "title": "If the script exits non-zero",
      "default": "fail",
      "enum": ["fail", "continue"],
      "enumNames": ["Fail this step", "Carry on — the flow checks the exit code"],
      "description": "A script that exits non-zero has failed, and by default so does this step. Choose 'Carry on' and the step succeeds instead, with the script's exit code on the 'Exit code' output for the flow to branch on — the way a script author expects exit codes to work (2 might mean 'nothing to do today' rather than 'broken'). This covers ONLY a script that ran and returned a code: a machine that is switched off, an agent that refused the script, or a script the runner had to stop still fail the step, because there is no exit code to hand you and pretending otherwise would send the flow down the wrong path."
    },
    "env": {
      "type": "object",
      "title": "Environment variables",
      "additionalProperties": { "type": "string" },
      "description": "Values the script reads from its environment — $NAME in a shell, os.environ in Python. Use ${secret.NAME} for anything sensitive: the value reaches the machine but is never written into the flow, and is blanked out of the run's output and logs. A value set here wins over one the agent's own environment already has."
    },
    "timeout_seconds": {
      "type": "integer",
      "title": "Give up after (seconds)",
      "x_advanced": true,
      "description": "Defaults to 600. The runner kills the script when this elapses."
    }
  },
  "required": ["tags", "script"]
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

	tags := targetTags(job)
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
	if bad := badEnvName(job); bad != "" {
		// Refused here rather than on the machine: an environment block cannot
		// carry these, and a script that starts with a mangled environment fails
		// somewhere far from the field that caused it.
		return failed(job, "bad_env",
			"the environment variable name "+bad+" cannot be used — a name must not be "+
				"empty, contain '=', or contain control characters"), nil
	}
	onNonzero := strings.ToLower(strings.TrimSpace(
		params.StringDefault(job.Params, "on_nonzero_exit", ExitFail)))
	if onNonzero == "" {
		onNonzero = ExitFail
	}
	if onNonzero != ExitFail && onNonzero != ExitContinue {
		// Refused rather than read as the default: someone who wrote "ignore"
		// meant not to fail, and silently failing anyway would look like the
		// setting does nothing.
		return failed(job, "bad_param",
			"'if the script exits non-zero' is "+onNonzero+", which is neither "+
				ExitFail+" nor "+ExitContinue), nil
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
	if len(tags) == 0 {
		return failed(job, "no_target",
			"this step has no tags saying where to run — pick a machine's name, "+
				"or the tags the machines that may take this all carry"), nil
	}

	timeout := DefaultTimeout
	if s := params.IntDefault(job.Params, "timeout_seconds", 0); s > 0 {
		timeout = time.Duration(s) * time.Second
	}

	res, err := d.Dispatch(ctx, Request{
		Tenant: tenant,
		Tags:   tags,
		Script: script,
		Shell:  shell,
		// What the script reads from its environment, with ${secret.…} already
		// resolved by the engine — for both the node's own env and the step's
		// own field, since the engine resolves params and env in one pass.
		//
		// The step's field wins over the node's env, which wins over whatever
		// the machine already has (the agent merges over its own environment).
		// Deliberately that order: the field is the one a person can see while
		// editing the step, so it must not be the one silently overridden.
		Env:     mergeEnv(job),
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
	if res.ExitCode != 0 && onNonzero != ExitContinue {
		// The script's own stderr is the useful part — it is the author's
		// message about what went wrong, and burying it would leave them with
		// only a number.
		msg := fmt.Sprintf("the command exited with status %d", res.ExitCode)
		if trimmed := strings.TrimSpace(res.Stderr); trimmed != "" {
			msg += ": " + trimmed
		}
		return failed(job, "nonzero_exit", msg), nil
	}
	// Reached either because the script succeeded, or because the flow asked to
	// handle the exit code itself. Both emit the same three outputs, so a step
	// switched from one mode to the other does not change what its wires carry.
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"out": {MIME: "text/plain", Inline: res.Stdout},
			// Text, not a number, matching the shell drop's 'Exit code' — it is
			// compared against "0" and routed on, not arithmetic.
			"exit_code": {MIME: "text/plain", Inline: strconv.Itoa(res.ExitCode)},
			"stderr":    {MIME: "text/plain", Inline: res.Stderr},
		},
	}, nil
}

// targetTags reads where this step should run.
//
// Two older shapes are still honoured, and have to be: a flow saved before this
// step took tags carries `runner` (one machine, by name) or `label` (any machine
// carrying it), and those flows keep running. Both collapse to a single tag,
// because a machine's own name is now one of its tags — which is exactly why the
// two fields could be replaced by one in the first place.
//
// Normalized the same way registration normalizes labels: lower-cased and
// trimmed. A runner installed with `--labels Linux,Build` is stored (and listed
// in the admin page) as linux and build, so a step targeting "Linux" — or
// "build " with a trailing space from a paste — matched nothing and failed while
// the page plainly showed one. Names get the same treatment, since
// validRunnerName only ever allows lower-case.
func targetTags(job core.Job) []string {
	raw := params.StringSlice(job.Params, "tags")
	if len(raw) == 0 {
		// The pre-tags params. Read in a fixed order rather than merged: they
		// were mutually exclusive, so at most one is ever set.
		for _, key := range []string{"runner", "label"} {
			if v := params.StringDefault(job.Params, key, ""); v != "" {
				raw = append(raw, v)
			}
		}
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(raw))
	for _, t := range raw {
		t = strings.ToLower(strings.TrimSpace(t))
		if t == "" {
			continue
		}
		if _, dup := seen[t]; dup {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	return out
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

// mergeEnv builds the environment the script runs with.
//
// Two sources, because there were already two: core.Node.Env, which every step
// has and almost none uses, and this step's own `env` param, which is the one
// with a box in the editor. Merging rather than choosing means a flow already
// setting node env keeps working, and the layering has an order somebody can
// predict — see the note at the call site.
//
// Both arrive with ${secret.…} already expanded: the engine resolves params and
// node env in the same pass before the step runs, and the resolved values exist
// only for the length of this call plus the sealed queue row. Nothing here
// writes them anywhere.
func mergeEnv(job core.Job) map[string]string {
	out := map[string]string{}
	for k, v := range job.Env {
		out[k] = v
	}
	for k, v := range envParam(job) {
		out[k] = v
	}
	if len(out) == 0 {
		// nil rather than an empty map, so the task row's env column stays NULL
		// and there is nothing to seal.
		return nil
	}
	return out
}

// envParam reads the step's `env` field as strings.
//
// Non-string values are stringified rather than dropped: the schema says string,
// but a flow built by the API or an older editor can carry a number, and a
// script asking for $RETRIES would rather have "3" than nothing.
func envParam(job core.Job) map[string]string {
	raw, ok := job.Params["env"].(map[string]any)
	if !ok {
		return nil
	}
	out := make(map[string]string, len(raw))
	for k, v := range raw {
		switch tv := v.(type) {
		case nil:
			out[k] = ""
		case string:
			out[k] = tv
		default:
			out[k] = fmt.Sprint(tv)
		}
	}
	return out
}

// badEnvName returns the first environment variable name that cannot be put in
// an environment block, quoted, or "" when they are all usable.
//
// Not a security boundary — the author of this step can already run any script
// on that machine, so there is no privilege here to protect. It is about the
// failure being legible: a name containing '=' splits the assignment, and a
// name with a newline in it corrupts everything after it, both of which surface
// on the machine as something unrelated going wrong.
func badEnvName(job core.Job) string {
	for name := range mergeEnv(job) {
		if name == "" {
			return `""`
		}
		if strings.ContainsRune(name, '=') {
			return `"` + name + `"`
		}
		for _, r := range name {
			if r < 0x20 || r == 0x7f {
				return `"` + name + `"`
			}
		}
	}
	return ""
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
