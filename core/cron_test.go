// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"testing"
	"time"
)

// Existing flows were authored against robfig/cron, where a restricted
// day-of-month and day-of-week are ORed (Vixie cron). go-cron defaults to AND,
// which would silently change when those flows fire.
func TestCronParser_DomDowAreORed(t *testing.T) {
	sched, err := ParseCron(CronParser, "CRON_TZ=UTC 0 0 13 * FRI")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	// 2026-01-01 is a Thursday; the next Friday (Jan 2) is not the 13th.
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	want := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	if got := sched.Next(from); !got.Equal(want) {
		t.Errorf("next = %v, want %v (any Friday OR the 13th)", got, want)
	}
}

func TestCronParser_RejectsMalformed(t *testing.T) {
	for _, spec := range []string{
		"",
		"* * * *",
		"* * * * * *",
		"61 * * * *",
		"@yearly",
		"CRON_TZ=",
		"CRON_TZ=UTC",
		"TZ=UTC TZ=UTC * * * * *",
		"*/0 * * * *",
		"5-1-2 * * * *",
	} {
		if _, err := ParseCron(CronParser, spec); err == nil {
			t.Errorf("ParseCron(%q) accepted a malformed spec", spec)
		}
	}
}
