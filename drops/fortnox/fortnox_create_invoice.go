// SPDX-FileCopyrightText: 2026 Angels' Ware
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
			ID:          "fortnox_create_invoice",
			Version:     "1.0",
			Label:       "Fortnox",
			Subtitle:    "Create invoice",
			Summary:     "Create an invoice in Fortnox for a customer — the headline billing step (customer → invoice).",
			Description: "Create an invoice in your connected Fortnox account. Pick the customer (or connect the 'Customer number' input from 'Create customer'), and supply the invoice rows.\n\nRows are Fortnox InvoiceRow objects — each an object with the Fortnox field names, e.g. {\"Description\":\"Consulting\",\"Price\":1500,\"DeliveredQuantity\":\"2\"}. Connect an array into the 'Rows' input, or set the 'rows' param. The created invoice's document number comes out on 'document_number'.\n\nFortnox has no idempotency key, so this step does not auto-retry — a retried create would make a duplicate invoice.",
			Integration: "Fortnox",
			Category:    "network",
			Icon:        "file-text",
			BrandLogo:   "/brands/fortnox.svg",
			Color:       "#003824",
			Provider:    "internal",
			Tags:        []string{"fortnox", "invoice", "accounting", "invoicing", "sweden"},
			Examples: []core.ParamsExample{
				{Title: "One-line consulting invoice", Params: json.RawMessage(`{"customer_number":"1","rows":[{"Description":"Consulting","Price":1500,"DeliveredQuantity":"2"}]}`), Notes: "Connect 'Customer number' from a preceding Create customer step instead of typing it."},
			},
			RequiresConnections: []core.ConnectionRequirement{
				{Kind: "oauth", Name: "fortnox", Note: "Connect a Fortnox account (invoice + customer scopes) under Apps."},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				{Port: "customer_number", Label: "Customer number", MIME: []string{"text/plain"}},
				{Port: "rows", Label: "Rows", MIME: []string{"application/json"}},
			},
			Outputs: []core.Port{
				{Port: "document_number", Label: "Document number", MIME: []string{"text/plain"}},
				{Port: "meta", Label: "Details", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"account":{"type":"string","title":"Account","default":"default","x_advanced":true,"description":"Which connected Fortnox account to use (for multiple connections)."},
					"customer_number":{"type":"string","format":"fortnox-customer","title":"Customer","description":"Pick the customer to bill — listed from your connected account. Overridden by the 'Customer number' input when connected."},
					"rows":{"type":"array","title":"Rows","description":"Invoice rows as Fortnox InvoiceRow objects (Description, Price, DeliveredQuantity, ArticleNumber, VAT…). Overridden by the 'Rows' input.","items":{"type":"object"}},
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
		Execute: executeCreateInvoice,
	})
}

func executeCreateInvoice(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	customer, ok := params.TextInputOr(job, "customer_number", params.StringDefault(job.Params, "customer_number", ""))
	if !ok {
		return params.Err(job, "bad_input", "'Customer number' input must be text"), nil
	}
	if customer == "" {
		return params.Err(job, "bad_param", "'customer_number' is required — pick a customer or connect the 'Customer number' input"), nil
	}

	rows, err := resolveRows(job)
	if err != nil {
		return params.Err(job, "bad_param", err.Error()), nil
	}
	if len(rows) == 0 {
		return params.Err(job, "bad_param", "at least one invoice row is required — set 'rows' or connect the 'Rows' input"), nil
	}

	// Fortnox wraps the request body in a singular "Invoice" envelope.
	body, err := json.Marshal(map[string]any{"Invoice": map[string]any{
		"CustomerNumber": customer,
		"InvoiceRows":    rows,
	}})
	if err != nil {
		return params.Err(job, "bad_param", "encode invoice: "+err.Error()), nil
	}

	status, respBody, err := call(ctx, job, http.MethodPost, "/invoices", body)
	if r := fortnoxFailure(job, status, respBody, err); r != nil {
		return *r, nil
	}
	var parsed struct {
		Invoice struct {
			DocumentNumber string `json:"DocumentNumber"`
			Total          any    `json:"Total"`
			CustomerNumber string `json:"CustomerNumber"`
		} `json:"Invoice"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil || parsed.Invoice.DocumentNumber == "" {
		return params.Err(job, "fortnox_error", "Fortnox response had no invoice document number"), nil
	}
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"document_number": {MIME: "text/plain", Inline: parsed.Invoice.DocumentNumber},
			"meta": {MIME: "application/json", Inline: map[string]any{
				"document_number": parsed.Invoice.DocumentNumber,
				"customer_number": parsed.Invoice.CustomerNumber,
				"total":           parsed.Invoice.Total,
			}},
		},
	}, nil
}

// resolveRows reads the invoice rows from the 'Rows' input (a wired JSON
// array wins) or the 'rows' param. Each row passes through to Fortnox verbatim
// as an InvoiceRow object, so the caller uses Fortnox's PascalCase field names.
func resolveRows(job core.Job) ([]any, error) {
	if in, present := job.Input["rows"]; present && in.Inline != nil {
		switch v := in.Inline.(type) {
		case []any:
			return v, nil
		case []byte:
			return unmarshalRows(v)
		case string:
			return unmarshalRows([]byte(v))
		default:
			return nil, errRowsShape
		}
	}
	if raw, ok := job.Params["rows"].([]any); ok {
		return raw, nil
	}
	return nil, nil
}

var errRowsShape = errInvalid("'Rows' input must be a JSON array of invoice-row objects")

func unmarshalRows(b []byte) ([]any, error) {
	var rows []any
	if err := json.Unmarshal(b, &rows); err != nil {
		return nil, errRowsShape
	}
	return rows, nil
}

// errInvalid is a tiny error type so the sentinel above reads cleanly.
type errInvalid string

func (e errInvalid) Error() string { return string(e) }
