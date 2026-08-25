// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package runner

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/engine"
)

// The step's job is to turn a script's outcome into something a flow author can
// read. Most of these tests are about the failure shapes, because a script on
// someone else's machine can fail in more ways than a built-in step and each
// one needs to arrive as a sentence, not a number.

// errNoMachine stands in for the dispatcher giving up — no machine carries the
// tags, or none is switched on.
var errNoMachine = errors.New("no machine tagged box has checked in recently")

type fakeDispatcher struct {
	got Request
	res Result
	err error
}

func (f *fakeDispatcher) Dispatch(_ context.Context, req Request, onProgress func(string)) (Result, error) {
	f.got = req
	if onProgress != nil {
		onProgress("waiting")
	}
	return f.res, f.err
}

// install swaps the dispatcher for one test and restores it, so the tests do
// not leak a fake into each other through the package-level variable.
func install(t *testing.T, d Dispatcher) *fakeDispatcher {
	t.Helper()
	prev := current()
	SetDispatcher(d)
	t.Cleanup(func() { SetDispatcher(prev) })
	if f, ok := d.(*fakeDispatcher); ok {
		return f
	}
	return nil
}

func run(t *testing.T, params map[string]any, input map[string]core.Ref) core.Result {
	t.Helper()
	ctx := core.WithTenant(t.Context(), "acme")
	res, err := execute(ctx, core.Job{ID: "j1", Params: params, Input: input}, nil)
	if err != nil {
		t.Fatalf("execute returned a transport error: %v", err)
	}
	return res
}

func TestExecute_PassesTheScriptAndTargetThrough(t *testing.T) {
	f := install(t, &fakeDispatcher{res: Result{Stdout: "hello\n"}})
	res := run(t, map[string]any{"tags": []any{"box"}, "script": "./go.sh"}, nil)

	if res.Status != core.StatusOK {
		t.Fatalf("status = %q (%+v)", res.Status, res.Error)
	}
	if strings.Join(f.got.Tags, ",") != "box" || f.got.Script != "./go.sh" {
		t.Errorf("dispatched %+v", f.got)
	}
	if f.got.Tenant != "acme" {
		t.Errorf("tenant = %q, want the executing org", f.got.Tenant)
	}
	if got := res.Output["out"].Inline; got != "hello\n" {
		t.Errorf("out = %v, want the script's output", got)
	}
}

func TestExecute_DefaultsTheTimeout(t *testing.T) {
	f := install(t, &fakeDispatcher{})
	run(t, map[string]any{"tags": []any{"box"}, "script": "x"}, nil)
	if f.got.Timeout != DefaultTimeout {
		t.Errorf("timeout = %v, want the default", f.got.Timeout)
	}
	run(t, map[string]any{"tags": []any{"box"}, "script": "x", "timeout_seconds": 30}, nil)
	if f.got.Timeout != 30*time.Second {
		t.Errorf("timeout = %v, want the configured 30s", f.got.Timeout)
	}
}

// A value wired in arrives on standard input, because that is the one interface
// every language and every shell tool already agrees on.
func TestExecute_WiresTheInputToStdin(t *testing.T) {
	f := install(t, &fakeDispatcher{})
	run(t, map[string]any{"tags": []any{"box"}, "script": "x"},
		map[string]core.Ref{"in": {Inline: "plain text"}})
	if f.got.Stdin != "plain text" {
		t.Errorf("stdin = %q", f.got.Stdin)
	}
}

// Structured input crosses as JSON. A Go fmt-style rendering of a map would be
// unparseable by jq, python, or anything else a script would reach for.
func TestExecute_StructuredInputBecomesJSON(t *testing.T) {
	f := install(t, &fakeDispatcher{})
	run(t, map[string]any{"tags": []any{"box"}, "script": "x"},
		map[string]core.Ref{"in": {Inline: map[string]any{"total": 42}}})
	if !strings.Contains(f.got.Stdin, `"total"`) || !strings.Contains(f.got.Stdin, "42") {
		t.Errorf("stdin = %q, want JSON", f.got.Stdin)
	}
}

