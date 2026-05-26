// Package github hosts the GitHub launch connector — fourth T1
// stop after Slack, Gmail, and Sheets. Three action drops cover
// the common Zapier-shape patterns:
//
//	github_create_issue  — open a new issue on a repo
//	github_list_issues   — query issues (pairs with poll_trigger for
//	                       "fire on new issue" workflows)
//	github_add_comment   — comment on an issue or PR
//
// Webhook-driven triggers (`github_on_push`, `github_on_new_pr`)
// are queued separately — they need the same shape of work as
// slack_on_mention: HMAC-SHA256 signature verification against the
// webhook secret, plus tenant routing by installation/repo. v1
// here ships the action drops, which unlock both manual workflows
// and "every 5 min: list issues since cursor → fire" composition
// with poll_trigger.
package github

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"git.sr.ht/~klahr/hazy-flow/core"
)

// TokenLookup matches the per-connector pattern. GitHub's OAuth
// app is registered as "github" in hzd's OAuth provider registry.
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
		return "", fmt.Errorf("no GitHub token: pass `token` directly or connect a GitHub account via /api/v1/oauth/github/authorize")
	}
	tok, err := fn(ctx, account)
	if err != nil {
		return "", fmt.Errorf("lookup token for account %q: %w", account, err)
	}
	if tok == "" {
		return "", fmt.Errorf("GitHub account %q is not connected", account)
	}
	return tok, nil
}

var (
	httpBaseMu sync.RWMutex
	httpBase   = "https://api.github.com"
)

// SetHTTPBase swaps the GitHub API root. Tests point this at an
// httptest server so they never hit api.github.com. (Also lets
// GitHub Enterprise deployments self-host with their own API
// origin if anyone needs that down the line.)
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

func paramStringSlice(params map[string]any, key string) []string {
	v, ok := params[key]
	if !ok {
		return nil
	}
	switch arr := v.(type) {
	case []string:
		return arr
	case []any:
		out := make([]string, 0, len(arr))
		for _, item := range arr {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func errResult(job core.Job, code, msg string) core.Result {
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusError,
		Error:  &core.JobError{Code: code, Message: msg},
	}
}

// gitHubErrorEnvelope mirrors GitHub's REST v3 error shape. Most
// errors include a message + documentation_url; some include a
// list of detailed errors. The extractor returns the most useful
// human-readable string so users see something like "Validation
// Failed: title is too long" instead of a raw JSON blob.
type gitHubErrorEnvelope struct {
	Message          string `json:"message"`
	DocumentationURL string `json:"documentation_url"`
	Errors           []struct {
		Resource string `json:"resource"`
		Field    string `json:"field"`
		Code     string `json:"code"`
		Message  string `json:"message"`
	} `json:"errors"`
}

func extractGitHubError(body []byte) string {
	var env gitHubErrorEnvelope
	if err := json.Unmarshal(body, &env); err == nil && env.Message != "" {
		if len(env.Errors) > 0 {
			// Surface the first detailed validation error inline —
			// GitHub puts the actually-helpful detail there ("field
			// 'title' is missing" rather than just "Validation Failed").
			e := env.Errors[0]
			if e.Message != "" {
				return fmt.Sprintf("%s: %s", env.Message, e.Message)
			}
			if e.Field != "" {
				return fmt.Sprintf("%s: field %q (%s)", env.Message, e.Field, e.Code)
			}
		}
		return env.Message
	}
	if len(body) > 512 {
		return string(body[:512]) + "…"
	}
	return string(body)
}
