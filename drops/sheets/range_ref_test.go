// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package sheets

import "testing"

func TestSheetRangeRef(t *testing.T) {
	for in, want := range map[string]string{
		"Sheet1":       "Sheet1",
		"MyNamedRange": "MyNamedRange",
		"Q1":           "'Q1'",     // would be read as cell Q1
		"FY2024":       "'FY2024'", // would be read as cell FY2024
		"R1C1":         "'R1C1'",
		"Inbox Log":    "'Inbox Log'",
		"Sales-2024":   "'Sales-2024'",
		"Bob's":        "'Bob''s'",
		"Sheet1!A1:C3": "Sheet1!A1:C3",
		"'Q1'":         "'Q1'",
	} {
		if got := sheetRangeRef(in); got != want {
			t.Errorf("sheetRangeRef(%q) = %q, want %q", in, got, want)
		}
	}
}
