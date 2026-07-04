// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"strings"
	"testing"
)

func TestTicketStatusValid(t *testing.T) {
	for _, s := range []TicketStatus{TicketOpen, TicketAwaitingUser, TicketAwaitingSupport, TicketResolved, TicketClosed} {
		if !s.Valid() {
			t.Errorf("%q should be valid", s)
		}
	}
	for _, s := range []TicketStatus{"", "bogus", "OPEN"} {
		if TicketStatus(s).Valid() {
			t.Errorf("%q should be invalid", s)
		}
	}
	if !TicketResolved.IsTerminal() || !TicketClosed.IsTerminal() {
		t.Error("resolved/closed must be terminal")
	}
	if TicketOpen.IsTerminal() || TicketAwaitingSupport.IsTerminal() {
		t.Error("open/awaiting must not be terminal")
	}
}

func TestScrubSecrets(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string // "" means "must not contain the secret", checked separately
	}{
		{"stripe live", "my key is sk_live_abcdefgh12345678 ok", ""},
		{"github token", "token ghp_0123456789abcdefghij0123 here", ""},
		{"slack", "xoxb-1234567890-abcdefghij used", ""},
		{"aws", "AKIAIOSFODNN7EXAMPLE creds", ""},
		{"pem", "-----BEGIN RSA PRIVATE KEY-----\nMII...\n", ""},
		{"clean text passes through", "the daily invoice flow failed at node charge", "the daily invoice flow failed at node charge"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ScrubSecrets(c.in)
			if c.want != "" {
				if got != c.want {
					t.Errorf("ScrubSecrets(%q) = %q, want unchanged %q", c.in, got, c.want)
				}
				return
			}
			// Secret must be gone and the marker present.
			if !strings.Contains(got, redactedSecretMarker) {
				t.Errorf("ScrubSecrets(%q) = %q, expected a redaction marker", c.in, got)
			}
			for _, tok := range []string{"sk_live_abcdefgh12345678", "ghp_0123456789abcdefghij0123",
				"xoxb-1234567890-abcdefghij", "AKIAIOSFODNN7EXAMPLE", "BEGIN RSA PRIVATE KEY"} {
				if strings.Contains(c.in, tok) && strings.Contains(got, tok) {
					t.Errorf("secret %q survived scrub: %q", tok, got)
				}
			}
		})
	}
}
