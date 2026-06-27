// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"fmt"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
)

// MaxPollIntervalSeconds caps a poll trigger's interval. Beyond this,
// IntervalSeconds * time.Second would overflow time.Duration's int64
// nanoseconds (~292 years) and make the scheduler fire every tick. The
// scheduler rejects intervals past this at runtime; the trigger lint
// warns the owner at save time. One year is generous for a poll.
const MaxPollIntervalSeconds = 366 * 24 * 60 * 60

// cronLintParser mirrors the scheduler's 5-field parser exactly, so the
// lint accepts precisely what the scheduler will run — no false
// positives from a stricter/looser dialect.
var cronLintParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

// cronLintAnchor is a fixed reference time for probing whether a cron
// expression ever fires. Whether an expression is impossible (e.g. Feb
// 30) is independent of the anchor, so fixing it keeps lint
// deterministic (no time.Now() in a pure function).
var cronLintAnchor = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

// lintTriggers warns about trigger configurations that will silently
// never fire (or never reach a node) at runtime — the "I saved it but
// nothing happens" traps. These mirror the runtime guards in the
// webhook/form handlers and the scheduler, surfaced at save time so the
// owner finds out before they wait for an event that never comes.
//
// All findings are warnings, not errors: a half-built flow is saved all
// the time, and the trigger may be fixed in a later edit.
func lintTriggers(g Graph) []LintIssue {
	issues := make([]LintIssue, 0)

	for _, tr := range g.Triggers {
		switch tr.Type {
		case "cron":
			expr := strings.TrimSpace(tr.Cron)
			if expr == "" {
				issues = append(issues, triggerIssue("trigger_cron_invalid",
					"A cron trigger has no schedule expression, so it will never fire. Set a schedule like \"0 9 * * *\" (every day at 09:00)."))
				continue
			}
			sched, err := cronLintParser.Parse(expr)
			if err != nil {
				issues = append(issues, triggerIssue("trigger_cron_invalid",
					fmt.Sprintf("Cron trigger %q isn't a valid schedule (%v), so it will never fire. Use 5 fields: minute hour day-of-month month day-of-week.", expr, err)))
				continue
			}
			// An impossible date (e.g. "0 0 30 2 *" — Feb 30) parses but
			// Next() never finds a match and returns the zero time.
			if sched.Next(cronLintAnchor).IsZero() {
				issues = append(issues, triggerIssue("trigger_cron_never_fires",
					fmt.Sprintf("Cron trigger %q never matches a real calendar date (e.g. February 30th), so it will never fire.", expr)))
			}
		case "poll":
			// Poll is no longer a graph-level trigger — the interval lives on
			// the poll_trigger node now (scanned by the scheduler). A legacy
			// graph-level poll trigger is ignored at runtime, so flag it for
			// migration rather than validating an interval the scheduler won't read.
			issues = append(issues, triggerIssue("trigger_poll_deprecated",
				"Poll schedules are now set on the Poll node, not as a graph-level trigger — this one is ignored. Add a poll_trigger node and set its interval (seconds)."))
		case "webhook":
			// Webhook config (secret + hosted form) lives on the webhook_input
			// node now — a graph-level webhook trigger is ignored at runtime.
			issues = append(issues, triggerIssue("trigger_webhook_deprecated",
				"Webhook config is now set on the Webhook input node, not as a graph-level trigger — this one is ignored. Set the secret (and hosted-form options) on the webhook_input node instead."))
		default:
			issues = append(issues, triggerIssue("trigger_unknown_type",
				fmt.Sprintf("Trigger type %q isn't recognized, so this flow won't be triggered. The only graph-level trigger is cron; webhook and poll are configured on their nodes.", tr.Type)))
		}
	}

	// Single pass over the nodes, bucketing the trigger-bearing modules.
	// The per-module rules below run on these buckets so each module's
	// findings stay grouped (cron, then poll, then webhook) regardless of
	// how the nodes are interleaved on the canvas — preserving the issue
	// ordering callers/tests expect.
	var cronNodes, pollNodes, webhookNodes []Node
	hasCronNode := false
	for _, n := range g.Nodes {
		switch n.Module {
		case "cron_trigger":
			cronNodes = append(cronNodes, n)
			hasCronNode = true
		case "poll_trigger":
			pollNodes = append(pollNodes, n)
		case "webhook_input":
			webhookNodes = append(webhookNodes, n)
		}
	}

	// cron_trigger nodes carry their own schedule (Phase 2). Lint a
	// configured one the same way as a graph-level cron trigger. A blank
	// schedule is intentional ("run only on demand"), so it's not flagged —
	// only a malformed or never-firing expression is.
	for _, n := range cronNodes {
		expr, _ := n.Params["cron"].(string)
		expr = strings.TrimSpace(expr)
		if expr == "" {
			continue
		}
		sched, err := cronLintParser.Parse(expr)
		if err != nil {
			issues = append(issues, nodeTriggerIssue("trigger_cron_invalid", n.ID,
				fmt.Sprintf("Schedule node's cron %q isn't valid (%v), so it will never fire. Use 5 fields: minute hour day-of-month month day-of-week.", expr, err)))
			continue
		}
		if sched.Next(cronLintAnchor).IsZero() {
			issues = append(issues, nodeTriggerIssue("trigger_cron_never_fires", n.ID,
				fmt.Sprintf("Schedule node's cron %q never matches a real calendar date (e.g. February 30th), so it will never fire.", expr)))
		}
	}

	// poll_trigger nodes carry their interval on the node (like cron_trigger's
	// schedule). A blank interval is intentional ("run only on demand"), so
	// it's not flagged — only a non-positive or over-the-ceiling value, which
	// the scheduler would refuse to run.
	for _, n := range pollNodes {
		secs, ok := paramInt(n.Params, "interval_seconds")
		if !ok || secs == 0 {
			continue // unset/zero — manual-only, fine (mirrors a blank cron)
		}
		switch {
		case secs < 0:
			issues = append(issues, nodeTriggerIssue("trigger_poll_interval", n.ID,
				"Poll node's interval must be a positive number of seconds, so it will never fire. Set how often it should run, or clear it to run only on demand."))
		case secs > MaxPollIntervalSeconds:
			issues = append(issues, nodeTriggerIssue("trigger_poll_interval", n.ID,
				fmt.Sprintf("Poll node's interval (%d seconds) exceeds the maximum of %d (1 year), so it will be ignored. Use a smaller interval.", secs, MaxPollIntervalSeconds)))
		}
	}

	// webhook_input nodes carry the secret + hosted-form opt-in. A node with
	// neither a secret nor a public form is unreachable: the /trigger endpoint
	// rejects unauthenticated POSTs and there's no form to receive submissions.
	for _, n := range webhookNodes {
		publicForm, _ := n.Params["public_form"].(bool)
		if len(WebhookSecrets(n.Params)) == 0 && !publicForm {
			// Worded for people who don't know what a bearer token or an
			// endpoint is: name the two fixes exactly as the editor's
			// Webhook inspector labels them.
			issues = append(issues, nodeTriggerIssue("trigger_webhook_no_secret", n.ID,
				"This Webhook step can't receive anything yet, so the flow will never start on its own. Open the Webhook step and either turn on \"Host a form for me\" (anyone with the link can submit), or press Generate under \"For developers\" to create the secret key other systems must send when they call this flow."))
		}
	}

	// A flow shouldn't carry BOTH a Schedule node and a graph-level cron
	// trigger: the scheduler tracks each independently, so the flow fires
	// twice at every due time. This can arise when a Schedule node is added
	// to a flow that still has a legacy graph-level cron — surface it so the
	// owner removes one. (The two are otherwise complementary: only one cron
	// authority should win.)
	hasGraphCron := false
	for _, tr := range g.Triggers {
		if tr.Type == "cron" {
			hasGraphCron = true
			break
		}
	}
	if hasCronNode && hasGraphCron {
		issues = append(issues, triggerIssue("trigger_cron_duplicate_source",
			"This flow has both a Schedule step and a graph-level schedule, so it will run twice at each scheduled time. Keep the Schedule step and remove the graph-level schedule on the Triggers → Schedule tab."))
	}
	return issues
}

// nodeTriggerIssue builds a trigger lint finding attributed to a specific
// node (a cron_trigger), so the editor can pin the warning to that node.
func nodeTriggerIssue(code, nodeID, msg string) LintIssue {
	return LintIssue{Code: code, Severity: LintWarn, Message: msg, NodeIDs: []string{nodeID}}
}

// triggerIssue builds a trigger-level lint finding. Triggers aren't
// nodes, so NodeIDs is left empty.
func triggerIssue(code, msg string) LintIssue {
	return LintIssue{Code: code, Severity: LintWarn, Message: msg}
}

// paramInt reads an integer-valued node param, tolerating the float64 that JSON
// unmarshalling produces. The bool is false when the key is absent or not a
// number, so callers can tell "unset" from a real value.
func paramInt(params map[string]any, key string) (int, bool) {
	switch v := params[key].(type) {
	case float64:
		return int(v), true
	case int:
		return v, true
	case int64:
		return int(v), true
	}
	return 0, false
}
