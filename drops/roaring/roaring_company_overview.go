// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package roaring

import (
	"context"
	"encoding/json"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/drops/internal/params"
	"git.sr.ht/~klahr/dazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "roaring_company_overview",
			Version:     "1.0",
			Label:       "Roaring",
			Subtitle:    "Company overview",
			Summary:     "Enrich a Nordic organisation number into company data (name, status, address) via Roaring.",
			Description: "Look up a company in Roaring by its organisation number — the enrichment step that turns a bare org number (e.g. from an order or a form) into structured company data: registered name, status, address and tax info. The org number can be typed on the step or wired in from upstream (the 'Org number' input overrides the param). Defaults to Sweden ('se'); set 'country' for another Nordic market Roaring covers.\n\nOut come the 'name' and 'status' as text and the whole Roaring record on the 'Company' output. This is a read — safe to retry. Connect your Roaring account once on the Apps page (Consumer Key + Secret).",
			Integration: "Roaring",
			Category:    "network",
			Icon:        "building-2",
			BrandLogo:   "/brands/roaring.svg",
			Color:       "#1F6FEB",
			Provider:    "internal",
			Tags:        []string{"roaring", "company", "enrichment", "org-number", "orgnr", "kyc", "business", "sweden", "nordic"},
			Examples: []core.ParamsExample{
				{Title: "Enrich a Swedish org number", Params: json.RawMessage(`{"company_id":"5566778899"}`), Notes: "Wire the 'Org number' input from an order or form step instead of typing it."},
			},
			ConnectionFields: roaringConnectionFields(),
			ExecutionModel:   core.ExecutionBatch,
			ProcessModel:     core.ProcessLongLived,
			Inputs: []core.Port{
				{Port: "company_id", Label: "Org number", Required: true, MIME: []string{"text/plain"}},
			},
			Outputs: []core.Port{
				{Port: "name", Label: "Company name", MIME: []string{"text/plain"}},
				{Port: "status", Label: "Status", MIME: []string{"text/plain"}},
				{Port: "company", Label: "Company", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"company_id":{"type":"string","title":"Org number","description":"The organisation number to look up. Overridden by the 'Org number' input."},
					"country":{"type":"string","title":"Country","default":"se","examples":["se","dk","no","fi"],"description":"ISO country of the register (Roaring is per-country). Defaults to Sweden."},
					"version":{"type":"string","title":"API version","default":"1.0","x_advanced":true,"description":"Roaring Company Overview API version segment."},
					"base_url":{"type":"string","description":"Override the API host (testing)."},
					"timeout_ms":{"type":"integer","default":15000,"minimum":1,"description":"Hard deadline for the request, in milliseconds."}
				},
				"required":["company_id"]
			}`),
			Idempotent:  true,
			RetryPolicy: core.RetryExponentialBackoff,
		},
		Execute: executeCompanyOverview,
	})
}

func executeCompanyOverview(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	companyID, ok := params.TextInputOr(job, "company_id", params.StringDefault(job.Params, "company_id", ""))
	if !ok {
		return params.Err(job, "bad_input", "'Org number' input must be text"), nil
	}
	if companyID == "" {
		return params.Err(job, "bad_param", "'company_id' is required — set it or wire the 'Org number' input"), nil
	}
	ctry := country(params.StringDefault(job.Params, "country", ""))
	version := params.StringDefault(job.Params, "version", "1.0")

	endpoint := baseURL(job) + "/" + ctry + "/company/overview/" + version + "/" + escapePathSeg(companyID)
	status, body, err := roaringGet(ctx, job, endpoint)
	if r := roaringFailure(job, status, body, err); r != nil {
		return *r, nil
	}

	var raw any
	if err := json.Unmarshal(body, &raw); err != nil {
		return params.Err(job, "roaring_error", "Roaring response was not valid JSON"), nil
	}
	obj := asObject(raw)
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"name":    {MIME: "text/plain", Inline: firstString(obj, "companyName", "name")},
			"status":  {MIME: "text/plain", Inline: firstString(obj, "status", "statusText", "companyStatus")},
			"company": {MIME: "application/json", Inline: raw},
		},
	}, nil
}
