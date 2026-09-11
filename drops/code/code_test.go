// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package code

import (
	"strings"
	"testing"
	"time"

	"github.com/dazyflow/dazyflow/core"
)

func run(t *testing.T, p map[string]any, in any) core.Result {
	t.Helper()
	job := core.Job{ID: "test", NodeID: "code_1", Params: p}
	if in != nil {
		job.Input = map[string]core.Ref{"in": {Inline: in}}
	}
	res, err := executeCode(t.Context(), job, nil)
	if err != nil {
		t.Fatalf("executeCode returned error: %v", err)
	}
	return res
}

func ok(t *testing.T, res core.Result) any {
	t.Helper()
	if res.Status != core.StatusOK {
		t.Fatalf("status = %v, error = %+v", res.Status, res.Error)
	}
	return res.Output["out"].Inline
}

func failsWith(t *testing.T, res core.Result, code string) {
	t.Helper()
	if res.Status != core.StatusError {
		t.Fatalf("status = %v, want an error", res.Status)
	}
	if res.Error.Code != code {
		t.Fatalf("error code = %q (%s), want %q", res.Error.Code, res.Error.Message, code)
	}
}

func TestOnce_ReshapesTheInput(t *testing.T) {
	got := ok(t, run(t, map[string]any{
		"code": `return { name: input.first + " " + input.last, paid: input.total > 0 };`,
	}, map[string]any{"first": "Ada", "last": "Lovelace", "total": int64(120)}))

	obj, isObject := got.(map[string]any)
	if !isObject {
		t.Fatalf("result is %T, want an object", got)
	}
	if obj["name"] != "Ada Lovelace" || obj["paid"] != true {
		t.Errorf("result = %v", obj)
	}
}

// A script returning a string or a boolean has to be wirable straight into
// the steps that expect one, so the MIME follows the value like Expression's.
func TestOnce_TypesTheOutputByValue(t *testing.T) {
	for _, tc := range []struct {
		code string
		mime string
	}{
		{`return "hello";`, "text/plain"},
		{`return input.total > 100;`, core.MIMEBool},
		{`return { a: 1 };`, "application/json"},
		{`return [1, 2];`, "application/json"},
	} {
		res := run(t, map[string]any{"code": tc.code}, map[string]any{"total": int64(1)})
		if res.Status != core.StatusOK {
			t.Fatalf("%s: %+v", tc.code, res.Error)
		}
		if got := res.Output["out"].MIME; got != tc.mime {
			t.Errorf("%s: MIME = %q, want %q", tc.code, got, tc.mime)
		}
	}
}

func TestEach_FiltersAndTransformsInOneStep(t *testing.T) {
	got := ok(t, run(t, map[string]any{
		"mode": "each",
		"code": "if (row.total <= 100) return;\nreturn { id: row.id, vat: row.total * 0.25, at: index };",
	}, []any{
		map[string]any{"id": "a", "total": int64(50)},
		map[string]any{"id": "b", "total": int64(200)},
		map[string]any{"id": "c", "total": int64(400)},
	}))

	list, isList := got.([]any)
	if !isList {
		t.Fatalf("result is %T, want a list", got)
	}
	if len(list) != 2 {
		t.Fatalf("kept %d rows, want 2 (the row under the threshold is dropped)", len(list))
	}
	first := list[0].(map[string]any)
	if first["id"] != "b" || first["vat"] != int64(50) {
		t.Errorf("first row = %v", first)
	}
	// index is the position in the INPUT, so a dropped row still advances it.
	if first["at"] != int64(1) {
		t.Errorf("index = %v, want 1", first["at"])
	}
}

// A webhook or form body arrives as a bare object; per-row mode reads that as
// a one-row list, the same as every other row-shaped step.
func TestEach_TakesASingleObjectAsOneRow(t *testing.T) {
	got := ok(t, run(t, map[string]any{
		"mode": "each",
		"code": `return { name: row.name.toUpperCase() };`,
	}, map[string]any{"name": "ada"}))

	list := got.([]any)
	if len(list) != 1 || list[0].(map[string]any)["name"] != "ADA" {
		t.Errorf("result = %v", list)
	}
}

func TestEach_EmptyInputIsAnEmptyList(t *testing.T) {
	got := ok(t, run(t, map[string]any{"mode": "each", "code": `return row;`}, nil))
	if list, isList := got.([]any); !isList || len(list) != 0 {
		t.Errorf("result = %#v, want an empty list", got)
	}
}

func TestEach_NamesTheRowThatFailed(t *testing.T) {
	res := run(t, map[string]any{
		"mode": "each",
		"code": `if (index === 1) throw new Error("bad row"); return row;`,
	}, []any{
		map[string]any{"id": "a"},
		map[string]any{"id": "b"},
	})
	failsWith(t, res, "eval")
	if want := "on row 2"; !strings.Contains(res.Error.Message, want) {
		t.Errorf("message = %q, want it to name the row (%q)", res.Error.Message, want)
	}
}

