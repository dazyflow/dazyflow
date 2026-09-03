// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package klarna

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/internal/params"
	"github.com/dazyflow/dazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "klarna_get_order",
			Version:     "1.0",
			Label:       "Klarna",
			Subtitle:    "Get order",
			Summary:     "Look up a Klarna order by id — status and the captured / refunded / remaining amounts.",
			Description: "Fetch one order from your connected Klarna account by its order id (from Klarna's checkout callback or the Merchant Portal). The order id can be typed on the step or connected from an earlier step (the 'Order ID' input overrides the param).\n\nOut come the order 'status' (ORDER_OPEN, PART_CAPTURED, CAPTURED, CANCELLED, EXPIRED, CLOSED), the 'order_amount', 'captured_amount' and 'remaining_authorized_amount' (all in the currency's smallest unit — öre/cents), the 'currency', and the whole order as JSON on the 'Order' output. This is a read — safe to retry. Connect your Klarna account once on the Apps page.",
			Integration: "Klarna",
			Category:    "network",
			Icon:        "search",
			BrandLogo:   "/brands/klarna.svg",
			Color:       "#FFB3C7",
			Provider:    "internal",
			Tags:        []string{"klarna", "order", "payment", "bnpl", "checkout", "sweden", "nordic"},
			Examples: []core.ParamsExample{
				{Title: "Look up an order", Params: json.RawMessage(`{"order_id":"3d4f2b1a-1234-4a5b-9c8d-0e1f2a3b4c5d"}`), Notes: "Connect the 'Order ID' input from a checkout callback instead of typing it."},
			},
			ConnectionFields: klarnaConnectionFields(),
			ExecutionModel:   core.ExecutionBatch,
			ProcessModel:     core.ProcessLongLived,
			Inputs: []core.Port{
				{Port: "order_id", Label: "Order ID", Required: true, MIME: []string{"text/plain"}},
			},
			Outputs: []core.Port{
				{Port: "status", Label: "Status", MIME: []string{"text/plain"}, Example: json.RawMessage(`"CAPTURED"`)},
				{Port: "order_amount", Label: "Order amount (smallest unit)", MIME: []string{"text/plain"}, Example: json.RawMessage(`"24900"`)},
				{Port: "captured_amount", Label: "Captured amount (smallest unit)", MIME: []string{"text/plain"}, Example: json.RawMessage(`"24900"`)},
				{Port: "remaining_authorized_amount", Label: "Remaining authorized (smallest unit)", MIME: []string{"text/plain"}, Example: json.RawMessage(`"0"`)},
				{Port: "currency", Label: "Currency", MIME: []string{"text/plain"}, Example: json.RawMessage(`"SEK"`)},
				{Port: "order", Label: "Order", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"order_id":{"type":"string","title":"Order ID","description":"The Klarna order id to look up. Overridden by the 'Order ID' input."},
					"base_url":{"type":"string","description":"Override the API host (testing)."},
					"timeout_ms":{"type":"integer","default":15000,"minimum":1,"description":"Hard deadline for the request, in milliseconds."}
				},
				"required":["order_id"]
			}`),
			Idempotent:  true,
			RetryPolicy: core.RetryExponentialBackoff,
		},
		Execute: executeGetOrder,
	})
}

func executeGetOrder(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	orderID, ok := params.TextInputOr(job, "order_id", params.StringDefault(job.Params, "order_id", ""))
	if !ok {
		return params.Err(job, "bad_input", "'Order ID' input must be text"), nil
	}
	if orderID == "" {
		return params.Err(job, "bad_param", "'order_id' is required — set it or connect the 'Order ID' input"), nil
	}

	status, body, _, err := klarnaDo(ctx, job, http.MethodGet, orderPath(job, orderID), nil)
	if r := klarnaFailure(job, status, body, err); r != nil {
		return *r, nil
	}

	var o order
	if err := json.Unmarshal(body, &o); err != nil {
		return params.Err(job, "klarna_error", "Klarna response was not valid JSON"), nil
	}
	// Re-decode as a generic object for the 'order' pin so downstream templates
	// can reach fields the typed struct doesn't surface (addresses, line items).
	var raw map[string]any
	_ = json.Unmarshal(body, &raw)

	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"status":                      {MIME: "text/plain", Inline: o.Status},
			"order_amount":                {MIME: "text/plain", Inline: itoa64(o.OrderAmount)},
			"captured_amount":             {MIME: "text/plain", Inline: itoa64(o.CapturedAmount)},
			"remaining_authorized_amount": {MIME: "text/plain", Inline: itoa64(o.RemainingAuthorizedAmount)},
			"currency":                    {MIME: "text/plain", Inline: o.PurchaseCurrency},
			"order":                       {MIME: "application/json", Inline: raw},
		},
	}, nil
}
