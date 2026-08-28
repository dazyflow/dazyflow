// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"strings"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
)

// FuzzRedactResult_NoSecretSurvives is the data-leak guard: for an arbitrary
// resolved-secret value scattered across every field redactResult is
// contracted to scrub — the output Ref string, the Inline value (plain
// string, []byte, nested map KEYS and values, and slice elements at any
// depth), and the error Message/Details — no occurrence of the plaintext may
// survive redaction. The example-based tests in redact_test.go pin specific
// shapes; this explores arbitrary secret values and surrounding bytes to find
// a placement or boundary they miss.
//
// The assertion uses an *independent* walker (redactionSurfaces) as its oracle
// rather than re-using redactValue, so a bug in the code under test can't hide
// the leak from the check. The walker visits exactly the contracted surfaces —
// not Ref.MIME, the output port key, or Error.Code, which redaction
// deliberately leaves untouched (controlled identifiers, not user data) and
// which would otherwise read as false-positive "leaks".
func FuzzRedactResult_NoSecretSurvives(f *testing.F) {
	for _, s := range []string{
		"sk_live_supersecret",
		"ghp_0123456789abcdef0123",
		"hunter2hunter2",
		"Bearer abc.def.ghi",
		"aX3$kf9_Lq!!",
		"行行行行行行", // multi-byte
	} {
		f.Add(s, "prefix ", " suffix")
	}

	f.Fuzz(func(t *testing.T, secret, pre, post string) {
		// Only values the redactor actually records can be scrubbed: shorter
		// ones no-op by design (minRedactableSecretLen) and fall back to the
		// save-time lint, so a "survival" there is expected, not a leak.
		if len(secret) < minRedactableSecretLen {
			t.Skip("below redaction threshold — not recorded by design")
		}
		// A secret that is itself a substring of the marker can never be fully
		// erased: the marker we substitute in contains it. This is a degenerate
		// case (a credential literally equal to a slice of "[redacted:secret]"),
		// not a real leak path — exclude it.
		if strings.Contains(redactionMarker, secret) {
			t.Skip("secret is a substring of the redaction marker")
		}

		set := newSecretSet()
		set.add(secret)

		result := core.Result{
			Status: core.StatusError,
			Output: map[string]core.Ref{
				"out": {
					MIME: "text/plain", // NOT redacted — must not hold the secret
					Ref:  "file-" + secret + "-" + post + ".log",
					Inline: map[string]any{
						secret:     "as-a-map-key",      // secret as a KEY
						"value":    secret,              // as a value
						"wrapped":  pre + secret + post, // adjacent fuzzed bytes
						"asbytes":  []byte("raw:" + secret),
						"repeated": secret + secret, // overlapping/adjacent
						"list": []any{
							secret,
							123,
							map[string]any{"deep": pre + secret, "deeper": []any{secret}},
						},
					},
				},
			},
			Error: &core.JobError{
				Code:    "upstream", // NOT redacted by design
				Message: "auth failed using " + secret,
				Details: "Authorization: Bearer " + secret,
			},
		}

		redactResult(&result, set)

		for _, s := range redactionSurfaces(&result) {
			if strings.Contains(s, secret) {
				t.Fatalf("secret %q survived redaction in surface %q", secret, s)
			}
		}
	})
}

// redactionSurfaces collects every string the contract says redactResult must
// scrub: each output Ref string, all string/[]byte/map-key content reachable
// in each Inline value, and the error Message/Details. Intentionally excludes
// Ref.MIME, the output port key, and Error.Code. This is the test's oracle —
// kept independent of redactValue on purpose.
func redactionSurfaces(r *core.Result) []string {
	var out []string
	for _, ref := range r.Output {
		if ref.Ref != "" {
			out = append(out, ref.Ref)
		}
		out = append(out, inlineStrings(ref.Inline)...)
	}
	if r.Error != nil {
		out = append(out, r.Error.Message, r.Error.Details)
	}
	return out
}

// inlineStrings walks a decoded JSON-ish value, returning every string it
// holds — including map keys, which a module could leak a secret through.
func inlineStrings(v any) []string {
	switch tv := v.(type) {
	case string:
		return []string{tv}
	case []byte:
		return []string{string(tv)}
	case map[string]any:
		out := make([]string, 0, len(tv)*2)
		for k, val := range tv {
			out = append(out, k)
			out = append(out, inlineStrings(val)...)
		}
		return out
	case []any:
		var out []string
		for _, val := range tv {
			out = append(out, inlineStrings(val)...)
		}
		return out
	default:
		return nil
	}
}
