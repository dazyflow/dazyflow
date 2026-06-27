// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package limits

import (
	"testing"
)

func TestMaxRows_DefaultAndOverride(t *testing.T) {
	if got := MaxRows(); got != DefaultMaxRows {
		t.Fatalf("default MaxRows = %d, want %d", got, DefaultMaxRows)
	}
	restore := SetMaxRows(42)
	if got := MaxRows(); got != 42 {
		t.Fatalf("after SetMaxRows(42), MaxRows = %d", got)
	}
	restore()
	if got := MaxRows(); got != DefaultMaxRows {
		t.Fatalf("after restore, MaxRows = %d, want %d", got, DefaultMaxRows)
	}
}

func TestEnvInt(t *testing.T) {
	const key = "DAZYFLOW_TEST_MAX_ROWS_COV"
	cases := []struct {
		name string
		set  bool
		val  string
		want int
	}{
		{"unset uses default", false, "", 7},
		{"empty uses default", true, "", 7},
		{"valid positive", true, "123", 123},
		{"zero rejected", true, "0", 7},
		{"negative rejected", true, "-5", 7},
		{"non-numeric rejected", true, "abc", 7},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.set {
				t.Setenv(key, c.val)
			} else {
				// t.Setenv on a sibling key keeps this key unset for the subtest.
				t.Setenv("DAZYFLOW_TEST_UNRELATED", "x")
			}
			if got := envInt(key, 7); got != c.want {
				t.Fatalf("envInt(%q) = %d, want %d", c.val, got, c.want)
			}
		})
	}
}
