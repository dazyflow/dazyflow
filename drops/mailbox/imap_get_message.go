// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package mailbox

import (
	"context"
	"encoding/json"
	"time"

	"github.com/emersion/go-imap/v2"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/internal/params"
	"github.com/dazyflow/dazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "imap_get_message",
			Version:     "1.0",
			Label:       "Mailbox",
			Subtitle:    "Read email",
			Summary:     "Read one email — who sent it, the subject, the date, and the whole body.",
			Description: "Read one email as friendly Date / From / Subject / Body values. Connect Search emails' Matching emails straight into Email to read the FIRST match — or, to read every match, connect Matching emails into a For each and put this step in the loop body with Email = the row's id. Unlike the search, which carries a shortened body for every match, this returns the message text in full.",
			Integration: integration,
			Category:    "network",
			Icon:        "mail-open",
			Color:       "#0ea5e9",
			Provider:    "internal",
			Tags:        []string{"imap", "email", "mailbox", "message", "read", "fetch"},
			Examples: []core.ParamsExample{
				{
					Title:  "Read each email a search found (inside For each)",
					Params: json.RawMessage(`{"id":"${item.id}"}`),
					Notes:  "The mail account comes from the Mailbox integration page. Leave 'folder' blank to read from the same folder the search used.",
				},
			},
			ExecutionModel:   core.ExecutionBatch,
			ProcessModel:     core.ProcessLongLived,
			ConnectionFields: connectionFields(),
			Inputs: []core.Port{
				// Accepts EITHER a single id (text, e.g. ${item.id} inside a
				// For each) OR Search emails' "Matching emails" list wired
				// straight in — then the FIRST match is read. Same shape as
				// Gmail's Read email.
				{Port: "id", Label: "Email", MIME: []string{"text/plain", "application/json"}},
			},
			Outputs: []core.Port{
				// Friendly scalar pins rather than a JSON blob — the same four
				// Gmail's Read email offers, so a flow can swap one for the
				// other without rewiring.
				{Port: "date", Label: "Date", MIME: []string{"text/plain"}, Example: json.RawMessage(`"Thu, 12 Feb 2026 09:12:04 +0100"`)},
				{Port: "from", Label: "From", MIME: []string{"text/plain"}, Example: json.RawMessage(`"Fortnox <faktura@fortnox.se>"`)},
				{Port: "subject", Label: "Subject", MIME: []string{"text/plain"}, Example: json.RawMessage(`"Faktura 4471"`)},
				{Port: "body", Label: "Body", MIME: []string{"text/plain"}, Example: json.RawMessage(`"Din faktura 4471 är nu tillgänglig."`)},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"folder":{"type":"string","title":"Folder","description":"Which folder the email is in, e.g. \"INBOX\". Leave blank to use the folder set on the Mailbox page. An email's id only means anything inside the folder it came from."},
					"id":{"type":"string","title":"Email","description":"Which email to read — the id from a Search emails match. Overridden by the 'Email' input when connected."},
					"timeout_ms":{"type":"integer","default":30000,"minimum":1,"description":"Hard deadline for reading the email, in milliseconds."}
				},
				"required":["id"]
			}`),
			Idempotent: true,
		},
		Execute: executeIMAPGetMessage,
	})
}

func executeIMAPGetMessage(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	uid, bad := resolveUID(job)
	if bad != nil {
		return *bad, nil
	}

	ctx, cancel := context.WithTimeout(ctx, time.Duration(params.TimeoutMS(job, 30000))*time.Millisecond)
	defer cancel()

	client, _, fail := openMailbox(ctx, job, true)
	if fail != nil {
		return *fail, nil
	}
	defer client.Close()

	// Ask for the shape of the message, not the message: envelope for the
	// headers (already decoded out of RFC 2047 by the server) and
	// BODYSTRUCTURE for the MIME tree. The body itself is one more fetch, of
	// only the part that holds it — so an email with a 20 MB PDF hanging off
	// it costs this step a few hundred bytes.
	buf, fetchFail := fetchOneUID(client, job, uid, &imap.FetchOptions{
		UID:           true,
		Flags:         true,
		InternalDate:  true,
		Envelope:      true,
		BodyStructure: &imap.FetchItemBodyStructure{Extended: true},
	})
	if fetchFail != nil {
		return *fetchFail, nil
	}

	body := ""
	if text := pickTextPart(leafParts(buf.BodyStructure)); text != nil {
		textBuf, textFail := fetchOneUID(client, job, uid, &imap.FetchOptions{
			BodySection: []*imap.FetchItemBodySection{sectionFor(*text)},
		})
		if textFail != nil {
			return *textFail, nil
		}
		if len(textBuf.BodySection) > 0 {
			body = string(decodePart(textBuf.BodySection[0].Bytes, text.leaf))
		}
	}
	// A message with no text part at all — a notification whose whole payload
	// is an attachment — reads as an empty body. That is a real email, so it
	// is not an error; Download attachments is the step that wants it.

	date, from, subject := headerValues(buf)
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"date":    {MIME: "text/plain", Inline: date},
			"from":    {MIME: "text/plain", Inline: from},
			"subject": {MIME: "text/plain", Inline: subject},
			"body":    {MIME: "text/plain", Inline: body},
		},
	}, nil
}
