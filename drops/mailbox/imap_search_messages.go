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
			Tags:        []string{"imap", "email", "mailbox", "inbox", "search", "list"},
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
			// The mail account (server/port/security/login/folder) is a
			// per-tenant ConnectionFields bundle configured once on the
			// integration page, exactly like the Email drop's SMTP server —
			// the engine injects it into each node's params at run time, so
			// flows carry only the per-search fields.
			ConnectionFields: connectionFields(),
			Inputs: []core.Port{
				// Named after their params so the card shows inline editable
				// boxes; a wired value overrides the typed one. From and
				// Subject are the two worth wiring — "search for whatever the
				// last step produced" is almost always one of those.
				{Port: "from", Label: "From", MIME: []string{"text/plain"}},
				{Port: "subject", Label: "Subject", MIME: []string{"text/plain"}},
			},
			Outputs: []core.Port{
				// The same record shape Gmail's Search emails emits — {id,
				// date, from, subject, body, unread} — so the For each /
				// ${item.id} idioms built on that carry over unchanged.
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
					"limit":{"type":"integer","title":"Max emails","default":50,"minimum":1,"maximum":500,"description":"How many matches to bring back at most, newest first."},
					"timeout_ms":{"type":"integer","default":30000,"minimum":1,"description":"Hard deadline for the whole search, in milliseconds."}
				}
			}`),
			Idempotent: true,
		},
		Execute: executeIMAPSearch,
	})
}

func executeIMAPSearch(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
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

	// Read-only (EXAMINE): a search must never change a flag on the server.
	folder, err := client.Select(cfg.Folder, true)
	if err != nil {
		return params.Err(job, "imap_error", err.Error()), nil
	}

	onlyNew := params.BoolDefault(job.Params, "only_new", false)
	var mark *watermark
	if onlyNew {
		mark = readWatermark(ctx, job, cfg.Folder, folder)
		if !mark.replay {
			// Ask the server for nothing older than the last UID we emitted.
			// This is the part that has no Gmail equivalent: the watermark is
			// the folder's own message numbering rather than a timestamp, so
			// there is no window where two emails share a cursor value.
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

	// Drop anything at or below the watermark. The UID range asked for
	// `lastUID+1:*` and that is NOT enough on its own: in an IMAP range `*` is
	// the highest UID that currently exists, and RFC 3501 says a range is
	// interpreted regardless of order — so once the watermark passes the
	// newest message, `3:*` against a folder whose highest UID is 2 becomes
	// the range 2:3 and matches message 2. Left unfiltered, every empty poll
	// would re-emit the newest email forever, which is precisely the
	// re-processing this mode exists to prevent. The range still earns its
	// place as a server-side narrowing; the client owns the boundary.
	if mark != nil && !mark.replay {
		kept := uids[:0]
		for _, uid := range uids {
			if uid > mark.lastUID {
				kept = append(kept, uid)
			}
		}
		uids = kept
	}

	// Newest first, capped: IMAP returns matches in ascending UID order, so
	// the tail of the list is the newest mail. Take that tail rather than the
	// head — a 5000-message folder searched with a limit of 50 should give
	// the 50 most recent matches, not the 50 oldest.
	if len(uids) > limit {
		uids = uids[len(uids)-limit:]
	}

	msgs := make([]any, 0, len(uids))
	if len(uids) > 0 {
		// One FETCH for the whole batch. Gmail's search needs a second HTTPS
		// request per match to turn its {id} stubs into real emails; IMAP
		// hands back every envelope and body in a single command.
		bufs, ferr := client.Fetch(imap.UIDSetNum(uids...), searchFetchOptions()).Collect()
		if ferr != nil {
			return params.Err(job, "imap_error", fmt.Sprintf("found %d emails but couldn't read them: %v", len(uids), ferr)), nil
		}
		for _, buf := range bufs {
			msgs = append(msgs, messageRecord(buf))
		}
	}

	if onlyNew {
		return emitOnlyNew(ctx, job, msgs, uids, mark), nil
	}

	// An ad-hoc search reports an empty result as an empty list, not as a
	// missing port: the author asked a question and "no matches" is the
	// answer. only_new is the mode where empty means "non-event" (below).
	pollstate.Report(ctx, job, len(msgs) > 0)
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"messages": {MIME: "application/json", Inline: msgs},
		},
	}, nil
}

// searchCriteria turns the step's fields into an IMAP SEARCH.
//
// Typed fields rather than one Gmail-style query box, deliberately. IMAP's
// SEARCH is a different language with no equivalent of `is:unread
// newer_than:1d`, and a box that silently ignored what someone typed into it
// would be worse than no box: the search would succeed and return the wrong
// mail. Each field below maps onto exactly one SEARCH key.
func searchCriteria(job core.Job) (*imap.SearchCriteria, error) {
	c := &imap.SearchCriteria{}

	// The From and Subject input pins override their params when wired (the
	// same "input overrides param" pattern as Gmail search's query pin).
	from, ok := params.TextInputOr(job, "from", params.StringDefault(job.Params, "from", ""))
	if !ok {
		return nil, fmt.Errorf("input port 'from' must be text")
	}
	subject, ok := params.TextInputOr(job, "subject", params.StringDefault(job.Params, "subject", ""))
	if !ok {
		return nil, fmt.Errorf("input port 'subject' must be text")
	}

	// A slice, not a map: ranging a map put the SEARCH keys in a different
	// order on every run, which makes one command hard to compare against
	// another in a server log or a packet capture. The semantics are unchanged
	// (SEARCH keys are ANDed), so this costs nothing.
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
		// SEARCH SINCE compares dates, not instants — the server ignores the
		// time of day — so this is "on or after that calendar day".
		c.Since = time.Now().AddDate(0, 0, -days)
	}
	return c, nil
}