// A non-zero exit fails the step, and the script's own stderr is the useful
// part — it is the author's message about what went wrong. Burying it would
// leave them with only a number.
func TestExecute_NonZeroExitFailsWithTheScriptsMessage(t *testing.T) {
	install(t, &fakeDispatcher{res: Result{ExitCode: 3, Stderr: "no such ledger"}})
	res := run(t, map[string]any{"tags": []any{"box"}, "script": "x"}, nil)

	if res.Status != core.StatusError {
		t.Fatalf("status = %q, want an error", res.Status)
	}
	if !strings.Contains(res.Error.Message, "3") {
		t.Errorf("message = %q, want the exit status", res.Error.Message)
	}
	if !strings.Contains(res.Error.Message, "no such ledger") {
		t.Errorf("message = %q, want the script's stderr", res.Error.Message)
	}
}

// The agent refusing to run something (not on its allow-list, binary missing,
// timed out) is a different failure from a script that ran and exited badly.
func TestExecute_AgentRefusalIsItsOwnFailure(t *testing.T) {
	install(t, &fakeDispatcher{res: Result{Error: "command not permitted by this runner"}})
	res := run(t, map[string]any{"tags": []any{"box"}, "script": "rm -rf /"}, nil)
	if res.Status != core.StatusError {
		t.Fatal("an agent refusal did not fail the step")
	}
	if res.Error.Code != "runner_error" {
		t.Errorf("code = %q, want it distinguished from a bad exit", res.Error.Code)
	}
	if !strings.Contains(res.Error.Message, "not permitted") {
		t.Errorf("message = %q, want the agent's reason", res.Error.Message)
	}
}

func TestExecute_ConfigurationMistakes(t *testing.T) {
	for _, tc := range []struct {
		name, wantCode string
		params         map[string]any
	}{
		{"no script", "no_script", map[string]any{"tags": []any{"box"}}},
		{"no target", "no_target", map[string]any{"script": "x"}},
		// Both would be ambiguous, and silently preferring one would make the
		// other look honoured.
	} {
		t.Run(tc.name, func(t *testing.T) {
			install(t, &fakeDispatcher{})
			res := run(t, tc.params, nil)
			if res.Status != core.StatusError {
				t.Fatalf("status = %q, want an error", res.Status)
			}
			if res.Error.Code != tc.wantCode {
				t.Errorf("code = %q, want %q", res.Error.Code, tc.wantCode)
			}
		})
	}
}

// A deployment without runners should say so, not fail obscurely.
func TestExecute_WithoutADispatcher(t *testing.T) {
	install(t, nil)
	SetDispatcher(nil)
	res := run(t, map[string]any{"tags": []any{"box"}, "script": "x"}, nil)
	if res.Status != core.StatusError || res.Error.Code != "not_configured" {
		t.Fatalf("result = %+v, want a clear not-configured failure", res)
	}
}

