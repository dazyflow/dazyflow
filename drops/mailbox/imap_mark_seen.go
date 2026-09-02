// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package mailbox

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/emersion/go-imap/v2"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/internal/params"
	"github.com/dazyflow/dazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "imap_mark_seen",
			Version:     "1.0",
			Label:       "Mailbox",
			Subtitle:    "Mark as read",
			Summary:     "Mark an email as read, so it stops showing up as new.",
			Description: "Mark an email as read once your flow has dealt with it. Put this at the end of a triage or filing flow — after the Slack post, after the attachment is filed — so the mail is no longer sitting in the inbox as unread, and an 'Unread only' search won't pick it up again. Connect Search emails' Matching emails into a For each and put this step in the loop body with Email = the row's id. Doing it twice is harmless: read is read.",
			Integration: integration,
			Category:    "network",
			Icon:        "mail-check",
			Color:       "#0ea5e9",
			Provider:    "internal",
			Tags:        []string{"imap", "email", "mailbox", "read", "seen", "flag"},
			Examples: []core.ParamsExample{
				{
					Title:  "Mark each handled email read (inside For each)",
					Params: json.RawMessage(`{"id":"${item.id}"}`),
					Notes:  "Wire the step that did the work into this one's pass-through pin, so the mail is only marked read once that succeeded.",
				},
			},
			ExecutionModel:   core.ExecutionBatch,
			ProcessModel:     core.ProcessLongLived,
			ConnectionFields: connectionFields(),
			Inputs: []core.Port{
				{Port: "id", Label: "Email", MIME: []string{"text/plain", "application/json"}},
			},
			// No declared outputs beyond the details: marking an email read is
			// a "do" step — "after it's marked, do X" chains through the
			// pass-through pin, which fires on success. Same shape as the send
			// steps (email_send, gmail_send_email, ntfy).
			Outputs: []core.Port{
				{Port: "meta", Label: "Details", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"folder":{"type":"string","title":"Folder","description":"Which folder the email is in, e.g. \"INBOX\". Leave blank to use the folder set on the Mailbox page. An email's id only means anything inside the folder it came from."},
					"id":{"type":"string","title":"Email","description":"Which email to mark read — the id from a Search emails match. Overridden by the 'Email' input when connected."},
					"timeout_ms":{"type":"integer","default":30000,"minimum":1,"description":"Hard deadline for the change, in milliseconds."}
				},
				"required":["id"]
			}`),
			// Setting a flag is genuinely idempotent — unlike sending an
			// email, which is the reason the send steps turn retries off.
			// Marking an already-read message read again leaves the mailbox in
			// exactly the same state, so the engine may retry this freely and
			// needs no write-dedupe to recover a crashed run.
			Idempotent: true,
		},
		Execute: executeIMAPMarkSeen,
	})
}

func executeIMAPMarkSeen(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	uid, bad := resolveUID(job)
	if bad != nil {
		return *bad, nil
	}

	ctx, cancel := context.WithTimeout(ctx, time.Duration(params.TimeoutMS(job, 30000))*time.Millisecond)
	defer cancel()

	// The one step here that opens the folder for WRITING. Everything else in
	// this package passes true and gets EXAMINE.
	client, _, fail := openMailbox(ctx, job, false)
	if fail != nil {
		return *fail, nil
	}
	defer client.Close()

	// Silent is deliberately OFF. A STORE against a UID that no longer exists
	// is not an error in IMAP — the server accepts the command and does
	// nothing — so with .SILENT this step would report success for an email
	// somebody had already deleted. The updated flags coming back are the only
	// evidence the message was actually there, so we ask for them and treat
	// their absence as "not found".
	bufs, err := client.Store(imap.UIDSetNum(uid), &imap.StoreFlags{
		Op:    imap.StoreFlagsAdd,
		Flags: []imap.Flag{imap.FlagSeen},
	}, nil).Collect()
	if err != nil {
		return params.Err(job, "imap_error", fmt.Sprintf("couldn't mark email %d read: %v", uid, err)), nil
	}
	if len(bufs) == 0 {
		return params.Err(job, "not_found", fmt.Sprintf("there's no email %d in %q any more — it may have been deleted or moved since the search found it", uid, folderName(job))), nil
	}

	flags := make([]string, 0, len(bufs[0].Flags))
	for _, f := range bufs[0].Flags {
		flags = append(flags, string(f))
	}
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"meta": {MIME: "application/json", Inline: map[string]any{
				"id":     fmt.Sprint(uid),
				"folder": folderName(job),
				"flags":  flags,
			}},
		},
	}, nil
}