// watermark is the "only new since last run" position: the folder identity the
// UIDs were counted in, and the last UID already emitted.
type watermark struct {
	uidValidity uint32
	lastUID     imap.UID

	// folder is the folder these UIDs were counted in — part of the cursor
	// key, so pointing a step at another folder keeps its own position.
	folder string
	// uidNext is the folder's UIDNext at select time: the UID the server will
	// hand the next message to arrive. Everything already in the folder is
	// therefore below it, which is what a first run baselines to.
	uidNext imap.UID

	// baseline means this run must not emit anything — either it is the first
	// run, or the folder was renumbered underneath us. It records where the
	// folder is up to and stops.
	baseline bool
	// replay means the stored position can't be used to narrow the search, so
	// the criteria go out unbounded. Set alongside baseline.
	replay bool
}

// cursorName is the per-(flow, node) watermark key. The folder is part of it:
// pointing one step at another folder is a different position, and inheriting
// INBOX's UID would silently skip mail.
func cursorName(job core.Job, folder string) string {
	return fmt.Sprintf("cursor.imap_search.%s.%s.%s", job.GraphID, job.NodeID, folder)
}

// readWatermark loads the stored position and decides what this run may emit.
//
// UIDVALIDITY is the part with no Gmail counterpart and the part that must not
// be got wrong. A UID identifies a message only within one incarnation of a
// folder: if the folder is deleted and recreated, or the server rebuilds its
// index, UIDVALIDITY changes and every stored UID becomes meaningless. The RFC
// requires a client to discard what it cached. Treating a stale UID as a
// watermark anyway would compare against numbers from a folder that no longer
// exists — which, depending on which way the new numbering falls, either
// replays the whole folder into a flow that acts on each email, or skips mail
// forever. So a UIDVALIDITY change re-baselines: emit nothing once, resume
// cleanly after.
func readWatermark(ctx context.Context, job core.Job, folder string, state *imap.SelectData) *watermark {
	mark := &watermark{uidValidity: state.UIDValidity, folder: folder, uidNext: state.UIDNext}

	stored := cursor.Read(ctx, job.Tenant, cursorName(job, folder))
	validity, uid, ok := parseWatermark(stored)
	switch {
	case !ok:
		mark.baseline, mark.replay = true, true // first run
	case validity != state.UIDValidity:
		mark.baseline, mark.replay = true, true // folder renumbered — discard it
	default:
		mark.lastUID = uid
	}
	return mark
}

// parseWatermark reads a stored "<uidvalidity>:<uid>" position. Anything it
// can't parse is treated as absent, which re-baselines — the same
// fail-to-the-beginning stance cursor.Read takes on a failed read.
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

// emitOnlyNew advances the watermark and emits the fresh batch.
//
// First run (or a re-baseline): record where the folder is up to and emit
// NOTHING, so a flow published against a full mailbox starts watching from
// "now" instead of blasting the backlog. Mirrors gmail_search's emitOnlyNew
// and the google_form_trigger.
//
// A nothing-new run emits no output ports at all, so downstream edges go
// dormant and the rest of the flow is skipped — an empty poll is a non-event,
// not an empty list. The cursor write is best-effort/at-least-once: a failed
// write means at worst the next run re-emits this batch, never a silent drop.
func emitOnlyNew(ctx context.Context, job core.Job, msgs []any, uids []imap.UID, mark *watermark) core.Result {
	next := mark.lastUID
	for _, uid := range uids {
		if uid > next {
			next = uid
		}
	}
	if mark.baseline {
		// Nothing matched yet on a first run, but the folder still has a
		// position: baseline to the newest message in it so the next run
		// doesn't hand back mail that was already sitting there. UIDs only
		// ever increase within a UIDVALIDITY, so this can't skip a later
		// arrival. (Gmail's date watermark can't do this — with no matches
		// there is no timestamp to remember, so it stays unbaselined.)
		if next == 0 && mark.uidNext > 0 {
			next = mark.uidNext - 1
		}
		msgs = nil
	}
	if next > mark.lastUID {
		_ = cursor.Write(ctx, job.Tenant, cursorName(job, mark.folder),
			fmt.Sprintf("%d:%d", mark.uidValidity, next))
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
