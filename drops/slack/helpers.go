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

// resolveBlocks pulls the Block Kit array off the job in priority
// order: 'blocks' input port → params.blocks. Returns (nil, nil) when
// neither is set so the caller can fall through to text-only.
//
// Accepted port shapes: []any (already-decoded array, the common case
// from a transform), string / []byte (JSON-encoded array, parsed
// here). Anything else is a wiring mistake — return a JobError with a
// friendly Message and a typed Details so the UI can split the two.
func resolveBlocks(job core.Job) (any, *core.JobError) {
	if input, ok := job.Input["blocks"]; ok && input.Inline != nil {
		switch v := input.Inline.(type) {
		case []any:
			return v, nil
		case string:
			var arr []any
			if err := json.Unmarshal([]byte(v), &arr); err != nil {
				return nil, &core.JobError{
					Code:    "bad_input",
					Message: "Slack Block Kit needs an array of block objects. The upstream node is wiring a string, but it isn't valid JSON.",
					Details: fmt.Sprintf("JSON parse on input port 'blocks' failed: %v", err),
				}
			}
			return arr, nil
		case []byte:
			var arr []any
			if err := json.Unmarshal(v, &arr); err != nil {
				return nil, &core.JobError{
					Code:    "bad_input",
					Message: "Slack Block Kit needs an array of block objects. The upstream node is wiring raw bytes, but they aren't valid JSON.",
					Details: fmt.Sprintf("JSON parse on input port 'blocks' failed: %v", err),
				}
			}
			return arr, nil
		default:
			return nil, &core.JobError{
				Code:    "bad_input",
				Message: "Slack Block Kit needs an array of block objects. The upstream node is sending a different shape.",
				Details: fmt.Sprintf("Received type %T on input port 'blocks'; expected []any, string (JSON), or []byte (JSON).", v),
			}
		}
	}
	if v, ok := job.Params["blocks"]; ok && v != nil {
		return v, nil
	}
	return nil, nil
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
	if err := hfnet.EgressAllowed(url); err != nil {
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

// bodyInputText pulls a string off the optional 'body' input port,
// returning (text, true, nil) when present. A structured value is a
// wiring mistake — the caller gets a friendly JobError telling them to
// render it as a string (e.g. via render_text). Shared by the connectors
// whose body/text comes from upstream.
func bodyInputText(job core.Job) (string, bool, *core.JobError) {
	in, ok := job.Input["body"]
	if !ok || in.Inline == nil {
		return "", false, nil
	}
	switch v := in.Inline.(type) {
	case string:
		return v, true, nil
	case []byte:
		return string(v), true, nil
	default:
		return "", false, &core.JobError{
			Code:    "bad_input",
			Message: "The Slack message needs text on its 'body' input, but the upstream node is sending a structured value. Route it through render_text and wire that text output instead.",
			Details: fmt.Sprintf("Received type %T on input port 'body'; expected a string.", v),
		}
	}
}
