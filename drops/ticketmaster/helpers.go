// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package ticketmaster hosts the Ticketmaster Discovery API connector: search
// live events, and a polled trigger that fires when one shows up the flow has
// not seen before.
//
// It finishes the Spotify connector's sentence — Spotify has no events data, so
// "tell me when someone I follow plays near me" composes as
// spotify_followed_artists → this search → notify.
//
// Auth is a single per-tenant ConnectionField, set once on the Apps page.
// Ticketmaster takes the key as an `apikey` QUERY parameter, not a header, so no
// error message here ever quotes the request URL.
//
// Discovery is ticketing inventory, not a listings database: events sold through
// Ticketmaster and its family are in it, a club show sold via DICE or Tickster
// is not, and no query will make it appear.
package ticketmaster

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/internal/apibase"
	"github.com/dazyflow/dazyflow/drops/internal/params"
	"github.com/dazyflow/dazyflow/drops/internal/reltime"
	hfnet "github.com/dazyflow/dazyflow/drops/net"
)

const maxResponseBytes = 16 << 20 // 16 MiB

const pageLimit = 200

var httpBase = apibase.New("https://app.ticketmaster.com/discovery/v2")

func SetHTTPBase(base string) { httpBase.Set(base) }

func baseURL(job core.Job) string { return httpBase.For(job) }

func connectionFields() []core.ConnectionField {
	return []core.ConnectionField{
		{Key: "api_key", Label: "API key", Secret: true, Required: true,
			Help: "The Consumer Key of an app created at developer.ticketmaster.com. The free tier allows 5000 calls a day."},
	}
}

func resolveKey(job core.Job) (string, error) {
	key := strings.TrimSpace(params.StringDefault(job.Params, "api_key", ""))
	if key == "" {
		return "", fmt.Errorf("Ticketmaster is not connected: add your API key on the Apps page (Ticketmaster)")
	}
	return key, nil
}

func eventQuery(job core.Job, limit int, now time.Time) (url.Values, error) {
	q := url.Values{}
	if kw := strings.TrimSpace(params.StringDefault(job.Params, "keyword", "")); kw != "" {
		q.Set("keyword", kw)
	}
	if id := strings.TrimSpace(params.StringDefault(job.Params, "attraction_id", "")); id != "" {
		q.Set("attractionId", id)
	}
	if cc := strings.TrimSpace(params.StringDefault(job.Params, "country_code", "")); cc != "" {
		q.Set("countryCode", strings.ToUpper(cc))
	}
	if city := strings.TrimSpace(params.StringDefault(job.Params, "city", "")); city != "" {
		q.Set("city", city)
	}
	if c := strings.TrimSpace(params.StringDefault(job.Params, "classification", "")); c != "" {
		q.Set("classificationName", c)
	}
	for param, field := range map[string]string{"startDateTime": "start_date", "endDateTime": "end_date"} {
		raw := strings.TrimSpace(params.StringDefault(job.Params, field, ""))
		if raw == "" {
			continue
		}
		// Relative windows ("+90d") are resolved in UTC: a concert search
		// window is weeks wide, so the day boundary a local zone would move
		// changes nothing, and asking for a timezone would be one more field
		// on every node for no answer anyone would notice.
		t, ok, err := reltime.Resolve(raw, time.UTC, now)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		q.Set(param, t.UTC().Format("2006-01-02T15:04:05Z"))
	}
	q.Set("size", strconv.Itoa(limit))
	// Date order is what a "what's coming up" list means; relevance order (the
	// API default) would put next year's stadium show above tonight's.
	q.Set("sort", "date,asc")
	return q, nil
}

