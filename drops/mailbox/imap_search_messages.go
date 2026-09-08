// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package mailbox

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/cursor"
	"github.com/dazyflow/dazyflow/drops/internal/params"
	"github.com/dazyflow/dazyflow/engine"
	"github.com/dazyflow/dazyflow/internal/imaputil"
	"github.com/dazyflow/dazyflow/pollstate"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "imap_search_messages",
			Version:     "1.0",
			Label:       "Mailbox",
			Subtitle:    "Search emails",
			Summary:     "Find emails in any mail account — Fastmail, mailbox.org, your own server — using the mailbox's own search.",
			Description: "Find emails in a folder of any mail account that speaks IMAP, which is nearly all of them. Fill in the parts you care about — who it's from, words in the subject, unread only, the last few days — and each match comes out as a real email (date, sender, subject, body), ready to log to a sheet, loop over with For each, or hand to an AI step. Turn on 'Only new since last run' to make this a safe poll source: a published flow then acts on each email once, instead of re-processing the folder every few minutes. Connect the mail account once on the Mailbox integration page.",
			Integration: integration,
			Category:    "network",
			Icon:        "search",
			Color:       "#0ea5e9",
			Provider:    "internal",
			Tags: []string{"imap", "email", "mailbox", "inbox", "search", "list", "incoming",
				"arrive", "receive", "poll", "trigger"},
			Examples: []core.ParamsExample{
				{
					Title:  "Unread mail from a customer, last day",
					Params: json.RawMessage(`{"from":"@customer.com","unread_only":true,"since_days":1,"limit":20}`),
					Notes:  "The mail server and login come from the Mailbox integration page, not the step.",
				},
				{
					Title:  "Invoices, one pass each (safe to publish on a schedule)",
					Params: json.RawMessage(`{"subject":"invoice","only_new":true,"folder":"INBOX"}`),
					Notes:  "With 'Only new since last run' on, the first run emits nothing and just remembers where the folder is up to.",
				},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			// A tenant connection, so the fields are injected rather than typed per node.
			ConnectionFields: connectionFields(),
			Inputs: []core.Port{
				{Port: "from", Label: "From", MIME: []string{"text/plain"}},
				{Port: "subject", Label: "Subject", MIME: []string{"text/plain"}},
			},
			Outputs: []core.Port{
				{Port: "messages", Label: "Matching emails", MIME: []string{"application/json"},
					Example: json.RawMessage(`[
						{"id":"4471","date":"Thu, 12 Feb 2026 09:12:04 +0100","from":"Fortnox <faktura@fortnox.se>","subject":"Faktura 4471","body":"Din faktura 4471 är nu tillgänglig.","unread":true},
						{"id":"4470","date":"Thu, 12 Feb 2026 08:47:51 +0100","from":"anna@nordkraft.se","subject":"Påminnelse","body":"Hej! Hinner ni titta på detta i veckan?","unread":false}
					]`)},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"folder":{"type":"string","title":"Folder","description":"Which folder to search, e.g. \"INBOX\" or \"INBOX/Invoices\". Leave blank to use the folder set on the Mailbox page."},
					"from":{"type":"string","title":"From","examples":["@customer.com","boss@company.com"],"description":"Match the sender. Any part of the address or name counts, so \"@customer.com\" finds everyone at that company. Overridden by the 'From' input."},
					"to":{"type":"string","title":"To","description":"Match a recipient — useful on a shared mailbox that receives several addresses."},
					"subject":{"type":"string","title":"Subject contains","examples":["invoice"],"description":"Match words in the subject line. Overridden by the 'Subject' input."},
					"body":{"type":"string","title":"Body contains","description":"Match words in the message text. Slower than the other fields on a large mailbox — most servers search bodies without an index."},
					"unread_only":{"type":"boolean","title":"Unread only","default":false,"description":"Only emails still marked unread. Searching never marks anything read by itself."},
					"since_days":{"type":"integer","title":"Only the last N days","minimum":1,"description":"Ignore anything older than this many days. Leave blank to search the whole folder."},
					"only_new":{"type":"boolean","title":"Only new since last run","default":false,"description":"When on, each run emits only emails that arrived since the previous run — nothing on the first run (it just remembers where the folder is up to). Turn this on when a published, polling flow acts on each match, so it doesn't re-process the same emails on every poll. Leave off for ad-hoc searches that should return every match."},
					"limit":{"type":"integer","title":"Max emails","default":50,"minimum":1,"maximum":500,"description":"How many matches to bring back at most. An ad-hoc search returns the newest ones. With 'Only new since last run' on it takes the OLDEST waiting emails instead, in arrival order, and the rest follow on the next polls — so a burst bigger than this is delayed, never skipped."},
					"timeout_ms":{"type":"integer","default":30000,"minimum":1,"description":"Hard deadline for the whole search, in milliseconds."}
				}
			}`),
			Idempotent: true,
		},
		Execute: executeIMAPSearch,
	})
}

func executeIMAPSearch(ctx context.Context, job core.Job, progress chan<- core.Progress) (core.Result, error) {
	cfg, err := configFromJob(job)
	if err != nil {
		return params.Err(job, "not_connected", err.Error()), nil
	}
	limit := params.ClampInt(params.IntDefault(job.Params, "limit", 50), 1, 500)

	criteria, err := searchCriteria(job)
	if err != nil {
		return params.Err(job, "bad_input", err.Error()), nil
	}

	ctx, cancel := context.WithTimeout(ctx, time.Duration(params.TimeoutMS(job, 30000))*time.Millisecond)
	defer cancel()

	client, err := imaputil.Dial(ctx, cfg)
	if err != nil {
		return params.Err(job, "imap_error", err.Error()), nil
	}
	defer client.Close()

	folder, err := client.Select(cfg.Folder, true)
	if err != nil {
		return params.Err(job, "imap_error", err.Error()), nil
	}

	onlyNew := params.BoolDefault(job.Params, "only_new", false)
	var mark *watermark
	if onlyNew {
		var rerr error
		mark, rerr = readWatermark(ctx, job, cfg.Folder, folder)
		if rerr != nil {
			// Without the stored UID there is no way to ask for "mail since last time".
			return cursor.FailRead(job, rerr), nil
		}
		if !mark.replay {
			var set imap.UIDSet
			set.AddRange(mark.lastUID+1, 0) // 0 == "*", i.e. up to the newest
			criteria.UID = append(criteria.UID, set)
		}
	}

	found, err := client.UIDSearch(criteria, nil).Wait()
	if err != nil {
		return params.Err(job, "imap_error", fmt.Sprintf("the search failed: %v", err)), nil
	}
	uids := found.AllUIDs()

	// The requested range is inclusive, so the watermark itself comes back.
	if mark != nil && !mark.replay {
		kept := uids[:0]
		for _, uid := range uids {
			if uid > mark.lastUID {
				kept = append(kept, uid)
			}
		}
		uids = kept
	}

	// Which END the cap keeps matters: a poll must drain the OLDEST first, or the
	// watermark steps over everything below it and that mail is never offered again.
	truncated := 0
	if len(uids) > limit {
		truncated = len(uids) - limit
		if onlyNew {
			uids = uids[:limit]
		} else {
			uids = uids[len(uids)-limit:]
		}
	}
	if truncated > 0 && onlyNew {
		params.EmitProgress(progress, job, 1, fmt.Sprintf(
			"%d emails waiting; taking the oldest %d this run, the rest follow on the next polls",
			truncated+limit, limit))
	}

	msgs := make([]any, 0, len(uids))
	fetched := make([]imap.UID, 0, len(uids))
	if len(uids) > 0 {
		bufs, ferr := client.Fetch(imap.UIDSetNum(uids...), searchFetchOptions()).Collect()
		if ferr != nil {
			return params.Err(job, "imap_error", fmt.Sprintf("found %d emails but couldn't read them: %v", len(uids), ferr)), nil
		}
		for _, buf := range bufs {
			msgs = append(msgs, messageRecord(buf))
			fetched = append(fetched, buf.UID)
		}
		// A short answer with no error is legal, so a gap must not advance the watermark.
		if len(bufs) != len(uids) {
			params.EmitProgress(progress, job, 1, fmt.Sprintf(
				"read %d of %d matching emails; the rest were not returned by the server "+
					"(most likely deleted since the search) and are not counted as handled",
				len(bufs), len(uids)))
		}
	}

	if onlyNew {
		return emitOnlyNew(ctx, job, msgs, safeAdvance(uids, fetched), mark), nil
	}

	pollstate.Report(ctx, job, len(msgs) > 0)
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"messages": {MIME: "application/json", Inline: msgs},
		},
	}, nil
}

func searchCriteria(job core.Job) (*imap.SearchCriteria, error) {
	c := &imap.SearchCriteria{}

	from, ok := params.TextInputOr(job, "from", params.StringDefault(job.Params, "from", ""))
	if !ok {
		return nil, fmt.Errorf("input port 'from' must be text")
	}
	subject, ok := params.TextInputOr(job, "subject", params.StringDefault(job.Params, "subject", ""))
	if !ok {
		return nil, fmt.Errorf("input port 'subject' must be text")
	}

	// A slice, not a map: map order made the SEARCH non-deterministic.
	for _, h := range []imap.SearchCriteriaHeaderField{
		{Key: "From", Value: from},
		{Key: "To", Value: params.StringDefault(job.Params, "to", "")},
		{Key: "Subject", Value: subject},
	} {
		if val := strings.TrimSpace(h.Value); val != "" {
			c.Header = append(c.Header, imap.SearchCriteriaHeaderField{Key: h.Key, Value: val})
		}
	}
	if body := strings.TrimSpace(params.StringDefault(job.Params, "body", "")); body != "" {
		c.Body = append(c.Body, body)
	}
	if params.BoolDefault(job.Params, "unread_only", false) {
		c.NotFlag = append(c.NotFlag, imap.FlagSeen)
	}
	if days := params.IntDefault(job.Params, "since_days", 0); days > 0 {
		c.Since = time.Now().AddDate(0, 0, -days)
	}
	return c, nil
}

type watermark struct {
	uidValidity uint32
	lastUID     imap.UID

	folder  string
	uidNext imap.UID

	// A baseline run emits nothing; it only records where to start.
	baseline bool
	replay   bool
}

// The folder is part of the key, or switching folders reuses a foreign position.
func cursorName(job core.Job, folder string) string {
	return fmt.Sprintf("cursor.imap_search.%s.%s.%s", job.GraphID, job.NodeID, folder)
}

// A UIDVALIDITY change means the server renumbered, so the stored UID means
// nothing and the run must re-baseline rather than emit.
func readWatermark(ctx context.Context, job core.Job, folder string, state *imap.SelectData) (*watermark, error) {
	mark := &watermark{uidValidity: state.UIDValidity, folder: folder, uidNext: state.UIDNext}

	stored, err := cursor.Read(ctx, job.Tenant, cursorName(job, folder))
	if err != nil {
		return nil, err
	}
	validity, uid, ok := parseWatermark(stored)
	switch {
	case !ok:
		mark.baseline, mark.replay = true, true // first run
	case validity != state.UIDValidity:
		mark.baseline, mark.replay = true, true // folder renumbered — discard it
	default:
		mark.lastUID = uid
	}
	return mark, nil
}

// Only the LEADING run of requested UIDs that actually arrived: advancing past a
// gap would skip the missing mail for good.
func safeAdvance(requested, fetched []imap.UID) []imap.UID {
	got := make(map[imap.UID]bool, len(fetched))
	for _, uid := range fetched {
		got[uid] = true
	}
	safe := make([]imap.UID, 0, len(fetched))
	for _, uid := range requested {
		if !got[uid] {
			break
		}
		safe = append(safe, uid)
	}
	return safe
}

// Anything unparseable re-baselines rather than guessing a position.
func parseWatermark(s string) (validity uint32, uid imap.UID, ok bool) {
	before, after, found := strings.Cut(strings.TrimSpace(s), ":")
	if !found {
		return 0, 0, false
	}
	v, verr := strconv.ParseUint(before, 10, 32)
	u, uerr := strconv.ParseUint(after, 10, 32)
	if verr != nil || uerr != nil {
		return 0, 0, false
	}
	return uint32(v), imap.UID(u), true
}

// At-least-once: a failed cursor write re-emits, never silently drops.
func emitOnlyNew(ctx context.Context, job core.Job, msgs []any, uids []imap.UID, mark *watermark) core.Result {
	next := mark.lastUID
	for _, uid := range uids {
		if uid > next {
			next = uid
		}
	}
	if mark.baseline {
		// A first run with no matches still records the folder's position.
		if next == 0 && mark.uidNext > 0 {
			next = mark.uidNext - 1
		}
		msgs = nil
	}
	if next > mark.lastUID {
		// Ignorable once mail is emitted; on a baseline run it is not.
		werr := cursor.Write(ctx, job.Tenant, cursorName(job, mark.folder),
			fmt.Sprintf("%d:%d", mark.uidValidity, next))
		if werr != nil && mark.baseline {
			return cursor.FailBaseline(job, werr)
		}
	}

	pollstate.Report(ctx, job, len(msgs) > 0)
	if len(msgs) == 0 {
		return core.Result{JobID: job.ID, Status: core.StatusOK, Output: map[string]core.Ref{}}
	}
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"messages": {MIME: "application/json", Inline: msgs},
		},
	}
}
