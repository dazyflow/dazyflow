// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package stripe

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/internal/params"
	"github.com/dazyflow/dazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "stripe_get_customer",
			Version:     "1.0",
			Label:       "Stripe",
			Subtitle:    "Get customer",
			Summary:     "Look a customer up by their Stripe id — the step that turns cus_… into an email address.",
			Description: "Fetch one customer by their Stripe id. Every subscription and payment event carries a cus_… id rather than an email, so this is the step that gets you someone to write to: connect the trigger's Customer straight into Customer here, and its Email into a Send email step. (Searching by email instead? Use Search customers — Stripe's search can't look up by id.)",
			Integration: "Stripe",
			Category:    "network",
			Icon:        "credit-card",
			BrandLogo:   "/brands/stripe.svg",
			Color:       "#635BFF",
			Provider:    "internal",
			Tags:        []string{"stripe", "customer", "lookup", "email", "billing"},
			Examples: []core.ParamsExample{
				{
					Title:  "Who cancelled?",
					Params: json.RawMessage(`{}`),
					Notes:  "Connect the On subscription canceled trigger's Customer into Customer, then Email into a Send email step.",
				},
			},
			ConnectionFields: stripeConnectionFields(),
			ExecutionModel:   core.ExecutionBatch,
			ProcessModel:     core.ProcessLongLived,
			Inputs: []core.Port{
				{Port: "customer", Label: "Customer", Required: true, MIME: []string{"text/plain"}},
			},
			Outputs: []core.Port{
				{Port: "email", Label: "Email", MIME: []string{"text/plain"}},
				{Port: "name", Label: "Name", MIME: []string{"text/plain"}},
				{Port: "customer", Label: "Customer", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"customer":{"type":"string","format":"stripe-customer","title":"Customer","description":"The cus_… id to look up. Overridden by the Customer input."},
					"base_url":{"type":"string","description":"Override the API host (testing)."},
					"timeout_ms":{"type":"integer","default":15000,"minimum":1,"description":"Hard deadline for the request, in milliseconds."}
				},
				"required":["customer"]
			}`),
			Idempotent:  true,
			RetryPolicy: core.RetryExponentialBackoff,
		},
		Execute: executeGetCustomer,
	})
}

func executeGetCustomer(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	id, ok := params.TextInputOr(job, "customer", params.StringDefault(job.Params, "customer", ""))
	if !ok {
		return params.Err(job, "bad_input", "'Customer' input must be text"), nil
	}
	if id == "" {
		return params.Err(job, "bad_param", "'customer' is required — set it or connect the Customer input"), nil
	}

	status, body, err := stripeDo(ctx, job, http.MethodGet, baseURL(job)+"/customers/"+url.PathEscape(id), "")
	if r := stripeFailure(job, status, body, err); r != nil {
		return *r, nil
	}
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		return params.Err(job, "stripe_error", "could not decode Stripe customer response"), nil
	}
	email, _ := parsed["email"].(string)
	name, _ := parsed["name"].(string)
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"email":    {MIME: "text/plain", Inline: email},
			"name":     {MIME: "text/plain", Inline: name},
			"customer": {MIME: "application/json", Inline: parsed},
		},
	}, nil
}
