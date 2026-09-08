// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"fmt"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
)

// MaxHostedFormFields caps a hosted form's fields. The page renders every one on
// every anonymous GET, so an uncapped list amplifies the only endpoint needing no
// credential. It is also the SUBMISSION cap (daemon.maxFormFields), so a field
// past it could never be filled in anyway.
const MaxHostedFormFields = 50

// MaxHostedFormFieldLen caps ONE declared field name. Capping the count alone
// bounded the wrong half: a name has no natural length, the page emits each one
// four times, and the only ceiling left was the 16 MiB graph budget — so 50 names
// of 300 KB answered one unauthenticated GET with 60 MB in 0.6s, repeatable by
// anyone holding the link.
const MaxHostedFormFieldLen = 128

const MaxHostedFormTitleLen = 200

// MaxPollIntervalSeconds stops IntervalSeconds * time.Second overflowing
// time.Duration's int64 nanoseconds (~292 years), which would make the scheduler
// fire every tick. The scheduler rejects past it at runtime; the lint warns at
// save time.
const MaxPollIntervalSeconds = 366 * 24 * 60 * 60

var cronLintParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

var cronLintAnchor = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

// lintTriggers catches the "I saved it but nothing happens" traps, mirroring the
// runtime guards in the webhook/form handlers and the scheduler so the owner
// finds out before waiting for an event that never comes. All warnings, not
// errors: a half-built flow is saved all the time.
func lintTriggers(g Graph) []LintIssue {
	issues := make([]LintIssue, 0)

	for _, tr := range g.Triggers {
		// A literal trigger secret is cleartext in the workspace git repo and on
		// the control plane; a ${secret.NAME} reference resolves at run time from
		// the encrypted store instead. Warn rather than reject — graphs predating
		// secret references still carry raw values and must keep working.
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
			// An impossible date ("0 0 30 2 *") parses, but Next() never matches
			// and returns the zero time.
			if sched.Next(cronLintAnchor).IsZero() {
				issues = append(issues, triggerIssue("trigger_cron_never_fires",
					fmt.Sprintf("Cron trigger %q never matches a real calendar date (e.g. February 30th), so it will never fire.", expr)))
			}
		case "poll":
			issues = append(issues, triggerIssue("trigger_poll_deprecated",
				"Poll schedules are now set on the Poll node, not as a graph-level trigger — this one is ignored. Add a poll_trigger node and set its interval (seconds)."))
		case "webhook":
			issues = append(issues, triggerIssue("trigger_webhook_deprecated",
				"Webhook config is now set on the Webhook step, not as a graph-level trigger — this one is ignored. Set the secret on the Webhook step, and use the Form step for a hosted form."))
		default:
			issues = append(issues, triggerIssue("trigger_unknown_type",
				fmt.Sprintf("Trigger type %q isn't recognized, so this flow won't be triggered. The only graph-level trigger is cron; webhook and poll are configured on their nodes.", tr.Type)))
		}
	}

	// Bucketing in one pass keeps each module's findings grouped however the
	// nodes are interleaved on the canvas, preserving the issue order callers
	// expect.
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

	// A blank schedule is intentional ("run only on demand"); only a malformed or
	// never-firing expression is flagged.
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

	// A blank interval is intentional; only a non-positive or over-the-ceiling
	// value the scheduler would refuse is flagged.
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

	// Without a secret the /trigger endpoint rejects every POST, so the flow
	// never starts on its own.
	for _, n := range webhookNodes {
		if len(WebhookSecrets(n.Params)) == 0 && !WebhookPublic(n.Params) {
			issues = append(issues, nodeTriggerIssue("trigger_webhook_no_secret", n.ID,
				"This Webhook step can't receive anything yet, so the flow will never start on its own. Open it and press Generate to create a key callers send — as a header, or as ?key=… on the end of the address if the sending service only lets you paste a URL. If it can send neither, turn on 'Accept calls with no key'."))
		}
	}

	for _, n := range formNodes {
		names := formFieldNames(n.Params)
		// Neither the page nor a submission goes past MaxHostedFormFields, so say
		// so rather than letting the owner publish a form whose tail never appears.
		if declared := len(names); declared > MaxHostedFormFields {
			issues = append(issues, nodeTriggerIssue("trigger_form_too_many_fields", n.ID,
				fmt.Sprintf("This Form step declares %d fields, but a form shows and accepts at most %d — the rest are ignored. Remove the extras, or collect them in one field.",
					declared, MaxHostedFormFields)))
		}
		// Reported once with a count: a generated list can be all of them, and one
		// issue per field would drown the panel.
		if over := countOver(names, MaxHostedFormFieldLen); over > 0 {
			issues = append(issues, nodeTriggerIssue("trigger_form_field_name_too_long", n.ID,
				fmt.Sprintf("This Form step has %d field name(s) longer than %d characters, which the form won't show. Shorten them — a field name is the label someone reads above the box.",
					over, MaxHostedFormFieldLen)))
		}
	}

	// There is no hosted form to fall back on, so a key-less Request step can
	// never be called.
	for _, n := range requestNodes {
		if len(WebhookSecrets(n.Params)) == 0 && !WebhookPublic(n.Params) {
			issues = append(issues, nodeTriggerIssue("trigger_request_no_secret", n.ID,
				"This Request step can't receive anything yet, so the flow will never start on its own. Open it and press Generate to create a key callers send — as a header, or as ?key=… on the end of the address if the calling system only lets you paste a URL. If it can send neither, turn on 'Answer calls with no key'."))
		}
	}

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

	// The scheduler tracks a Schedule node and a graph-level cron independently,
	// so carrying both fires the flow twice at every due time — which happens when
	// a Schedule node is added to a flow still holding a legacy cron.
	hasGraphCron := false
	for _, tr := range g.Triggers {
		if tr.Type == "cron" {
			hasGraphCron = true
			break
		}
	}
	if hasCronNode && hasGraphCron {
		issues = append(issues, triggerIssue("trigger_cron_duplicate_source",
			"This flow has both a Schedule step and a graph-level schedule, so it will run twice at each scheduled time. Keep the Schedule step — the graph-level schedule has no editor (the Triggers menu it lived on is gone) and has to be cleared through the API."))
	}
	return issues
}

func countOver(names []string, max int) int {
	n := 0
	for _, s := range names {
		if len(s) > max {
			n++
		}
	}
	return n
}

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

func nodeTriggerIssue(code, nodeID, msg string) LintIssue {
	return LintIssue{Code: code, Severity: LintWarn, Message: msg, NodeIDs: []string{nodeID}}
}

func triggerIssue(code, msg string) LintIssue {
	return LintIssue{Code: code, Severity: LintWarn, Message: msg}
}

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
