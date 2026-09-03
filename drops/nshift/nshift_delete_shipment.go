// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package nshift

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
			ID:          "nshift_delete_shipment",
			Version:     "1.0",
			Label:       "nShift",
			Subtitle:    "Delete shipment",
			Summary:     "Delete an unprinted draft shipment from your connected nShift account.",
			Description: "Delete a shipment from nShift by its id — the way to cancel a draft consignment you booked in error. nShift only allows deleting a shipment that has not been printed/confirmed; a printed one is rejected and the reason is surfaced. The id can be typed on the step or connected from an earlier step (the 'Shipment ID' input overrides the param).\n\nOut comes 'deleted' (true) on success. Deleting can't be undone, so this step runs once and is never retried automatically. Connect your nShift account once on the Apps page.",
			Integration: "nShift",
			Category:    "network",
			Icon:        "trash-2",
			BrandLogo:   "/brands/nshift.svg",
			Color:       "#0A1E3C",
			Provider:    "internal",
			Tags:        []string{"nshift", "unifaun", "consignor", "shipping", "logistics", "parcel", "cancel", "sweden", "nordic"},
			Examples: []core.ParamsExample{
				{Title: "Cancel a draft shipment", Params: json.RawMessage(`{"shipment_id":"774"}`), Notes: "Only an unprinted shipment can be deleted."},
			},
			ConnectionFields: nshiftConnectionFields(),
			ExecutionModel:   core.ExecutionBatch,
			ProcessModel:     core.ProcessLongLived,
			Inputs: []core.Port{
				{Port: "shipment_id", Label: "Shipment ID", Required: true, MIME: []string{"text/plain"}},
			},
			Outputs: []core.Port{
				{Port: "deleted", Label: "Deleted", MIME: []string{"text/plain"}, Example: json.RawMessage(`"true"`)},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"shipment_id":{"type":"string","title":"Shipment ID","description":"The nShift shipment id to delete. Overridden by the 'Shipment ID' input."},
					"base_url":{"type":"string","description":"Override the API host (testing)."},
					"timeout_ms":{"type":"integer","default":20000,"minimum":1,"description":"Hard deadline for the request, in milliseconds."}
				},
				"required":["shipment_id"]
			}`),
			// Destructive write, no idempotency key. A retried DELETE of an
			// already-gone id 404s, so retries are off; the engine de-dupes a
			// same-job re-execution (expired-lease reclaim / crash recovery).
			Idempotent:   false,
			RetryPolicy:  core.RetryNever,
			DedupeWrites: true,
		},
		Execute: executeDeleteShipment,
	})
}

func executeDeleteShipment(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	id, ok := params.TextInputOr(job, "shipment_id", params.StringDefault(job.Params, "shipment_id", ""))
	if !ok {
		return params.Err(job, "bad_input", "'Shipment ID' input must be text"), nil
	}
	if id == "" {
		return params.Err(job, "bad_param", "'shipment_id' is required — set it or connect the 'Shipment ID' input"), nil
	}

	status, body, _, err := nshiftDo(ctx, job, http.MethodDelete, shipmentPath(job, id), nil)
	if r := nshiftFailure(job, status, body, err); r != nil {
		return *r, nil
	}
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"deleted": {MIME: "text/plain", Inline: "true"},
		},
	}, nil
}