func searchEvents(ctx context.Context, job core.Job, limit, page int, now time.Time) ([]map[string]any, pageInfo, *core.Result) {
	key, err := resolveKey(job)
	if err != nil {
		r := params.Err(job, "auth", err.Error())
		return nil, pageInfo{}, &r
	}
	q, err := eventQuery(job, limit, now)
	if err != nil {
		r := params.Err(job, "bad_param", err.Error())
		return nil, pageInfo{}, &r
	}
	if page > 0 {
		q.Set("page", strconv.Itoa(page))
	}
	q.Set("apikey", key)

	status, body, err := tmGet(ctx, baseURL(job)+"/events.json?"+q.Encode(), params.TimeoutMS(job, 15000))
	if r := tmFailure(job, status, body, err); r != nil {
		return nil, pageInfo{}, r
	}
	var parsed struct {
		Embedded struct {
			Events []map[string]any `json:"events"`
		} `json:"_embedded"`
		Page pageInfo `json:"page"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		r := params.Err(job, "ticketmaster_error", "could not decode the Ticketmaster events response")
		return nil, pageInfo{}, &r
	}
	rows := make([]map[string]any, 0, len(parsed.Embedded.Events))
	for _, e := range parsed.Embedded.Events {
		rows = append(rows, flattenEvent(e))
	}
	return rows, parsed.Page, nil
}

type pageInfo struct {
	Size          int `json:"size"`
	TotalElements int `json:"totalElements"`
	TotalPages    int `json:"totalPages"`
	Number        int `json:"number"`
}

// tmGet runs one Discovery GET. The URL carries the API key, so it is never
// echoed into an error.
func tmGet(ctx context.Context, url string, timeoutMS int) (int, []byte, error) {
	if timeoutMS <= 0 {
		timeoutMS = 15000
	}
	headers := map[string]string{"Accept": "application/json"}
	// base_url is a tenant-supplied param, so net.Do guards the dial: the SSRF
	// client blocks loopback/private/link-local targets and the egress allowlist
	// (when set) bounds which public hosts the API key may be sent to.
	status, raw, _, err := hfnet.Do(ctx, "GET", url, headers, nil, timeoutMS, maxResponseBytes)
	return status, raw, err
}

func extractTMError(body []byte) string {
	var e struct {
		Fault struct {
			FaultString string `json:"faultstring"`
		} `json:"fault"`
		Errors []struct {
			Detail string `json:"detail"`
			Code   string `json:"code"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(body, &e); err == nil {
		if e.Fault.FaultString != "" {
			return e.Fault.FaultString
		}
		if len(e.Errors) > 0 && e.Errors[0].Detail != "" {
			return e.Errors[0].Detail
		}
	}
	return params.Truncate(string(body), 200)
}

func tmFailure(job core.Job, status int, body []byte, err error) *core.Result {
	if err != nil {
		r := params.Err(job, "ticketmaster_http_error", err.Error())
		return &r
	}
	switch {
	case status == 401 || status == 403:
		msg := "Ticketmaster rejected the API key. Check the Consumer Key on the Apps page — a newly created key can take a few minutes to start working."
		if detail := extractTMError(body); detail != "" {
			msg = "Ticketmaster rejected the API key: " + detail
		}
		r := params.Err(job, "auth", msg)
		return &r
	case status == 429:
		r := params.Err(job, "rate_limited",
			"Ticketmaster is rate limiting this key (5 requests a second, 5000 a day). Widen the schedule on this step, or narrow the search so fewer calls are needed.")
		return &r
	}
	return params.HTTPFailure(job, "ticketmaster", "Ticketmaster", status, body, nil, extractTMError)
}

func flattenEvent(e map[string]any) map[string]any {
	row := map[string]any{
		"id":   str(e["id"]),
		"name": str(e["name"]),
		"url":  str(e["url"]),
	}
	if dates, ok := e["dates"].(map[string]any); ok {
		if start, ok := dates["start"].(map[string]any); ok {
			row["date"] = str(start["localDate"])
			row["time"] = str(start["localTime"])
			row["datetime"] = str(start["dateTime"])
		}
		if st, ok := dates["status"].(map[string]any); ok {
			row["status"] = str(st["code"])
		}
	}
	if emb, ok := e["_embedded"].(map[string]any); ok {
		if venues, ok := emb["venues"].([]any); ok && len(venues) > 0 {
			if v, ok := venues[0].(map[string]any); ok {
				row["venue"] = str(v["name"])
				if city, ok := v["city"].(map[string]any); ok {
					row["city"] = str(city["name"])
				}
				if country, ok := v["country"].(map[string]any); ok {
					row["country"] = str(country["countryCode"])
				}
			}
		}
		if attrs, ok := emb["attractions"].([]any); ok && len(attrs) > 0 {
			if a, ok := attrs[0].(map[string]any); ok {
				row["artist"] = str(a["name"])
			}
		}
	}
	if img := widestImage(e["images"]); img != "" {
		row["image"] = img
	}
	return row
}

// widestImage picks the largest artwork Discovery offers. It returns every
// size of every crop in no useful order, so "the first one" is a thumbnail as
// often as not.
func widestImage(v any) string {
	arr, ok := v.([]any)
	if !ok {
		return ""
	}
	best, bestW := "", -1.0
	for _, it := range arr {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		w, _ := m["width"].(float64)
		if u := str(m["url"]); u != "" && w > bestW {
			best, bestW = u, w
		}
	}
	return best
}

func str(v any) string {
	s, _ := v.(string)
	return s
}