func TestConsoleLogReachesTheRunLog(t *testing.T) {
	progress := make(chan core.Progress, 8)
	job := core.Job{ID: "test", NodeID: "code_1", Params: map[string]any{
		"code": `console.log("saw", input); return 1;`,
	}, Input: map[string]core.Ref{"in": {Inline: "x"}}}

	if _, err := executeCode(t.Context(), job, progress); err != nil {
		t.Fatalf("executeCode: %v", err)
	}
	close(progress)

	var lines []string
	for p := range progress {
		if p.Data["stream"] == "stdout" {
			lines = append(lines, p.Data["line"].(string))
		}
	}
	if len(lines) != 1 || lines[0] != "saw x" {
		t.Errorf("run log = %q, want one line \"saw x\"", lines)
	}
}

func TestFailures(t *testing.T) {
	for name, tc := range map[string]struct {
		params map[string]any
		code   string
	}{
		"no code":       {map[string]any{}, "bad_param"},
		"empty code":    {map[string]any{"code": "  "}, "bad_param"},
		"syntax error":  {map[string]any{"code": "return ("}, "bad_param"},
		"unknown mode":  {map[string]any{"code": "return 1;", "mode": "twice"}, "bad_param"},
		"script throws": {map[string]any{"code": `throw new Error("nope");`}, "eval"},
		"not data":      {map[string]any{"code": `return function () {};`}, "eval"},
		"runs forever":  {map[string]any{"code": `while (true) {}`, "timeout_ms": 150}, "timeout"},
	} {
		t.Run(name, func(t *testing.T) { failsWith(t, run(t, tc.params, nil), tc.code) })
	}
}

// The time limit is the flow author's, but it is not unbounded: a graph that
// asks for ten minutes of a worker gets the ceiling instead.
func TestTimeoutIsClamped(t *testing.T) {
	for ms, want := range map[any]time.Duration{
		nil:      defaultTimeoutMS * time.Millisecond,
		600000:   maxTimeoutMS * time.Millisecond,
		50:       100 * time.Millisecond,
		12000:    12 * time.Second,
		"twenty": defaultTimeoutMS * time.Millisecond,
	} {
		p := map[string]any{"code": "return 1;"}
		if ms != nil {
			p["timeout_ms"] = ms
		}
		if got := timeout(core.Job{Params: p}); got != want {
			t.Errorf("timeout_ms=%v → %v, want %v", ms, got, want)
		}
	}
}

// --- Fixes found in QA. Each of these shipped broken once. ---

// A timeout is not a row's fault. Routing one would report success with the
// offending row rejected — and, because the watchdog fires once, let every row
// after it run with no limit left to stop it. Measured at 9.5 seconds under a
// 200ms limit before this.
func TestEach_TimeoutIsNeverRoutedAsARowFailure(t *testing.T) {
	rows := []any{
		map[string]any{"n": 0}, map[string]any{"n": 1},
		map[string]any{"n": 2}, map[string]any{"n": 3},
	}
	start := time.Now()
	res := run(t, map[string]any{
		"mode": "each", "on_row_error": "route", "timeout_ms": 200,
		"code": `if (row.n === 0) { for (;;) {} } return row;`,
	}, rows)

	failsWith(t, res, "timeout")
	if !strings.Contains(res.Error.Message, "row 1") {
		t.Errorf("error = %q, want the row named", res.Error.Message)
	}
	// The rows after the overrun must not have run at all.
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("step took %v under a 200ms limit — the deadline stopped being enforced", elapsed)
	}
}

// The sandbox's clock is spent once. Anything asked of it afterwards has to
// fail the same way rather than run unmeasured.
func TestEach_RowsAfterATimeoutDoNotRunUnbounded(t *testing.T) {
	rows := []any{map[string]any{"n": 0}, map[string]any{"n": 1}, map[string]any{"n": 2}}
	start := time.Now()
	res := run(t, map[string]any{
		"mode": "each", "on_row_error": "route", "timeout_ms": 150,
		"code": `if (row.n === 0) { for (;;) {} }
let x = 0; for (let i = 0; i < 40000000; i++) x += i; return { x: x };`,
	}, rows)
	failsWith(t, res, "timeout")
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("step took %v — later rows ran without a deadline", elapsed)
	}
}

// An async function or a .then() chain hands back a Promise, which exported as
// an empty object: the step succeeded and passed `{}` on, silently.
func TestOnce_APromiseIsRefusedRatherThanEmptied(t *testing.T) {
	for name, code := range map[string]string{
		"async function": "async function main() { return { total: 42 }; }\nreturn main();",
		"then chain":     `return Promise.resolve([1, 2, 3]).then(a => a.length);`,
		"bare promise":   `return Promise.resolve(5);`,
	} {
		t.Run(name, func(t *testing.T) {
			res := run(t, map[string]any{"code": code}, nil)
			failsWith(t, res, "eval")
			if !strings.Contains(res.Error.Message, "Promise") {
				t.Errorf("error = %q, want it to name the Promise", res.Error.Message)
			}
		})
	}
}

