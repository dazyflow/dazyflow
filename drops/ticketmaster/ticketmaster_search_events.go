// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package ticketmaster

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/internal/params"
	"github.com/dazyflow/dazyflow/engine"
)

// searchParams is the filter surface both drops share, spliced into each
// drop's own schema so a filter means the same thing in the search step and
// the trigger. Kept as a string rather than composed at run time because
// ParamsSchema is raw JSON the catalog serves verbatim.
const searchParams = `
	"keyword":{"type":"string","title":"Search for","description":"Artist, team or event name to search for — e.g. \"Robyn\". Leave empty to list everything matching the other filters."},
	"attraction_id":{"type":"string","title":"Attraction ID","x_advanced":true,"description":"Ticketmaster's own id for one artist or team (e.g. K8vZ9171C-f). More exact than a name search when you have it."},
	"country_code":{"type":"string","title":"Country","description":"Two-letter country code to search in — SE, NO, DK, FI, GB, US. Strongly recommended: without it the search is worldwide."},
	"city":{"type":"string","title":"City","description":"City to search in — e.g. Stockholm. Combine with the country code for the tightest result."},
	"classification":{"type":"string","title":"Kind of event","default":"music","format":"suggest","enum":["","music","sports","arts & theatre","film","miscellaneous"],"enumNames":["Anything","Music","Sports","Arts & theatre","Film","Other"],"description":"Restrict to one kind of event. Empty means all of them."},
	"start_date":{"type":"string","title":"From","description":"Only events starting after this. Takes a date (2026-10-01), a full timestamp, or a relative form like now or +7d. Empty means from now on."},
	"end_date":{"type":"string","title":"Until","description":"Only events starting before this. Takes the same forms as From — e.g. +90d for the next three months."}`

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "ticketmaster_search_events",
			Version:     "1.0",
			Label:       "Ticketmaster",
			Subtitle:    "Search events",
			Summary:     "Find live events — concerts, matches, shows — by artist, city and date.",
			Description: "Search Ticketmaster for live events and get one row each: what it is, which artist, the venue and city, the date, and a link to the tickets. Filter by artist or team, by country and city, and by a date window.\n\nPair it with Spotify · Followed artists to turn someone's listening into a gig list: loop over the artists, search each name, and mail or post what comes back.\n\nThis searches Ticketmaster's own inventory — Ticketmaster, TicketWeb, Universe and resale. A club show sold somewhere else won't be in it.",
			Integration: "Ticketmaster",
			Category:    "network",
			Icon:        "ticket",
			Color:       "#026CDF",
			Provider:    "internal",
			Tags:        []string{"ticketmaster", "events", "concerts", "gigs", "tickets", "live", "search"},
			Examples: []core.ParamsExample{
				{
					Title:  "Concerts in Stockholm over the next three months",
					Params: json.RawMessage(`{"country_code":"SE","city":"Stockholm","classification":"music","end_date":"+90d","limit":50}`),
				},
				{
					Title:  "Is this artist playing in Sweden at all?",
					Params: json.RawMessage(`{"keyword":"Robyn","country_code":"SE"}`),
					Notes:  "Wire the artist name in from Spotify · Followed artists (inside a For each) to check a whole watch list.",
				},
			},
			ConnectionFields: connectionFields(),
			ExecutionModel:   core.ExecutionBatch,
			ProcessModel:     core.ProcessLongLived,
			Inputs: []core.Port{
				// Wireable so a For each over an artist list can drive the
				// search; a wired value overrides the param.
				{Port: "keyword", Label: "Search for", MIME: []string{"text/plain"}},
			},
			Outputs: []core.Port{
				{Port: "events", Label: "Events found", MIME: []string{"application/json"}, Example: json.RawMessage(`[{"id":"Z698xZbpZ17q1RA","name":"Robyn","artist":"Robyn","venue":"Avicii Arena","city":"Stockholm","country":"SE","date":"2026-11-14","time":"20:00:00","url":"https://www.ticketmaster.se/event/Z698xZbpZ17q1RA"}]`)},
				{Port: "count", Label: "Count", MIME: []string{"text/plain"}, Example: json.RawMessage(`"3"`)},
				{Port: "total", Label: "Total found", MIME: []string{"text/plain"}, Example: json.RawMessage(`"41"`)},
				{Port: "meta", Label: "Details", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{` + searchParams + `,
					"limit":{"type":"integer","title":"Max results","default":20,"minimum":1,"maximum":200,"description":"How many events to return at most (Ticketmaster caps a page at 200)."},
					"page":{"type":"integer","title":"Page","default":0,"minimum":0,"x_advanced":true,"description":"Zero-based page number. Ticketmaster stops paging at the 1000th result."},
					"base_url":{"type":"string","description":"Override the API host (testing)."},
					"timeout_ms":{"type":"integer","default":15000,"minimum":1,"description":"Hard deadline for the request, in milliseconds."}
				}
			}`),
			Idempotent:  true,
			RetryPolicy: core.RetryExponentialBackoff,
		},
		Execute: executeSearchEvents,
	})
}

func executeSearchEvents(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	if kw, ok := params.TextInputOr(job, "keyword", ""); ok && kw != "" {
		// Copy rather than mutate: the wired keyword belongs to this run.
		p := make(map[string]any, len(job.Params)+1)
		for k, v := range job.Params {
			p[k] = v
		}
		p["keyword"] = kw
		job.Params = p
	}
	limit := params.ClampInt(params.IntDefault(job.Params, "limit", 20), 1, pageLimit)
	page := params.IntDefault(job.Params, "page", 0)
	if page < 0 {
		page = 0
	}

	rows, info, fail := searchEvents(ctx, job, limit, page, time.Now())
	if fail != nil {
		return *fail, nil
	}
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"events": {MIME: "application/json", Inline: rows},
			"count":  {MIME: "text/plain", Inline: strconv.Itoa(len(rows))},
			"total":  {MIME: "text/plain", Inline: strconv.Itoa(info.TotalElements)},
			"meta": {MIME: "application/json", Inline: map[string]any{
				"count":       len(rows),
				"total":       info.TotalElements,
				"page":        info.Number,
				"total_pages": info.TotalPages,
			}},
		},
	}, nil
}
