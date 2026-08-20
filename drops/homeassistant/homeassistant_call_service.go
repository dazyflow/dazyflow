// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package homeassistant

import (
	"context"
	"encoding/json"
	"maps"
	"net/url"
	"strings"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/drops/internal/params"
	"git.sr.ht/~klahr/dazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "homeassistant_call_service",
			Version:     "1.0",
			Label:       "Home Assistant",
			Subtitle:    "Call service",
			Summary:     "Run a Home Assistant service — turn a light on/off, run a script, set a scene.",
			Description: "Tell Home Assistant to do something: turn a light or switch on or off, run a script, activate a scene, set a thermostat. Pick the service (like light.turn_on) and the entity it acts on; both can be typed on the step or wired in from another step. Extra options (brightness, temperature, …) go in 'Service data'.",
			Integration: "Home Assistant",
			Category:    "network",
			Icon:        "house",
			BrandLogo:   "/brands/homeassistant.svg",
			Color:       "#18BCF2",
			Provider:    "internal",
			Tags:        []string{"home assistant", "homeassistant", "hass", "smart home", "iot", "light", "switch", "scene", "service"},
			Examples: []core.ParamsExample{
				{
					Title:  "Turn a light on",
					Params: json.RawMessage(`{"service":"light.turn_on","entity_id":"light.living_room"}`),
				},
				{
					Title:  "Turn a light on at 50% brightness",
					Params: json.RawMessage(`{"service":"light.turn_on","entity_id":"light.living_room","data":{"brightness_pct":50}}`),
					Notes:  "Anything in 'data' is passed straight through as service data alongside the entity_id.",
				},
				{
					Title:  "Activate a scene",
					Params: json.RawMessage(`{"service":"scene.turn_on","entity_id":"scene.movie_night"}`),
				},
			},
			// base_url + token are the per-tenant connection (injected into the
			// node's params at run time), not node fields — flows carry only
			// which service to call and on what.
			ConnectionFields: []core.ConnectionField{
				{Key: "base_url", Label: "Instance URL", Required: true, Placeholder: "http://homeassistant.local:8123"},
				{Key: "token", Label: "Long-lived access token", Secret: true, Required: true, Help: "Create one in Home Assistant under Profile → Long-Lived Access Tokens."},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				// Named after their params so the card shows inline editable
				// boxes (Unreal-style); a wired value overrides the typed one.
				{Port: "service", Label: "Service", MIME: []string{"text/plain"}},
				{Port: "entity_id", Label: "Entity", MIME: []string{"text/plain"}},
			},
			// Re-emit the targeted entity so a following step can check it —
			// e.g. Call service (light.turn_on) → Get state (Entity wired) →
			// Branch. Same "re-emit an id for chaining" pattern as Sheets'
			// Spreadsheet ID. Omitted when the service targets no entity (the
			// edge stays dormant, so the follow-up is skipped). The states Home
			// Assistant reports as changed are still EMITTED under "meta" for run
			// records (emitted-but-undeclared convention); chain control via pass.
			Outputs: []core.Port{
				{Port: "entity_id", Label: "Entity", MIME: []string{"text/plain"}},
				{Port: "meta", Label: "Details", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"service":{"type":"string","format":"homeassistant-service","title":"Service","description":"What to do, as domain.service — e.g. light.turn_on, switch.toggle, scene.turn_on, script.run. Overridden by the 'Service' input."},
					"entity_id":{"type":"string","format":"homeassistant-entity","title":"Entity","description":"Which entity it acts on — e.g. light.living_room. Leave empty for services that don't target one. Overridden by the 'Entity' input."},
					"data":{"type":"object","title":"Service data","x_advanced":true,"description":"Extra service options passed through as-is — e.g. {\"brightness_pct\":50} or {\"temperature\":21}."},
					"timeout_ms":{"type":"integer","default":15000,"minimum":1,"description":"Hard deadline for the request, in milliseconds."}
				},
				"required":["service"]
			}`),
			// Calling a service is not idempotent in general. turn_on/turn_off
			// happen to absorb duplicates, but this drop is generic over every
			// service — lock.unlock, cover.open_cover, button.press, script.*,
			// *.toggle, media volume steps are NOT safe to replay, and Home
			// Assistant's REST API has no idempotency key to dedupe a retry.
			// So a transport blip after HA already executed would fire the
			// physical action twice. Don't auto-retry; the author can wire an
			// explicit on_error edge for the services they know are safe.
			Idempotent:  false,
			RetryPolicy: core.RetryNever,
			// …and the engine dedupes a same-job re-execution (expired-lease
			// reclaim / crash recovery) so a recovered run doesn't re-invoke
			// the service.
			DedupeWrites: true,
		},
		Execute: executeCallService,
	})
}

// executeCallService POSTs to /api/services/<domain>/<service>. The service
// (domain.service) and entity each take their value from the matching input
// port when wired, else from the param. The entity_id is merged into the
// service-data body (Home Assistant's protocol), along with anything in the
// 'data' param.
func executeCallService(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	service, ok := params.TextInputOr(job, "service", params.StringDefault(job.Params, "service", ""))
	if !ok {
		return params.Err(job, "bad_input", "'Service' input must be text"), nil
	}
	service = strings.TrimSpace(service)
	if service == "" {
		return params.Err(job, "bad_param", "'service' is required — set it (e.g. light.turn_on) or wire the 'Service' input"), nil
	}
	domain, svc, found := strings.Cut(service, ".")
	if !found || domain == "" || svc == "" {
		return params.Err(job, "bad_param", "'service' must be written as domain.service — e.g. light.turn_on, not just turn_on"), nil
	}

	entityID, ok := params.TextInputOr(job, "entity_id", params.StringDefault(job.Params, "entity_id", ""))
	if !ok {
		return params.Err(job, "bad_input", "'Entity' input must be text"), nil
	}
	entityID = strings.TrimSpace(entityID)

	// Build the service-data body: start from the 'data' param (if any), then
	// layer entity_id on top so the targeted entity always wins.
	body := map[string]any{}
	if raw, present := job.Params["data"]; present && raw != nil {
		if m, isMap := raw.(map[string]any); isMap {
			maps.Copy(body, m)
		} else {
			return params.Err(job, "bad_param", "'data' must be an object of service options — e.g. {\"brightness_pct\":50}"), nil
		}
	}
	if entityID != "" {
		body["entity_id"] = entityID
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return params.Err(job, "internal", err.Error()), nil
	}

	status, respBody, err := haDo(ctx, job, "POST", "/api/services/"+url.PathEscape(domain)+"/"+url.PathEscape(svc), payload)
	if f := httpFailure(job, status, respBody, err); f != nil {
		return *f, nil
	}

	// Home Assistant returns the array of states it changed. Keep it under
	// "meta" for run records; it's not a declared pin.
	var changed []entityState
	_ = json.Unmarshal(respBody, &changed)
	out := map[string]core.Ref{"meta": {MIME: "application/json", Inline: map[string]any{
		"service":          service,
		"entity_id":        entityID,
		"changed_count":    len(changed),
		"changed_entities": changed,
	}}}
	// Re-emit the targeted entity for chaining into a status check. Omit it
	// for entity-less services so the downstream edge stays dormant.
	if entityID != "" {
		out["entity_id"] = core.Ref{MIME: "text/plain", Inline: entityID}
	}
	return core.Result{JobID: job.ID, Status: core.StatusOK, Output: out}, nil
}
