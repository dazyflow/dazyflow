package engine

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func makeSub(values map[string]string) Substituter {
	return func(_ context.Context, scheme, path string) (string, bool, error) {
		v, ok := values[scheme+":"+path]
		return v, ok, nil
	}
}

func TestSubstituteString_BasicReplacement(t *testing.T) {
	sub := makeSub(map[string]string{"env:KEY": "secret"})
	got, err := SubstituteString(t.Context(), "Bearer ${env.KEY}", sub)
	if err != nil {
		t.Fatalf("substitute: %v", err)
	}
	if got != "Bearer secret" {
		t.Errorf("got %q", got)
	}
}

func TestSubstituteString_MultiplePlaceholders(t *testing.T) {
	sub := makeSub(map[string]string{
		"env:USER": "alice",
		"env:PASS": "shh",
	})
	got, err := SubstituteString(t.Context(), "${env.USER}:${env.PASS}@host", sub)
	if err != nil {
		t.Fatalf("substitute: %v", err)
	}
	if got != "alice:shh@host" {
		t.Errorf("got %q", got)
	}
}

func TestSubstituteString_DotSeparator(t *testing.T) {
	sub := makeSub(map[string]string{"secret:PASSWD": "hunter2"})
	// Dot is the separator.
	got, err := SubstituteString(t.Context(), "pw=${secret.PASSWD}", sub)
	if err != nil {
		t.Fatalf("substitute: %v", err)
	}
	if got != "pw=hunter2" {
		t.Errorf("dot form → %q, want pw=hunter2", got)
	}
}

func TestSubstituteString_ColonNoLongerResolves(t *testing.T) {
	// Colon is no longer a separator — ${secret:PASSWD} is not a placeholder
	// and is left verbatim rather than resolved.
	sub := makeSub(map[string]string{"secret:PASSWD": "hunter2"})
	got, err := SubstituteString(t.Context(), "pw=${secret:PASSWD}", sub)
	if err != nil {
		t.Fatalf("substitute: %v", err)
	}
	if got != "pw=${secret:PASSWD}" {
		t.Errorf("colon form → %q, want it left literal", got)
	}
}

func TestSubstituteString_DotSplitsOnFirstSeparator(t *testing.T) {
	// ${secret.db.password} → scheme "secret", path "db.password" (split on
	// the first separator, so a dotted secret name still resolves).
	sub := makeSub(map[string]string{"secret:db.password": "pg"})
	got, err := SubstituteString(t.Context(), "${secret.db.password}", sub)
	if err != nil {
		t.Fatalf("substitute: %v", err)
	}
	if got != "pg" {
		t.Errorf("got %q, want pg", got)
	}
}

func TestSubstituteString_UnknownSchemeLeftIntact(t *testing.T) {
	sub := makeSub(nil)
	got, err := SubstituteString(t.Context(), "before ${item.id} after", sub)
	if err != nil {
		t.Fatalf("substitute: %v", err)
	}
	if got != "before ${item.id} after" {
		t.Errorf("got %q", got)
	}
}

func TestSubstituteString_FastPathNoPlaceholder(t *testing.T) {
	// Strings without "${" should skip the regex entirely. We can't
	// observe the skip directly, but we can confirm it doesn't error
	// even when the substituter would have failed.
	sub := func(_ context.Context, _, _ string) (string, bool, error) {
		t.Fatal("substituter should not be called")
		return "", false, nil
	}
	got, err := SubstituteString(t.Context(), "no placeholders here", sub)
	if err != nil || got != "no placeholders here" {
		t.Errorf("got = %q, err = %v", got, err)
	}
}

func TestSubstituteString_PropagatesSubstituterError(t *testing.T) {
	sub := func(_ context.Context, _, _ string) (string, bool, error) {
		return "", true, errors.New("kaboom")
	}
	_, err := SubstituteString(t.Context(), "x=${env.Y}", sub)
	if err == nil || !strings.Contains(err.Error(), "kaboom") {
		t.Fatalf("err = %v", err)
	}
}

func TestSubstituteValue_RecursesIntoMapsAndSlices(t *testing.T) {
	sub := makeSub(map[string]string{
		"env:A": "1",
		"env:B": "2",
	})
	tree := map[string]any{
		"top": "${env.A}",
		"nested": map[string]any{
			"x": "${env.B}",
		},
		"list":   []any{"${env.A}", "literal"},
		"number": 42,
	}
	out, err := SubstituteValue(t.Context(), tree, sub)
	if err != nil {
		t.Fatalf("substitute: %v", err)
	}
	got := out.(map[string]any)
	if got["top"] != "1" {
		t.Errorf("top = %v", got["top"])
	}
	if got["nested"].(map[string]any)["x"] != "2" {
		t.Errorf("nested.x = %v", got["nested"])
	}
	if got["list"].([]any)[0] != "1" {
		t.Errorf("list[0] = %v", got["list"].([]any)[0])
	}
	if got["number"] != 42 {
		t.Errorf("number = %v", got["number"])
	}
}
