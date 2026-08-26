// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package datetime

import (
	"git.sr.ht/~klahr/dazyflow/internal/datenames"
	"strings"
	"testing"
	"time"
)

// A Thursday afternoon, so the weekday and the 12-hour clock are both
// distinguishable from their alternatives in the assertions below.
var ref = time.Date(2026, 8, 27, 14, 5, 9, 0, time.UTC)

func TestRenderCustom(t *testing.T) {
	cases := []struct{ name, format, want string }{
		{"the spelling everyone types", "YYYY-MM-DD", "2026-08-27"},
		{"European date", "DD/MM/YYYY", "27/08/2026"},
		{"two-digit year", "DD.MM.YY", "27.08.26"},
		{"unpadded day and month", "D/M/YYYY", "27/8/2026"},
		{"month names", "D MMM YYYY", "27 Aug 2026"},
		{"long month name", "D MMMM YYYY", "27 August 2026"},
		{"weekday names", "ddd D MMM", "Thu 27 Aug"},
		{"long weekday", "dddd", "Thursday"},
		{"24-hour clock", "HH:mm:ss", "14:05:09"},
		{"12-hour clock", "hh:mm A", "02:05 PM"},
		{"lowercase meridiem", "h:mm a", "2:05 pm"},
		// The distinction the tokens are case-sensitive FOR: getting these two
		// the wrong way round is the mistake the whole vocabulary risks.
		{"MM is the month, mm the minute", "MM mm", "08 05"},
		{"date and time together", "YYYY-MM-DD HH:mm", "2026-08-27 14:05"},
		{"zone offset", "YYYY-MM-DDZ", "2026-08-27+00:00"},
		{"zone name", "HH:mm z", "14:05 UTC"},
		// Literals: punctuation and digits pass through, and a bracketed word
		// survives even though its letters are all tokens.
		{"bracketed literal", "[week of] D MMM", "week of 27 Aug"},
		{"literal made of token letters", "[Monday] [and] [DD]", "Monday and DD"},
		{"tokens with no separator", "YYYYMMDD", "20260827"},
		{"punctuation only", "--/--", "--/--"},
		// Only NON-LETTERS pass through unbracketed; a word is a word even
		// when it's Swedish, so "år" goes in brackets like any other.
		{"literal word in brackets", "D MMM [år] YYYY", "27 Aug år 2026"},
		{"empty", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := renderCustom(ref, c.format, datenames.English)
			if err != nil {
				t.Fatalf("renderCustom(%q): %v", c.format, err)
			}
			if got != c.want {
				t.Errorf("renderCustom(%q) = %q, want %q", c.format, got, c.want)
			}
		})
	}
}

// The reason this renderer exists: an unrecognised token must fail the step.
// Printing it verbatim is what put the literal text "YYYY-MM-DD" into people's
// email, and a format that is wrong is worth a red node, not a silent guess.
func TestRenderCustom_RejectsUnknownTokens(t *testing.T) {
	cases := []struct{ format, wantIn string }{
		// Lowercase spellings are the common near-miss, so they get named.
		{"yyyy-mm-dd", `did you mean "YYYY"`},
		// Go has no unpadded 24-hour hour, so H is not a token.
		{"H:mm", `did you mean "HH"`},
		// Anything else lists the vocabulary rather than guessing.
		{"YYYY quux DD", "isn't a format token"},
		// A repeated token letter renders a plausible-looking number rather
		// than failing, which is the worst possible outcome: "mmm" as mm+m.
		{"YYYY-mmm-DD", `did you mean "MMM"`},
		{"YYYYY", `did you mean "YYYY"`},
		{"DD of MMM", "isn't a format token"},
	}
	for _, c := range cases {
		t.Run(c.format, func(t *testing.T) {
			out, err := renderCustom(ref, c.format, datenames.English)
			if err == nil {
				t.Fatalf("renderCustom(%q) = %q, want an error", c.format, out)
			}
			if !strings.Contains(err.Error(), c.wantIn) {
				t.Errorf("error = %q, want it to contain %q", err, c.wantIn)
			}
		})
	}
}

func TestRenderCustom_UnclosedBracket(t *testing.T) {
	if _, err := renderCustom(ref, "[week of D MMM", datenames.English); err == nil {
		t.Fatal("an unclosed [ should be an error, not a silent literal")
	}
}

// Go reference layouts are what the format field took before it became a
// dropdown, and saved graphs still carry them.
func TestRenderLegacyFormat_KeepsGoLayouts(t *testing.T) {
	cases := []struct{ format, want string }{
		{"2006-01-02", "2026-08-27"},
		{"02/01/2006", "27/08/2026"},
		{"Mon 2 Jan 2006", "Thu 27 Aug 2026"},
		{"15:04", "14:05"},
	}
	for _, c := range cases {
		got, err := renderLegacyFormat(ref, c.format, datenames.English)
		if err != nil {
			t.Fatalf("renderLegacyFormat(%q): %v", c.format, err)
		}
		if got != c.want {
			t.Errorf("renderLegacyFormat(%q) = %q, want %q", c.format, got, c.want)
		}
	}
}

// The silent echo, fixed: time.Format consumed nothing from "YYYY-MM-DD", so
// it used to come back as itself and land in the message. Now the token
// vocabulary gets a turn.
func TestRenderLegacyFormat_FallsBackToTokens(t *testing.T) {
	got, err := renderLegacyFormat(ref, "YYYY-MM-DD", datenames.English)
	if err != nil {
		t.Fatalf("renderLegacyFormat: %v", err)
	}
	if got != "2026-08-27" {
		t.Errorf("got %q, want 2026-08-27 (not the format string itself)", got)
	}
	// And a format that is neither errors rather than shipping itself.
	if out, err := renderLegacyFormat(ref, "sometime soon", datenames.English); err == nil {
		t.Errorf("got %q, want an error", out)
	}
}

func TestParseClock(t *testing.T) {
	cases := []struct {
		in      string
		h, m, s int
	}{
		{"09:00", 9, 0, 0},
		{"9:00", 9, 0, 0},
		{"17:30:15", 17, 30, 15},
		{"00:00", 0, 0, 0},
		{"23:59:59", 23, 59, 59},
		{" 08:15 ", 8, 15, 0},
	}
	for _, c := range cases {
		h, m, s, err := parseClock(c.in)
		if err != nil {
			t.Errorf("parseClock(%q): %v", c.in, err)
			continue
		}
		if h != c.h || m != c.m || s != c.s {
			t.Errorf("parseClock(%q) = %d:%d:%d, want %d:%d:%d", c.in, h, m, s, c.h, c.m, c.s)
		}
	}
	// Out of range is a typo, not a request for the next day.
	for _, bad := range []string{"", "9", "25:00", "09:60", "09:00:60", "-1:00", "nine", "09:00:00:00", "09:xx"} {
		if _, _, _, err := parseClock(bad); err == nil {
			t.Errorf("parseClock(%q) should have errored", bad)
		}
	}
}
