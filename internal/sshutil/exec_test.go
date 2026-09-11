// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package sshutil

import (
	"strings"
	"testing"
)

// The script this builds is handed to a remote shell, so a value that escapes
// its quotes is a command the operator did not write running on their server.
// These are the cases that do it.
func TestShellQuote_ContainsEveryShellMetacharacter(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ in, want string }{
		{`plain`, `'plain'`},
		{`with space`, `'with space'`},
		{`it's`, `'it'\''s'`},
		{`$(rm -rf /)`, `'$(rm -rf /)'`},
		{"`whoami`", "'`whoami`'"},
		{`a; rm -rf /`, `'a; rm -rf /'`},
		{`$HOME`, `'$HOME'`},
		{`a"b`, `'a"b'`},
		{`back\slash`, `'back\slash'`},
		{``, `''`},
	} {
		if got := shellQuote(tc.in); got != tc.want {
			t.Errorf("shellQuote(%q) = %s, want %s", tc.in, got, tc.want)
		}
	}
}

// A single quote is the one character that can end the quoting, so the closing
// half of every quoted word must survive it.
func TestShellQuote_ClosesEveryWordItOpens(t *testing.T) {
	t.Parallel()
	for _, in := range []string{`'`, `''`, `a'b'c`, `'; id; '`} {
		got := shellQuote(in)
		if !strings.HasPrefix(got, "'") || !strings.HasSuffix(got, "'") {
			t.Errorf("shellQuote(%q) = %s, not a quoted word", in, got)
		}
		// Every quote inside the word must be the escaped form.
		inner := got[1 : len(got)-1]
		if strings.Contains(strings.ReplaceAll(inner, `'\''`, ""), "'") {
			t.Errorf("shellQuote(%q) = %s leaves a bare quote inside", in, got)
		}
	}
}

func TestValidEnvName(t *testing.T) {
	t.Parallel()
	for _, ok := range []string{"A", "_x", "API_TOKEN", "a1", "_"} {
		if !validEnvName(ok) {
			t.Errorf("validEnvName(%q) = false, want true", ok)
		}
	}
	for _, bad := range []string{"", "1A", "A-B", "A B", "A=1", "A;id", "Å", "A.B"} {
		if validEnvName(bad) {
			t.Errorf("validEnvName(%q) = true, want false", bad)
		}
	}
}

func TestShellScript_ExportsThenChangesDirectoryThenRuns(t *testing.T) {
	t.Parallel()
	got := shellScript(RunRequest{
		Command:    "./report.sh",
		Env:        map[string]string{"B": "two", "A": "it's one"},
		WorkingDir: "/srv/app dir",
	})
	want := "export A='it'\\''s one'\nexport B='two'\ncd '/srv/app dir' || exit 1\n./report.sh"
	if got != want {
		t.Errorf("shellScript()\n got: %q\nwant: %q", got, want)
	}
}

// A cd that fails must stop the script. Carrying on in the home directory is
// how a backup gets written somewhere nobody looks for it.
func TestShellScript_AbortsOnAFailedChangeOfDirectory(t *testing.T) {
	t.Parallel()
	got := shellScript(RunRequest{Command: "pg_dump > out.sql", WorkingDir: "/backups"})
	if !strings.Contains(got, "cd '/backups' || exit 1\n") {
		t.Errorf("shellScript() does not abort on a failed cd: %q", got)
	}
}

// A name that could carry its own shell syntax is dropped rather than quoted:
// there is no quoting for the left-hand side of an assignment.
func TestShellScript_DropsUnusableEnvNames(t *testing.T) {
	t.Parallel()
	got := shellScript(RunRequest{Command: "true", Env: map[string]string{"A=1; id": "x", "OK": "y"}})
	if strings.Contains(got, "id") {
		t.Errorf("shellScript() passed an unusable name through: %q", got)
	}
	if !strings.Contains(got, "export OK='y'") {
		t.Errorf("shellScript() dropped the usable name too: %q", got)
	}
}

func TestShellScript_BareCommandWhenNothingElseIsAsked(t *testing.T) {
	t.Parallel()
	if got := shellScript(RunRequest{Command: "uptime"}); got != "uptime" {
		t.Errorf("shellScript() = %q, want %q", got, "uptime")
	}
}

func TestCapWriter_KeepsTheLimitAndReportsTheDrop(t *testing.T) {
	t.Parallel()
	w := &capWriter{limit: 10}
	// Short writes make the ssh library tear the session down, so Write must
	// always claim the whole slice even when it keeps none of it.
	if n, err := w.Write([]byte("0123456789abcdef")); n != 16 || err != nil {
		t.Fatalf("Write() = %d, %v; want 16, nil", n, err)
	}
	if n, err := w.Write([]byte("more")); n != 4 || err != nil {
		t.Fatalf("Write() after the cap = %d, %v; want 4, nil", n, err)
	}
	got, truncated := w.result()
	if got != "0123456789" {
		t.Errorf("result() = %q, want %q", got, "0123456789")
	}
	if !truncated {
		t.Error("result() reported no truncation after dropping output")
	}
}

func TestCapWriter_UnlimitedKeepsEverything(t *testing.T) {
	t.Parallel()
	w := &capWriter{}
	_, _ = w.Write([]byte("all of it"))
	if got, truncated := w.result(); got != "all of it" || truncated {
		t.Errorf("result() = %q, %v; want %q, false", got, truncated, "all of it")
	}
}

// The console shows lines, so half a line must wait for its newline — and
// whatever never gets one still has to be shown at the end.
func TestCapWriter_EmitsWholeLinesThenTheRemainder(t *testing.T) {
	t.Parallel()
	var lines []string
	w := &capWriter{onLine: func(s string) { lines = append(lines, s) }}
	_, _ = w.Write([]byte("first\nsec"))
	if len(lines) != 1 || lines[0] != "first" {
		t.Fatalf("after a partial second line, lines = %q", lines)
	}
	_, _ = w.Write([]byte("ond\r\nthird, unterminated"))
	if len(lines) != 2 || lines[1] != "second" {
		t.Fatalf("CRLF line not joined or not trimmed: %q", lines)
	}
	_, _ = w.result()
	if len(lines) != 3 || lines[2] != "third, unterminated" {
		t.Errorf("result() did not flush the tail: %q", lines)
	}
}
