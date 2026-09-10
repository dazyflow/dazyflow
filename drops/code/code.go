// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package code is the escape hatch between a one-line formula and a machine
// you host: a JavaScript step that runs in the daemon, with no I/O of any
// kind. The sandbox lives in internal/jsvm; this file is the manifest, the
// two execution shapes, and the mapping from a sandbox failure onto an error
// code the run view can explain.
package code

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/internal/limits"
	"github.com/dazyflow/dazyflow/drops/internal/params"
	"github.com/dazyflow/dazyflow/drops/internal/rows"
	"github.com/dazyflow/dazyflow/engine"
	"github.com/dazyflow/dazyflow/internal/jsvm"
)

const (
	defaultTimeoutMS = 5_000
	maxTimeoutMS     = 30_000
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:       "code",
			Version:  "1.0",
			Label:    "Code",
			Subtitle: "Run a bit of JavaScript",
			Icon:     "code",
			Category: "transformation",
			Provider: "internal",
			Tags: []string{
				"code", "javascript", "js", "script", "function", "custom",
				"transform", "compute", "zapier", "n8n",
			},
			Description: "Write a little JavaScript when the ready-made steps cannot say what you mean. " +
				"The value wired into 'in' arrives as `input`, and whatever you `return` leaves on 'out' — " +
				"an object, a list, a number, a string, a true/false. `console.log(…)` writes to the run log, " +
				"so you can see what your code saw.\n\n" +
				"Choose how it runs. 'Once, over everything' hands you the whole input in one go — reach for " +
				"it to reshape a payload, do arithmetic across a list, or build a value no other step can. " +
				"'Once per row' runs your code for each row instead, with the row as `row` and its position " +
				"as `index`, and collects what you return into a new list; return nothing for a row and that " +
				"row is dropped, which makes this a filter as well as a transform.\n\n" +
				"It runs inside Dazyflow with no way out: no network, no files, no libraries to import. " +
				"That is deliberate — call an API with the Web request step and read files with the file " +
				"steps, then wire the result in here. Scripts are held to a few seconds by default (raise " +
				"'Time limit' up to 30 seconds) and to 8 MB of returned data. If you need a real runtime, a " +
				"library, or a network, use 'Run on your machine' instead.\n\n" +
				"For a single value — a bit of arithmetic, one field pulled out — the Expression step is " +
				"lighter, and 'Add a calculated column' does the same per row without any code at all.",
			Summary: "Run sandboxed JavaScript over the input, once or per row, and emit what it returns.",
			Examples: []core.ParamsExample{
				{
					Title:  "Reshape a payload",
					Params: json.RawMessage(`{"code":"return { name: input.first + ' ' + input.last, paid: input.total > 0 };"}`),
					Notes:  "The whole input arrives as `input`; the returned object leaves on 'out'.",
				},
				{
					Title:  "Keep the rows you want, and add a column",
					Params: json.RawMessage(`{"mode":"each","code":"if (row.total <= 100) return;\nreturn { ...row, vat: row.total * 0.25 };"}`),
					Notes:  "Per-row mode: returning nothing drops the row, so this filters and computes in one step.",
				},
				{
					Title:  "Summarize a list into one value",
					Params: json.RawMessage(`{"code":"const total = input.reduce((sum, r) => sum + r.amount, 0);\nconsole.log('rows', input.length);\nreturn { total, average: total / input.length };"}`),
					Notes:  "console.log lines show up in the run log while the flow runs.",
				},
				{
					Title:  "Give a slow script more time",
					Params: json.RawMessage(`{"code":"return input.map(r => r.id);","timeout_ms":15000}`),
					Notes:  "The limit covers the whole step, per-row runs included. 30 seconds is the ceiling.",
				},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				{Port: "in", Label: "Input"},
			},
			Outputs: []core.Port{
				{Port: "out", Label: "Result"},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"code":{"type":"string","format":"script","x_lang":"javascript","title":"JavaScript","description":"The script. 'input' is the value wired into 'in' (or 'row' and 'index' in per-row mode); whatever you return leaves on 'out'. console.log writes to the run log. No network, no files, no imports."},
					"mode":{"type":"string","enum":["once","each"],"enumNames":["Once, over everything","Once per row"],"default":"once","title":"Run it","description":"Once, with the whole input as 'input' — or once per row, with each row as 'row' and its position as 'index'. Per-row collects what you return into a list, and drops any row you return nothing for."},
					"timeout_ms":{"type":"integer","default":5000,"minimum":100,"maximum":30000,"title":"Time limit (ms)","description":"How long the script may run in total, per-row runs included. Past it the step fails rather than holding the flow up. Maximum 30000."}
				},
				"required":["code"]
			}`),
			Idempotent: true,
		},
		Execute: executeCode,
	})
}

func executeCode(ctx context.Context, job core.Job, progress chan<- core.Progress) (core.Result, error) {
	src, err := params.String(job.Params, "code")
	if err != nil {
		return params.Err(job, "bad_param", "param 'code' is required (the JavaScript to run)"), nil
	}
	mode := params.StringDefault(job.Params, "mode", "once")
	if mode != "once" && mode != "each" {
		return params.Err(job, "bad_param", fmt.Sprintf(
			"unknown mode %q (expected \"once\" or \"each\")", mode)), nil
	}
	sandbox, err := jsvm.New(ctx, src, jsvm.Options{
		Timeout: timeout(job),
		Log:     func(line string) { emitLog(progress, job, line) },
	})
	if err != nil {
		return params.Err(job, "bad_param", err.Error()), nil
	}
	defer sandbox.Close()

	var input any
	if ref, ok := job.Input["in"]; ok {
		input = ref.Inline
	}

	if mode == "each" {
		return eachRow(job, sandbox, input)
	}
	value, err := sandbox.Eval(map[string]any{"input": input})
	if err != nil {
		return fail(job, err, ""), nil
	}
	return emit(job, value), nil
}

// eachRow runs the script once per row and collects the results. A row the
// script returns nothing for is dropped — the shape that lets one step filter
// and transform at once — and the first row that errors fails the step,
// naming itself, since a script that breaks on row 7 will break on row 8.
func eachRow(job core.Job, sandbox *jsvm.Sandbox, input any) (core.Result, error) {
	list, err := rows.Normalize(input, rows.Options{Cap: capRows, AllowSingleObject: true})
	if err != nil {
		return params.Err(job, "bad_input", err.Error()), nil
	}
	out := make([]any, 0, len(list))
	for i, row := range list {
		value, err := sandbox.Eval(map[string]any{"row": row, "index": i})
		if err != nil {
			return fail(job, err, fmt.Sprintf(" on row %d", i+1)), nil
		}
		if value == nil {
			continue
		}
		out = append(out, value)
	}
	return emit(job, out), nil
}

// timeout is the author's time limit, held inside a floor and a ceiling: a
// graph asking for ten minutes of a worker gets thirty seconds.
func timeout(job core.Job) time.Duration {
	ms := params.ClampInt(params.TimeoutMS(job, defaultTimeoutMS), 100, maxTimeoutMS)
	return time.Duration(ms) * time.Millisecond
}

func capRows(n int) error {
	if max := limits.MaxRows(); n > max {
		return fmt.Errorf("%d rows exceeds the %d-row limit", n, max)
	}
	return nil
}

// fail maps a sandbox failure onto the error codes the run view explains.
// where names the row in per-row mode and is empty otherwise.
func fail(job core.Job, err error, where string) core.Result {
	switch {
	case errors.Is(err, jsvm.ErrTimeout):
		return params.Err(job, "timeout", "the script ran past its time limit"+where)
	case errors.Is(err, jsvm.ErrCancelled):
		return params.Err(job, "cancelled", "the run was cancelled")
	default:
		return params.Err(job, "eval", err.Error()+where)
	}
}

// emit types the output the way the Expression step does, so a script that
// returns a string or a boolean can be wired straight into the steps that
// expect one.
func emit(job core.Job, value any) core.Result {
	mime := "application/json"
	switch value.(type) {
	case string:
		mime = "text/plain"
	case bool:
		mime = core.MIMEBool
	}
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"out": {MIME: mime, Inline: value},
		},
	}
}

func emitLog(ch chan<- core.Progress, job core.Job, line string) {
	if ch == nil {
		return
	}
	select {
	case ch <- core.Progress{
		JobID:   job.ID,
		NodeID:  job.NodeID,
		Message: line,
		Data:    map[string]any{"stream": "stdout", "line": line},
	}:
	default:
	}
}
