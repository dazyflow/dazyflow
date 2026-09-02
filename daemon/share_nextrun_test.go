// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"testing"
	"time"

	"github.com/dazyflow/dazyflow/core"
)

// fixedNow is a deterministic instant so cron projections don't depend on the
// wall clock (and don't drift across the UTC-midnight boundary like the old
// share test did).
var nextRunNow = time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)

func TestNextScheduledFire_CronNode(t *testing.T) {
	t.Parallel()
	g := core.Graph{Nodes: []core.Node{
		{ID: "c", Module: "cron_trigger", Params: map[string]any{"cron": "*/5 * * * *"}},
	}}
	got := nextScheduledFire(g, nextRunNow)
	if got == nil {
		t.Fatal("want a next fire for a live cron flow, got nil")
	}
	// */5 from 12:00:00 → 12:05:00 UTC.
	want := time.Date(2026, 1, 15, 12, 5, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("next = %v, want %v", got, want)
	}
}

func TestNextScheduledFire_CronWithTZ(t *testing.T) {
	t.Parallel()
	// 09:00 daily in a +01:00 zone is 08:00 UTC. From 12:00 UTC the next is
	// the following day 08:00 UTC.
	g := core.Graph{Nodes: []core.Node{
		{ID: "c", Module: "cron_trigger", Params: map[string]any{"cron": "0 9 * * *", "tz": "Europe/Stockholm"}},
	}}
	got := nextScheduledFire(g, nextRunNow)
	if got == nil {
		t.Fatal("want a next fire, got nil")
	}
	if got.Hour() != 8 || got.Minute() != 0 { // UTC
		t.Errorf("tz-aware next = %v, want 08:00 UTC", got)
	}
}

func TestNextScheduledFire_PollNode(t *testing.T) {
	t.Parallel()
	g := core.Graph{Nodes: []core.Node{
		{ID: "p", Module: "poll_trigger", Params: map[string]any{"interval_seconds": 60}},
	}}
	got := nextScheduledFire(g, nextRunNow)
	if got == nil || !got.Equal(nextRunNow.Add(60*time.Second)) {
		t.Errorf("poll next = %v, want now+60s", got)
	}
}

func TestNextScheduledFire_GraphLevelCron(t *testing.T) {
	t.Parallel()
	g := core.Graph{Triggers: []core.GraphTrigger{{Type: "cron", Cron: "*/5 * * * *"}}}
	got := nextScheduledFire(g, nextRunNow)
	if got == nil || !got.Equal(time.Date(2026, 1, 15, 12, 5, 0, 0, time.UTC)) {
		t.Errorf("graph-level cron next = %v", got)
	}
}

func TestNextScheduledFire_EarliestWins(t *testing.T) {
	t.Parallel()
	// A daily cron (far) plus a 60s poll (soon): the poll's sooner fire wins.
	g := core.Graph{Nodes: []core.Node{
		{ID: "c", Module: "cron_trigger", Params: map[string]any{"cron": "0 0 * * *"}},
		{ID: "p", Module: "poll_trigger", Params: map[string]any{"interval_seconds": 60}},
	}}
	got := nextScheduledFire(g, nextRunNow)
	if got == nil || !got.Equal(nextRunNow.Add(60*time.Second)) {
		t.Errorf("earliest = %v, want the 60s poll", got)
	}
}

func TestNextScheduledFire_FlowDisabled(t *testing.T) {
	t.Parallel()
	g := core.Graph{
		Disabled: true,
		Nodes:    []core.Node{{ID: "c", Module: "cron_trigger", Params: map[string]any{"cron": "*/5 * * * *"}}},
	}
	if got := nextScheduledFire(g, nextRunNow); got != nil {
		t.Errorf("disabled flow should have no next fire, got %v", got)
	}
}

func TestNextScheduledFire_TriggerDisabled(t *testing.T) {
	t.Parallel()
	g := core.Graph{Nodes: []core.Node{
		{ID: "c", Module: "cron_trigger", Params: map[string]any{"cron": "*/5 * * * *", "disabled": true}},
	}}
	if got := nextScheduledFire(g, nextRunNow); got != nil {
		t.Errorf("paused trigger should have no next fire, got %v", got)
	}
}

func TestNextScheduledFire_ManualAndWebhookAndBadInput(t *testing.T) {
	t.Parallel()
	// No triggers → manual → nil.
	if got := nextScheduledFire(core.Graph{Nodes: []core.Node{{ID: "n", Module: "noop"}}}, nextRunNow); got != nil {
		t.Errorf("manual flow next = %v, want nil", got)
	}
	// Webhook trigger doesn't fire on a clock → nil.
	if got := nextScheduledFire(core.Graph{Nodes: []core.Node{{ID: "w", Module: "webhook_input", Params: map[string]any{"public_form": true}}}}, nextRunNow); got != nil {
		t.Errorf("webhook flow next = %v, want nil", got)
	}
	// Blank cron / zero interval / out-of-range interval → nil.
	if got := nextScheduledFire(core.Graph{Nodes: []core.Node{{ID: "c", Module: "cron_trigger", Params: map[string]any{"cron": "  "}}}}, nextRunNow); got != nil {
		t.Errorf("blank cron next = %v, want nil", got)
	}
	if got := nextScheduledFire(core.Graph{Nodes: []core.Node{{ID: "p", Module: "poll_trigger", Params: map[string]any{"interval_seconds": 0}}}}, nextRunNow); got != nil {
		t.Errorf("zero interval next = %v, want nil", got)
	}
	if got := nextScheduledFire(core.Graph{Nodes: []core.Node{{ID: "p", Module: "poll_trigger", Params: map[string]any{"interval_seconds": core.MaxPollIntervalSeconds + 1}}}}, nextRunNow); got != nil {
		t.Errorf("out-of-range interval next = %v, want nil", got)
	}
	// Invalid cron expression → nil (parse fails, not a panic).
	if got := nextScheduledFire(core.Graph{Nodes: []core.Node{{ID: "c", Module: "cron_trigger", Params: map[string]any{"cron": "not a cron"}}}}, nextRunNow); got != nil {
		t.Errorf("invalid cron next = %v, want nil", got)
	}
}
