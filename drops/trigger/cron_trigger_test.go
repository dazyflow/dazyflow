// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package trigger

import (
	"strings"
	"testing"
	"time"

	"github.com/dazyflow/dazyflow/core"
)

func TestCronTrigger_EmitsRFC3339Timestamp(t *testing.T) {
	res, err := executeCronTrigger(t.Context(), core.Job{}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	ts, ok := res.Output["fired_at"].Inline.(string)
	if !ok {
		t.Fatalf("fired_at = %T, want string", res.Output["fired_at"].Inline)
	}
	parsed, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		t.Fatalf("fired_at %q is not RFC3339: %v", ts, err)
	}
	if time.Since(parsed) > 5*time.Second {
		t.Errorf("fired_at %v is older than 5s — clock weirdness?", parsed)
	}
	if got := res.Output[core.PassPort].Inline; got != ts {
		t.Errorf("pass = %v, want it to mirror fired_at %q", got, ts)
	}
}

func TestCronTrigger_NoTimezoneIsUTC(t *testing.T) {
	res, _ := executeCronTrigger(t.Context(), core.Job{}, nil)
	ts := res.Output["fired_at"].Inline.(string)
	if !strings.HasSuffix(ts, "Z") {
		t.Errorf("fired_at %q should be UTC (Z suffix) when no tz is set", ts)
	}
}

func TestCronTrigger_StampsConfiguredTimezone(t *testing.T) {
	res, err := executeCronTrigger(t.Context(), core.Job{
		Params: map[string]any{"tz": "Europe/Stockholm"},
	}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	ts := res.Output["fired_at"].Inline.(string)
	parsed, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		t.Fatalf("fired_at %q is not RFC3339: %v", ts, err)
	}
	if strings.HasSuffix(ts, "Z") {
		t.Errorf("fired_at %q should carry the Stockholm offset, not UTC Z", ts)
	}
	loc, err := time.LoadLocation("Europe/Stockholm")
	if err != nil {
		t.Skipf("tzdata for Europe/Stockholm unavailable: %v", err)
	}
	_, wantOffset := time.Now().In(loc).Zone()
	_, gotOffset := parsed.Zone()
	if gotOffset != wantOffset {
		t.Errorf("fired_at offset = %ds, want %ds (Stockholm)", gotOffset, wantOffset)
	}
}

func TestCronTrigger_InvalidTimezoneFallsBackToUTC(t *testing.T) {
	// A bogus tz must not fail the fire — it falls back to UTC rather than
	// erroring, so a typo in the zone never silently breaks the schedule's
	// downstream steps.
	res, err := executeCronTrigger(t.Context(), core.Job{
		Params: map[string]any{"tz": "Mars/Olympus_Mons"},
	}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	ts := res.Output["fired_at"].Inline.(string)
	if !strings.HasSuffix(ts, "Z") {
		t.Errorf("fired_at %q should fall back to UTC (Z) for an invalid tz", ts)
	}
}
