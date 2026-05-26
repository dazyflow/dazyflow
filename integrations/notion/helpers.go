// Package notion provides Notion-API connector drops:
// notion_create_page and notion_query_database for V1. The
// "fire on new database row" pattern composes via poll_trigger
// + notion_query_database + secret_set cursor — no dedicated
// trigger drop needed.
//
// Auth: identical hook shape to slack/gmail/sheets/github —
// SetTokenLookup wires hzd's OAuthRegistry into this package
// at startup, avoiding the circular daemon→integrations import
// the alternative would create.
package notion

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"git.sr.ht/~klahr/hazy-flow/core"
)

// notionAPIVersion pins the Notion-Version header so behavior is
// stable across deployments. Notion supports header-pinned versions
// indefinitely — newer versions opt in explicitly by setting this to
// a later date.
const notionAPIVersion = "2022-06-28"

// TokenLookup resolves an account name to a Notion bot token via
// hzd's OAuth registry. nil = no lookup wired; drops then require an
// explicit `token` param.
type TokenLookup func(ctx context.Context, account string) (string, error)

var (
	tokenLookupMu sync.RWMutex
	tokenLookup   TokenLookup
)

// SetTokenLookup wires the daemon's OAuth registry to the Notion
// drops. Called once at startup.
func SetTokenLookup(fn TokenLookup) {
	tokenLookupMu.Lock()
	defer tokenLookupMu.Unlock()
	tokenLookup = fn
}

// resolveToken priorities: explicit `token` param (raw, useful for
// tests / quick one-offs) → OAuth lookup by account (production
// path). Returns a clear error so users see "connect your Notion
// account first" rather than a generic 401 later.
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
		return "", fmt.Errorf("no Notion token: pass `token` directly or connect a Notion workspace via /api/v1/oauth/notion/authorize")
	}
	tok, err := fn(ctx, account)
	if err != nil {
		return "", fmt.Errorf("lookup token for account %q: %w", account, err)
	}
	if tok == "" {
		return "", fmt.Errorf("Notion account %q is not connected", account)
	}
	return tok, nil
}

// httpBase is the Notion API root. Tests override via SetHTTPBase.
var (
	httpBaseMu sync.RWMutex
	httpBase   = "https://api.notion.com/v1"
)

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

// Param helpers (duplicated rather than imported from slack/etc. to
// keep the package boundary clean — each integration owns its tiny
// helper set).

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

func errResult(job core.Job, code, msg string) core.Result {
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusError,
		Error:  &core.JobError{Code: code, Message: msg},
	}
}

// notionError is the shape Notion's API returns on non-2xx. The
// `code` field is the canonical machine-readable identifier
// ("object_not_found", "validation_error", etc.) and `message` is
// the human description. Decoding it lets us surface the real
// reason instead of a generic "HTTP 400".
type notionError struct {
	Object  string `json:"object"`
	Status  int    `json:"status"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

func decodeNotionError(body []byte, status int) string {
	var e notionError
	if err := json.Unmarshal(body, &e); err != nil || e.Message == "" {
		return fmt.Sprintf("Notion returned %d: %s", status, string(body))
	}
	if e.Code != "" {
		return fmt.Sprintf("Notion %s: %s", e.Code, e.Message)
	}
	return e.Message
}
