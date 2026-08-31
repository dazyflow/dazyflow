// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// docsgen had no test, which is a gap in proportion to what it does: it renders
// the entire public step catalog for docs.dazyflow.app straight from the drop
// manifests, so a defect here is wrong documentation for every step at once,
// with nothing between the bug and the reader.
//
// These cover the text-mangling helpers — the part with the sharp edges, where
// one escape too few breaks a page and one too many shows entities to a user —
// plus one end-to-end run over the real registry.
package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMdSafe(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "a bare placeholder is escaped, so it survives as text",
			in:   "put <place> here",
			want: "put &lt;place> here",
		},
		{
			// Escaping inside a span would show "&lt;" to the reader, because a
			// renderer already treats span content as raw text.
			name: "inside an inline code span nothing is touched",
			in:   "use `<place>` verbatim",
			want: "use `<place>` verbatim",
		},
		{
			name: "escaping resumes after the span closes",
			in:   "`<a>` then <b>",
			want: "`<a>` then &lt;b>",
		},
		{
			// The VitePress/Vue era escaped these to "&#123;&#123;". Nothing
			// compiles through Vue now, and react-markdown renders the raw
			// braces identically, so they must pass through untouched.
			name: "Go template braces pass through unescaped",
			in:   "{{.name}} pulls a field, {{range .items}}…{{end}} loops",
			want: "{{.name}} pulls a field, {{range .items}}…{{end}} loops",
		},
		{
			name: "braces and a placeholder together",
			in:   "{{.x}} and <y>",
			want: "{{.x}} and &lt;y>",
		},
		{
			name: "an unbalanced span leaves the rest raw rather than dropping it",
			in:   "trailing `<tick",
			want: "trailing `<tick",
		},
		{
			name: "multi-byte prose is not corrupted",
			in:   "räkna <antal> steg — så här",
			want: "räkna &lt;antal> steg — så här",
		},
		{name: "empty", in: "", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := mdSafe(tt.in); got != tt.want {
				t.Errorf("mdSafe(%q)\n got %q\nwant %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestEscapeCell(t *testing.T) {
	// A pipe or a newline in a description would end the table cell early and
	// shear the rest of the row into the wrong columns.
	got := escapeCell("first | second\nthird <x>")
	want := `first \| second third &lt;x>`
	if got != want {
		t.Errorf("escapeCell\n got %q\nwant %q", got, want)
	}
}

func TestOneLine(t *testing.T) {
	if got, want := oneLine("a\n\tb  c\r\nd"), "a b c d"; got != want {
		t.Errorf("oneLine = %q, want %q", got, want)
	}
}

func TestSlug(t *testing.T) {
	tests := []struct{ in, want string }{
		{"Send email", "send-email"},
		{"Gmail / Google Workspace", "gmail-google-workspace"},
		{"46elks", "46elks"},
		{"  padded  ", "padded"},
		{"Läs rad", "l-s-rad"}, // non-ASCII collapses to one dash, not dropped
		{"---", ""},
		{"", ""},
	}
	for _, tt := range tests {
		if got := slug(tt.in); got != tt.want {
			t.Errorf("slug(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestPrettify(t *testing.T) {
	tests := []struct{ in, want string }{
		{"freeze_row", "Freeze row"},
		{"data", "Data"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := prettify(tt.in); got != tt.want {
			t.Errorf("prettify(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestEmptyParams(t *testing.T) {
	for _, in := range []string{"", "  ", "{}", " {} "} {
		if !emptyParams([]byte(in)) {
			t.Errorf("emptyParams(%q) = false, want true", in)
		}
	}
	if emptyParams([]byte(`{"a":1}`)) {
		t.Error(`emptyParams({"a":1}) = true, want false`)
	}
}

// TestRunGeneratesTheCatalog is the end-to-end check: run against the real drop
// registry and assert the shape the docs SPA relies on. It also guards the
// promise in the package comment — that output is deterministic, so a re-run
// produces a clean diff rather than churn.
func TestRunGeneratesTheCatalog(t *testing.T) {
	dir := t.TempDir()
	if err := run(dir); err != nil {
		t.Fatalf("run: %v", err)
	}

	index := filepath.Join(dir, "index.md")
	body, err := os.ReadFile(index)
	if err != nil {
		t.Fatalf("index.md: %v", err)
	}
	if !strings.Contains(string(body), "---") {
		t.Error("index.md has no front matter")
	}

	pages, err := filepath.Glob(filepath.Join(dir, "*.md"))
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) < 10 {
		t.Fatalf("got %d pages, want the index plus a page per group", len(pages))
	}

	// No page may carry the Vue-era brace entity any more.
	for _, p := range pages {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "&#123;") {
			t.Errorf("%s still escapes braces as entities", filepath.Base(p))
		}
	}

	// Deterministic: a second run into a fresh directory is byte-identical.
	again := t.TempDir()
	if err := run(again); err != nil {
		t.Fatalf("second run: %v", err)
	}
	for _, p := range pages {
		name := filepath.Base(p)
		a, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(filepath.Join(again, name))
		if err != nil {
			t.Fatalf("%s missing from the second run: %v", name, err)
		}
		if string(a) != string(b) {
			t.Errorf("%s is not deterministic across runs", name)
		}
	}
}
