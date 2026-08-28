// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package roaring

import (
	"context"
	"encoding/json"
	"net/url"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/drops/internal/params"
	"git.sr.ht/~klahr/dazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "roaring_company_search",
			Version:     "1.0",
			Label:       "Roaring",
			Subtitle:    "Company search",
			Summary:     "Find Nordic companies by name via Roaring — resolve a name to candidate org numbers.",
			Description: "Search Roaring for companies by name — the resolve step before enrichment: turn a typed or earlier company name into candidate matches (each with its organisation number), which you then feed to 'Company overview'. The search text can be typed on the step or connected from an earlier step (the 'Query' input overrides the param). Defaults to Sweden ('se').\n\nOut come the match 'count' as text and the whole Roaring search response on the 'Results' output (iterate it with a For Each to enrich each match). This is a read — safe to retry. Connect your Roaring account once on the Apps page (Consumer Key + Secret).",
			Integration: "Roaring",
			Category:    "network",
			Icon:        "search",
			BrandLogo:   "/brands/roaring.svg",
			Color:       "#1F6FEB",
			Provider:    "internal",
			Tags:        []string{"roaring", "company", "search", "enrichment", "org-number", "orgnr", "business", "sweden", "nordic"},
			Examples: []core.ParamsExample{
				{Title: "Search by name", Params: json.RawMessage(`{"query":"Spotify"}`), Notes: "Feed a match's org number into 'Company overview' to enrich it."},
			},
			ConnectionFields: roaringConnectionFields(),
			ExecutionModel:   core.ExecutionBatch,
			ProcessModel:     core.ProcessLongLived,
			Inputs: []core.Port{
				{Port: "query", Label: "Query", Required: true, MIME: []string{"text/plain"}},
			},
			Outputs: []core.Port{
				{Port: "count", Label: "Match count", MIME: []string{"text/plain"}},
				{Port: "results", Label: "Results", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"query":{"type":"string","title":"Query","description":"Company name to search for. Overridden by the 'Query' input."},
					"country":{"type":"string","title":"Country","default":"se","examples":["se","dk","no","fi"],"description":"ISO country of the register (Roaring is per-country). Defaults to Sweden."},
					"version":{"type":"string","title":"API version","default":"2.0","x_advanced":true,"description":"Roaring Company Search API version segment."},
					"base_url":{"type":"string","description":"Override the API host (testing)."},
					"timeout_ms":{"type":"integer","default":15000,"minimum":1,"description":"Hard deadline for the request, in milliseconds."}
				},
				"required":["query"]
			}`),
			Idempotent:  true,
			RetryPolicy: core.RetryExponentialBackoff,
		},
		Execute: executeCompanySearch,
	})
}

func executeCompanySearch(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	query, ok := params.TextInputOr(job, "query", params.StringDefault(job.Params, "query", ""))
	if !ok {
		return params.Err(job, "bad_input", "'Query' input must be text"), nil
	}
	if query == "" {
		return params.Err(job, "bad_param", "'query' is required — set it or connect the 'Query' input"), nil
	}
	ctry := country(params.StringDefault(job.Params, "country", ""))
	version := params.StringDefault(job.Params, "version", "2.0")

	q := url.Values{}
	q.Set("companyName", query)
	endpoint := baseURL(job) + "/" + ctry + "/company/search/" + version + "?" + q.Encode()
	status, body, err := roaringGet(ctx, job, endpoint)
	if r := roaringFailure(job, status, body, err); r != nil {
		return *r, nil
	}

	var raw any
	if err := json.Unmarshal(body, &raw); err != nil {
		return params.Err(job, "roaring_error", "Roaring response was not valid JSON"), nil
	}
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"count":   {MIME: "text/plain", Inline: itoa(countHits(asObject(raw)))},
			"results": {MIME: "application/json", Inline: raw},
		},
	}, nil
}
