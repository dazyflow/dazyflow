// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"strings"
	"testing"
)

// TestSanitizeAuditField_StripsLogForging is the regression: the failed
// sign-in path records the email the caller typed, unvalidated. A newline in
// it would forge an extra line in a compliance-relevant log.
func TestSanitizeAuditField_StripsLogForging(t *testing.T) {
	forged := "victim@acme.test\nauth.signin OK actor=admin@acme.test"
	got := sanitizeAuditField(forged)
	if strings.ContainsAny(got, "\n\r") {
		t.Fatalf("newline survived sanitizing: %q", got)
	}
	if !strings.Contains(got, "victim@acme.test") {
		t.Errorf("sanitizing destroyed the real value: %q", got)
	}

	for _, in := range []string{
		"a\rb", "a\nb", "a\tb", "a\x00b", "a\x1bb", "a\x7fb",
	} {
		if out := sanitizeAuditField(in); strings.ContainsAny(out, "\r\n\t\x00\x1b\x7f") {
			t.Errorf("sanitizeAuditField(%q) = %q, still holds a control char", in, out)
		}
	}
}

func TestSanitizeAuditField_CapsLengthAndKeepsNormalValues(t *testing.T) {
	if got := sanitizeAuditField(strings.Repeat("x", 5000)); len(got) > auditFieldLimit {
		t.Errorf("length = %d, want <= %d", len(got), auditFieldLimit)
	}
	for _, in := range []string{"", "user@acme.test", "method=password ip=10.0.0.1", "flow/ws/id"} {
		if got := sanitizeAuditField(in); got != in {
			t.Errorf("sanitizeAuditField(%q) = %q — ordinary values must pass through", in, got)
		}
	}
	// Non-ASCII must survive: names and org labels are not ASCII-only.
	if got := sanitizeAuditField("Åsa Öberg — Malmö"); got != "Åsa Öberg — Malmö" {
		t.Errorf("non-ASCII mangled: %q", got)
	}
}
