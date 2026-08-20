// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package fortnox

import (
	"context"
	"encoding/json"
	"net/http"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/drops/internal/params"
	"git.sr.ht/~klahr/dazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "fortnox_create_customer",
			Version:     "1.0",
			Label:       "Fortnox",
			Subtitle:    "Create customer",
			Summary:     "Create a customer in Fortnox — the entry point of most billing automations (form signup → customer → invoice).",
			Description: "Create a customer in your connected Fortnox account. Name (required) and Email can be typed on the step or wired in from upstream (the matching input port overrides the param). The new customer's number comes out on the 'Customer number' output, ready to wire into 'Create invoice'.\n\nFortnox has no idempotency key, so this step does not auto-retry — a retried create would make a duplicate customer.",
			Integration: "Fortnox",
			Category:    "network",
			Icon:        "user-plus",
			BrandLogo:   "/brands/fortnox.svg",
			Color:       "#003824",
			Provider:    "internal",
			Tags:        []string{"fortnox", "customer", "accounting", "invoicing", "sweden"},
			Examples: []core.ParamsExample{
				{Title: "Customer from a form signup", Params: json.RawMessage(`{"name":"Acme AB","email":"faktura@acme.se"}`), Notes: "Wire the form's name/email outputs into the matching pins instead of typing them."},
			},
			RequiresConnections: []core.ConnectionRequirement{
				{Kind: "oauth", Name: "fortnox", Note: "Connect a Fortnox account (customer scope) under Apps."},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				{Port: "name", Label: "Name", Required: true, MIME: []string{"text/plain"}},
				{Port: "email", Label: "Email", MIME: []string{"text/plain"}},
			},
			Outputs: []core.Port{
				{Port: "customer_number", Label: "Customer number", MIME: []string{"text/plain"}},
				{Port: "meta", Label: "Details", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"account":{"type":"string","title":"Account","default":"default","x_advanced":true,"description":"Which connected Fortnox account to use (for multiple connections)."},
					"name":{"type":"string","title":"Name","description":"Customer name. Overridden by the 'Name' input."},
					"email":{"type":"string","title":"Email","description":"Overridden by the 'Email' input."},
					"base_url":{"type":"string","description":"Override the API host (testing)."},
					"timeout_ms":{"type":"integer","default":15000,"minimum":1,"description":"Hard deadline for the request, in milliseconds."}
				}
			}`),
			Idempotent:  false,
			RetryPolicy: core.RetryNever,
			// A non-idempotent external write: opt into engine-side dedupe so an
			// expired-lease reclaim or crash recovery replays the recorded result
			// instead of firing the write a second time. Matches the other
			// send-style drops (discord/gmail/sheets/twilio/klarna/nshift/elks).
			DedupeWrites: true,
		},
		Execute: executeCreateCustomer,
	})
}

func executeCreateCustomer(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	name, ok := params.TextInputOr(job, "name", params.StringDefault(job.Params, "name", ""))
	if !ok {
		return params.Err(job, "bad_input", "'Name' input must be text"), nil
	}
	if name == "" {
		return params.Err(job, "bad_param", "'name' is required — set it or wire the 'Name' input"), nil
	}
	email, ok := params.TextInputOr(job, "email", params.StringDefault(job.Params, "email", ""))
	if !ok {
		return params.Err(job, "bad_input", "'Email' input must be text"), nil
	}

	// Fortnox wraps the request body in a singular "Customer" envelope.
	payload := map[string]any{"Name": name}
	if email != "" {
		payload["Email"] = email
	}
	body, err := json.Marshal(map[string]any{"Customer": payload})
	if err != nil {
		return params.Err(job, "bad_param", "encode customer: "+err.Error()), nil
	}

	status, respBody, err := call(ctx, job, http.MethodPost, "/customers", body)
	if r := fortnoxFailure(job, status, respBody, err); r != nil {
		return *r, nil
	}
	var parsed struct {
		Customer struct {
			CustomerNumber string `json:"CustomerNumber"`
			Name           string `json:"Name"`
			Email          string `json:"Email"`
		} `json:"Customer"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil || parsed.Customer.CustomerNumber == "" {
		return params.Err(job, "fortnox_error", "Fortnox response had no customer number"), nil
	}
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"customer_number": {MIME: "text/plain", Inline: parsed.Customer.CustomerNumber},
			"meta": {MIME: "application/json", Inline: map[string]any{
				"customer_number": parsed.Customer.CustomerNumber,
				"name":            parsed.Customer.Name,
				"email":           parsed.Customer.Email,
			}},
		},
	}, nil
}
