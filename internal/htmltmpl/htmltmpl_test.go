// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package htmltmpl

import (
	"errors"
	"strings"
	"testing"
)

func TestRender_MergeAndEscape(t *testing.T) {
	got, err := Render("<h1>Hi {{.name}}</h1>", map[string]any{"name": "<b>Acme</b>"}, 0)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	// Merge happened; the value's markup is auto-escaped.
	if strings.Contains(got, "<b>Acme</b>") || !strings.Contains(got, "&lt;b&gt;Acme&lt;/b&gt;") {
		t.Fatalf("value not escaped: %q", got)
	}
}

func TestRender_ParseErrorIsTyped(t *testing.T) {
	_, err := Render("{{.unclosed", nil, 0)
	var pe *ParseError
	if !errors.As(err, &pe) {
		t.Fatalf("want *ParseError, got %T (%v)", err, err)
	}
}

func TestRender_TooLarge(t *testing.T) {
	// A tiny cap makes the overflow cheap to trigger.
	_, err := Render("{{.big}}", map[string]any{"big": strings.Repeat("A", 100)}, 16)
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("want ErrTooLarge, got %v", err)
	}
}

func TestRender_RecursionErrorsNotHang(t *testing.T) {
	_, err := Render(`{{define "x"}}{{template "x" .}}{{end}}{{template "x" .}}`, map[string]any{}, 0)
	if err == nil {
		t.Fatal("infinite recursion should error (template depth cap), got nil")
	}
	// It's an execution error, not a parse error.
	var pe *ParseError
	if errors.As(err, &pe) {
		t.Fatalf("recursion should be an exec error, got ParseError: %v", err)
	}
}

func TestRender_NoSecondOrderInjection(t *testing.T) {
	// Data is the context, never re-parsed: a data value that looks like a
	// template action comes out as an escaped literal, not evaluated.
	got, err := Render("{{.greeting}}", map[string]any{
		"greeting": "{{.secret}}", "secret": "TOPSECRET",
	}, 0)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if strings.Contains(got, "TOPSECRET") {
		t.Fatalf("data was evaluated as a template: %q", got)
	}
}

func TestLimitedWriter(t *testing.T) {
	var b strings.Builder
	lw := &limitedWriter{w: &b, limit: 10}
	if _, err := lw.Write([]byte("12345")); err != nil {
		t.Fatalf("first write under limit: %v", err)
	}
	if _, err := lw.Write([]byte("678901")); err == nil {
		t.Fatal("write past limit should error")
	}
	if !lw.tripped {
		t.Error("tripped should be set after overflow")
	}
}

// TestParseError_ErrorAndUnwrap covers the two trivial methods on
// *ParseError: Error() returns the wrapped message, and Unwrap() returns
// the underlying error so errors.Is/As can see through it.
func TestParseError_ErrorAndUnwrap(t *testing.T) {
	sentinel := errors.New("bad action {{")
	pe := &ParseError{Err: sentinel}

	if pe.Error() != "bad action {{" {
		t.Errorf("Error() = %q, want %q", pe.Error(), "bad action {{")
	}
	if pe.Unwrap() != sentinel {
		t.Errorf("Unwrap() did not return the wrapped error")
	}
	if !errors.Is(pe, sentinel) {
		t.Errorf("errors.Is should see the wrapped sentinel through Unwrap")
	}
}

// TestFuncs_Default exercises every branch of the default helper: nil →
// fallback, empty string → fallback, non-empty string → value, and a
// non-string non-nil value → value.
func TestFuncs_Default(t *testing.T) {
	cases := []struct {
		name string
		tmpl string
		data any
		want string
	}{
		{"nil uses fallback", `{{.name | default "there"}}`, map[string]any{"name": nil}, "there"},
		{"missing key is nil → fallback", `{{.name | default "there"}}`, map[string]any{}, "there"},
		{"empty string uses fallback", `{{.name | default "there"}}`, map[string]any{"name": ""}, "there"},
		{"non-empty string kept", `{{.name | default "there"}}`, map[string]any{"name": "Ada"}, "Ada"},
		{"non-string value kept", `{{.n | default "x"}}`, map[string]any{"n": 7}, "7"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Render(tc.tmpl, tc.data, 0)
			if err != nil {
				t.Fatalf("render: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// TestFuncs_UpperLower covers the upper/lower aliases to strings funcs.
func TestFuncs_UpperLower(t *testing.T) {
	got, err := Render(`{{upper "aB"}}-{{lower "Cd"}}`, nil, 0)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if got != "AB-cd" {
		t.Errorf("got %q, want AB-cd", got)
	}
}

// TestFuncs_Join covers all three branches of join: []string, []any, and
// the fallback for a value that is neither slice type.
func TestFuncs_Join(t *testing.T) {
	cases := []struct {
		name string
		data any
		want string
	}{
		{"string slice", map[string]any{"xs": []string{"a", "b", "c"}}, "a,b,c"},
		{"any slice mixes types", map[string]any{"xs": []any{"a", 2, true}}, "a,2,true"},
		{"scalar fallback formats with %v", map[string]any{"xs": 42}, "42"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Render(`{{join "," .xs}}`, tc.data, 0)
			if err != nil {
				t.Fatalf("render: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// TestRender_ExecErrorNotTooLarge covers the non-tripped execution error
// path of Render: a template that errors at exec time (calling a method
// that doesn't exist) should surface the raw error, not ErrTooLarge and
// not a ParseError.
func TestRender_ExecErrorNotTooLarge(t *testing.T) {
	// Calling a method that doesn't exist on the data value is an
	// execution error (not a parse error, not a size overflow).
	_, err := Render(`{{.NoSuchMethod}}`, struct{}{}, 0)
	if err == nil {
		t.Fatal("expected execution error")
	}
	if errors.Is(err, ErrTooLarge) {
		t.Errorf("should not be ErrTooLarge: %v", err)
	}
	var pe *ParseError
	if errors.As(err, &pe) {
		t.Errorf("exec error should not be a ParseError: %v", err)
	}
}

// TestRender_DefaultMaxBytesAllowsLargeOutput confirms maxBytes<=0 falls
// back to DefaultMaxBytes (8 MiB) rather than rejecting ordinary output.
func TestRender_DefaultMaxBytesAllowsLargeOutput(t *testing.T) {
	big := strings.Repeat("x", 1<<20) // 1 MiB, well under the 8 MiB default
	got, err := Render("{{.s}}", map[string]any{"s": big}, -1)
	if err != nil {
		t.Fatalf("render under default cap: %v", err)
	}
	if len(got) != len(big) {
		t.Errorf("output truncated: got %d bytes, want %d", len(got), len(big))
	}
}
