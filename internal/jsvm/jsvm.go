// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package jsvm runs a flow author's JavaScript inside the daemon's own
// process, with nothing to reach out of it.
//
// The sandbox is the absence of a bridge rather than a wall around one: goja
// starts with a bare ECMAScript global object — no require, no fetch, no
// process, no timers, no filesystem — and this package adds exactly one thing
// back, a console that writes to the run log. A script therefore cannot do
// I/O of any kind; talking to the world stays the job of the steps built for
// it, which is what keeps a code step safe to offer on a multi-tenant
// deployment where drops/shell is (rightly) switched off.
//
// What is bounded, and what is not:
//
//	Wall clock is bounded. A watchdog goroutine interrupts the runtime when
//	the deadline passes or the job's context is cancelled, so a `while(true)`
//	stops at an instruction boundary rather than pinning a worker.
//
//	Result size is bounded, and the value must be data: the return value is
//	JSON-encoded through a capped writer, so a function, a cycle or a
//	gigabyte of string fails the step instead of the process.
//
//	Memory during the run is NOT bounded — goja exposes no heap ceiling, so a
//	script that allocates hard can still make the daemon sweat until its
//	deadline expires. The timeout is the backstop; keep the default short.
//	A regexp needing backtracking runs under dlclark/regexp2, which does not
//	check the interrupt flag, so a catastrophic pattern can outlive its
//	deadline. Both are the price of in-process execution; a runner (or the
//	opt-in shell drop) is the answer for work that deserves a real cage.
package jsvm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/dop251/goja"
)

const (
	// MaxCodeLen caps the script itself. Far above any plausible step —
	// past this the author wants a runner, not a text box.
	MaxCodeLen = 64 << 10
	// MaxResultBytes caps what the script may hand back, measured as
	// encoded JSON so the ceiling means the same thing as every other
	// payload the engine moves.
	MaxResultBytes = 8 << 20
	// MaxLogLines caps console output per sandbox. A loop that logs every
	// iteration would otherwise flood the run stream and the run record.
	MaxLogLines = 200
	// maxCallStack turns runaway recursion into a catchable JS error
	// rather than a Go stack overflow, which no recover() can save.
	maxCallStack = 512
	// scriptName labels the compiled program. It is how a stack frame from
	// the author's script is told apart from the console's own native frame.
	scriptName = "script.js"
)

var (
	// ErrTimeout and ErrCancelled are the two ways a run ends without the
	// script's consent; the drop maps them onto distinct error codes so a
	// user can tell "too slow" from "the flow was stopped".
	ErrTimeout   = errors.New("the script ran past its time limit")
	ErrCancelled = errors.New("the run was cancelled")
)

// LogLine is one console call, kept apart rather than flattened into a string:
// a reader hunting a failure wants the errors, and a reader hunting a value
// wants the line of script that printed it. Both are lost the moment the three
// become one piece of text.
type LogLine struct {
	// Level is the console method the script called — log, info, warn, error
	// or debug.
	Level string
	// Line is where in the author's own script the call sits, 1-based. 0 when
	// the sandbox itself speaks rather than the script.
	Line int
	// Message is the rendered arguments, joined by a space.
	Message string
}

// Options configures one sandbox. Timeout covers the sandbox's whole life —
// every Eval together — so a per-row script cannot buy more budget by being
// individually quick.
type Options struct {
	Timeout time.Duration
	// Log receives each console call. Optional.
	Log func(LogLine)
}

type Sandbox struct {
	rt   *goja.Runtime
	prog *goja.Program
	stop chan struct{}
	// why records the reason for an interrupt, since goja reports only that
	// one happened.
	why  atomic.Value
	logs int
}

// New compiles code and arms the sandbox's deadline. The script is wrapped in
// an immediately-invoked function so a top-level `return` means what an author
// coming from Zapier or n8n expects; the wrapper opens on the script's own
// first line so reported line numbers still match what they typed.
func New(ctx context.Context, code string, opt Options) (*Sandbox, error) {
	if strings.TrimSpace(code) == "" {
		return nil, errors.New("the script is empty")
	}
	if len(code) > MaxCodeLen {
		return nil, fmt.Errorf("the script is %d characters; the limit is %d", len(code), MaxCodeLen)
	}
	prog, err := goja.Compile(scriptName, "(function(){"+code+"\n})()", false)
	if err != nil {
		return nil, fmt.Errorf("the script does not parse: %s", cleanErr(err))
	}

	s := &Sandbox{rt: goja.New(), prog: prog, stop: make(chan struct{})}
	s.rt.SetMaxCallStackSize(maxCallStack)
	if err := s.installConsole(opt.Log); err != nil {
		return nil, err
	}

	timeout := opt.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	go func() {
		t := time.NewTimer(timeout)
		defer t.Stop()
		select {
		case <-ctx.Done():
			s.interrupt(ErrCancelled)
		case <-t.C:
			s.interrupt(ErrTimeout)
		case <-s.stop:
		}
	}()
	return s, nil
}

// Close releases the watchdog. Safe to call once; always defer it.
func (s *Sandbox) Close() { close(s.stop) }

func (s *Sandbox) interrupt(err error) {
	s.why.Store(err)
	s.rt.Interrupt(err)
}

