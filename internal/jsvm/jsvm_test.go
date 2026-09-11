// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package jsvm

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func run(t *testing.T, code string, vars map[string]any, opt Options) (any, error) {
	t.Helper()
	if opt.Timeout == 0 {
		opt.Timeout = 2 * time.Second
	}
	s, err := New(t.Context(), code, opt)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	return s.Eval(vars)
}

// The whole safety claim in one assertion: the global object carries no way
// out of the process. If a goja upgrade ever starts pre-installing one of
// these, this test is the alarm.
func TestSandbox_HasNoWayOut(t *testing.T) {
	v, err := run(t, `return [
		typeof fetch, typeof require, typeof process, typeof XMLHttpRequest,
		typeof setTimeout, typeof setInterval, typeof WebAssembly,
		typeof globalThis.Go, typeof module, typeof exports,
	].join(",")`, nil, Options{})
	if err != nil {
		t.Fatalf("eval: %v", err)
	}
	for i, kind := range strings.Split(v.(string), ",") {
		if kind != "undefined" {
			t.Errorf("global #%d is %q, want undefined — the sandbox grew a door", i, kind)
		}
	}
}

// A top-level `return` has to work: it is what every Zapier and n8n snippet
// an author might paste is written around.
func TestEval_TopLevelReturn(t *testing.T) {
	v, err := run(t, `const rows = input.rows.filter(r => r.total > 100);
		return { rows, count: rows.length };`,
		map[string]any{"input": map[string]any{"rows": []any{
			map[string]any{"total": 50},
			map[string]any{"total": 150},
		}}}, Options{})
	if err != nil {
		t.Fatalf("eval: %v", err)
	}
	got, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("result is %T, want an object", v)
	}
	if got["count"] != int64(1) {
		t.Errorf("count = %v (%T), want 1", got["count"], got["count"])
	}
}

func TestEval_NoReturnIsNil(t *testing.T) {
	for _, code := range []string{`1 + 1;`, `return;`, `return null;`, `return undefined;`} {
		v, err := run(t, code, nil, Options{})
		if err != nil || v != nil {
			t.Errorf("%q: (%v, %v), want (nil, nil)", code, v, err)
		}
	}
}

// An integer must survive as an integer. The result is validated by encoding
// it to JSON, and a round-trip through that encoding would silently turn every
// id in a payload into a float.
func TestEval_KeepsIntegers(t *testing.T) {
	v, err := run(t, `return { id: 9007199254740993 - 1, ratio: 0.5 };`, nil, Options{})
	if err != nil {
		t.Fatalf("eval: %v", err)
	}
	got := v.(map[string]any)
	if _, ok := got["id"].(int64); !ok {
		t.Errorf("id is %T, want int64", got["id"])
	}
	if _, ok := got["ratio"].(float64); !ok {
		t.Errorf("ratio is %T, want float64", got["ratio"])
	}
}

func TestEval_TimeoutStopsARunawayLoop(t *testing.T) {
	start := time.Now()
	_, err := run(t, `while (true) {}`, nil, Options{Timeout: 150 * time.Millisecond})
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("err = %v, want ErrTimeout", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("took %v to interrupt — the watchdog is not landing", elapsed)
	}
}

// The deadline belongs to the sandbox, not to one Eval: a per-row script
// cannot buy more budget by being individually quick.
func TestEval_TimeoutSpansEveryEval(t *testing.T) {
	s, err := New(t.Context(), `let n = 0; for (let i = 0; i < 400000; i++) n += i; return n;`,
		Options{Timeout: 120 * time.Millisecond})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer s.Close()
	for i := range 1000 {
		if _, err := s.Eval(nil); err != nil {
			if !errors.Is(err, ErrTimeout) {
				t.Fatalf("eval %d: err = %v, want ErrTimeout", i, err)
			}
			return
		}
	}
	t.Fatal("1000 evals never hit the shared deadline")
}

func TestEval_CancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	s, err := New(ctx, `while (true) {}`, Options{Timeout: 10 * time.Second})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer s.Close()
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	if _, err := s.Eval(nil); !errors.Is(err, ErrCancelled) {
		t.Fatalf("err = %v, want ErrCancelled", err)
	}
}

