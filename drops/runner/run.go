// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package runner

// The machine runs a small agent that ASKS the daemon for work, so nothing has to
// reach it: it can sit behind NAT, on a laptop, inside a network the daemon has
// never heard of.
//
// This step is deliberately the ONLY one in this package. A runner is not a way to
// add typed steps to the catalog — it is a way to run a command somewhere else.

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/internal/params"
	"github.com/dazyflow/dazyflow/engine"
)

// Injected by the daemon at boot, so this package does not import it. Without
// one the step reports that runners are not configured.
type Dispatcher interface {
	Dispatch(ctx context.Context, req Request, onProgress func(string)) (Result, error)
}

type Request struct {
	Tenant  string
	Tags    []string
	Script  string
	Shell   string
	Env     map[string]string
	Stdin   string
	Timeout time.Duration
}

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

const DefaultTimeout = 10 * time.Minute

const DefaultShell = "default"

var Shells = []string{DefaultShell, "sh", "bash", "python", "powershell", "node"}

const (
	ExitFail     = "fail"
	ExitContinue = "continue"
)

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
					Port:       "in",
					Label:      "Input",
					MIME:       []string{"text/plain", "application/json"},
					InlineOnly: true,
				},
				{
					Port:       "script",
					Label:      "Script",
					MIME:       []string{"text/plain"},
					InlineOnly: true,
				},
			},
			Outputs: []core.Port{
				{Port: "out", Label: "Output", MIME: []string{"text/plain"}, Example: json.RawMessage(`"3 files changed, 41 insertions(+), 8 deletions(-)"`)},
				{Port: "exit_code", Label: "Exit code", MIME: []string{"text/plain"}, Example: json.RawMessage(`"0"`)},
				{Port: "stderr", Label: "Error output", MIME: []string{"text/plain"}, Example: json.RawMessage(`"warning: 2 deprecated flags ignored"`)},
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
      "x_confirm_remove": true,
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
			Idempotent: false,
		},
		Execute: execute,
	})
}

func execute(ctx context.Context, job core.Job, progress chan<- core.Progress) (core.Result, error) {
	d := current()
	if d == nil {
		return params.Err(job, "not_configured",
			"runners are not set up on this Dazyflow deployment"), nil
	}
	tenant, _ := core.TenantFromContext(ctx)
	if tenant == "" {
		return params.Err(job, "no_tenant", "this step has no organisation to run against"), nil
	}

	tags := targetTags(job)
	script, ok := params.TextInputOr(job, "script", params.StringDefault(job.Params, "script", ""))
	if !ok {
		return params.Err(job, "bad_input",
			"the 'script' input must carry text — wire a step that produces text, "+
				"or type the script on this step"), nil
	}
	script = strings.TrimSpace(script)
	if script == "" {
		return params.Err(job, "no_script", "this step has no script to run"), nil
	}
	if bad := badEnvName(job); bad != "" {
		return params.Err(job, "bad_env",
			"the environment variable name "+bad+" cannot be used — a name must not be "+
				"empty, contain '=', or contain control characters"), nil
	}
	onNonzero := strings.ToLower(strings.TrimSpace(
		params.StringDefault(job.Params, "on_nonzero_exit", ExitFail)))
	if onNonzero == "" {
		onNonzero = ExitFail
	}
	if onNonzero != ExitFail && onNonzero != ExitContinue {
		return params.Err(job, "bad_param",
			"'if the script exits non-zero' is "+onNonzero+", which is neither "+
				ExitFail+" nor "+ExitContinue), nil
	}
	shell := strings.ToLower(strings.TrimSpace(params.StringDefault(job.Params, "shell", "")))
	if !knownShell(shell) {
		return params.Err(job, "bad_shell",
			"this step asks to run the script with "+shell+
				", which is not one of "+strings.Join(Shells, ", ")), nil
	}
	if len(tags) == 0 {
		return params.Err(job, "no_target",
			"this step has no tags saying where to run — pick a machine's name, "+
				"or the tags the machines that may take this all carry"), nil
	}

	timeout := DefaultTimeout
	if s := params.IntDefault(job.Params, "timeout_seconds", 0); s > 0 {
		timeout = time.Duration(s) * time.Second
	}

	res, err := d.Dispatch(ctx, Request{
		Tenant:  tenant,
		Tags:    tags,
		Script:  script,
		Shell:   shell,
		Env:     mergeEnv(job),
		Stdin:   stdinFrom(job),
		Timeout: timeout,
	}, func(msg string) {
		emit(job, progress, msg)
	})
	if err != nil {
		return params.Err(job, "dispatch_failed", err.Error()), nil
	}
	if res.Error != "" {
		return params.Err(job, "runner_error", res.Error), nil
	}
	if res.ExitCode != 0 && onNonzero != ExitContinue {
		msg := fmt.Sprintf("the command exited with status %d", res.ExitCode)
		if trimmed := strings.TrimSpace(res.Stderr); trimmed != "" {
			msg += ": " + trimmed
		}
		return params.Err(job, "nonzero_exit", msg), nil
	}
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"out":       {MIME: "text/plain", Inline: res.Stdout},
			"exit_code": {MIME: "text/plain", Inline: strconv.Itoa(res.ExitCode)},
			"stderr":    {MIME: "text/plain", Inline: res.Stderr},
		},
	}, nil
}

func targetTags(job core.Job) []string {
	raw := params.StringSlice(job.Params, "tags")
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
		b, err := json.Marshal(v)
		if err != nil {
			return fmt.Sprint(v)
		}
		return string(b)
	}
}

func mergeEnv(job core.Job) map[string]string {
	out := map[string]string{}
	for k, v := range job.Env {
		out[k] = v
	}
	for k, v := range envParam(job) {
		out[k] = v
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

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
	}
}
