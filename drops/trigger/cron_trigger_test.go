package trigger

import (
	"strings"
	"testing"
	"time"

	"git.sr.ht/~klahr/hazyflow/core"
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
}

func TestCronTrigger_NoTimezoneIsUTC(t *testing.T) {
	// A zone-less schedule is interpreted as UTC by the scheduler, so the
	// fire stamp matches: a "Z" suffix.
	res, _ := executeCronTrigger(t.Context(), core.Job{}, nil)
	ts := res.Output["fired_at"].Inline.(string)
	if !strings.HasSuffix(ts, "Z") {
		t.Errorf("fired_at %q should be UTC (Z suffix) when no tz is set", ts)
	}
}

func TestCronTrigger_StampsConfiguredTimezone(t *testing.T) {
	// The whole point: when the node carries a tz (the editor stamps the
	// author's browser zone), fired_at reads as the wall-clock time in THAT
	// zone — so the author doesn't have to convert UTC in their head. We
	// assert the offset matches the zone's offset at the fire instant, which
	// is DST-correct (e.g. +02:00 in summer, +01:00 in winter for Stockholm).
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
