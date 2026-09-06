// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package ticketmaster

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/cursor"
	"github.com/dazyflow/dazyflow/drops/internal/params"
	"github.com/dazyflow/dazyflow/engine"
	"github.com/dazyflow/dazyflow/pollstate"
)

// seenCap bounds the remembered-id list. Ticketmaster has no "announced at"
// field, so "new" can only mean "an id this node has not seen", and that needs
// a set that does not grow without limit.
//
// The cap has to exceed what one poll can return (200) by enough that an event
// cannot be forgotten while it is still in the result window, or it would fire
// twice. At 1000 an event survives five complete turnovers of a full page,
// by which time its date has passed and the search no longer returns it.
const seenCap = 1000

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "ticketmaster_on_new_event",
			Version:     "1.0",
			Label:       "Ticketmaster",
			Subtitle:    "When an event is announced",
			Summary:     "Fires when a live event turns up that this flow hasn't seen before.",
			Description: "Watches a Ticketmaster search and starts the flow when an event appears in it that this step hasn't seen before — a tour date announced, a second night added. The new events come out as a list, ready for For each.\n\nThe first check after you publish learns what's already on sale and fires nothing, so you aren't paged about a hundred existing events. After that, only genuinely new ones fire.\n\nTicketmaster has no webhooks, so this polls. Announcements happen on a daily rhythm and the free key allows 5000 calls a day, so once a day is the right setting and the default.",
			Integration: "Ticketmaster",
			Category:    "trigger",
			Icon:        "ticket",
			Color:       "#026CDF",
			Provider:    "internal",
			Tags:        []string{"ticketmaster", "events", "concerts", "gigs", "trigger", "poll", "announcement"},
			Examples: []core.ParamsExample{
				{
					Title:  "Any new gig in Stockholm",
					Params: json.RawMessage(`{"country_code":"SE","city":"Stockholm","classification":"music","end_date":"+180d","interval_seconds":86400}`),
				},
				{
					Title:  "One artist, anywhere in the Nordics",
					Params: json.RawMessage(`{"keyword":"Robyn","country_code":"SE","interval_seconds":86400}`),
					Notes:  "One watched search is one call per check. Watching a long artist list is better done as Schedule → Followed artists → For each → Search events, which spends the same quota deliberately.",
				},
			},
			ConnectionFields: connectionFields(),
			ExecutionModel:   core.ExecutionTrigger,
			ProcessModel:     core.ProcessLongLived,
			Outputs: []core.Port{
				{Port: "events", Label: "New events", MIME: []string{"application/json"}, Example: json.RawMessage(`[{"id":"Z698xZbpZ17q1RA","name":"Robyn","artist":"Robyn","venue":"Avicii Arena","city":"Stockholm","country":"SE","date":"2026-11-14","time":"20:00:00","url":"https://www.ticketmaster.se/event/Z698xZbpZ17q1RA"}]`)},
				{Port: "count", Label: "Count", MIME: []string{"text/plain"}, Example: json.RawMessage(`"2"`)},
				{Port: "fired_at", Label: "Time", MIME: []string{"text/plain"}, Example: json.RawMessage(`"2026-02-12T08:12:04Z"`)},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{` + searchParams + `,
					"limit":{"type":"integer","title":"Max results","default":50,"minimum":1,"maximum":200,"description":"How many events each check looks at. Raise it if the watched search has more results than this — anything past the cut is never seen."},
					"interval_seconds":{
						"type":"integer",
						"title":"Check every",
						"format":"duration-seconds",
						"minimum":1,
						"maximum":31622400,
						"default":86400,
						"description":"How often to check once the flow is published. Daily is right for announcements and kind to the 5000-a-day quota. Leave blank to only check when you press Run (for testing)."
					},
					"base_url":{"type":"string","description":"Override the API host (testing)."},
					"timeout_ms":{"type":"integer","default":15000,"minimum":1,"description":"Hard deadline for the request, in milliseconds."}
				}
			}`),
			// A fire is a discrete poll observation: rerunning re-reads against
			// the stored seen-set rather than re-deriving a past announcement.
			Idempotent: false,
		},
		Execute: executeOnNewEvent,
	})
}

// executeOnNewEvent polls the watched search and fires only on ids the node has
// not recorded. An empty batch emits no outputs, leaving downstream edges
// dormant so the rest of the flow is skipped — the same non-event shape the
// Google Forms and Home Assistant triggers use.
func executeOnNewEvent(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	limit := params.ClampInt(params.IntDefault(job.Params, "limit", 50), 1, pageLimit)

	rows, _, fail := searchEvents(ctx, job, limit, 0, time.Now())
	if fail != nil {
		return *fail, nil
	}

	// cursor.ticketmaster.<graph>.<node>: per-(flow,node) seen-set. The store
	// hides the "cursor." prefix from the Credentials UI.
	name := fmt.Sprintf("cursor.ticketmaster.%s.%s", job.GraphID, job.NodeID)
	priorIDs, seen, first := readSeen(ctx, job.Tenant, name)

	fresh := make([]map[string]any, 0, len(rows))
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		id, _ := row["id"].(string)
		if id == "" {
			continue
		}
		ids = append(ids, id)
		if !seen[id] {
			fresh = append(fresh, row)
		}
	}

	// First check after publishing: learn what is already on sale, fire
	// nothing. Without this, turning the flow on pages you about every event
	// that already existed.
	if first {
		_ = writeSeen(ctx, job.Tenant, name, ids, nil)
		pollstate.Report(ctx, job, false)
		return noNewEvents(job), nil
	}
	if len(fresh) == 0 {
		pollstate.Report(ctx, job, false) // empty poll — let the scheduler back off
		return noNewEvents(job), nil
	}

	// Record before firing. A failed write is at-least-once: at worst the next
	// check re-fires these same events, which is the safer direction for an
	// announcement than never firing at all.
	_ = writeSeen(ctx, job.Tenant, name, ids, priorIDs)
	pollstate.Report(ctx, job, true)

	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"events":   {MIME: "application/json", Inline: fresh},
			"count":    {MIME: "text/plain", Inline: strconv.Itoa(len(fresh))},
			"fired_at": {MIME: "text/plain", Inline: time.Now().UTC().Format(time.RFC3339)},
		},
	}, nil
}

// noNewEvents is the empty result for a check that found nothing new: no
// output ports, so every downstream edge is dormant.
func noNewEvents(job core.Job) core.Result {
	return core.Result{JobID: job.ID, Status: core.StatusOK, Output: map[string]core.Ref{}}
}

// seenState is what we persist between checks: the ids acted on, oldest first.
type seenState struct {
	IDs []string `json:"ids"`
}

// readSeen returns the recorded ids (oldest first), the same as a set for
// lookups, and whether this is the first check — nothing stored yet, or a
// stored value that no longer parses, both of which mean "learn the current
// state, don't fire".
func readSeen(ctx context.Context, tenant, name string) ([]string, map[string]bool, bool) {
	raw := cursor.Read(ctx, tenant, name)
	if raw == "" {
		return nil, nil, true
	}
	var s seenState
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		return nil, nil, true
	}
	set := make(map[string]bool, len(s.IDs))
	for _, id := range s.IDs {
		set[id] = true
	}
	return s.IDs, set, false
}

// writeSeen stores the ids just observed after the ones already known, capped
// at seenCap with the oldest dropped first. Prior ids keep their recorded
// order — the stored list is what makes "oldest" mean anything, so it is
// carried as a slice rather than rebuilt from a map, whose iteration order
// would reshuffle the eviction queue on every write.
func writeSeen(ctx context.Context, tenant, name string, observed, prior []string) error {
	nowSeen := make(map[string]bool, len(observed))
	for _, id := range observed {
		nowSeen[id] = true
	}
	kept := make([]string, 0, len(prior)+len(observed))
	for _, id := range prior {
		if !nowSeen[id] {
			kept = append(kept, id)
		}
	}
	kept = append(kept, observed...)
	if len(kept) > seenCap {
		kept = kept[len(kept)-seenCap:]
	}
	b, err := json.Marshal(seenState{IDs: kept})
	if err != nil {
		return err
	}
	return cursor.Write(ctx, tenant, name, string(b))
}
