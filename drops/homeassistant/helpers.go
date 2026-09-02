// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package homeassistant hosts the native Home Assistant connector: call a
// service (turn a light on, run a script, set a thermostat), read an entity's
// current state, and a poll-driven trigger that fires when a watched entity
// changes state.
//
// Auth + endpoint are a per-tenant ConnectionFields bundle (base_url + a
// long-lived access token), configured once on the integration page rather
// than typed on every node — exactly like ntfy's server+token. The engine
// injects the configured connection into each node's unset params at run time
// (injectConnectionDefaults), so flows carry only the per-use fields (which
// service, which entity).
//
// Home Assistant usually lives on the LAN (http://homeassistant.local:8123,
// 192.168.x.x). The connection's base_url is tenant-supplied, so every dial
// goes through the shared SSRF guard (net.SafeHTTPClient) — which refuses
// loopback/private/link-local targets UNLESS the operator opted in via
// DAZYFLOW_ALLOW_PRIVATE_EGRESS. That's the same posture the Postgres/MySQL
// drops take for private DB hosts: reaching a public Nabu Casa URL works out
// of the box; reaching a LAN instance needs the operator flag. The egress
// error message says exactly that.
//
// The state-changed trigger remembers the last state it emitted per (flow,
// node) via the cursor store the daemon wires at startup (cursor.SetStore),
// the same mechanism google_form_trigger uses for its watermark.
package homeassistant

import (
	"context"
	"fmt"
	"strings"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/internal/params"
	hfnet "github.com/dazyflow/dazyflow/drops/net"
)

// maxResponseBytes caps how much of an API response we buffer, so a hostile
// or buggy instance (reachable via the tenant-supplied base_url) can't OOM
// the daemon by streaming an unbounded body. A full /api/states dump on a
// large home is comfortably under this.
const maxResponseBytes = 16 << 20 // 16 MiB

// resolveConn reads the per-tenant connection the engine injected into the
// node's params: base_url (the instance root, e.g.
// http://homeassistant.local:8123) and token (a long-lived access token).
// Both come from ConnectionFields, so an empty value means the tenant hasn't
// connected Home Assistant yet — the error says exactly that.
func resolveConn(job core.Job) (base, token string, err error) {
	base = strings.TrimRight(strings.TrimSpace(params.StringDefault(job.Params, "base_url", "")), "/")
	token = strings.TrimSpace(params.StringDefault(job.Params, "token", ""))
	if base == "" || token == "" {
		return "", "", fmt.Errorf("Home Assistant isn't connected — add your instance URL and a long-lived access token on the Home Assistant integration page")
	}
	return base, token, nil
}

// haDo runs one authenticated Home Assistant REST call and returns the HTTP
// status + raw body. body is nil for GETs. The dial is SSRF-guarded: the
// base_url is tenant-supplied, so net.SafeHTTPClient refuses private/loopback
// targets unless the operator opted into private egress, and the egress
// allowlist (when set) bounds which hosts the token may be sent to.
func haDo(ctx context.Context, job core.Job, method, path string, body []byte) (int, []byte, error) {
	base, token, err := resolveConn(job)
	if err != nil {
		return 0, nil, err
	}
	url := base + path

	timeoutMS := params.TimeoutMS(job, 15000)
	headers := map[string]string{"Authorization": "Bearer " + token}
	if body != nil {
		headers["Content-Type"] = "application/json"
	}
	status, raw, _, err := hfnet.Do(ctx, method, url, headers, body, timeoutMS, maxResponseBytes)
	return status, raw, err
}

// extractError pulls a human message out of a Home Assistant error body
// ({"message":"..."}), so "Entity not found." reaches the user instead of a
// bare HTTP status. Falls back to a truncated raw body.
func extractError(body []byte) string {
	return params.JSONFieldMessage(body, "message", 300)
}

// httpFailure maps a transport error or a non-2xx response to an error
// Result, with a friendly code per HTTP class. Returns nil on success — the
// shared epilogue of every drop's haDo call. A 401 is the connected token
// being wrong/expired; a 404 from /api/states is an unknown entity_id.
func httpFailure(job core.Job, status int, body []byte, err error) *core.Result {
	if err != nil {
		if hfnet.IsSSRFError(err) {
			r := params.ErrDetails(job, "egress_blocked",
				"Couldn't reach your Home Assistant instance. It looks like a local/private address — the operator must enable private-network access (DAZYFLOW_ALLOW_PRIVATE_EGRESS) for dazyflow to reach it.",
				err.Error())
			return &r
		}
		r := params.Err(job, "ha_http_error", "Couldn't reach Home Assistant: "+err.Error())
		return &r
	}
	if status == 401 {
		r := params.Err(job, "auth", "Home Assistant rejected the access token (401). Re-create a long-lived access token and reconnect.")
		return &r
	}
	// Generic non-2xx → the shared "ha_error: Home Assistant returned %d: %s"
	// epilogue (transport-error path already handled above with HA's friendlier
	// wording, so err is nil here).
	return params.HTTPFailure(job, "ha", "Home Assistant", status, body, nil, extractError)
}

// entityState is the shape Home Assistant returns from GET /api/states/<id>.
// State is the value people care about ("on", "23.5", "home"); Attributes
// carries everything else (brightness, friendly_name, unit_of_measurement);
// LastChanged advances only when State changes (LastUpdated also moves on
// attribute-only updates), which is what the state-changed trigger watermarks.
type entityState struct {
	EntityID    string         `json:"entity_id"`
	State       string         `json:"state"`
	Attributes  map[string]any `json:"attributes"`
	LastChanged string         `json:"last_changed"`
	LastUpdated string         `json:"last_updated"`
}

// friendlyName returns the entity's friendly_name attribute, falling back to
// the entity_id when unset — so result metadata and progress read naturally.
func (e entityState) friendlyName() string {
	if n, ok := e.Attributes["friendly_name"].(string); ok && n != "" {
		return n
	}
	return e.EntityID
}
