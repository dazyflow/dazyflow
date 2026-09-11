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
	"strings"
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
			// The language's own mark rather than the generic code glyph: on a
			// canvas of thirty steps it is what tells this one from the
			// Expression step at a glance. The lucide icon stays as the
			// fallback for anywhere the asset does not load.
			BrandLogo: "/brands/javascript.svg",
			Category:  "transformation",
			Provider:  "internal",
			Tags: []string{
				"code", "javascript", "js", "script", "function", "custom",
				"transform", "compute", "zapier", "n8n",
			},
			Description: "Write a little JavaScript when the ready-made steps cannot say what you mean. " +
				"The value wired into 'in' arrives as `input`, and whatever you `return` leaves on 'out' — " +
				"an object, a list, a number, a string, a true/false. `console.log(…)` writes to the step's " +
				"console while you watch, and stays there once the run is over — each line tagged with the " +
				"level you printed it at, errors in red and warnings in yellow, and the line of script it came " +
				"from. The same lines leave on 'Log lines', so a flow can mail them or file them.\n\n" +
				"Choose how it runs. 'Once, over everything' hands you the whole input in one go — reach for " +
				"it to reshape a payload, do arithmetic across a list, or build a value no other step can. " +
				"'Once per row' runs your code for each row instead, with the row as `row` and its position " +
				"as `index`, and collects what you return into a new list; return nothing for a row and that " +
				"row is dropped, which makes this a filter as well as a transform. Every row runs in the same " +
				"sandbox, so a value you leave on a global outlives the row that set it — which is how you " +
				"carry a running total from one row to the next.\n\n" +
				"It runs inside Dazyflow with no way out: no network, no files, no libraries to import. " +
				"That is deliberate — call an API with the Web request step and read files with the file " +
				"steps, then wire the result in here. There is no clock to wait on either: no timers, and no " +
				"promises or async/await, so do the work in order and return the value itself rather than a " +
				"promise of one. `Intl` is absent too, which means `toLocaleString('sv-SE')` throws instead " +
				"of formatting — write the format you want, or let the Date & time step do it. Scripts are " +
				"held to a few seconds by default (raise 'Time limit' up to 30 seconds), to 8 MB of returned " +
				"data, and to the first 200 console lines. If you need a real runtime, a library, or a " +
				"network, use 'Run on your machine' instead.\n\n" +
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
					Notes:  "console.log lines show up in the step's console while the flow runs, and stay there afterwards.",
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
				// The script as data. Wire a Text step or a Read file here to
				// keep one script in one place and run it from several flows.
				// Executable marks what that costs: text arriving here is RUN,
				// so a lint rule can object when it arrives from a trigger.
				{Port: "code", Label: "Script", MIME: []string{"text/plain"}, Executable: true},
			},
			Outputs: []core.Port{
				{Port: "out", Label: "Result"},
				// Named ports rather than one port that changes shape: a
				// manifest is static per module (see route_rows), so a port
				// either declares itself a list or it does not.
				{Port: "failed", Label: "Failed rows", MIME: []string{"application/json"}, List: true},
				{Port: "logs", Label: "Log lines", MIME: []string{"application/json"}, List: true},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"code":{"type":"string","format":"script","x_lang":"javascript","title":"JavaScript","description":"The script. 'input' is the value wired into 'in' (or 'row' and 'index' in per-row mode); whatever you return leaves on 'out'. console.log writes to the step's console and to the 'logs' output, each line carrying its level and the line of script that printed it. No network, no files, no imports. Overridden by the 'Script' input."},
					"mode":{"type":"string","enum":["once","each"],"enumNames":["Once, over everything","Once per row"],"default":"once","title":"Run it","description":"Once, with the whole input as 'input' — or once per row, with each row as 'row' and its position as 'index'. Per-row collects what you return into a list, and drops any row you return nothing for. Every row runs in the same sandbox, so a global you set outlives the row that set it — useful for a running total, and worth knowing if you did not mean to."},
					"on_row_error":{"type":"string","enum":["fail","route"],"enumNames":["Fail the step","Send the row to 'Failed rows'"],"default":"fail","title":"If a row's script throws","description":"Per-row mode only. Fail the step on the first row that throws, or keep going and send that row — with its error — out the 'Failed rows' output. Routing lets 199 good rows through when row 7 is malformed."},
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
	src, ok := params.TextInputOr(job, "code", params.StringDefault(job.Params, "code", ""))
	if !ok {
		return params.Err(job, "bad_input", "the 'Script' input must be text"), nil
	}
	if strings.TrimSpace(src) == "" {
		return params.Err(job, "bad_param", "no script to run — type one in 'code' or connect the 'Script' input"), nil
	}
	mode := params.StringDefault(job.Params, "mode", "once")
	if mode != "once" && mode != "each" {
		return params.Err(job, "bad_param", fmt.Sprintf(
			"unknown mode %q (expected \"once\" or \"each\")", mode)), nil
	}
	// One recorder behind both destinations: the live console (a best-effort
	// send that a busy run may drop) and the 'logs' output (kept in full up to
	// the cap). Before this, a dropped line was gone with nothing to say so.
	log := &logRecorder{}
	sandbox, err := jsvm.New(ctx, src, jsvm.Options{
		Timeout: timeout(job),
		Log: func(rec jsvm.LogLine) {
			log.add(rec)
			emitLog(progress, job, rec)
		},
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
		return eachRow(job, sandbox, input, log, progress)
	}
	value, err := sandbox.Eval(map[string]any{"input": input})
	if err != nil {
		return fail(job, err, "", log, hint(err, mode, hasInput(job))), nil
	}
	return emit(job, value, log), nil
}

// hasInput reports whether anything is wired into 'in'. A script reaching into
// an input that was never connected throws a TypeError about `undefined`, which
// blames the script for what is really a missing edge.
func hasInput(job core.Job) bool {
	ref, ok := job.Input["in"]
	return ok && ref.Inline != nil
}

// hint is the sentence that turns a true error into an actionable one. Each
// case is a mistake the step's own shape invites: the two variable names swap
// with the mode, and the input is `undefined` whenever nothing is wired in.
func hint(err error, mode string, wired bool) string {
	msg := err.Error()
	switch {
	case mode == "each" && strings.Contains(msg, "input is not defined"):
		return " — this step is set to \"Once per row\", where the row is `row` and its position is `index`; " +
			"`input` only exists in \"Once, over everything\""
	case mode == "once" && (strings.Contains(msg, "row is not defined") || strings.Contains(msg, "index is not defined")):
		return " — this step is set to \"Once, over everything\", where the whole input is `input`; " +
			"`row` only exists in \"Once per row\""
	case !wired && strings.Contains(msg, "of undefined"):
		return " — nothing is wired into 'Input', so `input` is undefined"
	}
	return ""
}

// eachRow runs the script once per row and collects the results. A row the
// script returns nothing for is dropped — the shape that lets one step filter
// and transform at once.
//
// A row that THROWS is the interesting case, and what happens is the author's
// choice. Failing on the first one is the default because a script that breaks
// on row 7 usually breaks on row 8 too, and 200 identical errors help nobody.
// But "usually" is not "always": one malformed row in a feed should not cost the
// other 199, so on_row_error=route sends the offender out 'failed' with its
// error attached and carries on.
//
// Either way the step reports what it did with the rows. Silent filtering was
// the older behaviour and it made a script that dropped everything look exactly
// like one that dropped nothing.
func eachRow(job core.Job, sandbox *jsvm.Sandbox, input any, log *logRecorder, progress chan<- core.Progress) (core.Result, error) {
	list, err := rows.Normalize(input, rows.Options{Cap: capRows, AllowSingleObject: true})
	if err != nil {
		return params.Err(job, "bad_input", perRowInput(input, err)), nil
	}
	routeErrors := params.StringDefault(job.Params, "on_row_error", "fail") == "route"

	out := make([]any, 0, len(list))
	var failed []any
	dropped := 0
	every := progressEvery(len(list))
	for i, row := range list {
		value, err := sandbox.Eval(map[string]any{"row": row, "index": i})
		if err != nil {
			// A timeout or a cancellation is not this row's fault and not
			// something the next row can survive: the sandbox's clock is spent,
			// so every row after this one would be "failed" too. Routing them
			// would turn one overrun into a step that reports success with
			// every row rejected — and used to let the rest of the rows run
			// with no time limit left to stop them.
			if !routeErrors || errors.Is(err, jsvm.ErrTimeout) || errors.Is(err, jsvm.ErrCancelled) {
				return fail(job, err, atRow(i), log, hint(err, "each", true)), nil
			}
			failed = append(failed, map[string]any{"index": i, "row": row, "error": err.Error()})
			continue
		}
		if value == nil {
			dropped++
			continue
		}
		out = append(out, value)
		// Something to watch on a long list. Without this the console is the
		// only sign of life until the whole thing finishes, and the console
		// stops at 200 lines.
		if every > 0 && (i+1)%every == 0 {
			params.EmitProgress(progress, job, float64(i+1)/float64(len(list)),
				fmt.Sprintf("row %d of %d · kept %d · dropped %d · failed %d",
					i+1, len(list), len(out), dropped, len(failed)))
		}
	}

	params.EmitProgress(progress, job, 1, rowTally(len(list), len(out), dropped, len(failed)))
	res := emit(job, out, log)
	if routeErrors {
		// Declared unconditionally in route mode, empty list included: an edge
		// from a port that is absent reads as dormant and skips the branch, so
		// "no row failed" would silently look the same as "the step never ran".
		res.Output["failed"] = core.Ref{MIME: "application/json", Inline: failedRows(failed)}
	}
	return res, nil
}

// atRow names a row the same way in both places it can be named. The error
// message counts from 1, the way a person counts rows; 'failed' carries the
// 0-based index, the way a list is addressed. Saying both once here is what
// stops "on row 3" and `index: 2` looking like two different rows.
func atRow(i int) string { return fmt.Sprintf(" on row %d (index %d)", i+1, i) }

// progressEvery spaces updates out to about twenty over the whole list: enough
// to see it moving, not so many that the run stream carries more progress than
// work. Zero for a list short enough that the final tally says it all.
func progressEvery(n int) int {
	if n < 40 {
		return 0
	}
	return n / 20
}

// perRowInput explains what "Once per row" needed and did not get. The
// underlying error is a decoder's ("invalid character 'j'"), which describes
// the symptom of a wiring mistake rather than the mistake.
func perRowInput(input any, err error) string {
	switch input.(type) {
	case string:
		return "\"Once per row\" needs a list of rows, and the input is a single piece of text — " +
			"wire a list in, or set \"Run it\" to \"Once, over everything\" and read it as `input`"
	case float64, int, int64, bool:
		return fmt.Sprintf("\"Once per row\" needs a list of rows, and the input is a single %T — "+
			"wire a list in, or set \"Run it\" to \"Once, over everything\" and read it as `input`", input)
	}
	return err.Error()
}

func failedRows(xs []any) []any {
	if xs == nil {
		return []any{}
	}
	return xs
}

// rowTally is the one line the console shows for a per-row run. It names every
// fate a row can meet so the numbers add up in view: a reader who sees
// "kept 187" and nothing else cannot tell 13 filtered from 13 broken.
func rowTally(total, kept, dropped, failed int) string {
	msg := fmt.Sprintf("ran %d row(s) · kept %d · dropped %d", total, kept, dropped)
	if failed > 0 {
		msg += fmt.Sprintf(" · failed %d", failed)
	}
	return msg
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
// where names the row in per-row mode and is empty otherwise; extra carries a
// hint when the step can tell what the author meant.
//
// The console goes out WITH the failure. Print statements are added to a script
// because it is failing, so dropping them on failure emptied the console at the
// one moment it was worth reading — the lines were streamed live and then had
// nowhere to live afterwards.
func fail(job core.Job, err error, where string, log *logRecorder, extra string) core.Result {
	var res core.Result
	switch {
	case errors.Is(err, jsvm.ErrTimeout):
		res = params.Err(job, "timeout", "the script ran past its time limit"+where)
	case errors.Is(err, jsvm.ErrCancelled):
		res = params.Err(job, "cancelled", "the run was cancelled")
	default:
		res = params.Err(job, "eval", err.Error()+where+extra)
	}
	if rows := log.rows(); len(rows) > 0 {
		res.Output = map[string]core.Ref{"logs": {
			MIME:    "application/json",
			Inline:  rows,
			Headers: []string{"index", "line", "level", "message"},
		}}
	}
	return res
}

// emit types the output the way the Expression step does, so a script that
// returns a string or a boolean can be wired straight into the steps that
// expect one.
func emit(job core.Job, value any, log *logRecorder) core.Result {
	mime := "application/json"
	switch value.(type) {
	case string:
		mime = "text/plain"
	case bool:
		mime = core.MIMEBool
	}
	out := map[string]core.Ref{"out": {MIME: mime, Inline: value}}
	// Omitted when the script printed nothing, rather than sent out empty: an
	// empty list is still a value, and the run view shows the first port that
	// has one — so a logs port that is always present makes every silent
	// script preview as "[]" instead of as its result.
	if rows := log.rows(); len(rows) > 0 {
		out["logs"] = core.Ref{
			MIME:    "application/json",
			Inline:  rows,
			Headers: []string{"index", "line", "level", "message"},
		}
	}
	return core.Result{JobID: job.ID, Status: core.StatusOK, Output: out}
}

// logRecorder keeps what console.log said, so the lines survive past the live
// stream that may drop them and past the run that produced them.
//
// It does NOT cap: jsvm.MaxLogLines already stops the sandbox's console at 200
// and delivers its own "… console output stopped after N lines" through this
// same sink, so a second ceiling here could only ever be unreachable — and a
// second marker would contradict the first about where the log ends.
//
// Not concurrency-guarded: the sandbox evaluates on the calling goroutine, one
// row at a time, and its Log callback runs inside that evaluation.
type logRecorder struct {
	lines []jsvm.LogLine
}

func (l *logRecorder) add(rec jsvm.LogLine) { l.lines = append(l.lines, rec) }

// rows renders the log as a table: the position it was printed at, the line of
// script that printed it, how loudly, and what it said. Position and script
// line are not the same number and neither replaces the other — one script line
// inside a loop prints a hundred times, and `index` is what keeps those hundred
// in order once the rows travel on into a filter or a sort.
func (l *logRecorder) rows() []any {
	out := make([]any, 0, len(l.lines))
	for i, rec := range l.lines {
		out = append(out, map[string]any{
			"index":   i,
			"line":    rec.Line,
			"level":   rec.Level,
			"message": rec.Message,
		})
	}
	return out
}

func emitLog(ch chan<- core.Progress, job core.Job, rec jsvm.LogLine) {
	if ch == nil {
		return
	}
	select {
	case ch <- core.Progress{
		JobID:   job.ID,
		NodeID:  job.NodeID,
		Message: rec.Message,
		Data: map[string]any{
			"stream":  logStream(rec.Level),
			"line":    rec.Message,
			"level":   rec.Level,
			"at_line": rec.Line,
		},
	}:
	default:
	}
}

// logStream maps a console method onto the two streams the run log knows, the
// way a terminal would: the run view marks stderr, so console.error stands out
// there without the viewer learning a second vocabulary.
func logStream(level string) string {
	switch level {
	case "error", "warn":
		return "stderr"
	default:
		return "stdout"
	}
}
