// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package mailbox

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/engine"
	"github.com/dazyflow/dazyflow/pollstate"
)

// The Gmail twin of this reaches a Google account; this one reaches any
// mailbox with an IMAP server, which on a Nordic SMB is as likely to be the
// one their host gave them. Both wrap a search that already knew how to be
// incremental — what they add is the trigger's shape.
func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "imap_on_new_message",
			Version:     "1.0",
			Label:       "Mailbox",
			Subtitle:    "When an email arrives",
			Summary:     "Fires when an email arrives in a folder of the connected mailbox.",
			Description: "Watches a folder on the connected mailbox and starts the flow when email arrives in it — everything, or only what the filters below match: a sender, a subject, words in the body, unread only.\n\nEach email is reported once. The first check after you publish records where the folder is up to and fires nothing, so switching a watch on doesn't work through a folder's whole history. After that a check sees only what arrived since the one before it, and nothing is ever marked read as a side effect — use Mark as read for that, deliberately.\n\nA burst bigger than \"Max emails\" is delayed rather than skipped: the oldest waiting mail goes first, in arrival order, and the rest follow on the next checks.\n\nIMAP has a push of its own (IDLE), but it needs a connection held open per folder for as long as the flow lives, so this polls instead — one search per check.",
			Integration: integration,
			Category:    "trigger",
			Icon:        "mail",
			Color:       "#0ea5e9",
			Provider:    "internal",
			Tags: []string{"imap", "email", "mailbox", "trigger", "poll", "arrive",
				"incoming", "receive", "inbox", "new mail", "when an email"},
			Examples: []core.ParamsExample{
				{
					Title:  "Anything new in the inbox",
					Params: json.RawMessage(`{"folder":"INBOX","interval_seconds":300}`),
				},
				{
					Title:  "Only invoices from one supplier",
					Params: json.RawMessage(`{"folder":"INBOX","from":"@leverantor.se","subject":"faktura","interval_seconds":900}`),
				},
			},
			ConnectionFields: connectionFields(),
			ExecutionModel:   core.ExecutionTrigger,
			ProcessModel:     core.ProcessLongLived,
			Outputs: []core.Port{
				{Port: "messages", Label: "New emails", MIME: []string{"application/json"},
					Example: json.RawMessage(`[
						{"id":"4471","date":"Thu, 12 Feb 2026 09:12:04 +0100","from":"Fortnox <faktura@fortnox.se>","subject":"Faktura 4471","body":"Din faktura 4471 är nu tillgänglig.","unread":true}
					]`)},
				{Port: "count", Label: "Count", MIME: []string{"text/plain"}, Example: json.RawMessage(`"2"`)},
				{Port: "fired_at", Label: "Time", MIME: []string{"text/plain"}, Example: json.RawMessage(`"2026-02-12T09:15:00Z"`)},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"folder":{"type":"string","title":"Folder","description":"Which folder to watch, e.g. \"INBOX\" or \"INBOX/Invoices\". Leave blank to use the folder set on the Mailbox page."},
					"from":{"type":"string","title":"From","examples":["@customer.com"],"description":"Only fire for this sender. Any part of the address or name counts, so \"@customer.com\" covers everyone at that company."},
					"to":{"type":"string","title":"To","description":"Only fire for mail to this address — useful on a shared mailbox that receives several."},
					"subject":{"type":"string","title":"Subject contains","examples":["faktura"],"description":"Only fire when the subject line contains this."},
					"body":{"type":"string","title":"Body contains","description":"Only fire when the message text contains this. Slower than the other filters on a large mailbox — most servers search bodies without an index."},
					"unread_only":{"type":"boolean","title":"Unread only","default":false,"description":"Only mail still marked unread. Watching never marks anything read by itself."},
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
					"timeout_ms":{"type":"integer","default":30000,"minimum":1,"description":"Hard deadline for the whole check, in milliseconds."}
				}
			}`),
			Idempotent: false,
		},
		Execute: executeIMAPOnNewMessage,
	})
}

func executeIMAPOnNewMessage(ctx context.Context, job core.Job, progress chan<- core.Progress) (core.Result, error) {
	// Search emails owns the watermark, the folder handling and the ordering;
	// this forces the one param that makes it incremental rather than keeping a
	// second copy of any of it.
	inner := job
	p := make(map[string]any, len(job.Params)+1)
	for k, v := range job.Params {
		p[k] = v
	}
	p["only_new"] = true
	inner.Params = p

	res, err := executeIMAPSearch(ctx, inner, progress)
	if err != nil || res.Status != core.StatusOK {
		return res, err
	}

	msgs, _ := res.Output["messages"].Inline.([]any)
	pollstate.Report(ctx, job, len(msgs) > 0)
	if len(msgs) == 0 {
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
