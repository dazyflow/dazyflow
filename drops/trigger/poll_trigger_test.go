package trigger

import (
	"strings"
	"testing"
	"time"

	"git.sr.ht/~klahr/hazyflow/core"
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
	// Sanity: should be within the last few seconds.
	if time.Since(parsed) > 5*time.Second {
		t.Errorf("fired_at %v is older than 5s — clock weirdness?", parsed)
	}
}

func TestPollTrigger_ManualRunSucceedsWithoutTrigger(t *testing.T) {
	// Unlike webhook_input (which errors when run without a trigger
	// because there's no body), poll_trigger has no input data —
	// "the time" is intrinsic. A manual run is just a one-off fire,
	// which is the right UX for "test this poll workflow now."
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
