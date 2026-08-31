// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import "strings"

// FlowRunStatus is the GUI-facing answer to "will this flow run on its
// own?" — the signal behind the editor/list status chip. It mirrors the
// scheduler's enrollment rule and the webhook/form endpoints' reachability
// so the chip tells the truth instead of merely reporting that a trigger
// node exists on the canvas.
type FlowRunStatus string

const (
	// FlowPaused: the flow is disabled. Disabled suspends ALL automatic
	// firing (scheduler + webhook/form), so it takes precedence over any
	// configured trigger. A manual Run still works.
	FlowPaused FlowRunStatus = "paused"
	// FlowManual: enabled, but nothing will fire it automatically — it has
	// no configured trigger (e.g. a poll/form node with a blank interval,
	// or a Schedule node with a blank cron). Runs only on a manual Run.
	FlowManual FlowRunStatus = "manual"
	// FlowLive: enabled AND carries at least one configured auto-trigger,
	// so the daemon will start it without anyone pressing Run.
	FlowLive FlowRunStatus = "live"
	// FlowNeedsPublish: enabled and carries a configured auto-trigger that
	// would fire on its own — but the flow has never been published, and NO
	// automatic path runs an unpublished flow. Publishing flips it to live.
	//
	// This used to apply only to scheduler triggers, because the webhook,
	// form and event endpoints fell back to HEAD and so did fire while
	// unpublished. That asymmetry is gone: "published" now means the same
	// thing whatever the trigger.
	FlowNeedsPublish FlowRunStatus = "needs_publish"
)

// FlowRunStatusOf classifies a flow. Disabled wins; otherwise a configured
// auto-trigger makes it live, and the absence of one makes it manual-only.
func FlowRunStatusOf(g Graph) FlowRunStatus {
	if g.Disabled {
		return FlowPaused
	}
	if HasConfiguredAutoTrigger(g) {
		return FlowLive
	}
	return FlowManual
}

// FlowRunStatusPublished is the publish-aware classifier used where the
// caller knows whether the flow has a published revision (the flow list and
// the editor both track publish state). It's FlowRunStatusOf plus one rule:
// a flow that would fire on its own but isn't published is "needs publish",
// because no automatic path runs an unpublished flow.
func FlowRunStatusPublished(g Graph, published bool) FlowRunStatus {
	s := FlowRunStatusOf(g)
	if s == FlowLive && !published {
		return FlowNeedsPublish
	}
	return s
}

// HasConfiguredSchedulerTrigger reports whether the flow has a configured
// trigger that fires via the SCHEDULER (cron / poll / google-form interval),
// as opposed to one that fires from an inbound HTTP request or provider
// event. Every kind is gated on publish now, so this is no longer a
// publish-state question — it is still what the scheduler uses to decide
// what to enroll.
func HasConfiguredSchedulerTrigger(g Graph) bool {
	hasScheduler, _, _ := classifyTriggers(g)
	return hasScheduler
}

// HasConfiguredWebhookTrigger reports whether the flow has a REACHABLE
// webhook_input — one carrying a secret key or a public hosted form. A
// webhook node with neither is inert: the /trigger endpoint rejects every
// inbound call, so it does not make the flow live.
func HasConfiguredWebhookTrigger(g Graph) bool {
	_, hasWebhook, _ := classifyTriggers(g)
	return hasWebhook
}

// HasEventTrigger reports whether the flow carries an inbound provider-event
// trigger (a Slack mention, a GitHub push, a Stripe payment …) — the nodes the
// daemon's event fan-outs pre-complete when a provider calls in. Used by the
// upgrade migration to identify flows that were firing through the old
// fall-back-to-HEAD behaviour.
func HasEventTrigger(g Graph) bool {
	_, _, hasEvent := classifyTriggers(g)
	return hasEvent
}

