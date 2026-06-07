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

	hasWebhookInput := false
	for _, n := range g.Nodes {
		if n.Module == "webhook_input" {
			hasWebhookInput = true
			break
		}
	}

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
			switch {
			case tr.IntervalSeconds <= 0:
				issues = append(issues, triggerIssue("trigger_poll_interval",
					"A poll trigger's interval must be a positive number of seconds, so this one will never fire."))
			case tr.IntervalSeconds > MaxPollIntervalSeconds:
				issues = append(issues, triggerIssue("trigger_poll_interval",
					fmt.Sprintf("A poll trigger's interval (%d seconds) exceeds the maximum of %d (1 year), so it will be ignored. Use a smaller interval.", tr.IntervalSeconds, MaxPollIntervalSeconds)))
			}
		case "webhook":
			if tr.Secret == "" && !tr.PublicForm {
				issues = append(issues, triggerIssue("trigger_webhook_no_secret",
					"A webhook trigger has no secret, so its /trigger endpoint can't be called (callers must send the secret as a bearer token). Add a secret, or turn on the hosted form for a public, secret-less endpoint."))
			}
			if tr.PublicForm && !hasWebhookInput {
				issues = append(issues, triggerIssue("trigger_form_no_sink",
					"This flow's hosted form has no \"Webhook input\" node to deliver submissions to, so form posts will be rejected. Add a webhook_input node and wire it into the flow."))
			}
		default:
			issues = append(issues, triggerIssue("trigger_unknown_type",
				fmt.Sprintf("Trigger type %q isn't recognized, so this flow won't be triggered. Supported types are webhook, cron, and poll.", tr.Type)))
		}
	}

	// cron_trigger nodes carry their own schedule (Phase 2). Lint a
	// configured one the same way as a graph-level cron trigger. A blank
	// schedule is intentional ("run only on demand"), so it's not flagged —
	// only a malformed or never-firing expression is.
	for _, n := range g.Nodes {
		if n.Module != "cron_trigger" {
			continue
		}
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

	// A flow shouldn't carry BOTH a Schedule node and a graph-level cron
	// trigger: the scheduler tracks each independently, so the flow fires
	// twice at every due time. This can arise when a Schedule node is added
	// to a flow that still has a legacy graph-level cron — surface it so the
	// owner removes one. (The two are otherwise complementary: only one cron
	// authority should win.)
	hasCronNode := false
	for _, n := range g.Nodes {
		if n.Module == "cron_trigger" {
			hasCronNode = true
			break
		}
	}
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
