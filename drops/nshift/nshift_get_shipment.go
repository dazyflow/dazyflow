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
			ID:          "nshift_get_shipment",
			Version:     "1.0",
			Label:       "nShift",
			Subtitle:    "Get shipment",
			Summary:     "Look up a shipment in your connected nShift account by its id.",
			Description: "Fetch one shipment from nShift by its shipment id (returned when you created it, or from nShift Delivery). The id can be typed on the step or connected from an earlier step (the 'Shipment ID' input overrides the param).\n\nOut come the parcel 'tracking_numbers' (comma-separated) and the whole shipment as JSON on the 'Shipment' output — pair this with a poll trigger to react to a delivery status change. This is a read — safe to retry. Connect your nShift account once on the Apps page.",
			Integration: "nShift",
			Category:    "network",
			Icon:        "package-search",
			BrandLogo:   "/brands/nshift.svg",
			Color:       "#0A1E3C",
			Provider:    "internal",
			Tags:        []string{"nshift", "unifaun", "consignor", "shipping", "logistics", "parcel", "tracking", "sweden", "nordic"},
			Examples: []core.ParamsExample{
				{Title: "Look up a shipment", Params: json.RawMessage(`{"shipment_id":"774"}`), Notes: "Connect the 'Shipment ID' input from a create-shipment step instead of typing it."},
			},
			ConnectionFields: nshiftConnectionFields(),
			ExecutionModel:   core.ExecutionBatch,
			ProcessModel:     core.ProcessLongLived,
			Inputs: []core.Port{
				{Port: "shipment_id", Label: "Shipment ID", Required: true, MIME: []string{"text/plain"}},
			},
			Outputs: []core.Port{
				{Port: "tracking_numbers", Label: "Tracking numbers", MIME: []string{"text/plain"}},
				{Port: "shipment", Label: "Shipment", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"shipment_id":{"type":"string","title":"Shipment ID","description":"The nShift shipment id to look up. Overridden by the 'Shipment ID' input."},
					"base_url":{"type":"string","description":"Override the API host (testing)."},
					"timeout_ms":{"type":"integer","default":20000,"minimum":1,"description":"Hard deadline for the request, in milliseconds."}
				},
				"required":["shipment_id"]
			}`),
			Idempotent:  true,
			RetryPolicy: core.RetryExponentialBackoff,
		},
		Execute: executeGetShipment,
	})
}

func executeGetShipment(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	id, ok := params.TextInputOr(job, "shipment_id", params.StringDefault(job.Params, "shipment_id", ""))
	if !ok {
		return params.Err(job, "bad_input", "'Shipment ID' input must be text"), nil
	}
	if id == "" {
		return params.Err(job, "bad_param", "'shipment_id' is required — set it or connect the 'Shipment ID' input"), nil
	}

	status, body, _, err := nshiftDo(ctx, job, http.MethodGet, shipmentPath(job, id), nil)
	if r := nshiftFailure(job, status, body, err); r != nil {
		return *r, nil
	}

	obj, raw := firstShipment(body)
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"tracking_numbers": {MIME: "text/plain", Inline: joinTracking(trackingNumbers(obj))},
			"shipment":         {MIME: "application/json", Inline: raw},
		},
	}, nil
}
