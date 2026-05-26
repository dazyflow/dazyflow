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
// (daemon → integrations/slack would conflict with the umbrella
// integrations import that hzd already does); production wires it
// at startup, tests can stub it.
package slack

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"git.sr.ht/~klahr/hazy-flow/core"
)

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
	if t, _ := paramStringOpt(job.Params, "token"); t != "" {
		return t, nil
	}
	account, _ := paramStringOpt(job.Params, "account")
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
		return "", fmt.Errorf("Slack account %q is not connected", account)
	}
	return tok, nil
}

// paramString / paramStringOpt / errResult mirror the pattern used
// across integrations — duplicated rather than imported so this
// package doesn't depend on the io/notify/db packages.

func paramString(params map[string]any, key string) (string, error) {
	v, ok := params[key]
	if !ok {
		return "", fmt.Errorf("missing param %q", key)
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("param %q: expected string, got %T", key, v)
	}
	return s, nil
}

func paramStringOpt(params map[string]any, key string) (string, bool) {
	v, ok := params[key]
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	if !ok {
		return "", false
	}
	return s, true
}

func paramIntDefault(params map[string]any, key string, def int) int {
	v, ok := params[key]
	if !ok {
		return def
	}
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	}
	return def
}

func paramBoolDefault(params map[string]any, key string, def bool) bool {
	v, ok := params[key]
	if !ok {
		return def
	}
	if b, ok := v.(bool); ok {
		return b
	}
	return def
}

func errResult(job core.Job, code, msg string) core.Result {
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusError,
		Error:  &core.JobError{Code: code, Message: msg},
	}
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
