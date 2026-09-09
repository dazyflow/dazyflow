// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package slack hosts the Slack connector: two action drops
// (slack_send_message, slack_list_channels) for v1, with the slack_on_mention
// webhook trigger a follow-up because Events-API routing crosses the daemon
// boundary.
//
// Token resolution: the drops accept either an explicit `token` param (for tests
// and users pasting a bot token by hand) or an `account` param that the daemon's
// OAuth registry maps to the connected workspace's access token via the
// SetTokenLookup hook. The hook avoids an import cycle; production wires it at
// startup and tests can stub it.
package slack

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/internal/apibase"
	"github.com/dazyflow/dazyflow/drops/internal/oauthtok"
	hfnet "github.com/dazyflow/dazyflow/drops/net"
)

// maxResponseBytes caps how much of an API response we buffer, so a
// hostile or buggy upstream (reachable via the base_url override) can't
// OOM the daemon by streaming an unbounded body. Generous enough for any
// real Slack response.
const maxResponseBytes = 64 << 20 // 64 MiB

var tokenHook = oauthtok.New("Slack", "slack", "slack")

func SetTokenLookup(fn oauthtok.Lookup) { tokenHook.Set(fn) }

func resolveToken(ctx context.Context, job core.Job) (string, error) {
	return tokenHook.Resolve(ctx, job)
}

// resolveBlocks pulls the Block Kit array off the job in priority order:
// 'blocks' input port → params.blocks. Returns (nil, nil) when neither is set
// so the caller can fall through to text-only.
//
// It normalises the shapes people actually paste/build (see normalizeBlocks):
// a bare array (the canonical form), the Block Kit Builder's wrapped payload
// {"blocks":[…]}, or a lone block object — and parses string/[]byte JSON
// uniformly. Anything else is a wiring mistake — a JobError with a friendly
// Message and a typed Details so the UI can split the two.
func resolveBlocks(job core.Job) (any, *core.JobError) {
	if input, ok := job.Input["blocks"]; ok && input.Inline != nil {
		return normalizeBlocks(input.Inline)
	}
	if v, ok := job.Params["blocks"]; ok && v != nil {
		return normalizeBlocks(v)
	}
	return nil, nil
}

func normalizeBlocks(v any) (any, *core.JobError) {
	switch b := v.(type) {
	case []any:
		return b, nil
	case string:
		return parseBlocksJSON([]byte(b))
	case []byte:
		return parseBlocksJSON(b)
	case map[string]any:
		if inner, ok := b["blocks"].([]any); ok {
			return inner, nil // {"blocks":[…]} — unwrap.
		}
		return []any{b}, nil // a single block object — wrap it.
	default:
		return nil, &core.JobError{
			Code:    "bad_input",
			Message: "Slack Block Kit needs an array of block objects like [ {…}, {…} ]. This step is sending a different shape — connect a JSON array (or a single block) into 'Blocks'.",
			Details: fmt.Sprintf("Received type %T on 'blocks'; expected an array, a {\"blocks\":[…]} object, a single block object, or JSON text of one of those.", v),
		}
	}
}

func parseBlocksJSON(data []byte) (any, *core.JobError) {
	var parsed any
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, &core.JobError{
			Code:    "bad_input",
			Message: "Slack Block Kit needs valid JSON — an array of block objects like [ {…}, {…} ]. Check for missing quotes, trailing commas, or single quotes.",
			Details: fmt.Sprintf("JSON parse on 'blocks' failed: %v", err),
		}
	}
	return normalizeBlocks(parsed)
}

var httpBase = apibase.New("https://slack.com/api")

func SetHTTPBase(base string) { httpBase.Set(base) }

type slackEnvelope struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

func decodeSlackJSON(body []byte) (slackEnvelope, map[string]any, error) {
	var env slackEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return env, nil, fmt.Errorf("parse Slack response: %w", err)
	}
	var raw map[string]any
	_ = json.Unmarshal(body, &raw) // best-effort; envelope is the source of truth for ok/error
	return env, raw, nil
}

func slackBaseURL(job core.Job) string { return httpBase.For(job) }

// slackDo runs one authenticated Slack Web API call and returns the
// shared {ok,error,...} envelope plus the raw decoded body. A non-2xx
// status is a transport error; an {ok:false} body is returned for the
// caller to translate into a friendly slack_error. timeoutMS bounds the
// request (defaults to 15s). body is nil for GETs.
func slackDo(ctx context.Context, method, url, token string, body []byte, timeoutMS int) (slackEnvelope, map[string]any, error) {
	if timeoutMS <= 0 {
		timeoutMS = 15000
	}
	headers := map[string]string{"Authorization": "Bearer " + token}
	if body != nil {
		headers["Content-Type"] = "application/json; charset=utf-8"
	}

	// base_url is a tenant-supplied param, so net.Do guards the dial: the SSRF
	// client blocks loopback/private/link-local targets (cloud metadata,
	// internal services) and the egress allowlist (when the operator sets one)
	// bounds which public hosts the bearer token may be sent to.
	status, raw, _, err := hfnet.Do(ctx, method, url, headers, body, timeoutMS, maxResponseBytes)
	if err != nil {
		return slackEnvelope{}, nil, err
	}
	if status < 200 || status >= 300 {
		return slackEnvelope{}, nil, fmt.Errorf("slack returned %d: %s", status, string(raw))
	}
	return decodeSlackJSON(raw)
}
