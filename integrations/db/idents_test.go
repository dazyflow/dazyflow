package db

import (
	"strings"
	"testing"
)

func TestValidateIdent_AcceptsRealisticHeaders(t *testing.T) {
	// Every one of these used to be rejected by the old
	// [A-Za-z0-9_] check — they're the kinds of names that turn up
	// in real spreadsheets from non-English-speaking customers.
	for _, name := range []string{
		"FÖRETAG",
		"MOMS%",
		"Antal à",
		"日本語",
		"order", // SQL reserved word — fine, we quote it
		"weird col",
		"with-dash",
		"col.with.dot",
		"123leading_digit",
		"a", // single char
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateIdent(name); err != nil {
				t.Errorf("validateIdent(%q) = %v, want nil", name, err)
			}
		})
	}
}

func TestValidateIdent_RejectsUnsafe(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
	}{
		{"empty", ""},
		{"NUL byte", "a\x00b"},
		{"too long", strings.Repeat("x", maxIdentLen+1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateIdent(tc.in); err == nil {
				t.Errorf("validateIdent(%q) = nil, want error", tc.in)
			}
		})
	}
}

func TestQuoteIdent_DoublesEmbeddedQuotes(t *testing.T) {
	cases := map[string]string{
		// Common cases — round-trip cleanly.
		"FÖRETAG":     `"FÖRETAG"`,
		"normal":      `"normal"`,
		"MOMS%":       `"MOMS%"`,
		"with space":  `"with space"`,
		"with-dash":   `"with-dash"`,
		"order":       `"order"`,
		// Adversarial: embedded double quote. Go's %q would produce
		// `"weird\"col"` (C-escape) which SQL parsers misread.
		// quoteIdent must double the quote: `"weird""col"`.
		`weird"col`:   `"weird""col"`,
		// And the doubled-quote nested case.
		`a""b`:        `"a""""b"`,
	}
	for in, want := range cases {
		if got := quoteIdent(in); got != want {
			t.Errorf("quoteIdent(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestQuoteIdentBacktick_DoublesEmbeddedBackticks(t *testing.T) {
	if got, want := quoteIdentBacktick("FÖRETAG"), "`FÖRETAG`"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	// MySQL's escape convention mirrors the SQL standard: a doubled
	// backtick inside the quoted form means a single literal
	// backtick in the identifier.
	if got, want := quoteIdentBacktick("a`b"), "`a``b`"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
