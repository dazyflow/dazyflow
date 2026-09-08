// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"testing"
	"time"

	"github.com/robfig/cron/v3"
)

func TestParseCronInTZ(t *testing.T) {
	t.Parallel()
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

	// A fixed instant well clear of any DST boundary: 2026-06-01 00:00 UTC.
	from := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	cases := []struct {
		name        string
		tz          string
		wantUTCHour int // hour-of-day, in UTC, of the next fire
	}{
		{"stockholm summer", "Europe/Stockholm", 7},
		{"new york summer", "America/New_York", 13},
		// Empty tz must mean UTC, never the host's local zone.
		{"empty defaults utc", "", 9},
		{"explicit utc", "UTC", 9},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sched, err := parseCronInTZ(parser, "0 9 * * *", tc.tz)
			if err != nil {
				t.Fatalf("parseCronInTZ(%q): %v", tc.tz, err)
			}
			next := sched.Next(from).UTC()
			if next.Hour() != tc.wantUTCHour {
				t.Errorf("tz %q: next fire %s, want hour %02d UTC",
					tc.tz, next.Format(time.RFC3339), tc.wantUTCHour)
			}
		})
	}
}

// Surfaces a malformed IANA name as an error (so the scheduler skips it and
// the validate endpoint reports it) rather than silently firing in the wrong
// zone.
func TestParseCronInTZ_BadZone(t *testing.T) {
	t.Parallel()
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	if _, err := parseCronInTZ(parser, "0 9 * * *", "Not/AZone"); err == nil {
		t.Fatal("expected error for bad timezone, got nil")
	}
}
