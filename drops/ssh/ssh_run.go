// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package ssh holds the step that runs a command on a server over SSH.
//
// Deliberately one step, for the reason drops/runner gives for its own: this is
// not a way to add typed steps to the catalog, it is a way to run a command
// somewhere else. Files already have a home next door in drops/sftp, over the
// same connection and the same named credential.
//
// Where this sits against "Run on your machine": the runner agent PULLS work,
// so it reaches a laptop behind NAT but has to be installed first. This PUSHES,
// so it needs a reachable host and a key but nothing installed — which is what
// makes it the one that works on an appliance, a customer's box, or anything
// you will never be allowed to run an agent on.
package ssh

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/internal/params"
	"github.com/dazyflow/dazyflow/drops/sshcreds"
	"github.com/dazyflow/dazyflow/engine"
	"github.com/dazyflow/dazyflow/internal/sshutil"
)

const integration = "SSH"

const brandColor = "#4c5a67"

const (
	exitFail     = "fail"
	exitContinue = "continue"
)

const (
	defaultTimeoutSeconds = 300
	maxTimeoutSeconds     = 3600
	defaultMaxOutputKB    = 256
	maxMaxOutputKB        = 4096
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "ssh_run",
			Version:     "1.0",
			Label:       "SSH",
			Subtitle:    "Run a command",
			Summary:     "Run a command on one of your servers and use what it printed.",
			Integration: integration,
			Category:    "system",
			Icon:        "terminal",
			Color:       brandColor,
			Provider:    "internal",
			Tags: []string{
				"ssh", "remote", "command", "shell", "server", "run", "exec",
				"script", "deploy", "restart", "systemctl", "sysadmin",
			},
			Description: "Run a command on a server over SSH and carry on with what it printed. " +
				"Pick the server by name — they are configured once on the Servers page, so no address, " +
				"username or key is ever written into a flow. Whatever the command prints comes back on " +
				"'out', its error output on 'Error output', and the status it exited with on 'Exit code'.\n\n" +
				"A non-zero exit fails the step by default, with the command's error output attached. Set " +
				"'If the command exits non-zero' to carry on instead and branch on 'Exit code' — the way " +
				"exit codes are meant to work, where 2 might mean \"nothing to do today\" rather than " +
				"\"broken\".\n\n" +
				"Pass values in as environment variables — ${secret.NAME} for a credential, which reaches " +
				"the server without being written into the flow or into the run's log. The value wired " +
				"into 'in' arrives on the command's standard input. Wire 'Server address' to run the same " +
				"command across a fleet: the chosen server's username and key are reused against each " +
				"address, so give that credential a known_hosts covering every machine rather than a " +
				"single fingerprint.\n\n" +
				"There is no terminal attached, so a command that stops to ask a question waits until the " +
				"step's time runs out — use 'sudo -n' and a NOPASSWD rule rather than plain 'sudo'. The " +
				"command runs in the account's shell, so it is written the way you would write it in a " +
				"POSIX shell.",
			Examples: []core.ParamsExample{
				{
					Title:  "Restart a service",
					Params: json.RawMessage(`{"account":"web-1","command":"sudo -n systemctl restart nginx"}`),
					Notes:  "Pair it with 'Is it up?' before and after, so the flow proves the restart worked.",
				},
				{
					Title:  "How full are the disks?",
					Params: json.RawMessage(`{"account":"web-1","command":"df -h --output=pcent,target | tail -n +2"}`),
					Notes:  "Feed 'out' into Read CSV or Regex, then Branch over 90%.",
				},
				{
					Title:  "Back up a database, then fetch the file over SFTP",
					Params: json.RawMessage(`{"account":"db-1","command":"pg_dump -Fc app > /tmp/app.dump","timeout_seconds":1800}`),
					Notes:  "The SFTP steps use these same servers, so Download file picks 'db-1' and takes /tmp/app.dump.",
				},
				{
					Title:  "Let the flow decide what a failure means",
					Params: json.RawMessage(`{"account":"web-1","command":"./sync.sh","on_nonzero_exit":"continue"}`),
					Notes:  "The step succeeds whatever the command returns; branch on 'Exit code' (\"0\" is success).",
				},
				{
					Title:  "Give the command a credential and a parameter",
					Params: json.RawMessage(`{"account":"web-1","command":"./report.sh","env":{"API_TOKEN":"${secret.BILLING_TOKEN}","MONTH":"03"}}`),
					Notes:  "The secret is resolved on the way out and blanked from the run's log; the command reads $API_TOKEN.",
				},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				{Port: "in", Label: "Input", MIME: []string{"text/plain", "application/json"}, InlineOnly: true},
				{Port: "command", Label: "Command", MIME: []string{"text/plain"}, InlineOnly: true},
				{Port: "host", Label: "Server address", MIME: []string{"text/plain"}, InlineOnly: true},
			},
			Outputs: []core.Port{
				{Port: "out", Label: "Output", MIME: []string{"text/plain"}, Example: json.RawMessage(`"nginx.service: active (running)"`)},
				{Port: "exit_code", Label: "Exit code", MIME: []string{"text/plain"}, Example: json.RawMessage(`"0"`)},
				{Port: "stderr", Label: "Error output", MIME: []string{"text/plain"}, Example: json.RawMessage(`"warning: 2 deprecated flags ignored"`)},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"required":["command"],
				"properties":{
					"account":{"type":"string","title":"Server","format":"ssh-account","description":"Which of the org's saved servers to run this on. Set them up on the Servers page — the address, the login and the host key live there, never in the flow. The SFTP steps pick from the same list."},
					"command":{"type":"string","title":"Command","format":"script","examples":["sudo -n systemctl restart nginx"],"description":"What to run, as you would type it into a shell on that server. Connect the 'Command' input instead to have an earlier step build it."},
					"working_dir":{"type":"string","title":"Run it in folder","examples":["/srv/app"],"description":"Change to this folder first. The step fails rather than carrying on in the home directory if the folder isn't there. Leave blank for the account's home directory."},
					"env":{"type":"object","title":"Environment variables","additionalProperties":{"type":"string"},"x_confirm_remove":true,"description":"Values the command reads from its environment — $NAME in a shell. Use ${secret.NAME} for anything sensitive: the value reaches the server but is never written into the flow, and is blanked out of the run's output and logs."},
					"on_nonzero_exit":{"type":"string","title":"If the command exits non-zero","default":"fail","enum":["fail","continue"],"enumNames":["Fail this step","Carry on — the flow checks the exit code"],"description":"A command that exits non-zero has failed, and by default so does this step. Choose 'Carry on' and the step succeeds instead, with the code on the 'Exit code' output for the flow to branch on. This covers ONLY a command that ran and returned a code: a server that cannot be reached, a login the server refused, or a command that outran its timeout still fail the step, because there is no exit code to hand you and inventing one would send the flow down the wrong path."},
					"timeout_seconds":{"type":"integer","title":"Give up after (seconds)","default":300,"minimum":1,"maximum":3600,"x_advanced":true,"description":"How long the command may run before the step gives up and asks the server to stop it. Raise it for backups and migrations; a command with no terminal to answer a prompt will otherwise sit here until this elapses."},
					"max_output_kb":{"type":"integer","title":"Keep at most (KB of output)","default":256,"minimum":1,"maximum":4096,"x_advanced":true,"description":"How much of each of the two output streams to keep. A command that prints more still runs to completion — the extra is dropped rather than the command stopped, and the step says so."}
				}
			}`),
			Idempotent: false,
		},
		Execute: executeSSHRun,
	})
}

func executeSSHRun(ctx context.Context, job core.Job, progress chan<- core.Progress) (core.Result, error) {
	// No fallback name: there is no conventional "default" server, so an unset
	// account is a step nobody has pointed anywhere yet, not a lookup to try.
	account := strings.TrimSpace(params.StringDefault(job.Params, "account", ""))
	if account == "" {
		return params.Err(job, "no_server",
			"this step has no server chosen — pick one on the step, "+
				"or add one on the Servers page"), nil
	}
	cfg, configured, err := sshcreds.Get(ctx, account)
	if err != nil {
		return params.Err(job, "lookup_failed", err.Error()), nil
	}
	if !configured {
		return params.Err(job, "not_connected",
			"no saved server called "+strconv.Quote(account)+" — add it on the Servers page, "+
				"or pick one that is already there"), nil
	}

	// A wired address reuses the chosen credential's login and key against a
	// different machine, which is what makes one step serve a fleet.
	if host, ok := params.TextInputOr(job, "host", ""); !ok {
		return params.Err(job, "bad_input", "input port 'host' must be text"), nil
	} else if host = strings.TrimSpace(host); host != "" {
		cfg.Host = host
	}

	command, ok := params.TextInputOr(job, "command", params.StringDefault(job.Params, "command", ""))
	if !ok {
		return params.Err(job, "bad_input",
			"the 'Command' input must carry text — wire a step that produces text, "+
				"or type the command on this step"), nil
	}
	if command = strings.TrimSpace(command); command == "" {
		return params.Err(job, "no_command", "this step has no command to run"), nil
	}

	env := mergeEnv(job)
	if bad := badEnvName(env); bad != "" {
		return params.Err(job, "bad_env",
			"the environment variable name "+bad+" cannot be used — a name must be letters, "+
				"digits and underscores, and must not start with a digit"), nil
	}

	onNonzero := strings.ToLower(strings.TrimSpace(params.StringDefault(job.Params, "on_nonzero_exit", exitFail)))
	if onNonzero == "" {
		onNonzero = exitFail
	}
	if onNonzero != exitFail && onNonzero != exitContinue {
		return params.Err(job, "bad_param",
			"'if the command exits non-zero' is "+onNonzero+", which is neither "+
				exitFail+" nor "+exitContinue), nil
	}

	timeout := params.ClampInt(params.IntDefault(job.Params, "timeout_seconds", defaultTimeoutSeconds), 1, maxTimeoutSeconds)
	maxOutput := params.ClampInt(params.IntDefault(job.Params, "max_output_kb", defaultMaxOutputKB), 1, maxMaxOutputKB) * 1024

	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()

	client, err := sshutil.DialSSH(ctx, cfg)
	if err != nil {
		return params.Err(job, "ssh_error", err.Error()), nil
	}
	defer client.Close()

	res, err := client.Run(ctx, sshutil.RunRequest{
		Command:    command,
		Env:        env,
		WorkingDir: strings.TrimSpace(params.StringDefault(job.Params, "working_dir", "")),
		Stdin:      stdinFrom(job),
		MaxOutput:  maxOutput,
		OnStdout:   func(line string) { emit(job, progress, line) },
	})
	if err != nil {
		return params.Err(job, "ssh_error", err.Error()), nil
	}

	if res.ExitCode != 0 && onNonzero != exitContinue {
		msg := fmt.Sprintf("the command exited with status %d", res.ExitCode)
		if res.Signal != "" {
			msg = fmt.Sprintf("the command was killed by SIG%s", res.Signal)
		}
		if trimmed := strings.TrimSpace(res.Stderr); trimmed != "" {
			msg += ": " + trimmed
		}
		return params.Err(job, "nonzero_exit", msg), nil
	}

	// Truncation is reported where the operator is already looking rather than
	// as a silently shorter string: output that stops mid-line otherwise reads
	// as a command that died.
	if res.StdoutTruncated || res.StderrTruncated {
		emit(job, progress, fmt.Sprintf(
			"output was longer than the %d KB this step keeps — the rest was dropped", maxOutput/1024))
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
	raw, _ := job.Params["env"].(map[string]any)
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
	if len(out) == 0 {
		return nil
	}
	return out
}

// badEnvName rejects here rather than in sshutil, which silently drops what it
// cannot quote: a step that runs without the variable it was given is worse
// than one that says the name is unusable.
func badEnvName(env map[string]string) string {
	for name := range env {
		if !validEnvName(name) {
			return strconv.Quote(name)
		}
	}
	return ""
}

func validEnvName(k string) bool {
	if k == "" {
		return false
	}
	for i, r := range k {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '_':
		case r >= '0' && r <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
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
