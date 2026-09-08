// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package trigger

import (
	"strings"
	"testing"
	"time"

	"github.com/dazyflow/dazyflow/core"
)

func TestPollTrigger_EmitsRFC3339Timestamp(t *testing.T) {
	res, err := executePollTrigger(t.Context(), core.Job{}, nil)
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

func TestPollTrigger_ManualRunSucceedsWithoutTrigger(t *testing.T) {
	res, _ := executePollTrigger(t.Context(), core.Job{ID: "manual"}, nil)
	if res.Status != core.StatusOK {
		t.Errorf("manual run should succeed; got %q (%+v)", res.Status, res.Error)
	}
}

func TestPollTrigger_TimestampIsUTC(t *testing.T) {
	// RFC3339 with the "Z" suffix is UTC; "+02:00" etc. would be
	// local time. Logs and scheduling reason about UTC universally,
	// so the trigger emits UTC explicitly.
	res, _ := executePollTrigger(t.Context(), core.Job{}, nil)
	ts := res.Output["fired_at"].Inline.(string)
	if !strings.HasSuffix(ts, "Z") {
		t.Errorf("fired_at %q is not UTC (no Z suffix)", ts)
	}
}
