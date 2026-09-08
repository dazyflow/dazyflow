// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package spotify hosts the native Spotify connector. Auth is Spotify OAuth2:
// the daemon owns the token (its provider entry uses client_secret_basic, which
// Spotify's token endpoint expects) and this package resolves it per-job via
// the oauthtok hook the other OAuth connectors share.
//
// Two facts shape what can be built here. Spotify has no webhooks, so anything
// event-shaped polls — Schedule → a read step → dedupe, the same composition
// the Fortnox and Gmail connectors document. And a Spotify app stays in
// development mode unless its owner is a business with 250k monthly users, so
// an install can connect at most five allowlisted listeners and the app owner
// needs Premium; that ceiling belongs in front of an operator before they wire
// a flow, so it is in the provider's SetupHelp.
//
// Unlike Notion, no `token` param is offered on the steps: a Spotify access
// token expires in an hour, so a pasted one is a flow that breaks by lunchtime.
// Tests still inject through it — the oauthtok resolve sequence honors it —
// they just do it with an undeclared param.
package spotify

import (
	"context"
	"encoding/json"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/internal/apibase"
	"github.com/dazyflow/dazyflow/drops/internal/oauthtok"
	"github.com/dazyflow/dazyflow/drops/internal/params"
	hfnet "github.com/dazyflow/dazyflow/drops/net"
)

// maxResponseBytes caps how much of an API response we buffer, so a hostile or
// buggy upstream (reachable via the base_url override) can't OOM the daemon by
// streaming an unbounded body.
const maxResponseBytes = 8 << 20 // 8 MiB

var tokenHook = oauthtok.New("Spotify", "spotify", "Spotify")

func SetTokenLookup(fn oauthtok.Lookup) { tokenHook.Set(fn) }

func resolveToken(ctx context.Context, job core.Job) (string, error) {
	return tokenHook.Resolve(ctx, job)
}

var httpBase = apibase.New("https://api.spotify.com/v1")

func SetHTTPBase(base string) { httpBase.Set(base) }

func baseURL(job core.Job) string { return httpBase.For(job) }

// spotifyDo runs one authenticated Spotify API call. token is passed in (not
// resolved here) so tests can inject a value and the SSRF-guard test can call
// this directly.
func spotifyDo(ctx context.Context, method, url, token string, body []byte, timeoutMS int) (int, []byte, error) {
	if timeoutMS <= 0 {
		timeoutMS = 15000
	}
	headers := map[string]string{
		"Authorization": "Bearer " + token,
		"Accept":        "application/json",
	}
	if body != nil {
		headers["Content-Type"] = "application/json"
	}
	// base_url is a tenant-supplied param, so net.Do guards the dial: the SSRF
	// client blocks loopback/private/link-local targets and the egress allowlist
	// (when set) bounds which public hosts the bearer token may be sent to.
	status, raw, _, err := hfnet.Do(ctx, method, url, headers, body, timeoutMS, maxResponseBytes)
	return status, raw, err
}

func call(ctx context.Context, job core.Job, method, path string, body []byte) (int, []byte, error) {
	token, err := resolveToken(ctx, job)
	if err != nil {
		return 0, nil, err
	}
	return spotifyDo(ctx, method, baseURL(job)+path, token, body, params.TimeoutMS(job, 15000))
}

func extractSpotifyError(body []byte) string {
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &e); err == nil && e.Error.Message != "" {
		return e.Error.Message
	}
	return params.Truncate(string(body), 200)
}

func spotifyFailure(job core.Job, status int, body []byte, err error) *core.Result {
	return params.HTTPFailure(job, "spotify", "Spotify", status, body, err, extractSpotifyError)
}
