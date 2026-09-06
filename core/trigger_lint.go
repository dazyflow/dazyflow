// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"fmt"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
)

// MaxHostedFormFields caps how many fields one hosted form declares. The
// page renders every one of them on every anonymous GET, so an uncapped list
// is an amplifier on the only endpoint that needs no credential. It is also
// the cap on a SUBMISSION (daemon.maxFormFields), which is what makes it the
// honest number here: a field past it could never be filled in anyway.
const MaxHostedFormFields = 50

// MaxHostedFormFieldLen caps how long ONE declared field name may be. Capping
// the count alone bounded the wrong half: a name has no natural length, the
// page emits each one four times (for=, the label text, id= and name=), and
// the only ceiling left was the 16 MiB graph budget the names are charged
// against — so 50 names of 300 KB answered one unauthenticated GET with 60 MB
// in 0.6s, four times what the graph stores, repeatable by anyone holding the
// link. Generous for a real field name, which is a form label a person reads.
const MaxHostedFormFieldLen = 128

// MaxHostedFormTitleLen caps the hosted form's heading, which the same
// anonymous GET renders. Same reasoning as the field names, one copy rather
// than four.
const MaxHostedFormTitleLen = 200

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
		// A literal trigger secret is stored in cleartext: the graph JSON is
		// committed to the workspace git repo and travels the control plane
		// as-is. A ${secret.NAME} reference is resolved at run time from the
		// encrypted per-tenant store instead, so the plaintext never lands in
		// either. Warn rather than reject — graphs predating secret references
		// still carry raw values and must keep working.
		if raw := strings.TrimSpace(tr.Secret); raw != "" && !strings.HasPrefix(raw, "${secret.") {
			issues = append(issues, triggerIssue("trigger_secret_plaintext",
				"This trigger's secret is stored as plain text, which means it's written into the flow's saved history. Store it as a secret and reference it with ${secret.NAME} instead."))
		}
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
				"Webhook config is now set on the Webhook step, not as a graph-level trigger — this one is ignored. Set the secret on the Webhook step, and use the Form step for a hosted form."))
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
	var cronNodes, pollNodes, webhookNodes, formNodes, requestNodes, replyNodes []Node
	hasCronNode := false
	for _, n := range g.Nodes {
		switch n.Module {
		case "cron_trigger":
			cronNodes = append(cronNodes, n)
			hasCronNode = true
		case "poll_trigger":
			pollNodes = append(pollNodes, n)
		case WebhookInputModule:
			webhookNodes = append(webhookNodes, n)
		case FormInputModule:
			formNodes = append(formNodes, n)
		case RequestInputModule:
			requestNodes = append(requestNodes, n)
		case ReplyModule:
			replyNodes = append(replyNodes, n)
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

	// webhook_input nodes carry the secret. Without one the /trigger endpoint
	// rejects every unauthenticated POST, so the flow never starts on its own.
	for _, n := range webhookNodes {
		if len(WebhookSecrets(n.Params)) == 0 {
			issues = append(issues, nodeTriggerIssue("trigger_webhook_no_secret", n.ID,
				"This Webhook step can't receive anything yet, so the flow will never start on its own. Open it and press Generate to create the secret key other systems must send when they call this flow."))
		}
	}

	// form_input needs no key — its presence is the opt-in — so the only way
	// to misconfigure one is to declare fields the hosted page won't render.
	for _, n := range formNodes {
		names := formFieldNames(n.Params)
		// The page renders only the first MaxHostedFormFields, and a submission
		// carries no more than that either — so say so here rather than letting
		// the owner publish a form whose tail silently never appears and could
		// never be filled in.
		if declared := len(names); declared > MaxHostedFormFields {
			issues = append(issues, nodeTriggerIssue("trigger_form_too_many_fields", n.ID,
				fmt.Sprintf("This Form step declares %d fields, but a form shows and accepts at most %d — the rest are ignored. Remove the extras, or collect them in one field.",
					declared, MaxHostedFormFields)))
		}
		// The page drops an over-long name rather than rendering it, for the
		// same reason: it is an amplifier on the one endpoint that needs no
		// credential. Reported once with a count — a generated list can be all
		// of them, and one issue per field would drown the panel.
		if over := countOver(names, MaxHostedFormFieldLen); over > 0 {
			issues = append(issues, nodeTriggerIssue("trigger_form_field_name_too_long", n.ID,
				fmt.Sprintf("This Form step has %d field name(s) longer than %d characters, which the form won't show. Shorten them — a field name is the label someone reads above the box.",
					over, MaxHostedFormFieldLen)))
		}
	}

	// request_input carries only keys — there is no hosted form to fall back
	// on, so a key-less Request step can never be called.
	for _, n := range requestNodes {
		if len(WebhookSecrets(n.Params)) == 0 {
			issues = append(issues, nodeTriggerIssue("trigger_request_no_secret", n.ID,
				"This Request step can't receive anything yet, so the flow will never start on its own. Open it and press Generate to create the secret key the systems calling this flow must send."))
		}
	}

	// The Request/Reply pair only pays off when both halves are present: a
	// Request with no Reply answers its caller with a bare run status, and a
	// Reply with nothing waiting on it is dead configuration.
	if len(requestNodes) > 0 && len(replyNodes) == 0 {
		issues = append(issues, nodeTriggerIssue("trigger_request_no_reply", requestNodes[0].ID,
			"This flow answers its callers, but it has no Reply step — they'll get the run's status instead of an answer. Add a Reply step and put what you want to send back in it."))
	}
	for _, n := range replyNodes {
		if len(requestNodes) == 0 {
			issues = append(issues, nodeTriggerIssue("reply_without_request", n.ID,
				"This Reply can't reach anyone: the flow doesn't start from a Request step, so nobody is waiting for an answer. It will record what it would have sent and the flow will carry on."))
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

// countOver reports how many of names exceed max characters.
func countOver(names []string, max int) int {
	n := 0
	for _, s := range names {
		if len(s) > max {
			n++
		}
	}
	return n
}

// formFieldNames reads a form_input node's declared fields,
// tolerating the []any of strings JSON unmarshalling produces.
func formFieldNames(params map[string]any) []string {
	switch arr := params["form_fields"].(type) {
	case []string:
		return arr
	case []any:
		out := make([]string, 0, len(arr))
		for _, it := range arr {
			if s, ok := it.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
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
