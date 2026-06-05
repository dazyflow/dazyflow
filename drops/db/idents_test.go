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
		"FÖRETAG":    `"FÖRETAG"`,
		"normal":     `"normal"`,
		"MOMS%":      `"MOMS%"`,
		"with space": `"with space"`,
		"with-dash":  `"with-dash"`,
		"order":      `"order"`,
		// Adversarial: embedded double quote. Go's %q would produce
		// `"weird\"col"` (C-escape) which SQL parsers misread.
		// quoteIdent must double the quote: `"weird""col"`.
		`weird"col`: `"weird""col"`,
		// And the doubled-quote nested case.
		`a""b`: `"a""""b"`,
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

// TestQuoteIdent_InjectionCannotBreakOut is a property test over a corpus of
// SQL-injection payloads: whatever a (validated) identifier contains, the
// quoted form must remain a single well-formed quoted identifier — i.e. every
// interior delimiter is doubled, so the payload can never terminate the quote
// and inject trailing SQL.
func TestQuoteIdent_InjectionCannotBreakOut(t *testing.T) {
	payloads := []string{
		`x"; DROP TABLE users; --`,
		`a" OR "1"="1`,
		`"; SELECT pg_sleep(10); --`,
		`col") ; DELETE FROM secrets; ("`,
		`tab	and
newline`,
		strings.Repeat(`"`, 50),
		"FÖRETAG",
		"normal_col",
	}
	for _, p := range payloads {
		// Double-quote style (Postgres / SQLite).
		q := quoteIdent(p)
		if len(q) < 2 || q[0] != '"' || q[len(q)-1] != '"' {
			t.Errorf("quoteIdent(%q) not wrapped in double quotes: %q", p, q)
			continue
		}
		// Strip the outer pair; every remaining " must be part of a doubled
		// "" pair, i.e. the interior contains an even number of quotes.
		if inner := q[1 : len(q)-1]; strings.Count(inner, `"`)%2 != 0 {
			t.Errorf("quoteIdent(%q) leaves an unescaped quote — breakout possible: %q", p, q)
		}

		// Backtick style (MySQL): same property with backticks.
		b := quoteIdentBacktick(p)
		if len(b) < 2 || b[0] != '`' || b[len(b)-1] != '`' {
			t.Errorf("quoteIdentBacktick(%q) not wrapped in backticks: %q", p, b)
			continue
		}
		if inner := b[1 : len(b)-1]; strings.Count(inner, "`")%2 != 0 {
			t.Errorf("quoteIdentBacktick(%q) leaves an unescaped backtick: %q", p, b)
		}
	}
}
