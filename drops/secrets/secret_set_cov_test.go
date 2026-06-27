// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package secrets

import (
	"strings"
	"testing"
)

// TestValidSecretName_Boundaries covers the length branches of validSecretName
// directly: empty, too-long (>128), at-limit (128, accepted), and a valid name.
func TestValidSecretName_Boundaries(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		wantErr string // substring; "" means accept
	}{
		{"empty", "", "empty"},
		{"too long", strings.Repeat("a", 129), "too long"},
		{"at limit accepted", strings.Repeat("a", 128), ""},
		{"valid mixed", "poll.cursor-1_X", ""},
		{"bad char", "has space", "may only contain"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validSecretName(c.input)
			if c.wantErr == "" {
				if err != nil {
					t.Fatalf("validSecretName(%q) = %v, want nil", c.input, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("validSecretName(%q) = %v, want substring %q", c.input, err, c.wantErr)
			}
		})
	}
}
