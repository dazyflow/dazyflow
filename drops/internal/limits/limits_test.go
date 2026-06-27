// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package limits

import "testing"

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
