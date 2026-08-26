// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package datenames

import (
	"strings"
	"testing"
	"time"
)

func TestFor(t *testing.T) {
	// Only the primary subtag decides, so a region tag is not a trap.
	for _, code := range []string{"sv", "sv-SE", "SV", "sv_FI", " sv "} {
		if got := For(code).Days[int(time.Monday)]; got != "måndag" {
			t.Errorf("For(%q) → %q, want måndag", code, got)
		}
	}
	// Anything we don't carry reads as English rather than as blanks.
	for _, code := range []string{"", "en", "en-GB", "de", "zz", "nonsense"} {
		if got := For(code).Days[int(time.Monday)]; got != "Monday" {
			t.Errorf("For(%q) → %q, want Monday", code, got)
		}
	}
}

// Casing is data, not a rule: Swedish writes day and month names lowercase,
// and a formatter that capitalised them would be as wrong there as lowercasing
// them would be in English.
func TestSwedishIsLowercase(t *testing.T) {
	sv := For("sv")
	for _, set := range [][]string{sv.Days[:], sv.DaysShort[:], sv.Months[:], sv.MonthsShort[:]} {
		for _, name := range set {
			if name != strings.ToLower(name) {
				t.Errorf("%q is not lowercase", name)
			}
		}
	}
	en := For("en")
	for _, name := range append(en.Days[:], en.Months[:]...) {
		if name == strings.ToLower(name) {
			t.Errorf("%q should be capitalised in English", name)
		}
	}
}

func TestFormatDate(t *testing.T) {
	d := time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC)
	if got := FormatDate(d, "en"); got != "27 August 2026" {
		t.Errorf("en = %q, want 27 August 2026", got)
	}
	if got := FormatDate(d, "sv"); got != "27 augusti 2026" {
		t.Errorf("sv = %q, want 27 augusti 2026", got)
	}
	// An unknown language is English, not blank.
	if got := FormatDate(d, "de"); got != "27 August 2026" {
		t.Errorf("de = %q, want the English fallback", got)
	}
}
