package engine

import (
	"context"
	"testing"
)

// FuzzSubstituteString exercises the ${scheme.path} template parser against
// arbitrary input bytes, pinning two contract invariants the example-based
// tests in template_test.go only spot-check:
//
//  1. Passthrough identity — a substituter that claims no scheme (ok=false for
//     everything) must return the input verbatim. SubstituteString's whole
//     reason for the restrictive scheme charset is that unrelated text (JSON
//     templates, shell snippets, money amounts like ${5}) survives untouched;
//     any input where a "left intact" match is silently dropped or mangled is
//     a data-corruption bug.
//
//  2. Full resolution — a substituter that resolves every scheme to a
//     brace-free constant must leave no ${scheme.path} placeholder behind.
//     Replacement is a single non-overlapping pass and the constant contains
//     no "${", so a surviving match means the regex skipped a placeholder it
//     was contracted to handle. (A leftmost-greedy "${a." can only survive
//     unmatched when no "}" follows it at all — in which case no "}" remains to
//     re-form a placeholder around the inserted constant either.)
//
// Neither substituter errors, so err must always be nil and the call must
// never panic on arbitrary bytes.
func FuzzSubstituteString(f *testing.F) {
	for _, s := range []string{
		"",
		"plain text",
		"Bearer ${secret.KEY}",
		"${secret.USER}:${secret.PASS}@host",
		"${secret.db.password}",
		"price is ${5} and ${UPPER.x}", // not placeholders: no dot / scheme not lowercase
		"${item.id} ${item.}",          // empty path is still a match
		"${a.${b.c}}",                  // nested-looking; nesting is unsupported
		"unterminated ${secret.KEY",    // no closing brace
		"}{$ broken ${.}",              // missing scheme
		"行${secret.鍵}行",                // multi-byte around and inside
		"$${escaped.x}",                // leading dollar before a real match
	} {
		f.Add(s)
	}

	ctx := context.Background()

	passthrough := func(_ context.Context, _, _ string) (string, bool, error) {
		return "", false, nil
	}
	resolveAll := func(_ context.Context, _, _ string) (string, bool, error) {
		return "X", true, nil
	}

	f.Fuzz(func(t *testing.T, s string) {
		// Invariant 1: nothing resolved → input survives byte-for-byte.
		got, err := SubstituteString(ctx, s, passthrough)
		if err != nil {
			t.Fatalf("passthrough substituter must not error, got %v", err)
		}
		if got != s {
			t.Fatalf("passthrough mangled input\n in: %q\nout: %q", s, got)
		}

		// Invariant 2: everything resolved to a brace-free constant → no
		// placeholder of the matched form may remain.
		out, err := SubstituteString(ctx, s, resolveAll)
		if err != nil {
			t.Fatalf("resolveAll substituter must not error, got %v", err)
		}
		if placeholderPattern.MatchString(out) {
			t.Fatalf("placeholder survived full resolution\n in: %q\nout: %q", s, out)
		}
	})
}