// Eval runs the script with vars bound as globals and returns what it
// returned, as plain JSON-shaped Go data. A script that returns nothing
// yields nil, which callers read as "no result" (the per-row mode drops the
// row).
func (s *Sandbox) Eval(vars map[string]any) (any, error) {
	for k, v := range vars {
		if err := s.rt.Set(k, v); err != nil {
			return nil, fmt.Errorf("cannot pass %q into the script: %v", k, err)
		}
	}
	v, err := s.rt.RunProgram(s.prog)
	if err != nil {
		return nil, s.explain(err)
	}
	return result(v)
}

// explain turns goja's failure modes into one sentence a flow author can act
// on. An interrupt is reported by the reason the watchdog stored, not by
// goja's own opaque "interrupted".
func (s *Sandbox) explain(err error) error {
	var interrupted *goja.InterruptedError
	if errors.As(err, &interrupted) {
		if why, ok := s.why.Load().(error); ok && why != nil {
			return why
		}
		return ErrTimeout
	}
	var stack *goja.StackOverflowError
	if errors.As(err, &stack) {
		return errors.New("the script recursed too deeply")
	}
	var ex *goja.Exception
	if errors.As(err, &ex) {
		return fmt.Errorf("the script threw: %s", cleanErr(ex.Value()))
	}
	return errors.New(cleanErr(err))
}

// result exports the script's return value and proves it is data. The
// exported Go value is what ships — a JSON round-trip would turn every
// integer into a float — while the encoding exists to enforce the size cap
// and to reject what the rest of the engine could never carry: a function, a
// cycle, a symbol.
func result(v goja.Value) (any, error) {
	if v == nil || goja.IsUndefined(v) || goja.IsNull(v) {
		return nil, nil
	}
	exported := v.Export()
	var buf bytes.Buffer
	enc := json.NewEncoder(&limitWriter{w: &buf, left: MaxResultBytes})
	enc.SetEscapeHTML(false)
	if err := enc.Encode(exported); err != nil {
		if errors.Is(err, errTooBig) {
			return nil, fmt.Errorf("the script returned more than %d bytes", MaxResultBytes)
		}
		return nil, errors.New("the script returned something that is not data — " +
			"return an object, an array, a string, a number or a boolean " +
			"(not a function, and not a value that refers to itself)")
	}
	return exported, nil
}

func (s *Sandbox) installConsole(sink func(LogLine)) error {
	console := s.rt.NewObject()
	// One closure per method rather than one shared writer: the method name is
	// the only place the script says how much it meant by a line, and a single
	// writer throws that away before anyone downstream can read it.
	for _, level := range []string{"log", "info", "warn", "error", "debug"} {
		if err := console.Set(level, s.consoleWriter(sink, level)); err != nil {
			return err
		}
	}
	return s.rt.Set("console", console)
}

func (s *Sandbox) consoleWriter(sink func(LogLine), level string) func(goja.FunctionCall) goja.Value {
	return func(call goja.FunctionCall) goja.Value {
		if sink == nil || s.logs >= MaxLogLines {
			return goja.Undefined()
		}
		s.logs++
		parts := make([]string, 0, len(call.Arguments))
		for _, a := range call.Arguments {
			parts = append(parts, format(a))
		}
		sink(LogLine{Level: level, Line: s.callerLine(), Message: strings.Join(parts, " ")})
		if s.logs == MaxLogLines {
			// The marker is the sandbox talking, not the script: warn, so a
			// reader filtering for trouble sees that the log ends early, and
			// no line number, because no line of theirs produced it.
			sink(LogLine{
				Level:   "warn",
				Message: fmt.Sprintf("… console output stopped after %d lines", MaxLogLines),
			})
		}
		return goja.Undefined()
	}
}

// callerLine is the line of the script that called console. goja reports the
// console function's own native frame first, so the first frame belonging to
// the compiled script is the author's — and because New opens its wrapper on
// the script's first line, that number is the one they see in their editor.
func (s *Sandbox) callerLine() int {
	var buf [2]goja.StackFrame
	for _, f := range s.rt.CaptureCallStack(2, buf[:0]) {
		if f.SrcName() == scriptName {
			return f.Position().Line
		}
	}
	return 0
}

// format renders one console argument. Objects go out as JSON because
// goja's own String() renders them as "[object Object]", which tells a
// debugging author nothing.
func format(v goja.Value) string {
	if v == nil || goja.IsUndefined(v) {
		return "undefined"
	}
	if goja.IsNull(v) {
		return "null"
	}
	switch v.ExportType().Kind().String() {
	case "map", "slice", "struct":
		var buf bytes.Buffer
		enc := json.NewEncoder(&limitWriter{w: &buf, left: 8 << 10})
		enc.SetEscapeHTML(false)
		if err := enc.Encode(v.Export()); err == nil {
			return strings.TrimRight(buf.String(), "\n")
		}
	}
	return v.String()
}

var errTooBig = errors.New("over the size limit")

// limitWriter fails the encode as soon as the output passes the ceiling, so
// an oversized result is never fully materialized just to be measured.
type limitWriter struct {
	w    *bytes.Buffer
	left int
}

func (l *limitWriter) Write(p []byte) (int, error) {
	if len(p) > l.left {
		return 0, errTooBig
	}
	l.left -= len(p)
	return l.w.Write(p)
}

// cleanErr keeps a runtime's error to one line: goja attaches a JS stack to
// exceptions, which belongs in a debugger, not in a node's error message.
func cleanErr(v any) string {
	s := strings.TrimSpace(fmt.Sprint(v))
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}
