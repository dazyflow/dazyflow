// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package homeassistant hosts the native Home Assistant connector: call a
// service (turn a light on, run a script, set a thermostat), read an entity's
// current state, and a poll-driven trigger that fires when a watched entity
// changes state.
//
// Auth + endpoint are a per-tenant ConnectionFields bundle (base_url + a
// long-lived access token), configured once on the integration page and injected
// into each node's unset params at run time, so flows carry only the per-use
// fields.
//
// Home Assistant usually lives on the LAN, and the base_url is tenant-supplied,
// so every dial goes through the shared SSRF guard: a public Nabu Casa URL works
// out of the box, and reaching a LAN instance needs
// DAZYFLOW_ALLOW_PRIVATE_EGRESS — the same posture the Postgres/MySQL drops take
// for private DB hosts, and the egress error says so.
//
// The state-changed trigger remembers the last state it emitted per (flow, node)
// via the cursor store, the same mechanism google_form_trigger uses.
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

func extractError(body []byte) string {
	return params.JSONFieldMessage(body, "message", 300)
}

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
	return params.HTTPFailure(job, "ha", "Home Assistant", status, body, nil, extractError)
}

type entityState struct {
	EntityID    string         `json:"entity_id"`
	State       string         `json:"state"`
	Attributes  map[string]any `json:"attributes"`
	LastChanged string         `json:"last_changed"`
	LastUpdated string         `json:"last_updated"`
}

func (e entityState) friendlyName() string {
	if n, ok := e.Attributes["friendly_name"].(string); ok && n != "" {
		return n
	}
	return e.EntityID
}
