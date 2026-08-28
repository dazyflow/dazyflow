// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package fortnox

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/drops/internal/params"
	"git.sr.ht/~klahr/dazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "fortnox_list_invoices",
			Version:     "1.0",
			Label:       "Fortnox",
			Subtitle:    "List invoices",
			Summary:     "Read invoices from Fortnox, filtered by status — the building block for a 'fire on newly paid invoice' flow.",
			Description: "List invoices in your connected Fortnox account, optionally filtered by status (e.g. 'fullypaid' for a paid-invoice feed, 'unpaidoverdue' for a dunning flow). Newest first, one page per poll.\n\nFortnox has no webhooks, so compose a trigger: Schedule/Poll → this step (filter=fullypaid) → For each invoice → dedupe on DocumentNumber against a Set-secret 'seen' list so each paid invoice fires once. Page through with the 'page' param when 'has_more' is true.",
			Integration: "Fortnox",
			Category:    "network",
			Icon:        "file-text",
			BrandLogo:   "/brands/fortnox.svg",
			Color:       "#003824",
			Provider:    "internal",
			Tags:        []string{"fortnox", "invoice", "trigger", "poll", "accounting", "sweden"},
			Examples: []core.ParamsExample{
				{Title: "Newly paid invoices", Params: json.RawMessage(`{"filter":"fullypaid"}`), Notes: "Dedupe later on DocumentNumber against a saved 'seen' secret so each invoice fires once."},
				{Title: "Overdue dunning feed", Params: json.RawMessage(`{"filter":"unpaidoverdue","limit":100}`)},
			},
			RequiresConnections: []core.ConnectionRequirement{
				{Kind: "oauth", Name: "fortnox", Note: "Connect a Fortnox account (invoice scope) under Apps."},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				{Port: "page", Label: "Page", MIME: []string{"text/plain"}},
			},
			Outputs: []core.Port{
				{Port: "invoices", Label: "Invoices", MIME: []string{"application/json"}},
				{Port: "has_more", Label: "Has more", MIME: []string{"text/plain"}},
				{Port: "meta", Label: "Details", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"account":{"type":"string","title":"Account","default":"default","x_advanced":true,"description":"Which connected Fortnox account to use (for multiple connections)."},
					"filter":{"type":"string","title":"Status filter","format":"suggest","enum":["","fullypaid","unpaid","unpaidoverdue","cancelled","unbooked"],"enumNames":["All","Fully paid","Unpaid","Unpaid & overdue","Cancelled","Unbooked"],"description":"Restrict to invoices of this status. Empty = all."},
					"limit":{"type":"integer","title":"Limit","default":100,"minimum":1,"maximum":500,"description":"Max invoices per page."},
					"page":{"type":"integer","title":"Page","default":1,"minimum":1,"description":"1-based page number. Overridden by the 'Page' input."},
					"base_url":{"type":"string","description":"Override the API host (testing)."},
					"timeout_ms":{"type":"integer","default":15000,"minimum":1,"description":"Hard deadline for the request, in milliseconds."}
				}
			}`),
			Idempotent:  true,
			RetryPolicy: core.RetryExponentialBackoff,
		},
		Execute: executeListInvoices,
	})
}

func executeListInvoices(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	page := params.IntDefault(job.Params, "page", 1)
	if pageIn, ok := params.TextInputOr(job, "page", ""); ok && pageIn != "" {
		if n, err := strconv.Atoi(pageIn); err == nil && n >= 1 {
			page = n
		}
	}
	if page < 1 {
		page = 1
	}
	limit := params.IntDefault(job.Params, "limit", 100)
	if limit < 1 {
		limit = 1
	}
	if limit > 500 {
		limit = 500
	}

	q := url.Values{}
	q.Set("limit", strconv.Itoa(limit))
	q.Set("page", strconv.Itoa(page))
	q.Set("sortby", "documentnumber")
	q.Set("sortorder", "descending")
	if f := params.StringDefault(job.Params, "filter", ""); f != "" {
		q.Set("filter", f)
	}

	status, body, err := call(ctx, job, http.MethodGet, "/invoices?"+q.Encode(), nil)
	if r := fortnoxFailure(job, status, body, err); r != nil {
		return *r, nil
	}
	var parsed struct {
		Invoices        []map[string]any `json:"Invoices"`
		MetaInformation struct {
			CurrentPage int `json:"@CurrentPage"`
			TotalPages  int `json:"@TotalPages"`
		} `json:"MetaInformation"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return params.Err(job, "fortnox_error", "could not decode Fortnox invoices response"), nil
	}
	hasMore := parsed.MetaInformation.CurrentPage < parsed.MetaInformation.TotalPages
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"invoices": {MIME: "application/json", Inline: parsed.Invoices},
			"has_more": {MIME: "text/plain", Inline: strconv.FormatBool(hasMore)},
			"meta": {MIME: "application/json", Inline: map[string]any{
				"count":       len(parsed.Invoices),
				"page":        parsed.MetaInformation.CurrentPage,
				"total_pages": parsed.MetaInformation.TotalPages,
				"next_page":   page + 1,
				"has_more":    hasMore,
			}},
		},
	}, nil
}
