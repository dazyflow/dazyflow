// Package slack hosts the Slack launch connector — the first T1
// drops on the path to a Zapier-shape product. Two action drops
// (slack_send_message, slack_list_channels) for v1; the
// slack_on_mention webhook trigger is a follow-up because Events-
// API routing crosses the daemon boundary.
//
// Token resolution: the drops accept either an explicit `token`
// param (for tests / users pasting a bot token by hand) OR an
// `account` param that the daemon's OAuth registry maps to the
// connected Slack workspace's access token via the
// SetTokenLookup hook. The lookup hook avoids an import cycle
// (daemon → drops/slack would conflict with the umbrella
// integrations import that hzd already does); production wires it
// at startup, tests can stub it.
package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"git.sr.ht/~klahr/hazyflow/core"
	"git.sr.ht/~klahr/hazyflow/drops/internal/params"
	hfnet "git.sr.ht/~klahr/hazyflow/drops/net"
)

// maxResponseBytes caps how much of an API response we buffer, so a
// hostile or buggy upstream (reachable via the base_url override) can't
// OOM the daemon by streaming an unbounded body. Generous enough for any
// real Slack response.
const maxResponseBytes = 64 << 20 // 64 MiB

// TokenLookup resolves an account name to a Slack access token by
// asking the daemon's OAuth registry. Returns ("", err) when the
// account isn't connected.
type TokenLookup func(ctx context.Context, account string) (string, error)

var (
	tokenLookupMu sync.RWMutex
	tokenLookup   TokenLookup
)

// SetTokenLookup wires the daemon's OAuth registry to the Slack
// drops. Called once at hzd startup. nil clears the lookup (drops
// then require an explicit `token` param).
func SetTokenLookup(fn TokenLookup) {
	tokenLookupMu.Lock()
	defer tokenLookupMu.Unlock()
	tokenLookup = fn
}

// resolveToken figures out the access token for a job, in priority
// order: explicit params.token (raw, useful for tests and quick
// one-offs) → OAuth lookup by params.account (production path).
// Returns a clear error code so users see "connect your Slack
// account first" rather than a generic auth failure.
func resolveToken(ctx context.Context, job core.Job) (string, error) {
	if t, _ := params.StringOpt(job.Params, "token"); t != "" {
		return t, nil
	}
	account, _ := params.StringOpt(job.Params, "account")
	if account == "" {
		account = "default"
	}
	tokenLookupMu.RLock()
	fn := tokenLookup
	tokenLookupMu.RUnlock()
	if fn == nil {
		return "", fmt.Errorf("no Slack token: pass `token` directly or connect a Slack account via /api/v1/oauth/slack/authorize")
	}
	tok, err := fn(ctx, account)
	if err != nil {
		return "", fmt.Errorf("lookup token for account %q: %w", account, err)
	}
	if tok == "" {
		return "", fmt.Errorf("slack account %q is not connected", account)
	}
	return tok, nil
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

// normalizeBlocks coerces a Block Kit value into the array Slack's
// chat.postMessage wants. Accepts:
//   - []any                         — the canonical decoded array, as-is.
//   - string / []byte               — JSON; parsed, then re-normalised.
//   - map with a "blocks" array     — the Block Kit Builder export / a full
//     message payload {"blocks":[…]} — unwrap to the inner array.
//   - any other map                 — a lone block object; wrap as a 1-element
//     array so a single section/divider "just works".
//
// A malformed string or a non-object scalar (number, bool) is a genuine
// mistake and returns a friendly JobError.
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
			Message: "Slack Block Kit needs an array of block objects like [ {…}, {…} ]. This step is sending a different shape — wire a JSON array (or a single block) into 'Blocks'.",
			Details: fmt.Sprintf("Received type %T on 'blocks'; expected an array, a {\"blocks\":[…]} object, a single block object, or JSON text of one of those.", v),
		}
	}
}

// parseBlocksJSON parses JSON text into a value, then re-runs normalizeBlocks
// so a string carrying any accepted shape (array, {"blocks":[…]}, or a lone
// object) is handled the same as the already-decoded form.
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

// httpBase is the Slack API root. Tests override via SetHTTPBase to
// point at an httptest server.
var (
	httpBaseMu sync.RWMutex
	httpBase   = "https://slack.com/api"
)

// SetHTTPBase swaps the API base URL — tests use this to redirect
// all Slack calls to a local httptest server.
func SetHTTPBase(base string) {
	httpBaseMu.Lock()
	defer httpBaseMu.Unlock()
	httpBase = base
}

func currentHTTPBase() string {
	httpBaseMu.RLock()
	defer httpBaseMu.RUnlock()
	return httpBase
}

// decodeSlackResponse reads + parses a Slack API JSON response.
// Every Slack API call follows the same {ok, error, ...} envelope,
// so the decoder is shared across drops.
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

// slackBaseURL resolves the API root for a job: an explicit base_url param
// (proxy / self-hosted / tests) wins, else the package default (which
// tests can swap via SetHTTPBase).
func slackBaseURL(job core.Job) string {
	if b, _ := params.StringOpt(job.Params, "base_url"); b != "" {
		return strings.TrimRight(b, "/")
	}
	return currentHTTPBase()
}

// slackDo runs one authenticated Slack Web API call and returns the
// shared {ok,error,...} envelope plus the raw decoded body. A non-2xx
// status is a transport error; an {ok:false} body is returned for the
// caller to translate into a friendly slack_error. timeoutMS bounds the
// request (defaults to 15s). body is nil for GETs.
func slackDo(ctx context.Context, method, url, token string, body []byte, timeoutMS int) (slackEnvelope, map[string]any, error) {
	if timeoutMS <= 0 {
		timeoutMS = 15000
	}
	reqCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutMS)*time.Millisecond)
	defer cancel()

	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(reqCtx, method, url, rdr)
	if err != nil {
		return slackEnvelope{}, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json; charset=utf-8")
	}

	// base_url is a tenant-supplied param, so guard the dial: the SSRF
	// client blocks loopback/private/link-local targets (cloud metadata,
	// internal services) and the egress allowlist (when the operator sets
	// one) bounds which public hosts the bearer token may be sent to.
	if err := hfnet.EgressAllowedFor(ctx, url); err != nil {
		return slackEnvelope{}, nil, err
	}
	resp, err := hfnet.SafeHTTPClient(time.Duration(timeoutMS)*time.Millisecond, hfnet.PrivateEgressAllowed()).Do(req)
	if err != nil {
		return slackEnvelope{}, nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return slackEnvelope{}, nil, err
	}
	if int64(len(raw)) > maxResponseBytes {
		return slackEnvelope{}, nil, fmt.Errorf("slack response exceeds %d bytes", maxResponseBytes)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return slackEnvelope{}, nil, fmt.Errorf("slack returned %d: %s", resp.StatusCode, string(raw))
	}
	return decodeSlackJSON(raw)
}