func TestEval_ThrowIsReportedWithoutAStack(t *testing.T) {
	_, err := run(t, `throw new Error("no good");`, nil, Options{})
	if err == nil || !strings.Contains(err.Error(), "no good") {
		t.Fatalf("err = %v, want the thrown message", err)
	}
	if strings.Contains(err.Error(), "\n") {
		t.Errorf("err spans lines, so a JS stack leaked into the node error: %q", err)
	}
}

func TestEval_RejectsWhatIsNotData(t *testing.T) {
	for name, code := range map[string]string{
		"function": `return function () {};`,
		"cycle":    `const a = {}; a.self = a; return a;`,
	} {
		if _, err := run(t, code, nil, Options{}); err == nil {
			t.Errorf("%s: returned no error", name)
		}
	}
}

func TestEval_ResultSizeIsCapped(t *testing.T) {
	_, err := run(t, `return "x".repeat(9 * 1024 * 1024);`, nil, Options{Timeout: 5 * time.Second})
	if err == nil || !strings.Contains(err.Error(), "bytes") {
		t.Fatalf("err = %v, want a size-limit error", err)
	}
}

func TestEval_DeepRecursionIsCatchable(t *testing.T) {
	_, err := run(t, `function f(n) { return f(n + 1); } return f(0);`, nil, Options{})
	if err == nil || !strings.Contains(err.Error(), "deeply") {
		t.Fatalf("err = %v, want the recursion message", err)
	}
}

func TestConsole_LogsAndStopsAtTheCap(t *testing.T) {
	var lines []LogLine
	_, err := run(t, `console.log("plain", { a: 1 }, null);
		for (let i = 0; i < 500; i++) console.log(i);
		return 1;`,
		nil, Options{Log: func(l LogLine) { lines = append(lines, l) }})
	if err != nil {
		t.Fatalf("eval: %v", err)
	}
	if len(lines) == 0 || lines[0].Message != `plain {"a":1} null` {
		t.Fatalf("first line = %+v, want the formatted arguments", lines)
	}
	if len(lines) > MaxLogLines+1 {
		t.Errorf("%d lines, want the cap to hold at %d", len(lines), MaxLogLines)
	}
	last := lines[len(lines)-1]
	if !strings.Contains(last.Message, "stopped after") {
		t.Errorf("last line = %q, want the truncation notice", last.Message)
	}
	// The sandbox's own notice, not the script's: loud, and from no line.
	if last.Level != "warn" || last.Line != 0 {
		t.Errorf("truncation notice = %+v, want warn from line 0", last)
	}
}

// Which console method the script called is the only place it says how much it
// meant by a line, and the line number is how an author finds the print they
// are reading. Both used to be thrown away before the drop could see them.
func TestConsole_CarriesTheLevelAndTheScriptLine(t *testing.T) {
	var lines []LogLine
	_, err := run(t, `console.log("one");
console.warn("two");
function deeper() { console.error("three"); }
deeper();
return 1;`, nil, Options{Log: func(l LogLine) { lines = append(lines, l) }})
	if err != nil {
		t.Fatalf("eval: %v", err)
	}
	want := []LogLine{
		{Level: "log", Line: 1, Message: "one"},
		{Level: "warn", Line: 2, Message: "two"},
		// Reported where the call sits, not where the function was called
		// from — that is the line the author is looking at in their editor.
		{Level: "error", Line: 3, Message: "three"},
	}
	if len(lines) != len(want) {
		t.Fatalf("lines = %+v, want %d", lines, len(want))
	}
	for i, w := range want {
		if lines[i] != w {
			t.Errorf("line %d = %+v, want %+v", i, lines[i], w)
		}
	}
}

func TestNew_RejectsEmptyOversizedAndUnparsable(t *testing.T) {
	for name, code := range map[string]string{
		"empty":    "   ",
		"too long": strings.Repeat("x", MaxCodeLen+1),
		"syntax":   "return (",
	} {
		if _, err := New(t.Context(), code, Options{}); err == nil {
			t.Errorf("%s: compiled without complaint", name)
		}
	}
}
