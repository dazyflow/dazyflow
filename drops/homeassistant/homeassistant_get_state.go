// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package homeassistant

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/drops/internal/params"
	"git.sr.ht/~klahr/dazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "homeassistant_get_state",
			Version:     "1.0",
			Label:       "Home Assistant",
			Subtitle:    "Get state",
			Summary:     "Read an entity's current state and attributes from Home Assistant.",
			Description: "Look up where something is right now — is the door open, what's the temperature, is the light on. Give it an entity (like sensor.kitchen_temperature) and it returns the current State plus all its Attributes, ready to connect into a notification, a condition, or a sheet. The entity can be typed on the step or connected in from another step.",
			Integration: "Home Assistant",
			Category:    "network",
			Icon:        "house",
			BrandLogo:   "/brands/homeassistant.svg",
			Color:       "#18BCF2",
			Provider:    "internal",
			Tags:        []string{"home assistant", "homeassistant", "hass", "smart home", "iot", "state", "sensor", "read"},
			Examples: []core.ParamsExample{
				{
					Title:  "Read a sensor",
					Params: json.RawMessage(`{"entity_id":"sensor.kitchen_temperature"}`),
				},
				{
					Title:  "Check if a light is on",
					Params: json.RawMessage(`{"entity_id":"light.living_room"}`),
					Notes:  "State is the value ('on'/'off'/a number as text); Attributes carries brightness, friendly_name, etc.",
				},
			},
			ConnectionFields: []core.ConnectionField{
				{Key: "base_url", Label: "Instance URL", Required: true, Placeholder: "http://homeassistant.local:8123"},
				{Key: "token", Label: "Long-lived access token", Secret: true, Required: true, Help: "Create one in Home Assistant under Profile → Long-Lived Access Tokens."},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				{Port: "entity_id", Label: "Entity", MIME: []string{"text/plain"}},
			},
			Outputs: []core.Port{
				{Port: "state", Label: "State", MIME: []string{"text/plain"}},
				{Port: "attributes", Label: "Attributes", MIME: []string{"application/json"}},
				{Port: "entity", Label: "Full entity", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"entity_id":{"type":"string","format":"homeassistant-entity","title":"Entity","description":"Which entity to read — e.g. sensor.kitchen_temperature, light.living_room, binary_sensor.front_door. Overridden by the 'Entity' input."},
					"timeout_ms":{"type":"integer","default":15000,"minimum":1,"description":"Hard deadline for the request, in milliseconds."}
				},
				"required":["entity_id"]
			}`),
			// Reading state has no side effects — safe to retry on a blip.
			Idempotent:  true,
			RetryPolicy: core.RetryExponentialBackoff,
		},
		Execute: executeGetState,
	})
}

// executeGetState GETs /api/states/<entity_id> and emits the current state as
// a text pin plus the attributes and the full entity object as JSON pins. A
// 404 means the entity_id doesn't exist on this instance — surfaced with a
// pointed message rather than a bare HTTP status.
func executeGetState(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	entityID, ok := params.TextInputOr(job, "entity_id", params.StringDefault(job.Params, "entity_id", ""))
	if !ok {
		return params.Err(job, "bad_input", "'Entity' input must be text"), nil
	}
	entityID = strings.TrimSpace(entityID)
	if entityID == "" {
		return params.Err(job, "bad_param", "'entity_id' is required — set it (e.g. sensor.kitchen_temperature) or connect the 'Entity' input"), nil
	}

	status, body, err := haDo(ctx, job, "GET", "/api/states/"+url.PathEscape(entityID), nil)
	if err == nil && status == 404 {
		return params.Err(job, "not_found", "No entity called "+entityID+" on this Home Assistant instance — check the entity_id (Developer Tools → States lists them)."), nil
	}
	if f := httpFailure(job, status, body, err); f != nil {
		return *f, nil
	}

	var st entityState
	if uerr := json.Unmarshal(body, &st); uerr != nil {
		return params.ErrDetails(job, "ha_error", "Home Assistant returned an unexpected response for this entity.", uerr.Error()), nil
	}

	attrs := st.Attributes
	if attrs == nil {
		attrs = map[string]any{}
	}
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"state":      {MIME: "text/plain", Inline: st.State},
			"attributes": {MIME: "application/json", Inline: attrs},
			"entity":     {MIME: "application/json", Inline: st},
		},
	}, nil
}
