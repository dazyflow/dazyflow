// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package gcal

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/drops/internal/params"
)

// ListCalendars lists the connected account's calendars as {id, name} options —
// the backend for the calendar_id picker on gcal_list_events / gcal_create_event.
// A synthetic "primary" option is prepended (matching the param default) so the
// common case is one click; the account's own primary entry is then skipped to
// avoid a duplicate. Reads account/timeout_ms from job.Params.
func ListCalendars(ctx context.Context, job core.Job) ([]core.AccountResource, error) {
	token, err := resolveToken(ctx, job)
	if err != nil {
		return nil, err
	}
	q := url.Values{}
	q.Set("fields", "items(id,summary,summaryOverride,primary)")
	q.Set("minAccessRole", "writer") // only calendars the user can act on
	endpoint := calBaseURL(job) + "/users/me/calendarList?" + q.Encode()
	status, body, err := googleDo(ctx, "GET", endpoint, token, "", nil, params.IntDefault(job.Params, "timeout_ms", 15000))
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("%s", calErr(body))
	}
	var parsed struct {
		Items []struct {
			ID              string `json:"id"`
			Summary         string `json:"summary"`
			SummaryOverride string `json:"summaryOverride"`
			Primary         bool   `json:"primary"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("calendarList decode: %w", err)
	}
	out := make([]core.AccountResource, 0, len(parsed.Items)+1)
	// "primary" is the alias the param defaults to — list it first.
	out = append(out, core.AccountResource{ID: "primary", Name: "Primary calendar"})
	for _, it := range parsed.Items {
		if it.Primary {
			continue // already represented by the synthetic "primary" option
		}
		name := it.SummaryOverride
		if name == "" {
			name = it.Summary
		}
		if name == "" {
			name = it.ID
		}
		out = append(out, core.AccountResource{ID: it.ID, Name: name})
	}
	return out, nil
}
