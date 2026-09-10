// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package code

import (
	"fmt"
	"strings"
	"testing"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/internal/jsvm"
)

func runJob(t *testing.T, job core.Job, progress chan<- core.Progress) core.Result {
	t.Helper()
	res, err := executeCode(t.Context(), job, progress)
	if err != nil {
		t.Fatalf("executeCode returned error: %v", err)
	}
	return res
}

func TestScriptInputOverridesTheParam(t *testing.T) {
	t.Parallel()
	// Same rule the message sinks use: a wired input wins over what is typed on
	// the step, so one script can live in one place and run from several flows.
	job := core.Job{ID: "t", NodeID: "code_1",
		Params: map[string]any{"code": "return 'from the param';"},
		Input: map[string]core.Ref{
			"code": {MIME: "text/plain", Inline: "return 'from the wire';"},
		},
	}
	res := runJob(t, job, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status = %v, error = %+v", res.Status, res.Error)
	}
	if got := res.Output["out"].Inline; got != "from the wire" {
		t.Errorf("out = %v, want the wired script to win", got)
	}
}

func TestScriptParamStillRunsWhenNothingIsWired(t *testing.T) {
	t.Parallel()
	job := core.Job{ID: "t", NodeID: "code_1", Params: map[string]any{"code": "return 'from the param';"}}
	if got := runJob(t, job, nil).Output["out"].Inline; got != "from the param" {
		t.Errorf("out = %v, want the param's script", got)
	}
}

func TestAnEmptyScriptSaysWhereToPutOne(t *testing.T) {
	t.Parallel()
	// The message has to name BOTH places a script can come from now, or an
	// author who wired the input reads "param 'code' is required" and looks in
	// the wrong one.
	res := runJob(t, core.Job{ID: "t", NodeID: "code_1", Params: map[string]any{"code": "   "}}, nil)
	if res.Status != core.StatusError {
		t.Fatalf("status = %v, want an error", res.Status)
	}
	msg := res.Error.Message
	if !strings.Contains(msg, "code") || !strings.Contains(msg, "Script") {
		t.Errorf("message names only one source of the script: %q", msg)
	}
}

func TestANonTextScriptInputIsRejected(t *testing.T) {
	t.Parallel()
	job := core.Job{ID: "t", NodeID: "code_1",
		Params: map[string]any{"code": "return 1;"},
		Input:  map[string]core.Ref{"code": {MIME: "application/json", Inline: map[string]any{"not": "text"}}},
	}
	res := runJob(t, job, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_input" {
		t.Errorf("status = %v error = %+v, want bad_input", res.Status, res.Error)
	}
}

func TestConsoleLogLeavesOnTheLogsOutput(t *testing.T) {
	t.Parallel()
	// The live console is best-effort and a run that outpaces its reader drops
	// lines. The port keeps them, so what the script said outlives the stream.
	job := core.Job{ID: "t", NodeID: "code_1", Params: map[string]any{
		"code": "console.log('first'); console.log('second'); return 1;",
	}}
	res := runJob(t, job, nil)
	logs, ok := res.Output["logs"].Inline.([]any)
	if !ok {
		t.Fatalf("logs = %T, want a row list", res.Output["logs"].Inline)
	}
	if len(logs) != 2 {
		t.Fatalf("logs = %d rows, want 2", len(logs))
	}
	if got := logs[0].(map[string]any)["line"]; got != "first" {
		t.Errorf("first line = %v", got)
	}
	if got := logs[1].(map[string]any)["line"]; got != "second" {
		t.Errorf("second line = %v", got)
	}
}

func TestATruncatedLogSaysSoInTheLogItself(t *testing.T) {
	t.Parallel()
	// The sandbox stops the console at jsvm.MaxLogLines and says so on the last
	// line it delivers. The port carries that marker like any other line, so a
	// reader who scrolls to the end of the log learns the log ended early —
	// which is the only place they would look.
	job := core.Job{ID: "t", NodeID: "code_1", Params: map[string]any{
		"code":       fmt.Sprintf("for (let i = 0; i < %d; i++) console.log('line ' + i); return 1;", jsvm.MaxLogLines+25),
		"timeout_ms": 20000,
	}}
	res := runJob(t, job, nil)
	logs := res.Output["logs"].Inline.([]any)
	// MaxLogLines lines, then the marker as one more delivered line.
	if len(logs) != jsvm.MaxLogLines+1 {
		t.Fatalf("logs = %d rows, want %d lines plus the marker", len(logs), jsvm.MaxLogLines)
	}
	last := logs[len(logs)-1].(map[string]any)["line"].(string)
	if !strings.Contains(last, "stopped after") {
		t.Errorf("last line = %q, want the sandbox's truncation marker", last)
	}
}

