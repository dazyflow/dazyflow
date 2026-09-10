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
