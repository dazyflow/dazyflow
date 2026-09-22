// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"regexp/syntax"
	"strings"
	"testing"
)

func literalHasMarker(re *syntax.Regexp) bool {
	if re.Op != syntax.OpLiteral || re.Flags&syntax.FoldCase != 0 {
		return false
	}
	lit := string(re.Rune)
	for _, m := range secretValueMarkers {
		if strings.Contains(lit, m) {
			return true
		}
	}
	return false
}

// The pre-filter is only sound if every alternative carries a marker as a
// MANDATORY literal: a top-level piece of the alternative's concatenation, or a
// top-level alternation whose every branch is such a literal.
func TestKnownSecretPatterns_EachHasMarker(t *testing.T) {
	for _, pat := range knownSecretPatterns {
		re, err := syntax.Parse(pat, syntax.Perl)
		if err != nil {
			t.Fatalf("parse %q: %v", pat, err)
		}
		pieces := []*syntax.Regexp{re}
		if re.Op == syntax.OpConcat {
			pieces = re.Sub
		}
		found := false
		for _, p := range pieces {
			if literalHasMarker(p) {
				found = true
			}
			if p.Op == syntax.OpAlternate {
				all := true
				for _, b := range p.Sub {
					all = all && literalHasMarker(b)
				}
				found = found || all
			}
		}
		if !found {
			t.Errorf("pattern %q has no mandatory literal from secretValueMarkers", pat)
		}
	}
}

func TestKnownSecretValue_NewPatterns(t *testing.T) {
	for _, s := range []string{
		"github_pat_11ABCDEFG0123456789_abcdefghijklmnop",
		"sk-ant-api03-abcdefghijklmnopqrstuvwxyz0123",
		"sk-proj-abcdefghijklmnopqrstuvwxyz0123",
		"sk-" + strings.Repeat("a1B2", 12),
		"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U",
		"aws_secret_access_key = wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
		`AWS_SECRET_ACCESS_KEY="wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"`,
	} {
		if !matchesKnownSecret(s) {
			t.Errorf("not detected: %q", s)
		}
	}
	for _, s := range []string{
		"task-management-service-name-long",
		"https://github.com/dazyflow/dazyflow",
		"wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY", // no variable name: too ambiguous
		"a mask-" + strings.Repeat("x", 45),
	} {
		if matchesKnownSecret(s) {
			t.Errorf("false positive: %q", s)
		}
	}
}

func TestKnownSecretValue_PEMWholeBlock(t *testing.T) {
	block := "-----BEGIN RSA PRIVATE KEY-----\nMIIEowIBAAKCAQEA\nabc+/=\n-----END RSA PRIVATE KEY-----"
	got := knownSecretValue.ReplaceAllString("before "+block+" after", redactedSecretMarker)
	if got != "before "+redactedSecretMarker+" after" {
		t.Errorf("PEM block not fully redacted: %q", got)
	}
	noEnd := "key: -----BEGIN PRIVATE KEY-----\nMIIEowIBAAKCAQEA\nmore"
	got = knownSecretValue.ReplaceAllString(noEnd, redactedSecretMarker)
	if strings.Contains(got, "MIIE") || strings.Contains(got, "more") {
		t.Errorf("PEM without END left body behind: %q", got)
	}
	// In marshalled JSON the redaction must stay inside the string.
	js := `{"a":"-----BEGIN PRIVATE KEY-----\nMIIE\n-----END PRIVATE KEY-----","b":"keep"}`
	got = knownSecretValue.ReplaceAllString(js, redactedSecretMarker)
	if got != `{"a":"`+redactedSecretMarker+`","b":"keep"}` {
		t.Errorf("JSON PEM scrub wrong: %q", got)
	}
	js = `{"a":"-----BEGIN PRIVATE KEY-----\nMIIE \"q\" tail","b":"keep"}`
	got = knownSecretValue.ReplaceAllString(js, redactedSecretMarker)
	if got != `{"a":"`+redactedSecretMarker+`","b":"keep"}` {
		t.Errorf("JSON PEM (no END) scrub wrong: %q", got)
	}
}