func TestPerRowErrorsCanRouteInsteadOfFailingTheStep(t *testing.T) {
	t.Parallel()
	// One malformed row in a feed should not cost the other rows.
	job := core.Job{ID: "t", NodeID: "code_1",
		Params: map[string]any{
			"mode":         "each",
			"on_row_error": "route",
			"code":         "if (row.bad) throw new Error('nope'); return { id: row.id };",
		},
		Input: map[string]core.Ref{"in": {Inline: []any{
			map[string]any{"id": 1},
			map[string]any{"id": 2, "bad": true},
			map[string]any{"id": 3},
		}}},
	}
	res := runJob(t, job, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status = %v error = %+v, want the good rows through", res.Status, res.Error)
	}
	if out := res.Output["out"].Inline.([]any); len(out) != 2 {
		t.Errorf("out = %d rows, want the 2 that worked", len(out))
	}
	failed, ok := res.Output["failed"].Inline.([]any)
	if !ok || len(failed) != 1 {
		t.Fatalf("failed = %v, want the one row that threw", res.Output["failed"].Inline)
	}
	rec := failed[0].(map[string]any)
	if rec["index"] != 1 {
		t.Errorf("index = %v, want the row's position 1", rec["index"])
	}
	if msg, _ := rec["error"].(string); !strings.Contains(msg, "nope") {
		t.Errorf("error = %q, want the script's own message", msg)
	}
	if rec["row"] == nil {
		t.Error("failed row carries no row — nothing to retry or inspect")
	}
}

func TestRoutingEmitsAnEmptyFailedListWhenNothingFailed(t *testing.T) {
	t.Parallel()
	// An edge from an absent port reads as dormant and skips the branch, so
	// "nothing failed" must not look like "the step never ran".
	job := core.Job{ID: "t", NodeID: "code_1",
		Params: map[string]any{"mode": "each", "on_row_error": "route", "code": "return row;"},
		Input:  map[string]core.Ref{"in": {Inline: []any{map[string]any{"id": 1}}}},
	}
	res := runJob(t, job, nil)
	failed, ok := res.Output["failed"].Inline.([]any)
	if !ok {
		t.Fatalf("failed port absent on a clean run: %+v", res.Output)
	}
	if len(failed) != 0 {
		t.Errorf("failed = %v, want an empty list", failed)
	}
}

func TestPerRowStillFailsFastByDefault(t *testing.T) {
	t.Parallel()
	// Routing is opt-in: a script that breaks on row 7 usually breaks on row 8,
	// and existing flows expect the step to stop.
	job := core.Job{ID: "t", NodeID: "code_1",
		Params: map[string]any{"mode": "each", "code": "throw new Error('nope');"},
		Input:  map[string]core.Ref{"in": {Inline: []any{map[string]any{"id": 1}}}},
	}
	if res := runJob(t, job, nil); res.Status != core.StatusError {
		t.Errorf("status = %v, want the step to fail without on_row_error=route", res.Status)
	}
}

func TestPerRowReportsWhatItDidWithTheRows(t *testing.T) {
	t.Parallel()
	// Silent filtering made a script that dropped everything look exactly like
	// one that dropped nothing.
	ch := make(chan core.Progress, 8)
	job := core.Job{ID: "t", NodeID: "code_1",
		Params: map[string]any{"mode": "each", "code": "if (row.id === 2) return; return row;"},
		Input: map[string]core.Ref{"in": {Inline: []any{
			map[string]any{"id": 1}, map[string]any{"id": 2}, map[string]any{"id": 3},
		}}},
	}
	runJob(t, job, ch)
	close(ch)

	var tally string
	for p := range ch {
		if strings.Contains(p.Message, "kept") {
			tally = p.Message
		}
	}
	if tally == "" {
		t.Fatal("no tally reported for a per-row run")
	}
	for _, want := range []string{"3 row", "kept 2", "dropped 1"} {
		if !strings.Contains(tally, want) {
			t.Errorf("tally %q is missing %q", tally, want)
		}
	}
}
