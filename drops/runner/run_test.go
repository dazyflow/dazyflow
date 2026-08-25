// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package runner

import (
	"context"
	"encoding/json"
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
	res := run(t, map[string]any{"runner": "box", "script": "./go.sh"}, nil)

	if res.Status != core.StatusOK {
		t.Fatalf("status = %q (%+v)", res.Status, res.Error)
	}
	if f.got.Runner != "box" || f.got.Script != "./go.sh" {
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
	run(t, map[string]any{"runner": "box", "script": "x"}, nil)
	if f.got.Timeout != DefaultTimeout {
		t.Errorf("timeout = %v, want the default", f.got.Timeout)
	}
	run(t, map[string]any{"runner": "box", "script": "x", "timeout_seconds": 30}, nil)
	if f.got.Timeout != 30*time.Second {
		t.Errorf("timeout = %v, want the configured 30s", f.got.Timeout)
	}
}

// A value wired in arrives on standard input, because that is the one interface
// every language and every shell tool already agrees on.
func TestExecute_WiresTheInputToStdin(t *testing.T) {
	f := install(t, &fakeDispatcher{})
	run(t, map[string]any{"runner": "box", "script": "x"},
		map[string]core.Ref{"in": {Inline: "plain text"}})
	if f.got.Stdin != "plain text" {
		t.Errorf("stdin = %q", f.got.Stdin)
	}
}

// Structured input crosses as JSON. A Go fmt-style rendering of a map would be
// unparseable by jq, python, or anything else a script would reach for.
func TestExecute_StructuredInputBecomesJSON(t *testing.T) {
	f := install(t, &fakeDispatcher{})
	run(t, map[string]any{"runner": "box", "script": "x"},
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
	res := run(t, map[string]any{"runner": "box", "script": "x"}, nil)

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
	res := run(t, map[string]any{"runner": "box", "script": "rm -rf /"}, nil)
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
		{"no command", "no_script", map[string]any{"runner": "box"}},
		{"no target", "no_target", map[string]any{"script": "x"}},
		// Both would be ambiguous, and silently preferring one would make the
		// other look honoured.
		{"both target kinds", "two_targets", map[string]any{"runner": "box", "label": "linux", "script": "x"}},
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
	res := run(t, map[string]any{"runner": "box", "script": "x"}, nil)
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
		Params: map[string]any{"runner": "box", "script": "x"},
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

	run(t, map[string]any{"label": "  Linux ", "script": "x"}, nil)
	if f.got.Label != "linux" {
		t.Errorf("label = %q, want it normalized the way registration stores it", f.got.Label)
	}
	if f.got.Runner != "" {
		t.Errorf("runner = %q, want it left empty", f.got.Runner)
	}

	// The same for the name: validRunnerName only ever allows lower-case, so a
	// capital or a pasted space could never match anything.
	run(t, map[string]any{"runner": "Build-Box ", "script": "x"}, nil)
	if f.got.Runner != "build-box" {
		t.Errorf("runner = %q, want it normalized", f.got.Runner)
	}

	// Whitespace alone is still no target, not a target of "".
	res := run(t, map[string]any{"runner": "   ", "script": "x"}, nil)
	if res.Status != core.StatusError || res.Error.Code != "no_target" {
		t.Errorf("result = %+v, want a no_target failure", res)
	}
}

// ---- the script, and what starts it -----------------------------------

// The 'script' input exists so an earlier step can build the script — a
// template, a table cell, the AI step. Wired, it decides; the box on the step
// is the fallback, which is how nearly every flow uses this.
func TestExecute_TheScriptInputOverridesTheTypedOne(t *testing.T) {
	f := install(t, &fakeDispatcher{})

	run(t, map[string]any{"runner": "box", "script": "./typed.sh"}, map[string]core.Ref{
		"script": {MIME: "text/plain", Inline: "./wired.sh"},
	})
	if f.got.Script != "./wired.sh" {
		t.Errorf("script = %q, want the wired one", f.got.Script)
	}

	// An unwired port must not blank the typed script out.
	run(t, map[string]any{"runner": "box", "script": "./typed.sh"}, nil)
	if f.got.Script != "./typed.sh" {
		t.Errorf("script = %q, want the typed one", f.got.Script)
	}
}

// A script's insides are significant — a Python one stops working if its
// indentation is rearranged — so only the surrounding blank space goes.
func TestExecute_KeepsTheShapeOfTheScript(t *testing.T) {
	f := install(t, &fakeDispatcher{})
	run(t, map[string]any{
		"runner": "box",
		"shell":  "python",
		"script": "\nif True:\n    print(1)\n\n",
	}, nil)
	if f.got.Script != "if True:\n    print(1)" {
		t.Errorf("script = %q, want the indentation intact", f.got.Script)
	}
}

func TestExecute_RejectsAScriptInputThatIsNotText(t *testing.T) {
	install(t, &fakeDispatcher{})
	res := run(t, map[string]any{"runner": "box", "script": "./typed.sh"}, map[string]core.Ref{
		"script": {MIME: "application/json", Inline: map[string]any{"not": "text"}},
	})
	if res.Status != core.StatusError || res.Error.Code != "bad_input" {
		t.Errorf("result = %+v, want a bad_input failure naming the port", res)
	}
}

func TestExecute_PassesTheChosenShellThrough(t *testing.T) {
	f := install(t, &fakeDispatcher{})
	run(t, map[string]any{"runner": "box", "script": "print(1)", "shell": " Python "}, nil)
	if f.got.Shell != "python" {
		t.Errorf("shell = %q, want it normalized and passed on", f.got.Shell)
	}

	// Nothing chosen stays nothing, so the agent does what it always did.
	run(t, map[string]any{"runner": "box", "script": "x"}, nil)
	if f.got.Shell != "" {
		t.Errorf("shell = %q, want it left empty", f.got.Shell)
	}
}

// Caught here rather than on the machine: the daemon knows the list, and a
// message about a field beats a script that never started.
func TestExecute_RefusesAShellItDoesNotKnow(t *testing.T) {
	install(t, &fakeDispatcher{})
	res := run(t, map[string]any{"runner": "box", "script": "x", "shell": "erlang"}, nil)
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
