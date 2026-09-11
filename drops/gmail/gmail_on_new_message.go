// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package gmail

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/engine"
	"github.com/dazyflow/dazyflow/pollstate"
)

// The incremental half of this already existed: Search emails has carried
// `only_new` and its watermark since it was written. What it did not have was a
// trigger's shape — a place in the entry-point palette, an interval of its own,
// and a flow that counts as live because it is there. "When an email arrives"
// is the commonest thing anyone wants to automate, and it was a two-step
// assembly with a Schedule beside it.
func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "gmail_on_new_message",
			Version:     "1.0",
			Label:       "Gmail",
			Subtitle:    "When an email arrives",
			Summary:     "Fires when an email arrives in the connected mailbox, optionally only those matching a search.",
			Description: "Watches the connected mailbox and starts the flow when email arrives — every message, or only the ones a search matches, written exactly as you would type it into Gmail's own search box: `from:faktura@leverantor.se`, `has:attachment`, `is:unread`.\n\nEach email is reported once. The first check after you publish records where the mailbox is up to and fires nothing, so switching a watch on doesn't work through everything already in the inbox. After that a check sees only what arrived since the one before it.\n\nA burst bigger than \"Max emails\" is delayed rather than skipped: the oldest waiting mail goes first, in the order it arrived, and the rest follow on the next checks.\n\nGmail's own push needs a Cloud Pub/Sub topic and a verified domain to reach a deployment at all, so this polls — one search per check, whether anything arrived or not.",
			Integration: "Gmail",
			Category:    "trigger",
			Icon:        "mail",
			BrandLogo:   "/brands/gmail.svg",
			Color:       "#D14836",
			Provider:    "internal",
			Tags: []string{"gmail", "email", "trigger", "poll", "arrive", "incoming",
				"receive", "inbox", "new mail", "when an email"},
			Examples: []core.ParamsExample{
				{
					Title:  "Anything new in the inbox",
					Params: json.RawMessage(`{"account":"default","interval_seconds":300}`),
				},
				{
					Title:  "Only invoices, with an attachment",
					Params: json.RawMessage(`{"account":"default","query":"has:attachment subject:faktura","interval_seconds":900}`),
					Notes:  "The search is Gmail's own, so anything that works in its search box works here.",
				},
			},
			RequiresConnections: []core.ConnectionRequirement{
				{Kind: "oauth", Name: "google", Note: "Google OAuth — gmail.readonly scope."},
			},
			ExecutionModel: core.ExecutionTrigger,
			ProcessModel:   core.ProcessLongLived,
			Outputs: []core.Port{
				{Port: "messages", Label: "New emails", MIME: []string{"application/json"},
					Example: json.RawMessage(`[
						{"id":"18f2a9c4d1e0b7a3","threadId":"18f2a9c4d1e0b7a3","date":"Thu, 12 Feb 2026 09:12:04 +0100","from":"Fortnox <faktura@fortnox.se>","subject":"Faktura 4471","body":"Din faktura 4471 är nu tillgänglig."}
					]`)},
				{Port: "count", Label: "Count", MIME: []string{"text/plain"}, Example: json.RawMessage(`"2"`)},
				{Port: "fired_at", Label: "Time", MIME: []string{"text/plain"}, Example: json.RawMessage(`"2026-02-12T09:15:00Z"`)},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"account":{"type":"string","default":"default"},
					"query":{"type":"string","title":"Only emails matching","examples":["from:boss@company.com","has:attachment subject:faktura"],"description":"Leave blank to fire on every email. Otherwise works exactly like Gmail's search box, e.g. 'is:unread', 'from:someone@example.com', 'has:attachment'."},
					"limit":{"type":"integer","title":"Max emails","default":50,"minimum":1,"maximum":500,"description":"How many emails one check may take. The oldest waiting ones go first, in arrival order, so a burst bigger than this is delayed rather than skipped."},
					"interval_seconds":{
						"type":"integer",
						"title":"Check every",
						"format":"duration-seconds",
						"minimum":1,
						"maximum":31622400,
						"default":300,
						"description":"How often to check for new mail once the flow is published. Leave blank to only check when you press Run (for testing)."
					},
					"base_url":{"type":"string","description":"Override the API host (testing)."},
					"timeout_ms":{"type":"integer","default":15000,"minimum":1,"description":"Hard deadline for the request, in milliseconds."}
				}
			}`),
			// A fire is a discrete poll of the mailbox; rerunning reads from the
			// stored watermark rather than re-deriving a past fire.
			Idempotent: false,
		},
		Execute: executeGmailOnNewMessage,
	})
}

func executeGmailOnNewMessage(ctx context.Context, job core.Job, progress chan<- core.Progress) (core.Result, error) {
	// The search step IS the implementation — watermark, backlog paging, the
	// held position when an email cannot be fetched. Forcing its two poll
	// params rather than reimplementing any of that is the whole point: a
	// second copy of this logic would be a second place for it to go wrong.
	inner := job
	p := make(map[string]any, len(job.Params)+1)
	for k, v := range job.Params {
		p[k] = v
	}
	p["only_new"] = true
	delete(p, "page_token") // a watch has no hand-driven pagination
	// Search emails carries the older `max_results` name, grandfathered there
	// because renaming a shipped param drops the value out of saved flows. A
	// new step has no such history, so it takes the canonical `limit` and
	// translates on the way in.
	if v, ok := job.Params["limit"]; ok {
		p["max_results"] = v
		delete(p, "limit")
	}
	inner.Params = p

	res, err := executeGmailSearch(ctx, inner, progress)
	if err != nil || res.Status != core.StatusOK {
		return res, err
	}

	msgs, _ := res.Output["messages"].Inline.([]any)
	pollstate.Report(ctx, job, len(msgs) > 0)
	if len(msgs) == 0 {
		// Nothing new: an empty output map, which the engine reads as "skip the
		// rest of the flow".
		return core.Result{JobID: job.ID, Status: core.StatusOK, Output: map[string]core.Ref{}}, nil
	}
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"messages": {MIME: "application/json", Inline: msgs},
			"count":    {MIME: "text/plain", Inline: strconv.Itoa(len(msgs))},
			"fired_at": {MIME: "text/plain", Inline: time.Now().UTC().Format(time.RFC3339)},
		},
	}, nil
}
