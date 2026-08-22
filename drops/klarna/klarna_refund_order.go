// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package klarna

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
			ID:          "klarna_refund_order",
			Version:     "1.0",
			Label:       "Klarna",
			Subtitle:    "Refund order",
			Summary:     "Refund a captured Klarna order, fully or partially — pairs with an approval step before it.",
			Description: "Refund a captured Klarna order by its order id (typed or connected from an earlier step). Leave Amount empty to refund the full remaining refundable amount (captured minus already refunded), or set it in the currency's smallest unit (öre/cents) for a partial refund. A short Description shows on the customer's Klarna statement. Put an Approval step before this one for the classic 'approve in Slack → refund' flow.\n\nThe new refund's id comes out on 'refund_id'. Refunding pays money back, so this step runs once and is never retried automatically — Dazyflow won't refund the same order twice. Connect your Klarna account once on the Apps page.",
			Integration: "Klarna",
			Category:    "network",
			Icon:        "credit-card",
			BrandLogo:   "/brands/klarna.svg",
			Color:       "#FFB3C7",
			Provider:    "internal",
			Tags:        []string{"klarna", "refund", "order", "payment", "bnpl", "support", "sweden", "nordic"},
			Examples: []core.ParamsExample{
				{Title: "Full refund", Params: json.RawMessage(`{"order_id":"3d4f2b1a-1234-4a5b-9c8d-0e1f2a3b4c5d","description":"Returned"}`), Notes: "Amount omitted → refunds the whole remaining refundable amount."},
				{Title: "Partial refund", Params: json.RawMessage(`{"order_id":"3d4f2b1a-1234-4a5b-9c8d-0e1f2a3b4c5d","amount":500,"description":"Goodwill"}`), Notes: "amount is in the smallest unit — 500 = 5.00 SEK/EUR."},
			},
			ConnectionFields: klarnaConnectionFields(),
			ExecutionModel:   core.ExecutionBatch,
			ProcessModel:     core.ProcessLongLived,
			Inputs: []core.Port{
				{Port: "order_id", Label: "Order ID", Required: true, MIME: []string{"text/plain"}},
				{Port: "amount", Label: "Amount (smallest unit)", MIME: []string{"text/plain", "application/json"}},
			},
			Outputs: []core.Port{
				{Port: "refund_id", Label: "Refund ID", MIME: []string{"text/plain"}},
				{Port: "refunded_amount", Label: "Refunded amount (smallest unit)", MIME: []string{"text/plain"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"order_id":{"type":"string","title":"Order ID","description":"The Klarna order to refund. Overridden by the 'Order ID' input."},
					"amount":{"type":"integer","title":"Amount (smallest unit)","minimum":1,"description":"Leave empty to refund the whole remaining refundable amount (captured minus already refunded). For a partial refund, enter the amount in the currency's smallest unit — e.g. 500 = 5.00. Overridden by the 'Amount' input."},
					"description":{"type":"string","title":"Description","description":"A note shown on the customer's Klarna statement (e.g. \"Returned\")."},
					"base_url":{"type":"string","description":"Override the API host (testing)."},
					"timeout_ms":{"type":"integer","default":15000,"minimum":1,"description":"Hard deadline for the request, in milliseconds."}
				},
				"required":["order_id"]
			}`),
			Idempotent: false,
			// Klarna refunds have no reliable idempotency key here, and a retried
			// POST refunds a second time. So auto-retry is off and the engine
			// dedupes a same-job re-execution (expired-lease reclaim / crash
			// recovery) so a recovered run doesn't re-refund.
			RetryPolicy:  core.RetryNever,
			DedupeWrites: true,
		},
		Execute: executeRefundOrder,
	})
}

func executeRefundOrder(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	orderID, ok := params.TextInputOr(job, "order_id", params.StringDefault(job.Params, "order_id", ""))
	if !ok {
		return params.Err(job, "bad_input", "'Order ID' input must be text"), nil
	}
	if orderID == "" {
		return params.Err(job, "bad_param", "'order_id' is required — set it or connect the 'Order ID' input"), nil
	}
	amount, ok := wholeNumberInputOr(job, "amount", params.IntDefault(job.Params, "amount", 0))
	if !ok {
		return params.Err(job, "bad_input", "'Amount' input must be a whole number (smallest currency unit, e.g. 500 = 5.00)"), nil
	}
	if amount < 0 {
		return params.Err(job, "bad_input", "'Amount' cannot be negative"), nil
	}

	// Full refund: no amount given, so read the order and refund what's left
	// refundable — the captured amount minus what's already been refunded.
	if amount == 0 {
		o, r := fetchOrder(ctx, job, orderID)
		if r != nil {
			return *r, nil
		}
		refundable := o.CapturedAmount - o.RefundedAmount
		if refundable <= 0 {
			return params.Err(job, "klarna_error", "order has no remaining refundable amount — set 'amount' for a partial refund"), nil
		}
		amount = int(refundable)
	}

	body, err := json.Marshal(map[string]any{
		"refunded_amount": amount,
		"description":     params.StringDefault(job.Params, "description", ""),
	})
	if err != nil {
		return params.Err(job, "bad_param", "encode refund: "+err.Error()), nil
	}

	status, respBody, hdr, err := klarnaDo(ctx, job, http.MethodPost, orderPath(job, orderID)+"/refunds", body)
	if r := klarnaFailure(job, status, respBody, err); r != nil {
		return *r, nil
	}

	refundID := idFromHeaderOrLocation(hdr, "Refund-ID")
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"refund_id":       {MIME: "text/plain", Inline: refundID},
			"refunded_amount": {MIME: "text/plain", Inline: itoa64(int64(amount))},
		},
	}, nil
}
