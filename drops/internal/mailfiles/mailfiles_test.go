// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package mailfiles

import (
	"strings"
	"testing"
)

// SanitizeFilename is a security boundary, so it gets tested as one: the name
// comes from whoever sent the email, and the only guarantee worth having is
// that whatever comes out can land nowhere but where it was told to.
//
// The os.Root the caller writes through refuses an escape as well. This is the
// layer that means the escape is never attempted — and the layer a future
// "save a file someone sent us" drop will reach for, which is why it lives
// here rather than inside one connector.
func TestSanitizeFilename(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want string
	}{
		{name: "ordinary name survives", in: "invoice-900.pdf", want: "invoice-900.pdf"},
		{name: "traversal is flattened", in: "../../../../etc/passwd", want: "passwd"},
		// path.Base — not filepath.Base — so a backslash is never a separator,
		// on any platform. The name flattens to one dashed segment instead of
		// being split, which is equally safe and the same on every host.
		{name: "windows separators flatten", in: `..\..\windows\system32\cfg`, want: "windows-system32-cfg"},
		{name: "absolute path loses its root", in: "/etc/shadow", want: "shadow"},
		{name: "leading dots go", in: "...hidden.pdf", want: "hidden.pdf"},
		{name: "a name of only dots and dashes has nothing left", in: "..-.-", want: "attachment"},
		{name: "empty name gets one", in: "", want: "attachment"},
		{name: "just a separator gets one", in: "/", want: "attachment"},
		{name: "spaces and quotes become dashes", in: `my "big" report.pdf`, want: "my--big--report.pdf"},
		{name: "non-ascii becomes dashes rather than being dropped", in: "räkning.pdf", want: "r-kning.pdf"},
		{name: "newlines can't split a path", in: "a\n/b.pdf", want: "b.pdf"},
		{name: "null byte can't truncate a path", in: "safe.pdf\x00.exe", want: "safe.pdf-.exe"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := SanitizeFilename(tc.in)
			if got != tc.want {
				t.Errorf("SanitizeFilename(%q) = %q, want %q", tc.in, got, tc.want)
			}
			// The invariants that matter, asserted independently of the exact
			// expected string above — these are what the caller relies on.
			if strings.ContainsAny(got, `/\`) {
				t.Errorf("%q still carries a path separator", got)
			}
			if got == "" || got == "." || got == ".." {
				t.Errorf("%q is not a usable filename", got)
			}
			if strings.HasPrefix(got, ".") || strings.HasPrefix(got, "-") {
				t.Errorf("%q starts with a dot or dash", got)
			}
		})
	}

	// A very long name is truncated, so a sender can't push a path past the
	// filesystem's limit and have the create fail instead of the save.
	long := SanitizeFilename(strings.Repeat("a", 500) + ".pdf")
	if len(long) > 120 {
		t.Errorf("a 500-character name came out %d long", len(long))
	}
}

func TestKeepExtensions(t *testing.T) {
	// Blank means "keep everything", expressed as nil so Keep can tell it
	// apart from "keep nothing".
	if got := KeepExtensions("  "); got != nil {
		t.Errorf("blank filter = %v, want nil", got)
	}
	// The spellings someone actually types.
	got := KeepExtensions(" PDF , .png ,*.csv, ")
	for _, want := range []string{"pdf", "png", "csv"} {
		if !got[want] {
			t.Errorf("filter %v is missing %q", got, want)
		}
	}
	if len(got) != 3 {
		t.Errorf("filter %v has stray entries", got)
	}
}

func TestKeep(t *testing.T) {
	if !Keep("anything.exe", nil) {
		t.Error("no filter must keep everything")
	}
	only := KeepExtensions("pdf")
	if !Keep("Invoice.PDF", only) {
		t.Error("the extension match is case-insensitive")
	}
	if Keep("signature.png", only) {
		t.Error("a non-matching extension must be dropped")
	}
	if Keep("noextension", only) {
		t.Error("a name with no extension can't match a filter")
	}
}

// Dest is what stops two emails whose invoice is both called "invoice.pdf"
// from overwriting each other.
func TestDest(t *testing.T) {
	a := Dest("", "101", 0, "invoice.pdf")
	b := Dest("", "102", 0, "invoice.pdf")
	if a == b {
		t.Errorf("two messages' same-named attachments collide: %q", a)
	}
	if !strings.HasPrefix(a, "scratch://") {
		t.Errorf("no folder should mean the run's scratch area, got %q", a)
	}
	// Two attachments on ONE message stay distinct too.
	if Dest("", "101", 0, "a.pdf") == Dest("", "101", 1, "a.pdf") {
		t.Error("two attachments on one message collide")
	}
	// A named folder is workspace-relative, not scratch.
	inFolder := Dest("invoices/2026", "101", 0, "invoice.pdf")
	if strings.Contains(inFolder, "scratch://") || !strings.HasPrefix(inFolder, "invoices/2026/") {
		t.Errorf("folder path = %q", inFolder)
	}
	// A hostile message id can't escape either — it goes through the same
	// sanitiser as the filename.
	if got := Dest("", "../../etc", 0, "x.pdf"); strings.Contains(got, "..") {
		t.Errorf("a traversal in the message id survived: %q", got)
	}
}
