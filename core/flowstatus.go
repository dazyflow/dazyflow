// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import "strings"

type FlowRunStatus string

const (
	FlowPaused       FlowRunStatus = "paused"
	FlowManual       FlowRunStatus = "manual"
	FlowLive         FlowRunStatus = "live"
	FlowNeedsPublish FlowRunStatus = "needs_publish"
)

func FlowRunStatusOf(g Graph) FlowRunStatus {
	if g.Disabled {
		return FlowPaused
	}
	if HasConfiguredAutoTrigger(g) {
		return FlowLive
	}
	return FlowManual
}

func FlowRunStatusPublished(g Graph, published bool) FlowRunStatus {
	s := FlowRunStatusOf(g)
	if s == FlowLive && !published {
		return FlowNeedsPublish
	}
	return s
}

// Fired by an inbound provider event, so there is no interval or secret to check
// — the node's presence makes the flow live, the fan-out matching on module id
// alone. TestEventTriggerModulesMatchCatalog keeps this in lockstep.
var EventTriggerModules = map[string]bool{
	"slack_on_mention":                true,
	"github_on_push":                  true,
	"github_on_new_pr":                true,
	"stripe_on_payment":               true,
	"stripe_on_payment_failed":        true,
	"stripe_on_subscription_canceled": true,
	"homeassistant_state_changed":     true,
}

// Presence only, deliberately: this answers "is this the node a run STARTED
// from" rather than "is it configured to fire".
func IsTriggerModule(module string) bool {
	switch module {
	case WebhookInputModule, FormInputModule, RequestInputModule,
		"cron_trigger", "poll_trigger", "google_form_trigger",
		"ticketmaster_on_new_event":
		return true
	}
	return EventTriggerModules[module]
}

// Whether the trigger's data ARRIVES with an external delivery, which is what
// makes a run replayable: a delivery happened once and cannot be re-derived, so a
// replay must feed the step the payload the original run received.
func IsInboundEventTriggerModule(module string) bool {
	return module == WebhookInputModule || module == FormInputModule ||
		module == RequestInputModule || EventTriggerModules[module]
}

// The other half: the scheduler starts these, and they derive the fire moment
// themselves rather than receiving it. That is what makes them runnable
// standalone — and why a run something else started has to skip them, rather
// than let one invent a moment that never happened.
//
// Together with IsInboundEventTriggerModule this partitions IsTriggerModule;
// TestTriggerModulesArePartitioned holds that.
func IsScheduledTriggerModule(module string) bool {
	switch module {
	case "cron_trigger", "poll_trigger", "google_form_trigger",
		"ticketmaster_on_new_event":
		return true
	}
	return false
}

func classifyTriggers(g Graph) (hasScheduler, hasWebhook, hasEvent bool) {
	for _, tr := range g.Triggers {
		if tr.Type == "cron" && strings.TrimSpace(tr.Cron) != "" {
			hasScheduler = true
		}
	}
	for _, n := range g.Nodes {
		switch n.Module {
		case "cron_trigger":
			if expr, _ := n.Params["cron"].(string); strings.TrimSpace(expr) != "" {
				hasScheduler = true
			}
		case "poll_trigger", "google_form_trigger", "ticketmaster_on_new_event":
			if secs, ok := paramInt(n.Params, "interval_seconds"); ok && secs > 0 && secs <= MaxPollIntervalSeconds {
				hasScheduler = true
			}
		case WebhookInputModule:
			if len(WebhookSecrets(n.Params)) > 0 || WebhookPublic(n.Params) {
				hasWebhook = true
			}
		case FormInputModule:
			hasWebhook = true
		case RequestInputModule:
			if len(WebhookSecrets(n.Params)) > 0 || WebhookPublic(n.Params) {
				hasWebhook = true
			}
		default:
			// Node-level Disabled is deliberately NOT checked, because the runtime does not
			// check it either: the fan-outs match on module id and /trigger consults only the
			// whole-flow switch, so such a flow really does still fire.
			if EventTriggerModules[n.Module] {
				hasEvent = true
			}
		}
	}
	return hasScheduler, hasWebhook, hasEvent
}

// A trigger actually CONFIGURED to fire, not merely present, with each rule in
// lockstep with what the runtime honors. Deprecated graph-level webhook and poll
// triggers are not counted: the runtime ignores them.
func HasConfiguredAutoTrigger(g Graph) bool {
	hasScheduler, hasWebhook, hasEvent := classifyTriggers(g)
	return hasScheduler || hasWebhook || hasEvent
}