// Without a tenant there is no way to know whose runners these are, and
// guessing would mean running a script on someone else's machine.
func TestExecute_RefusesWithoutATenant(t *testing.T) {
	install(t, &fakeDispatcher{})
	res, err := execute(context.Background(), core.Job{
		ID:     "j1",
		Params: map[string]any{"tags": []any{"box"}, "script": "x"},
	}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusError || res.Error.Code != "no_tenant" {
		t.Fatalf("result = %+v, want a no-tenant refusal", res)
	}
}

// The manifest must not claim idempotence: the daemon has no idea what the
// script does, so it must never assume running it twice is harmless.
func TestManifest_IsNotIdempotent(t *testing.T) {
	for _, m := range manifestsUnderTest(t) {
		if m.Idempotent {
			t.Errorf("%s declares itself idempotent", m.ID)
		}
		for _, p := range m.Inputs {
			if !p.InlineOnly {
				t.Errorf("%s input %q is not marked inline-only", m.ID, p.Port)
			}
		}
	}
}

// manifestsUnderTest pulls this package's registered manifest back out, so the
// test reads what the palette will.
func manifestsUnderTest(t *testing.T) []core.Manifest {
	t.Helper()
	var out []core.Manifest
	for id, m := range engine.Default.Manifests() {
		if id == "run_on_runner" {
			out = append(out, m)
		}
	}
	if len(out) == 0 {
		t.Fatal("run_on_runner is not registered")
	}
	return out
}

// Registration normalizes labels — lower-cased, trimmed, de-duplicated — and
// the admin page shows them that way. A step targeting the label as the
// operator TYPED it therefore has to be normalized too, or a flow author sees
// 'no runner is labelled "Linux"' next to a page plainly showing linux.
func TestExecute_NormalizesTheTarget(t *testing.T) {
	f := install(t, &fakeDispatcher{})

	// Registration normalizes labels — lower-cased, trimmed, de-duplicated — and
	// the admin page shows them that way. A tag has to get the same treatment, or
	// a step reads 'no machine carries the tag "Linux"' next to a page plainly
	// showing linux. Names too: validRunnerName only ever allows lower-case.
	run(t, map[string]any{"tags": []any{"  Linux ", "Build-Box "}, "script": "x"}, nil)
	if strings.Join(f.got.Tags, ",") != "linux,build-box" {
		t.Errorf("tags = %v, want them normalized the way registration stores them", f.got.Tags)
	}

	// Duplicates and blanks collapse: one tag listed twice is one requirement,
	// and an empty row left in the editor is not a tag no machine can carry.
	run(t, map[string]any{"tags": []any{"linux", "LINUX", "  ", ""}, "script": "x"}, nil)
	if strings.Join(f.got.Tags, ",") != "linux" {
		t.Errorf("tags = %v, want duplicates and blanks dropped", f.got.Tags)
	}

	// Whitespace alone is no target, not a target of "".
	res := run(t, map[string]any{"tags": []any{"   "}, "script": "x"}, nil)
	if res.Status != core.StatusError || res.Error.Code != "no_target" {
		t.Errorf("result = %+v, want a no_target failure", res)
	}
}

// A flow saved before this step took tags carries `runner` (one machine) or
// `label` (any machine with it), and has to keep running: those flows are in
// production, and the whole reason one field could replace two is that a
// machine's name is now itself a tag.
func TestExecute_HonoursThePreTagsParams(t *testing.T) {
	f := install(t, &fakeDispatcher{})

	run(t, map[string]any{"runner": "Invoices-Box", "script": "x"}, nil)
	if strings.Join(f.got.Tags, ",") != "invoices-box" {
		t.Errorf("tags = %v, want the old `runner` read as one tag", f.got.Tags)
	}

	run(t, map[string]any{"label": "build", "script": "x"}, nil)
	if strings.Join(f.got.Tags, ",") != "build" {
		t.Errorf("tags = %v, want the old `label` read as one tag", f.got.Tags)
	}

	// A step re-saved with tags ignores the leftovers rather than quietly adding
	// them as extra requirements — which, since every tag must match, would
	// narrow the step to nothing.
	run(t, map[string]any{"tags": []any{"gpu"}, "runner": "old-box", "label": "stale", "script": "x"}, nil)
	if strings.Join(f.got.Tags, ",") != "gpu" {
		t.Errorf("tags = %v, want only the new field once it is set", f.got.Tags)
	}
}

// ---- the script, and what starts it -----------------------------------

// The 'script' input exists so an earlier step can build the script — a
// template, a table cell, the AI step. Wired, it decides; the box on the step
// is the fallback, which is how nearly every flow uses this.
func TestExecute_TheScriptInputOverridesTheTypedOne(t *testing.T) {
	f := install(t, &fakeDispatcher{})

	run(t, map[string]any{"tags": []any{"box"}, "script": "./typed.sh"}, map[string]core.Ref{
		"script": {MIME: "text/plain", Inline: "./wired.sh"},
	})
	if f.got.Script != "./wired.sh" {
		t.Errorf("script = %q, want the wired one", f.got.Script)
	}

	// An unwired port must not blank the typed script out.
	run(t, map[string]any{"tags": []any{"box"}, "script": "./typed.sh"}, nil)
	if f.got.Script != "./typed.sh" {
		t.Errorf("script = %q, want the typed one", f.got.Script)
	}
}

// A script's insides are significant — a Python one stops working if its
// indentation is rearranged — so only the surrounding blank space goes.
func TestExecute_KeepsTheShapeOfTheScript(t *testing.T) {
	f := install(t, &fakeDispatcher{})
	run(t, map[string]any{
		"tags":   []any{"box"},
		"shell":  "python",
		"script": "\nif True:\n    print(1)\n\n",
	}, nil)
	if f.got.Script != "if True:\n    print(1)" {
		t.Errorf("script = %q, want the indentation intact", f.got.Script)
	}
}

func TestExecute_RejectsAScriptInputThatIsNotText(t *testing.T) {
	install(t, &fakeDispatcher{})
	res := run(t, map[string]any{"tags": []any{"box"}, "script": "./typed.sh"}, map[string]core.Ref{
		"script": {MIME: "application/json", Inline: map[string]any{"not": "text"}},
	})
	if res.Status != core.StatusError || res.Error.Code != "bad_input" {
		t.Errorf("result = %+v, want a bad_input failure naming the port", res)
	}
}

func TestExecute_PassesTheChosenShellThrough(t *testing.T) {
	f := install(t, &fakeDispatcher{})
	run(t, map[string]any{"tags": []any{"box"}, "script": "print(1)", "shell": " Python "}, nil)
	if f.got.Shell != "python" {
		t.Errorf("shell = %q, want it normalized and passed on", f.got.Shell)
	}

	// Nothing chosen stays nothing, so the agent does what it always did.
	run(t, map[string]any{"tags": []any{"box"}, "script": "x"}, nil)
	if f.got.Shell != "" {
		t.Errorf("shell = %q, want it left empty", f.got.Shell)
	}
}

// Caught here rather than on the machine: the daemon knows the list, and a
// message about a field beats a script that never started.
func TestExecute_RefusesAShellItDoesNotKnow(t *testing.T) {
	install(t, &fakeDispatcher{})
	res := run(t, map[string]any{"tags": []any{"box"}, "script": "x", "shell": "erlang"}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_shell" {
		t.Fatalf("result = %+v, want a bad_shell failure", res)
	}
	if !strings.Contains(res.Error.Message, "bash") {
		t.Errorf("message = %q, want it to name the shells that do work", res.Error.Message)
	}
}

// The step's enum and the Shells list are the same list seen from two sides: a
// value the form offers and the drop then refuses is a dead field.
func TestManifest_OffersExactlyTheShellsTheStepAccepts(t *testing.T) {
	for _, m := range manifestsUnderTest(t) {
		var schema struct {
			Properties struct {
				Shell struct {
					Enum      []string `json:"enum"`
					EnumNames []string `json:"enumNames"`
					Default   string   `json:"default"`
				} `json:"shell"`
			} `json:"properties"`
		}
		if err := json.Unmarshal(m.ParamsSchema, &schema); err != nil {
			t.Fatalf("params schema: %v", err)
		}
		got := schema.Properties.Shell
		if !slices.Equal(got.Enum, Shells) {
			t.Errorf("shell enum = %v, want %v", got.Enum, Shells)
		}
		if len(got.EnumNames) != len(got.Enum) {
			t.Errorf("%d shells but %d names — one would render as its raw value",
				len(got.Enum), len(got.EnumNames))
		}
		if got.Default != DefaultShell {
			t.Errorf("shell default = %q, want %q", got.Default, DefaultShell)
		}
		for _, s := range got.Enum {
			if !knownShell(s) {
				t.Errorf("the form offers %q and the step refuses it", s)
			}
		}
	}
}

// The script can arrive on a port now, so the port has to exist.
func TestManifest_HasAScriptInput(t *testing.T) {
	for _, m := range manifestsUnderTest(t) {
		var found bool
		for _, p := range m.Inputs {
			if p.Port == "script" {
				found = true
			}
		}
		if !found {
			t.Errorf("%s has no 'script' input", m.ID)
		}
	}
}

// ---- the environment the script runs with ------------------------------

// The point of the field: a credential reaches the machine without being
// written into the flow. The engine has already expanded ${secret.…} by the
// time the step runs, so what this checks is that the expanded value is
// actually handed on — the plumbing that was decoration once before.
func TestExecute_PassesTheStepsEnvToTheMachine(t *testing.T) {
	f := install(t, &fakeDispatcher{})
	run(t, map[string]any{
		"tags":   []any{"box"},
		"script": "./fetch.sh",
		"env":    map[string]any{"API_TOKEN": "resolved-secret", "MONTH": "03"},
	}, nil)

	if f.got.Env["API_TOKEN"] != "resolved-secret" || f.got.Env["MONTH"] != "03" {
		t.Errorf("env = %v, want the step's own values", f.got.Env)
	}
}

// Two sources existed before this field: core.Node.Env, which every step has,
// and now the step's own box. Both have to arrive, and the order has to be one
// somebody can predict.
func TestExecute_TheStepsEnvLayersOverTheNodes(t *testing.T) {
	f := install(t, &fakeDispatcher{})
	ctx := core.WithTenant(t.Context(), "acme")
	_, err := execute(ctx, core.Job{
		ID:     "j1",
		Params: map[string]any{"tags": []any{"box"}, "script": "x", "env": map[string]any{"SHARED": "from-step"}},
		Env:    map[string]string{"SHARED": "from-node", "NODE_ONLY": "kept"},
	}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	// The field is the one a person can see while editing the step, so it must
	// not be the half that is silently overridden.
	if f.got.Env["SHARED"] != "from-step" {
		t.Errorf("SHARED = %q, want the step's field to win", f.got.Env["SHARED"])
	}
	if f.got.Env["NODE_ONLY"] != "kept" {
		t.Errorf("env = %v, want the node's own entries kept", f.got.Env)
	}
}

// Nothing set means nothing sent, so the queued task's env column stays NULL
// and there is nothing to seal.
func TestExecute_NoEnvSendsNone(t *testing.T) {
	f := install(t, &fakeDispatcher{})
	run(t, map[string]any{"tags": []any{"box"}, "script": "x"}, nil)
	if f.got.Env != nil {
		t.Errorf("env = %v, want nil when nothing is set", f.got.Env)
	}
}

// A name an environment block cannot carry is refused before the task is
// queued. Not a privilege boundary — this step already runs arbitrary scripts —
// but a '=' splits the assignment and a newline corrupts everything after it,
// and both surface on the machine as something unrelated going wrong.
func TestExecute_RefusesAnUnusableEnvName(t *testing.T) {
	for _, name := range []string{"", "A=B", "WITH\nNEWLINE"} {
		t.Run(name, func(t *testing.T) {
			install(t, &fakeDispatcher{})
			res := run(t, map[string]any{
				"tags": []any{"box"}, "script": "x",
				"env": map[string]any{name: "v"},
			}, nil)
			if res.Status != core.StatusError || res.Error.Code != "bad_env" {
				t.Fatalf("result = %+v, want a bad_env failure", res)
			}
			if !strings.Contains(res.Error.Message, "must not be") {
				t.Errorf("message = %q, want it to say what a name may not contain", res.Error.Message)
			}
		})
	}
}

// A flow built by the API or an older editor can carry a non-string; a script
// asking for $RETRIES would rather have "3" than nothing.
func TestExecute_StringifiesANonStringEnvValue(t *testing.T) {
	f := install(t, &fakeDispatcher{})
	run(t, map[string]any{
		"tags": []any{"box"}, "script": "x",
		"env": map[string]any{"RETRIES": 3, "EMPTY": nil},
	}, nil)
	if f.got.Env["RETRIES"] != "3" {
		t.Errorf("RETRIES = %q, want it stringified", f.got.Env["RETRIES"])
	}
	if v, ok := f.got.Env["EMPTY"]; !ok || v != "" {
		t.Errorf("EMPTY = %q (present=%v), want an empty value, not a missing one", v, ok)
	}
}

// The field has to be in the schema for the editor to render its dict box.
func TestManifest_DeclaresTheEnvField(t *testing.T) {
	for _, m := range manifestsUnderTest(t) {
		var schema struct {
			Properties struct {
				Env struct {
					Type                 string          `json:"type"`
					AdditionalProperties json.RawMessage `json:"additionalProperties"`
					Description          string          `json:"description"`
				} `json:"env"`
			} `json:"properties"`
		}
		if err := json.Unmarshal(m.ParamsSchema, &schema); err != nil {
			t.Fatalf("params schema: %v", err)
		}
		if schema.Properties.Env.Type != "object" || len(schema.Properties.Env.AdditionalProperties) == 0 {
			t.Errorf("env is not a string-keyed map, so the editor renders no dict box: %+v",
				schema.Properties.Env)
		}
		// The whole reason the field is safe to use for a credential, so the
		// help has to say it.
		if !strings.Contains(schema.Properties.Env.Description, "${secret.") {
			t.Error("the env help does not mention ${secret.…}, which is how a credential gets in safely")
		}
	}
}

// ---- letting the flow handle the exit code -----------------------------

// The default is unchanged and has to stay that way: a non-zero exit is a
// failure, and every flow written before this param relies on it.
func TestExecute_NonZeroExitStillFailsByDefault(t *testing.T) {
	install(t, &fakeDispatcher{res: Result{ExitCode: 3, Stderr: "no such invoice"}})
	res := run(t, map[string]any{"tags": []any{"box"}, "script": "x"}, nil)
	if res.Status != core.StatusError || res.Error.Code != "nonzero_exit" {
		t.Fatalf("result = %+v, want a nonzero_exit failure", res)
	}
	// The script's own message is the useful part; a bare number leaves the
	// author guessing.
	if !strings.Contains(res.Error.Message, "no such invoice") {
		t.Errorf("message = %q, want the script's stderr attached", res.Error.Message)
	}
}

// The point of the param: a script's exit codes become a flow signal, so 2 can
// mean "nothing to do today" rather than "broken".
func TestExecute_CarriesOnAndHandsTheExitCodeToTheFlow(t *testing.T) {
	install(t, &fakeDispatcher{res: Result{ExitCode: 2, Stdout: "partial", Stderr: "warned"}})
	res := run(t, map[string]any{
		"tags": []any{"box"}, "script": "x", "on_nonzero_exit": "continue",
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status = %q (%+v), want the step to carry on", res.Status, res.Error)
	}
	if got := res.Output["exit_code"].Inline; got != "2" {
		t.Errorf("exit_code = %v, want the script's own code as text", got)
	}
	if got := res.Output["stderr"].Inline; got != "warned" {
		t.Errorf("stderr = %v, want the script's error output on its own wire", got)
	}
	if got := res.Output["out"].Inline; got != "partial" {
		t.Errorf("out = %v, want whatever the script managed to print", got)
	}
}

// A step switched between the two modes must not change what its wires carry,
// or turning the setting on would silently break the branch downstream.
func TestExecute_EmitsTheExitCodeOnSuccessToo(t *testing.T) {
	install(t, &fakeDispatcher{res: Result{Stdout: "done", Stderr: "a warning"}})
	res := run(t, map[string]any{"tags": []any{"box"}, "script": "x"}, nil)
	if got := res.Output["exit_code"].Inline; got != "0" {
		t.Errorf("exit_code = %v, want \"0\" on a success", got)
	}
	// A script that succeeded can still have written warnings, and a flow may
	// want them.
	if got := res.Output["stderr"].Inline; got != "a warning" {
		t.Errorf("stderr = %v, want it emitted on success as well", got)
	}
}

// The line the param draws, and the one that matters most: 'carry on' covers a
// script that RAN and returned a code. A machine that is switched off, an agent
// that refused the script, or a script the runner had to stop have no exit code
// to hand over — succeeding with a made-up one would send the flow down the
// "the script ran and said no" path when nothing ran at all.
func TestExecute_CarryOnDoesNotSwallowAFailureToRunAtAll(t *testing.T) {
	for _, tc := range []struct {
		name string
		res  Result
		err  error
		code string
	}{
		{"the agent refused it", Result{Error: "not on this runner's allow-list"}, nil, "runner_error"},
		{"the script was stopped", Result{Error: "still running after 30s and was stopped"}, nil, "runner_error"},
		{"no machine took it", Result{}, errNoMachine, "dispatch_failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			install(t, &fakeDispatcher{res: tc.res, err: tc.err})
			res := run(t, map[string]any{
				"tags": []any{"box"}, "script": "x", "on_nonzero_exit": "continue",
			}, nil)
			if res.Status != core.StatusError || res.Error.Code != tc.code {
				t.Fatalf("result = %+v, want a %s failure even with 'carry on'", res, tc.code)
			}
		})
	}
}

// Someone who wrote "ignore" meant not to fail; failing anyway would look like
// the setting does nothing.
func TestExecute_RefusesAnUnknownExitMode(t *testing.T) {
	install(t, &fakeDispatcher{})
	res := run(t, map[string]any{
		"tags": []any{"box"}, "script": "x", "on_nonzero_exit": "ignore",
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Fatalf("result = %+v, want a bad_param failure", res)
	}
	if !strings.Contains(res.Error.Message, ExitContinue) {
		t.Errorf("message = %q, want it to name the value that works", res.Error.Message)
	}
}

// The outputs have to exist in the manifest, or there is nothing to wire the
// branch from.
func TestManifest_DeclaresTheExitCodeOutputs(t *testing.T) {
	for _, m := range manifestsUnderTest(t) {
		have := map[string]bool{}
		for _, p := range m.Outputs {
			have[p.Port] = true
		}
		for _, want := range []string{"out", "exit_code", "stderr"} {
			if !have[want] {
				t.Errorf("%s has no %q output", m.ID, want)
			}
		}
		var schema struct {
			Properties struct {
				OnNonzeroExit struct {
					Enum    []string `json:"enum"`
					Default string   `json:"default"`
				} `json:"on_nonzero_exit"`
			} `json:"properties"`
		}
		if err := json.Unmarshal(m.ParamsSchema, &schema); err != nil {
			t.Fatalf("params schema: %v", err)
		}
		if !slices.Equal(schema.Properties.OnNonzeroExit.Enum, []string{ExitFail, ExitContinue}) {
			t.Errorf("enum = %v, want exactly the two the step accepts",
				schema.Properties.OnNonzeroExit.Enum)
		}
		// The default has to be the old behaviour, or every existing flow
		// changes meaning on upgrade.
		if schema.Properties.OnNonzeroExit.Default != ExitFail {
			t.Errorf("default = %q, want %q", schema.Properties.OnNonzeroExit.Default, ExitFail)
		}
	}
}
