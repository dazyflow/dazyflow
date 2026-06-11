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
//
// Deprecated graph-level webhook/poll triggers are intentionally NOT counted:
// the runtime ignores them (see lintTriggers), so a flow carrying only one
// of those is manual-only in practice.
func HasConfiguredAutoTrigger(g Graph) bool {
	for _, tr := range g.Triggers {
		if tr.Type == "cron" && strings.TrimSpace(tr.Cron) != "" {
			return true
		}
	}
	for _, n := range g.Nodes {
		switch n.Module {
		case "cron_trigger":
			if expr, _ := n.Params["cron"].(string); strings.TrimSpace(expr) != "" {
				return true
			}
		case "poll_trigger", "google_form_trigger":
			if secs, ok := paramInt(n.Params, "interval_seconds"); ok && secs > 0 && secs <= MaxPollIntervalSeconds {
				return true
			}
		case "webhook_input":
			publicForm, _ := n.Params["public_form"].(bool)
			if len(WebhookSecrets(n.Params)) > 0 || publicForm {
				return true
			}
		}
	}
	return false
}