// Print statements are added to a script BECAUSE it is failing, so the console
// has to survive the failure that prompted it.
func TestFailure_KeepsTheConsole(t *testing.T) {
	res := run(t, map[string]any{
		"code": "console.log('checkpoint 1');\nconsole.warn('about to break');\nthrow new Error('boom');",
	}, nil)
	failsWith(t, res, "eval")
	logs, ok := res.Output["logs"].Inline.([]any)
	if !ok || len(logs) != 2 {
		t.Fatalf("logs on failure = %#v, want the two lines printed before the throw", res.Output["logs"].Inline)
	}
	first, _ := logs[0].(map[string]any)
	if first["message"] != "checkpoint 1" || first["line"] != 1 {
		t.Errorf("first line = %v", first)
	}
}

// "on row 3" and `index: 2` are the same row. Saying only one of them left a
// reader filtering the failed list on the wrong number.
func TestEach_NamesTheRowTheSameWayInBothPlaces(t *testing.T) {
	rows := []any{map[string]any{"ok": true}, map[string]any{"bad": true}}
	code := `if (row.bad) throw new Error('nope'); return row;`

	failing := run(t, map[string]any{"mode": "each", "code": code}, rows)
	failsWith(t, failing, "eval")
	if !strings.Contains(failing.Error.Message, "row 2 (index 1)") {
		t.Errorf("error = %q, want both numberings", failing.Error.Message)
	}

	routed := run(t, map[string]any{"mode": "each", "on_row_error": "route", "code": code}, rows)
	failed, _ := routed.Output["failed"].Inline.([]any)
	if len(failed) != 1 {
		t.Fatalf("failed = %#v", routed.Output["failed"].Inline)
	}
	if got := failed[0].(map[string]any)["index"]; got != 1 {
		t.Errorf("failed index = %v, want 1 — the row the message called 'row 2'", got)
	}
}

// The two variable names swap with the mode, which makes using the wrong one
// the most likely mistake this step invites.
func TestErrors_PointAtTheModeWhenTheVariableIsWrong(t *testing.T) {
	each := run(t, map[string]any{"mode": "each", "code": `return input.length;`},
		[]any{map[string]any{"v": 1}})
	failsWith(t, each, "eval")
	if !strings.Contains(each.Error.Message, "Once per row") {
		t.Errorf("error = %q, want the mode named", each.Error.Message)
	}

	once := run(t, map[string]any{"code": `return row.v;`}, []any{map[string]any{"v": 1}})
	failsWith(t, once, "eval")
	if !strings.Contains(once.Error.Message, "Once, over everything") {
		t.Errorf("error = %q, want the mode named", once.Error.Message)
	}
}

// A TypeError about `undefined` blamed the script for a missing edge.
func TestErrors_SayWhenNothingIsWiredIn(t *testing.T) {
	res := run(t, map[string]any{"code": `return input.name;`}, nil)
	failsWith(t, res, "eval")
	if !strings.Contains(res.Error.Message, "wired into 'Input'") {
		t.Errorf("error = %q, want the unwired input named", res.Error.Message)
	}
}

// A decoder's complaint about a stray character described the symptom of a
// wiring mistake rather than the mistake.
func TestEach_ExplainsWhatAListMeans(t *testing.T) {
	res := run(t, map[string]any{"mode": "each", "code": `return row;`}, "just a string")
	failsWith(t, res, "bad_input")
	for _, want := range []string{"needs a list of rows", "Once, over everything"} {
		if !strings.Contains(res.Error.Message, want) {
			t.Errorf("error = %q, missing %q", res.Error.Message, want)
		}
	}
}

// Rows share a sandbox, so a global outlives the row that set it. That is a
// documented affordance now — a running total across rows — and this pins it,
// because it is equally a way to leak state by accident.
func TestEach_GlobalsSurviveFromRowToRow(t *testing.T) {
	got := ok(t, run(t, map[string]any{
		"mode": "each",
		"code": `if (typeof running === 'undefined') { running = 0; }
running += row.v;
return { running: running };`,
	}, []any{
		map[string]any{"v": 1}, map[string]any{"v": 2}, map[string]any{"v": 3},
	}))
	list, _ := got.([]any)
	if len(list) != 3 {
		t.Fatalf("rows out = %#v", got)
	}
	last, _ := list[2].(map[string]any)
	if last["running"] != int64(6) && last["running"] != 6.0 {
		t.Errorf("running total = %v (%T), want 6", last["running"], last["running"])
	}
}
