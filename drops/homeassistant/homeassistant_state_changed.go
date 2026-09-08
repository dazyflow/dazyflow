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

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/cursor"
	"github.com/dazyflow/dazyflow/drops/internal/params"
	"github.com/dazyflow/dazyflow/engine"
	"github.com/dazyflow/dazyflow/pollstate"
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
				{Port: "state", Label: "State", MIME: []string{"text/plain"}, Example: json.RawMessage(`"on"`)},
				{Port: "previous_state", Label: "Previous state", MIME: []string{"text/plain"}, Example: json.RawMessage(`"off"`)},
				{Port: "attributes", Label: "Attributes", MIME: []string{"application/json"}},
				{Port: "entity", Label: "Full entity", MIME: []string{"application/json"}},
				{Port: "fired_at", Label: "Time", MIME: []string{"text/plain"}, Example: json.RawMessage(`"2026-02-12T08:12:04Z"`)},
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
			Idempotent: false,
		},
		Execute: executeStateChanged,
	})
}

type cursorState struct {
	LastChanged string `json:"lc"`
	State       string `json:"state"`
}

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

	cursorName := fmt.Sprintf("cursor.homeassistant.%s.%s", job.GraphID, job.NodeID)
	prev, rerr := readStoredCursor(ctx, job.Tenant, cursorName)
	if rerr != nil {
		// Without the previous observation this run cannot tell a change from a
		// steady state, and calling it a first observation would overwrite the
		// stored one — losing the change that happened in between.
		return cursor.FailRead(job, rerr), nil
	}

	now := cursorState{LastChanged: cur.LastChanged, State: cur.State}

	if prev == nil {
		// Nothing was emitted, so a failed write is not "re-emit next time" —
		// it is a first observation that never landed, and the next run makes
		// the same one. A persistent failure means the entity is never
		// actually watched, on green runs. See cursor.FailBaseline.
		if werr := writeStoredCursor(ctx, job.Tenant, cursorName, now); werr != nil {
			return cursor.FailBaseline(job, werr), nil
		}
		pollstate.Report(ctx, job, false) // no change to act on yet
		return noChange(job), nil
	}

	if prev.LastChanged == cur.LastChanged && prev.State == cur.State {
		pollstate.Report(ctx, job, false) // empty poll — let the scheduler back off
		return noChange(job), nil
	}

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

func noChange(job core.Job) core.Result {
	return core.Result{JobID: job.ID, Status: core.StatusOK, Output: map[string]core.Ref{}}
}

// readStoredCursor decodes the persisted cursorState, or nil when nothing is
// stored yet (first observation) or the stored value is unparseable.
//
// A failed READ returns an error instead of nil: nil means "no previous
// observation", which makes the caller record the current state and fire
// nothing, and doing that on a transient read failure would overwrite a
// position that was fine.
func readStoredCursor(ctx context.Context, tenant, name string) (*cursorState, error) {
	raw, err := cursor.Read(ctx, tenant, name)
	if err != nil {
		return nil, err
	}
	if raw == "" {
		return nil, nil
	}
	var c cursorState
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		return nil, nil
	}
	return &c, nil
}

func writeStoredCursor(ctx context.Context, tenant, name string, c cursorState) error {
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return cursor.Write(ctx, tenant, name, string(b))
}
