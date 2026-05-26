// Package gmail hosts the Gmail launch connector — second T1 stop
// after Slack and the first connector that exercises poll_trigger
// end-to-end. Three action drops cover the common Zapier-shape
// patterns:
//
//	gmail_send_email      — RFC822 construction + send
//	gmail_search_messages — list message IDs matching a query
//	gmail_get_message     — fetch one message's headers + body
//
// "Fire on new email" is composable from poll_trigger →
// gmail_search_messages (with a `newer_than:5m` query) → for_each
// → gmail_get_message → process. There's no dedicated trigger drop
// because the composition is cleaner than another drop with its
// own state-management story.
//
// Token resolution mirrors integrations/slack — a SetTokenLookup
// hook the daemon wires from the OAuthRegistry. Same provider
// ("google") covers Gmail and Sheets and any other Google API
// that lives under the same OAuth app.
package gmail

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"git.sr.ht/~klahr/hazy-flow/core"
)

// TokenLookup resolves an account name to a Gmail access token.
// Identical shape to slack.TokenLookup — kept separate so each
// connector's lookup function lives in its own package.
type TokenLookup func(ctx context.Context, account string) (string, error)

var (
	tokenLookupMu sync.RWMutex
	tokenLookup   TokenLookup
)

func SetTokenLookup(fn TokenLookup) {
	tokenLookupMu.Lock()
	defer tokenLookupMu.Unlock()
	tokenLookup = fn
}

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
		return "", fmt.Errorf("no Gmail token: pass `token` directly or connect a Google account via /api/v1/oauth/google/authorize")
	}
	tok, err := fn(ctx, account)
	if err != nil {
		return "", fmt.Errorf("lookup token for account %q: %w", account, err)
	}
	if tok == "" {
		return "", fmt.Errorf("Google account %q is not connected", account)
	}
	return tok, nil
}

var (
	httpBaseMu sync.RWMutex
	httpBase   = "https://gmail.googleapis.com/gmail/v1"
)

// SetHTTPBase swaps the Gmail API root. Tests point this at an
// httptest server so they never hit Google.
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

func paramStringDefault(params map[string]any, key, def string) string {
	if v, ok := paramStringOpt(params, key); ok && v != "" {
		return v
	}
	return def
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

func errResult(job core.Job, code, msg string) core.Result {
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusError,
		Error:  &core.JobError{Code: code, Message: msg},
	}
}

// gmailErrorEnvelope is the shape Google APIs use for errors —
// the same outer object for all of Gmail / Sheets / Drive etc.
type gmailErrorEnvelope struct {
	Error struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Status  string `json:"status"`
	} `json:"error"`
}

// extractGmailError pulls a usable message out of a Google API
// error response. Falls back to the raw body when the envelope
// shape doesn't match (some Google APIs occasionally return
// HTML error pages on 5xx).
func extractGmailError(body []byte) string {
	var env gmailErrorEnvelope
	if err := json.Unmarshal(body, &env); err == nil && env.Error.Message != "" {
		return env.Error.Message
	}
	if len(body) > 512 {
		return string(body[:512]) + "…"
	}
	return string(body)
}
