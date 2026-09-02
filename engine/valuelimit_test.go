// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/dazyflow/dazyflow/core"
)

// A template may name a large value several times, and each reference
// multiplies it. Uncapped, that is how a flow compounds a kilobyte into an
// out-of-memory throw, so expansion stops at the ceiling.
func TestSubstituteString_CapsExpansion(t *testing.T) {
	defer core.SetMaxValueBytes(1024)()

	big := strings.Repeat("x", 600)
	sub := func(_ context.Context, scheme, _ string) (string, bool, error) {
		if scheme != "up" {
			return "", false, nil
		}
		return big, true, nil
	}

	if out, err := SubstituteString(context.Background(), "${up.a}", sub); err != nil || len(out) != 600 {
		t.Fatalf("one reference: out=%d err=%v, want 600 bytes and no error", len(out), err)
	}

	_, err := SubstituteString(context.Background(), "${up.a}${up.a}", sub)
	var vt *ValueTooLargeError
	if !errors.As(err, &vt) {
		t.Fatalf("two references past the ceiling: err = %v, want ValueTooLargeError", err)
	}
	if templateErrCode(err) != "value_too_large" {
		t.Errorf("templateErrCode = %q, want value_too_large", templateErrCode(err))
	}
}

func TestSubstituteString_LeavesUnknownSchemesAlone(t *testing.T) {
	defer core.SetMaxValueBytes(16)()
	in := "${mystery.path} and ${another.one}"
	out, err := SubstituteString(context.Background(), in, func(context.Context, string, string) (string, bool, error) {
		return "", false, nil
	})
	if err != nil || out != in {
		t.Errorf("SubstituteString = (%q, %v), want the input unchanged", out, err)
	}
}

// The result boundary is the other half of the ceiling: a value a drop built
// itself (not through a template) must not reach the job store either.
func TestOversizedOutput(t *testing.T) {
	defer core.SetMaxValueBytes(32)()

	within := map[string]core.Ref{"a": {Inline: "small"}}
	if got := oversizedOutput(within); got != nil {
		t.Errorf("oversizedOutput = %v, want nil", got)
	}
	over := map[string]core.Ref{
		"a": {Inline: "small"},
		"b": {Inline: strings.Repeat("y", 100)},
	}
	got := oversizedOutput(over)
	if got == nil {
		t.Fatal("oversizedOutput accepted a 100-byte value against a 32-byte ceiling")
	}
	if !strings.Contains(got.Error(), `output "b"`) {
		t.Errorf("oversizedOutput = %v, want it to name port b", got)
	}
}
