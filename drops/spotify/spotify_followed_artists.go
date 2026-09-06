// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package spotify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/internal/params"
	"github.com/dazyflow/dazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "spotify_followed_artists",
			Version:     "1.0",
			Label:       "Spotify",
			Subtitle:    "Followed artists",
			Summary:     "Read the artists your connected Spotify account follows, one row each.",
			Description: "Read the artists the connected Spotify account follows. Each artist comes out as a simple record — name, genres, id and a link to open it in Spotify — ready to loop over with For each, log to a sheet, or use as the watch list for a 'tell me when they play near me' flow.\n\nThe list is paged: ask for up to 50 at a time and thread the 'Next cursor' output back into the 'After ID' input to walk the rest.",
			Integration: "Spotify",
			Category:    "network",
			Icon:        "music",
			BrandLogo:   "/brands/spotify.svg",
			Color:       "#1ED760",
			Provider:    "internal",
			Tags:        []string{"spotify", "music", "artists", "following", "list"},
			Examples: []core.ParamsExample{
				{Title: "Everyone I follow (first page)", Params: json.RawMessage(`{}`)},
				{
					Title:  "Next page",
					Params: json.RawMessage(`{"limit":50,"after_id":"0TnOYISbd1XYRBk9myaseg"}`),
					Notes:  "'after_id' is the previous run's 'Next cursor' output — Spotify pages this list by the last artist id, not by a page number.",
				},
			},
			RequiresConnections: []core.ConnectionRequirement{
				{Kind: "oauth", Name: "spotify", Note: "Connect a Spotify account (user-follow-read scope) under Apps."},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				// Wireable so a paging loop can thread the previous run's
				// cursor back in; a wired value overrides the param.
				{Port: "after_id", Label: "After ID", MIME: []string{"text/plain"}},
			},
			Outputs: []core.Port{
				// "artists" isn't one of core's conventional list-port names,
				// so the cardinality is declared here — without it a whole
				// list wired into a one-at-a-time step reads as legal.
				{Port: "artists", Label: "Artists", MIME: []string{"application/json"}, List: true, Example: json.RawMessage(`[{"id":"0TnOYISbd1XYRBk9myaseg","name":"Pitbull","genres":["dance pop","miami hip hop"],"url":"https://open.spotify.com/artist/0TnOYISbd1XYRBk9myaseg"}]`)},
				{Port: "next_cursor", Label: "Next cursor", MIME: []string{"text/plain"}, Example: json.RawMessage(`"0TnOYISbd1XYRBk9myaseg"`)},
				{Port: "has_more", Label: "Has more", MIME: []string{"text/plain"}, Example: json.RawMessage(`"false"`)},
				{Port: "meta", Label: "Details", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"account":{"type":"string","title":"Account","default":"default","x_advanced":true,"description":"Which connected Spotify account to use (for multiple connections)."},
					"limit":{"type":"integer","title":"Max results","default":20,"minimum":1,"maximum":50,"description":"How many artists to return at most (Spotify caps this at 50)."},
					"after_id":{"type":"string","title":"After id","description":"Artist id to continue from — the previous run's 'Next cursor'. Overridden by the 'After ID' input when connected. Leave empty to start at the beginning."},
					"base_url":{"type":"string","description":"Override the API host (testing)."},
					"timeout_ms":{"type":"integer","default":15000,"minimum":1,"description":"Hard deadline for the request, in milliseconds."}
				}
			}`),
			Idempotent:  true,
			RetryPolicy: core.RetryExponentialBackoff,
		},
		Execute: executeFollowedArtists,
	})
}

func executeFollowedArtists(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	limit := params.ClampInt(params.IntDefault(job.Params, "limit", 20), 1, 50)
	after, _ := params.TextInputOr(job, "after_id", params.StringDefault(job.Params, "after_id", ""))

	q := url.Values{}
	q.Set("type", "artist")
	q.Set("limit", strconv.Itoa(limit))
	if after != "" {
		q.Set("after", after)
	}

	status, body, err := call(ctx, job, http.MethodGet, "/me/following?"+q.Encode(), nil)
	if r := spotifyFailure(job, status, body, err); r != nil {
		return *r, nil
	}
	// Spotify wraps the cursor page in an "artists" key, so the pagination
	// fields sit a level down from the items.
	var parsed struct {
		Artists struct {
			Items []struct {
				ID           string   `json:"id"`
				Name         string   `json:"name"`
				Genres       []string `json:"genres"`
				URI          string   `json:"uri"`
				ExternalURLs struct {
					Spotify string `json:"spotify"`
				} `json:"external_urls"`
				Images []struct {
					URL string `json:"url"`
				} `json:"images"`
			} `json:"items"`
			Next    string `json:"next"`
			Total   int    `json:"total"`
			Cursors struct {
				After string `json:"after"`
			} `json:"cursors"`
		} `json:"artists"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return params.Err(job, "spotify_error", "could not decode Spotify followed-artists response"), nil
	}

	// Flat records rather than Spotify's artist objects: the fields a flow
	// actually templates. Followers and popularity are deliberately absent —
	// Spotify removed both from the artist object in February 2026, so reading
	// them would emit a column that is always empty.
	rows := make([]map[string]any, 0, len(parsed.Artists.Items))
	for _, a := range parsed.Artists.Items {
		row := map[string]any{
			"id":     a.ID,
			"name":   a.Name,
			"genres": a.Genres,
			"uri":    a.URI,
			"url":    a.ExternalURLs.Spotify,
		}
		if len(a.Images) > 0 {
			row["image"] = a.Images[0].URL
		}
		rows = append(rows, row)
	}

	hasMore := parsed.Artists.Next != ""
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"artists":     {MIME: "application/json", Inline: rows},
			"next_cursor": {MIME: "text/plain", Inline: parsed.Artists.Cursors.After},
			"has_more":    {MIME: "text/plain", Inline: strconv.FormatBool(hasMore)},
			"meta": {MIME: "application/json", Inline: map[string]any{
				"count":    len(rows),
				"total":    parsed.Artists.Total,
				"has_more": hasMore,
				"after":    parsed.Artists.Cursors.After,
			}},
		},
	}, nil
}
