// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package homeassistant

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/drops/internal/params"
	"git.sr.ht/~klahr/dazyflow/engine"
	"git.sr.ht/~klahr/dazyflow/pollstate"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "homeassistant_state_changed",
			Version:     "1.0",
			Label:       "Home Assistant",
			Subtitle:    "When state changes",
			Summary:     "Fires when a Home Assistant entity changes state — e.g. a door opens or a light turns on.",
			Description: "Watches one entity and starts the flow when its state changes — the front door opens, the temperature crosses a reading, a light turns on. Emits the new State, the Previous state, and the entity's Attributes. When a check finds no change, the rest of the flow is skipped. Publish the flow so it runs automatically on the schedule below; pressing Run only checks once (and records the current state without firing, so the next real change fires cleanly).",
			Integration: "Home Assistant",
			Category:    "trigger",
			Icon:        "house",
			BrandLogo:   "/brands/homeassistant.svg",
			Color:       "#18BCF2",
			Provider:    "internal",
			Tags:        []string{"home assistant", "homeassistant", "hass", "smart home", "iot", "trigger", "poll", "state"},
			Examples: []core.ParamsExample{
				{
					Title:  "When the front door opens, check every 30s",
					Params: json.RawMessage(`{"entity_id":"binary_sensor.front_door","interval_seconds":30}`),
					Notes:  "Checks on this interval; the new and previous values come out on the 'State' / 'Previous state' outputs, only when the state actually changes.",
				},
			},
			ConnectionFields: []core.ConnectionField{
				{Key: "base_url", Label: "Instance URL", Required: true, Placeholder: "http://homeassistant.local:8123"},
				{Key: "token", Label: "Long-lived access token", Secret: true, Required: true, Help: "Create one in Home Assistant under Profile → Long-Lived Access Tokens."},
			},
			ExecutionModel: core.ExecutionTrigger,
			ProcessModel:   core.ProcessLongLived,
			Outputs: []core.Port{
				{Port: "state", Label: "State", MIME: []string{"text/plain"}},
				{Port: "previous_state", Label: "Previous state", MIME: []string{"text/plain"}},
				{Port: "attributes", Label: "Attributes", MIME: []string{"application/json"}},
				{Port: "entity", Label: "Full entity", MIME: []string{"application/json"}},
				{Port: "fired_at", Label: "Time", MIME: []string{"text/plain"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"entity_id":{"type":"string","format":"homeassistant-entity","title":"Entity","description":"Which entity to watch — e.g. binary_sensor.front_door, light.living_room, sensor.kitchen_temperature."},
					"interval_seconds":{
						"type":"integer",
						"title":"Check every",
						"format":"duration-seconds",
						"minimum":1,
						"maximum":31622400,
						"default":60,
						"description":"How often to check for a change once the flow is published. Leave blank to only check when you press Run (for testing)."
					},
					"timeout_ms":{"type":"integer","default":15000,"minimum":1,"description":"Hard deadline for the request, in milliseconds."}
				},
				"required":["entity_id"]
			}`),
			// A fire is a discrete poll observation; rerunning re-reads against
			// the stored watermark rather than re-deriving a past change.
			Idempotent: false,
		},
		Execute: executeStateChanged,
	})
}

// cursorState is what we persist between fires: the last_changed timestamp we
// last acted on plus the state value at that point. Storing the state too
// lets us emit `previous_state` without a second API call.
type cursorState struct {
	LastChanged string `json:"lc"`
	State       string `json:"state"`
}

// executeStateChanged polls one entity and fires only when its state has
// changed since the last observation. The watermark is the entity's
// last_changed timestamp (which HA advances only on a real state change),
// kept per (flow, node) in the cursor store.
//
// First observation (no stored cursor) records the current state and emits
// NOTHING — so publishing the flow doesn't spuriously fire on whatever the
// state happens to be. Thereafter, an advanced last_changed fires once,
// emitting the new state, the previous state, and the attributes. An
// unchanged poll emits no outputs, leaving downstream edges dormant (the
// dispatcher skips the rest of the flow) — same non-event pattern as the
// Google Forms trigger.
func executeStateChanged(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	entityID := strings.TrimSpace(params.StringDefault(job.Params, "entity_id", ""))
	if entityID == "" {
		return params.Err(job, "bad_param", "'entity_id' is required — e.g. binary_sensor.front_door"), nil
	}

	status, body, err := haDo(ctx, job, "GET", "/api/states/"+url.PathEscape(entityID), nil)
	if err == nil && status == 404 {
		return params.Err(job, "not_found", "No entity called "+entityID+" on this Home Assistant instance — check the entity_id (Developer Tools → States lists them)."), nil
	}
	if f := httpFailure(job, status, body, err); f != nil {
		return *f, nil
	}

	var cur entityState
	if uerr := json.Unmarshal(body, &cur); uerr != nil {
		return params.ErrDetails(job, "ha_error", "Home Assistant returned an unexpected response for this entity.", uerr.Error()), nil
	}

	// cursor.homeassistant.<graph>.<node>: per-(flow,node) watermark. The
	// store hides the "cursor." prefix from the Credentials UI.
	cursorName := fmt.Sprintf("cursor.homeassistant.%s.%s", job.GraphID, job.NodeID)
	prev := readStoredCursor(ctx, job.Tenant, cursorName)

	now := cursorState{LastChanged: cur.LastChanged, State: cur.State}

	// First observation: remember the current state, fire nothing.
	if prev == nil {
		_ = writeStoredCursor(ctx, job.Tenant, cursorName, now)
		pollstate.Report(ctx, job, false) // no change to act on yet
		return noChange(job), nil
	}

	// No change: same last_changed (and, defensively, same state value).
	if prev.LastChanged == cur.LastChanged && prev.State == cur.State {
		pollstate.Report(ctx, job, false) // empty poll — let the scheduler back off
		return noChange(job), nil
	}

	// Changed — advance the watermark, then fire. A failed write is at-least-
	// once: at worst the next poll re-fires this same change.
	_ = writeStoredCursor(ctx, job.Tenant, cursorName, now)
	pollstate.Report(ctx, job, true) // active — keep polling at the base cadence

	attrs := cur.Attributes
	if attrs == nil {
		attrs = map[string]any{}
	}
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"state":          {MIME: "text/plain", Inline: cur.State},
			"previous_state": {MIME: "text/plain", Inline: prev.State},
			"attributes":     {MIME: "application/json", Inline: attrs},
			"entity":         {MIME: "application/json", Inline: cur},
			"fired_at":       {MIME: "text/plain", Inline: time.Now().UTC().Format(time.RFC3339)},
		},
	}, nil
}

// noChange is the empty result for an unchanged (or first) observation: no
// output ports, so every downstream edge is dormant and the rest of the flow
// is skipped.
func noChange(job core.Job) core.Result {
	return core.Result{JobID: job.ID, Status: core.StatusOK, Output: map[string]core.Ref{}}
}

// readStoredCursor decodes the persisted cursorState, or nil when nothing is
// stored yet (first observation) or the stored value is unparseable.
func readStoredCursor(ctx context.Context, tenant, name string) *cursorState {
	raw := readCursor(ctx, tenant, name)
	if raw == "" {
		return nil
	}
	var c cursorState
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		return nil
	}
	return &c
}

func writeStoredCursor(ctx context.Context, tenant, name string, c cursorState) error {
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return writeCursor(ctx, tenant, name, string(b))
}
