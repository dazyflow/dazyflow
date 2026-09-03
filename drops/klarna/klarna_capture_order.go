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
			ID:          "klarna_capture_order",
			Version:     "1.0",
			Label:       "Klarna",
			Subtitle:    "Capture order",
			Summary:     "Capture a Klarna order when the goods ship — fully, or a partial amount.",
			Description: "Capture (charge) a Klarna order once you fulfil it — the money-movement step after a purchase is authorized. Give the order id (typed or connected from an earlier step); leave Amount empty to capture the whole remaining authorized amount, or set it in the currency's smallest unit (öre/cents) for a partial capture. A short Description shows on the customer's Klarna statement.\n\nThe new capture's id comes out on 'capture_id'. Capturing an order charges the customer, so this step runs once and is never retried automatically — Dazyflow won't charge the same order twice. Connect your Klarna account once on the Apps page.",
			Integration: "Klarna",
			Category:    "network",
			Icon:        "credit-card",
			BrandLogo:   "/brands/klarna.svg",
			Color:       "#FFB3C7",
			Provider:    "internal",
			Tags:        []string{"klarna", "capture", "order", "payment", "bnpl", "fulfilment", "sweden", "nordic"},
			Examples: []core.ParamsExample{
				{Title: "Full capture on ship", Params: json.RawMessage(`{"order_id":"3d4f2b1a-1234-4a5b-9c8d-0e1f2a3b4c5d","description":"Order shipped"}`), Notes: "Amount omitted → captures the whole remaining authorized amount."},
				{Title: "Partial capture", Params: json.RawMessage(`{"order_id":"3d4f2b1a-1234-4a5b-9c8d-0e1f2a3b4c5d","amount":2500,"description":"First shipment"}`), Notes: "amount is in the smallest unit — 2500 = 25.00 SEK/EUR."},
			},
			ConnectionFields: klarnaConnectionFields(),
			ExecutionModel:   core.ExecutionBatch,
			ProcessModel:     core.ProcessLongLived,
			Inputs: []core.Port{
				{Port: "order_id", Label: "Order ID", Required: true, MIME: []string{"text/plain"}},
				{Port: "amount", Label: "Amount (smallest unit)", MIME: []string{"text/plain", "application/json"}},
			},
			Outputs: []core.Port{
				{Port: "capture_id", Label: "Capture ID", MIME: []string{"text/plain"}, Example: json.RawMessage(`"cap_9f2ab7"`)},
				{Port: "captured_amount", Label: "Captured amount (smallest unit)", MIME: []string{"text/plain"}, Example: json.RawMessage(`"24900"`)},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"order_id":{"type":"string","title":"Order ID","description":"The Klarna order to capture. Overridden by the 'Order ID' input."},
					"amount":{"type":"integer","title":"Amount (smallest unit)","minimum":1,"description":"Leave empty to capture the whole remaining authorized amount. For a partial capture, enter the amount in the currency's smallest unit — e.g. 2500 = 25.00. Overridden by the 'Amount' input."},
					"description":{"type":"string","title":"Description","description":"A note shown on the customer's Klarna statement (e.g. \"Order shipped\")."},
					"base_url":{"type":"string","description":"Override the API host (testing)."},
					"timeout_ms":{"type":"integer","default":15000,"minimum":1,"description":"Hard deadline for the request, in milliseconds."}
				},
				"required":["order_id"]
			}`),
			Idempotent: false,
			// Klarna captures have no reliable idempotency key here, and a retried
			// POST captures — and charges — a second time. So auto-retry is off and
			// the engine dedupes a same-job re-execution (expired-lease reclaim /
			// crash recovery) so a recovered run doesn't re-capture.
			RetryPolicy:  core.RetryNever,
			DedupeWrites: true,
		},
		Execute: executeCaptureOrder,
	})
}

func executeCaptureOrder(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	orderID, ok := params.TextInputOr(job, "order_id", params.StringDefault(job.Params, "order_id", ""))
	if !ok {
		return params.Err(job, "bad_input", "'Order ID' input must be text"), nil
	}
	if orderID == "" {
		return params.Err(job, "bad_param", "'order_id' is required — set it or connect the 'Order ID' input"), nil
	}
	amount, ok := wholeNumberInputOr(job, "amount", params.IntDefault(job.Params, "amount", 0))
	if !ok {
		return params.Err(job, "bad_input", "'Amount' input must be a whole number (smallest currency unit, e.g. 2500 = 25.00)"), nil
	}
	if amount < 0 {
		return params.Err(job, "bad_input", "'Amount' cannot be negative"), nil
	}

	// Full capture: no amount given, so read the order's remaining authorized
	// amount and capture exactly that (Klarna requires captured_amount).
	if amount == 0 {
		o, r := fetchOrder(ctx, job, orderID)
		if r != nil {
			return *r, nil
		}
		if o.RemainingAuthorizedAmount <= 0 {
			return params.Err(job, "klarna_error", "order has no remaining authorized amount to capture — set 'amount' for a partial capture"), nil
		}
		amount = int(o.RemainingAuthorizedAmount)
	}

	body, err := json.Marshal(map[string]any{
		"captured_amount": amount,
		"description":     params.StringDefault(job.Params, "description", ""),
	})
	if err != nil {
		return params.Err(job, "bad_param", "encode capture: "+err.Error()), nil
	}

	status, respBody, hdr, err := klarnaDo(ctx, job, http.MethodPost, orderPath(job, orderID)+"/captures", body)
	if r := klarnaFailure(job, status, respBody, err); r != nil {
		return *r, nil
	}

	captureID := idFromHeaderOrLocation(hdr, "Capture-ID")
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"capture_id":      {MIME: "text/plain", Inline: captureID},
			"captured_amount": {MIME: "text/plain", Inline: itoa64(int64(amount))},
		},
	}, nil
}
