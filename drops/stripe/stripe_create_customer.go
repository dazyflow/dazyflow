// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package stripe

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/drops/internal/params"
	"git.sr.ht/~klahr/dazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "stripe_create_customer",
			Version:     "1.0",
			Label:       "Stripe",
			Subtitle:    "Create customer",
			Summary:     "Create a Stripe customer — the entry point of most billing automations (form signup → customer).",
			Description: "Create a customer in your Stripe account. Email, Name and Description can be typed on the step or wired in from upstream (the matching input port overrides the param). The new customer's id comes out on the 'Customer ID' output for downstream Stripe steps; retries reuse the same Idempotency-Key so a flaky run can't create duplicates.",
			Integration: "Stripe",
			Category:    "network",
			Icon:        "credit-card",
			BrandLogo:   "/brands/stripe.svg",
			Color:       "#635BFF",
			Provider:    "internal",
			Tags:        []string{"stripe", "customer", "billing", "payments"},
			Examples: []core.ParamsExample{
				{Title: "Customer from a form signup", Params: json.RawMessage(`{"email":"new@example.com","name":"New Customer"}`), Notes: "Wire the form's email/name outputs into the matching pins instead of typing them."},
				{Title: "With your own reference id", Params: json.RawMessage(`{"email":"new@example.com","metadata":{"crm_id":"acct_42"}}`)},
			},
			ConnectionFields: stripeConnectionFields(),
			ExecutionModel:   core.ExecutionBatch,
			ProcessModel:     core.ProcessLongLived,
			Inputs: []core.Port{
				{Port: "email", Label: "Email", Required: true, MIME: []string{"text/plain"}},
				{Port: "name", Label: "Name", MIME: []string{"text/plain"}},
				{Port: "description", Label: "Description", MIME: []string{"text/plain"}},
			},
			Outputs: []core.Port{
				{Port: "customer_id", Label: "Customer ID", MIME: []string{"text/plain"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"email":{"type":"string","title":"Email","description":"Customer email. Overridden by the 'Email' input."},
					"name":{"type":"string","title":"Name","description":"Overridden by the 'Name' input."},
					"description":{"type":"string","title":"Description","description":"Overridden by the 'Description' input."},
					"metadata":{"type":"object","title":"Metadata","description":"Your own key/value labels on the customer (e.g. a CRM id).","x_advanced":true},
					"base_url":{"type":"string","description":"Override the API host (testing)."},
					"timeout_ms":{"type":"integer","default":15000,"minimum":1,"description":"Hard deadline for the request, in milliseconds."}
				},
				"required":["email"]
			}`),
			Idempotent:  false,
			RetryPolicy: core.RetryExponentialBackoff,
		},
		Execute: executeCreateCustomer,
	})
}

func executeCreateCustomer(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	email, ok := params.TextInputOr(job, "email", params.StringDefault(job.Params, "email", ""))
	if !ok {
		return params.Err(job, "bad_input", "'Email' input must be text"), nil
	}
	if email == "" {
		return params.Err(job, "bad_param", "'email' is required — set it or wire the 'Email' input"), nil
	}
	name, ok := params.TextInputOr(job, "name", params.StringDefault(job.Params, "name", ""))
	if !ok {
		return params.Err(job, "bad_input", "'Name' input must be text"), nil
	}
	description, ok := params.TextInputOr(job, "description", params.StringDefault(job.Params, "description", ""))
	if !ok {
		return params.Err(job, "bad_input", "'Description' input must be text"), nil
	}

	form := url.Values{}
	form.Set("email", email)
	if name != "" {
		form.Set("name", name)
	}
	if description != "" {
		form.Set("description", description)
	}
	if md, ok := job.Params["metadata"].(map[string]any); ok {
		for k, v := range md {
			form.Set("metadata["+k+"]", fmt.Sprint(v))
		}
	}

	status, body, err := stripeDo(ctx, job, http.MethodPost, baseURL(job)+"/customers", form.Encode())
	if r := stripeFailure(job, status, body, err); r != nil {
		return *r, nil
	}
	var parsed struct {
		ID    string `json:"id"`
		Email string `json:"email"`
		Name  string `json:"name"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil || parsed.ID == "" {
		return params.Err(job, "stripe_error", "Stripe response had no customer id"), nil
	}
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"customer_id": {MIME: "text/plain", Inline: parsed.ID},
			"meta": {MIME: "application/json", Inline: map[string]any{
				"id": parsed.ID, "email": parsed.Email, "name": parsed.Name,
			}},
		},
	}, nil
}
