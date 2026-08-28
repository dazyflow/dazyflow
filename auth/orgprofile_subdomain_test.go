// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"errors"
	"testing"
)

func TestValidateSubdomain(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"", "", false},                // empty clears — valid
		{"  ", "", false},              // whitespace clears
		{"klahr", "klahr", false},      // simple label
		{"Klahr", "klahr", false},      // lowercased
		{"  Acme-2 ", "acme-2", false}, // trimmed + lowercased + hyphen ok
		{"a", "a", false},              // single char
		{"a1-b2", "a1-b2", false},
		{"-bad", "", true}, // leading hyphen
		{"bad-", "", true}, // trailing hyphen
		{"has space", "", true},
		{"under_score", "", true},
		{"www", "", true},   // reserved
		{"api", "", true},   // reserved
		{"admin", "", true}, // reserved
		{"a.b", "", true},   // dots not allowed in a single label
		{"a..b", "", true},
	}
	for _, c := range cases {
		got, err := ValidateSubdomain(c.in)
		if c.wantErr {
			if !errors.Is(err, ErrInvalidSubdomain) {
				t.Errorf("ValidateSubdomain(%q) err = %v, want ErrInvalidSubdomain", c.in, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("ValidateSubdomain(%q) unexpected err %v", c.in, err)
		}
		if got != c.want {
			t.Errorf("ValidateSubdomain(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestValidateSubdomain_LengthCap(t *testing.T) {
	// 63 chars is the max DNS label.
	ok := ""
	for range 63 {
		ok += "a"
	}
	if _, err := ValidateSubdomain(ok); err != nil {
		t.Errorf("63-char label should be valid: %v", err)
	}
	if _, err := ValidateSubdomain(ok + "a"); !errors.Is(err, ErrInvalidSubdomain) {
		t.Error("64-char label should be rejected")
	}
}
