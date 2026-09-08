// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"fmt"
	"strings"

	"github.com/robfig/cron/v3"

	"github.com/dazyflow/dazyflow/core"
)

var scheduleCronParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

type ScheduleSpec struct {
	Tenant    string
	Workspace string
	GraphID   string

	// EntryKey uniquely identifies this enrollment. It must stay stable
	// across rescans: carrying a live entry's next-fire time and poll
	// backoff forward is keyed on it, and so is the deterministic poll
	// stagger, so a changed key reshuffles a flow's fire time.
	EntryKey string

	// SpecKey identifies the cadence itself. A change re-anchors the next
	// fire, which is what makes an edited schedule take effect immediately
	// rather than after the old one's next tick.
	SpecKey string

	Cron            string
	TZ              string
	IntervalSeconds int
}

// IsPoll reports whether this spec is interval-driven rather than cron-driven.
func (s ScheduleSpec) IsPoll() bool { return s.IntervalSeconds > 0 }

func graphTriggerEntryKey(tenant, workspace, graphID, specKey string) string {
	return fmt.Sprintf("%s/%s/%s#%s", tenant, workspace, graphID, specKey)
}

func nodeTriggerEntryKey(tenant, workspace, graphID, nodeID string) string {
	return fmt.Sprintf("%s/%s/%s@%s", tenant, workspace, graphID, nodeID)
}

func cronSpecKey(expr, tz string) string { return "cron:" + expr + "|" + tz }

func pollSpecKey(seconds int) string { return fmt.Sprintf("poll:%d", seconds) }

// DeriveScheduleSpecs returns every scheduler enrollment a flow asks for.
// Pure apart from logf, which reports specs dropped as malformed.
//
// It reads the DRAFT graph deliberately: timing and pause changes take effect
// as soon as they are saved, while fireGraph runs the published revision. The
// caller gates enrollment on the flow being published — that state lives in a
// git tag, not in the graph.
//
// A disabled flow yields nothing, which is what pauses every one of its
// triggers at once.
func DeriveScheduleSpecs(parser cron.Parser, tenant, workspace string, g core.Graph, logf func(string, ...any)) []ScheduleSpec {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	if g.Disabled {
		return nil
	}
	// EntryKey is unique per enrollment, and the durable store enforces that as
	// a primary key — so duplicates must collapse HERE, not at whatever the
	// consumer happens to be. A flow carrying the same trigger twice is the
	// case that produces them, and it is not rare: duplicating a step in the
	// editor duplicates its trigger. Emitting both once cost the flow every one
	// of its schedules, because the insert of the second row rolled back the
	// first.
	var out []ScheduleSpec
	seen := make(map[string]struct{})
	add := func(spec ScheduleSpec) {
		if _, dup := seen[spec.EntryKey]; dup {
			return
		}
		seen[spec.EntryKey] = struct{}{}
		out = append(out, spec)
	}

	for _, t := range g.Triggers {
		if t.Type != "cron" || t.Cron == "" {
			continue
		}
		if _, err := parseCronInTZ(parser, t.Cron, t.TZ); err != nil {
			logf("bad cron %q (tz %q) on %s/%s/%s: %v", t.Cron, t.TZ, tenant, workspace, g.ID, err)
			continue
		}
		specKey := cronSpecKey(t.Cron, t.TZ)
		add(ScheduleSpec{
			Tenant:    tenant,
			Workspace: workspace,
			GraphID:   g.ID,
			EntryKey:  graphTriggerEntryKey(tenant, workspace, g.ID, specKey),
			SpecKey:   specKey,
			Cron:      t.Cron,
			TZ:        t.TZ,
		})
	}

	for _, node := range g.Nodes {
		if triggerNodeDisabled(node) {
			continue // this trigger is individually paused
		}
		switch node.Module {
		case "cron_trigger":
			expr := strings.TrimSpace(paramString(node.Params, "cron"))
			if expr == "" {
				continue // unscheduled node — runs only on manual Run
			}
			tz := paramString(node.Params, "tz")
			if _, err := parseCronInTZ(parser, expr, tz); err != nil {
				logf("bad cron %q (tz %q) on node %s of %s/%s/%s: %v",
					expr, tz, node.ID, tenant, workspace, g.ID, err)
				continue
			}
			add(ScheduleSpec{
				Tenant:    tenant,
				Workspace: workspace,
				GraphID:   g.ID,
				EntryKey:  nodeTriggerEntryKey(tenant, workspace, g.ID, node.ID),
				SpecKey:   cronSpecKey(expr, tz),
				Cron:      expr,
				TZ:        tz,
			})

		case "poll_trigger", "google_form_trigger", "ticketmaster_on_new_event":
			secs := paramSeconds(node.Params, "interval_seconds")
			if secs == 0 {
				continue // unset — runs only on manual Run
			}
			// Reject <= 0 and anything past the ceiling: IntervalSeconds *
			// time.Second overflows int64 ns past ~292y, which would read as
			// "due now" and fire every tick.
			if secs < 0 || secs > core.MaxPollIntervalSeconds {
				logf("bad poll interval %d on node %s of %s/%s/%s", secs, node.ID, tenant, workspace, g.ID)
				continue
			}
			add(ScheduleSpec{
				Tenant:          tenant,
				Workspace:       workspace,
				GraphID:         g.ID,
				EntryKey:        nodeTriggerEntryKey(tenant, workspace, g.ID, node.ID),
				SpecKey:         pollSpecKey(secs),
				IntervalSeconds: secs,
			})
		}
	}
	return out
}

func paramString(params map[string]any, key string) string {
	v, _ := params[key].(string)
	return v
}
