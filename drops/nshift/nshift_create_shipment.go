// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package nshift

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
			ID:          "nshift_create_shipment",
			Version:     "1.0",
			Label:       "nShift",
			Subtitle:    "Create shipment",
			Summary:     "Book a shipment with a carrier through your connected nShift account.",
			Description: "Create (book) a shipment with a carrier through nShift. Give it the shipment details — sender, receiver, parcels and the carrier service — as the 'Shipment' input, usually built by an earlier step for each order (you can also type it on the step).\n\nOut come the new 'shipment_id', the parcel 'tracking_numbers' (comma-separated), and the whole created shipment as JSON on the 'Shipment' output. Booking a shipment costs money, so this step runs once and is never retried automatically — Dazyflow won't book the same consignment twice. Connect your nShift account once on the Apps page; leave the connection on 'integration' to test without booking a real consignment.",
			Integration: "nShift",
			Category:    "network",
			Icon:        "truck",
			BrandLogo:   "/brands/nshift.svg",
			Color:       "#0A1E3C",
			Provider:    "internal",
			Tags:        []string{"nshift", "unifaun", "consignor", "shipping", "logistics", "parcel", "carrier", "sweden", "nordic"},
			Examples: []core.ParamsExample{
				{Title: "Book a parcel", Params: json.RawMessage(`{"shipment":{"sender":{"quickId":"1"},"receiver":{"name":"Ada Andersson","address1":"Storgatan 1","zipcode":"11122","city":"Stockholm","country":"SE"},"parcels":[{"copies":1,"weight":2.5}],"service":{"id":"DAOL"}}}`), Notes: "Wire the 'shipment' input from an upstream step that builds the payload per order instead of typing it."},
			},
			ConnectionFields: nshiftConnectionFields(),
			ExecutionModel:   core.ExecutionBatch,
			ProcessModel:     core.ProcessLongLived,
			Inputs: []core.Port{
				{Port: "shipment", Label: "Shipment", Required: true, MIME: []string{"application/json"}},
			},
			Outputs: []core.Port{
				{Port: "shipment_id", Label: "Shipment ID", MIME: []string{"text/plain"}},
				{Port: "tracking_numbers", Label: "Tracking numbers", MIME: []string{"text/plain"}},
				{Port: "shipment", Label: "Shipment", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"shipment":{"type":"object","title":"Shipment","description":"The shipment payload (sender / receiver / parcels / service), following nShift's /shipments schema. Overridden by the 'Shipment' input."},
					"base_url":{"type":"string","description":"Override the API host (testing)."},
					"timeout_ms":{"type":"integer","default":20000,"minimum":1,"description":"Hard deadline for the request, in milliseconds."}
				},
				"required":["shipment"]
			}`),
			// A booking is a money-moving write with no upstream idempotency key,
			// so a retried POST books a second consignment. Retries off; the engine
			// de-dupes a same-job re-execution (expired-lease reclaim / crash).
			Idempotent:   false,
			RetryPolicy:  core.RetryNever,
			DedupeWrites: true,
		},
		Execute: executeCreateShipment,
	})
}

func executeCreateShipment(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	shipment, err := jsonObjectInputOr(job, "shipment")
	if err != nil {
		return params.Err(job, "bad_input", err.Error()), nil
	}
	if len(shipment) == 0 {
		return params.Err(job, "bad_param", "'shipment' is required — set it or wire the 'Shipment' input"), nil
	}
	body, err := json.Marshal(shipment)
	if err != nil {
		return params.Err(job, "bad_param", "'shipment' could not be encoded as JSON"), nil
	}

	status, respBody, _, err := nshiftDo(ctx, job, http.MethodPost, shipmentsPath(job), body)
	if r := nshiftFailure(job, status, respBody, err); r != nil {
		return *r, nil
	}

	// The ExtAPI returns the created shipment(s); a create can yield an array
	// (batch) or a single object. Normalise to the first shipment object for the
	// extracted pins; the full decoded response always rides the 'shipment' pin.
	created, raw := firstShipment(respBody)
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"shipment_id":      {MIME: "text/plain", Inline: stringField(created, "id")},
			"tracking_numbers": {MIME: "text/plain", Inline: joinTracking(trackingNumbers(created))},
			"shipment":         {MIME: "application/json", Inline: raw},
		},
	}, nil
}
