// SPDX-FileCopyrightText: 2026 Joachim Klahr
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