// EventTriggerModules are the trigger drops fired by an inbound provider
// event rather than by the scheduler or the /trigger webhook. They carry no
// interval or secret to check — the node's mere presence makes the flow live,
// because the daemon's fan-out matches on the module ID alone.
//
// Kept in lockstep with the catalog by TestEventTriggerModulesMatchCatalog,
// which fails if a manifest in category "trigger" is neither listed here nor
// one of the known scheduler/webhook modules. Add a new *_on_* trigger drop
// and that test tells you to come here.
var EventTriggerModules = map[string]bool{
	"slack_on_mention":                true,
	"github_on_push":                  true,
	"github_on_new_pr":                true,
	"stripe_on_payment":               true,
	"stripe_on_payment_failed":        true,
	"stripe_on_subscription_canceled": true,
	"homeassistant_state_changed":     true,
}

// IsTriggerModule reports whether a module is a graph ENTRY POINT — the
// scheduler modules, the webhook, or an inbound provider event.
//
// Presence only, deliberately: this answers "is this the node a run STARTED
// from", not "is it configured to fire". A run that is executing already
// settled the second question, and ${trigger.…} needs the first.
//
// EventTriggerModules is kept in lockstep with the catalog by
// TestEventTriggerModulesMatchCatalog, so a new *_on_* trigger drop is
// covered here the moment that test tells you to list it.
func IsTriggerModule(module string) bool {
	switch module {
	case "webhook_input", "cron_trigger", "poll_trigger", "google_form_trigger":
		return true
	}
	return EventTriggerModules[module]
}

// classifyTriggers scans g once and reports the three ways a flow can fire on
// its own: the SCHEDULER (graph-level cron, cron_trigger, or a
// poll_trigger/google_form_trigger with a valid interval), a reachable
// webhook_input, and an inbound provider event (EventTriggerModules). The
// public classifiers are thin views over this.
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
		case "poll_trigger", "google_form_trigger":
			if secs, ok := paramInt(n.Params, "interval_seconds"); ok && secs > 0 && secs <= MaxPollIntervalSeconds {
				hasScheduler = true
			}
		case "webhook_input":
			publicForm, _ := n.Params["public_form"].(bool)
			if len(WebhookSecrets(n.Params)) > 0 || publicForm {
				hasWebhook = true
			}
		default:
			// Provider-event triggers. Node-level Disabled is deliberately NOT
			// checked, because the runtime doesn't check it either: the event
			// fan-outs match on module ID alone and the /trigger endpoint only
			// consults the whole-flow switch, so such a flow really does still
			// fire (the worker then records the disabled node as skipped).
			// Mirroring that keeps the chip honest. Whether those paths SHOULD
			// honour a disabled trigger node is a separate question.
			if EventTriggerModules[n.Module] {
				hasEvent = true
			}
		}
	}
	return hasScheduler, hasWebhook, hasEvent
}

// HasConfiguredAutoTrigger reports whether the flow has at least one
// trigger that is actually configured to fire — not merely present on the
// canvas. The rules are kept in lockstep with what the runtime honors:
//
//   - cron: graph-level cron with a non-blank expression, or a cron_trigger
//     node with a non-blank cron param (the scheduler fires both).
//   - poll / google form: a poll_trigger or google_form_trigger node whose
//     interval_seconds is a positive value within the scheduler's ceiling
//     (the scheduler skips a zero/blank or out-of-range interval).
//   - webhook: a webhook_input node that is reachable — it has a secret or
//     a public hosted form (otherwise the /trigger endpoint rejects every
//     inbound call, mirroring lintTriggers' "no secret, no form" warning).
//   - provider event: an enabled EventTriggerModules node (a Slack mention, a
//     GitHub push, a Stripe payment …). These were previously NOT counted, so
//     a flow whose only trigger was "On mention" reported as manual-only in
//     the UI while firing on every mention.
//
// Deprecated graph-level webhook/poll triggers are intentionally NOT counted:
// the runtime ignores them (see lintTriggers), so a flow carrying only one
// of those is manual-only in practice.
func HasConfiguredAutoTrigger(g Graph) bool {
	hasScheduler, hasWebhook, hasEvent := classifyTriggers(g)
	return hasScheduler || hasWebhook || hasEvent
}
