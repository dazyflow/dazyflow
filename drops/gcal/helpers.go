// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package gcal

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"time"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/internal/apibase"
	"github.com/dazyflow/dazyflow/drops/internal/google"
	"github.com/dazyflow/dazyflow/drops/internal/params"
)

// maxResponseBytes caps how much of an API response we buffer, so a hostile or
// buggy upstream can't OOM the daemon by streaming an unbounded body.
const maxResponseBytes = 16 << 20 // 16 MiB

const calendarAPIBase = "https://www.googleapis.com/calendar/v3"

func SetTokenLookup(fn google.TokenLookup) { google.SetTokenLookup(fn) }

func resolveToken(ctx context.Context, job core.Job) (string, error) {
	return google.ResolveToken(ctx, job)
}

var calBase = apibase.New(calendarAPIBase)

func SetHTTPBase(base string) { calBase.Set(base) }

func calBaseURL(job core.Job) string {
	if b, _ := params.StringOpt(job.Params, "base_url"); b != "" {
		return b
	}
	return calBase.Get()
}

func googleDo(ctx context.Context, method, url, token, contentType string, body []byte, timeoutMS int) (int, []byte, error) {
	return google.Do(ctx, method, url, token, contentType, body, timeoutMS, maxResponseBytes)
}

// calErr pulls the human message out of a Google API error envelope, falling
// back to a bounded slice of the raw body.
func calErr(body []byte) string { return google.ErrMessage(body, 512) }

func calendarID(job core.Job) string {
	id := params.StringDefault(job.Params, "calendar_id", "")
	if id == "" {
		return "primary"
	}
	return id
}

// eventsPage runs one events.list call with a prepared query and hands back the
// decoded items plus the token to continue paging with. The trigger drops build
// their own queries (a change feed and a start window want different
// parameters), so what they share is the call, not the query.
func eventsPage(ctx context.Context, job core.Job, token, calID string, q url.Values) (items []rawEvent, nextPageToken string, err error) {
	endpoint := calBaseURL(job) + "/calendars/" + url.PathEscape(calID) + "/events?" + q.Encode()
	status, body, err := googleDo(ctx, "GET", endpoint, token, "", nil, params.IntDefault(job.Params, "timeout_ms", 15000))
	if err != nil {
		return nil, "", err
	}
	if status < 200 || status >= 300 {
		return nil, "", fmt.Errorf("%s", calErr(body))
	}
	var parsed struct {
		Items         []rawEvent `json:"items"`
		NextPageToken string     `json:"nextPageToken"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, "", fmt.Errorf("events.list decode: %w", err)
	}
	return parsed.Items, parsed.NextPageToken, nil
}

// parseStamp reads one of Google's timestamps — RFC3339, usually with
// milliseconds. It reports failure rather than a zero time so a caller can
// tell "no position recorded yet" from "the epoch", which are the same value
// and opposite instructions.
func parseStamp(s string) (time.Time, bool) {
	if s == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

func nowStamp() string { return time.Now().UTC().Format(time.RFC3339) }

// nothingToReport is what a watch returns when a check found nothing worth
// firing for: an empty output map, which the engine reads as "skip the rest of
// the flow".
func nothingToReport(job core.Job) core.Result {
	return core.Result{JobID: job.ID, Status: core.StatusOK, Output: map[string]core.Ref{}}
}
