// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package google

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/internal/params"
	hfnet "github.com/dazyflow/dazyflow/drops/net"
)

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

// ResolveToken resolves the bearer token for a job: the `token` param when
// present (the injection seam integration tests use to stand in for a connected
// account), else the connected account's Google OAuth token via the wired
// lookup. The UI path always goes through the account lookup.
func ResolveToken(ctx context.Context, job core.Job) (string, error) {
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
		return "", fmt.Errorf("no Google token: connect a Google account via /api/v1/oauth/google/authorize")
	}
	tok, err := fn(ctx, account)
	if err != nil {
		return "", fmt.Errorf("lookup token for account %q: %w", account, err)
	}
	if tok == "" {
		return "", fmt.Errorf("google account %q is not connected", account)
	}
	return tok, nil
}

// Do runs one authenticated Google API call through net.Do — a Bearer token
// plus the shared SSRF dial guard, egress allowlist, timeout, and response cap.
// It returns the status and capped body; callers classify non-2xx via
// ErrMessage. maxBytes is the connector's own per-API response cap, and a
// timeoutMS <= 0 falls back to the connectors' historical 15s default.
func Do(ctx context.Context, method, url, token, contentType string, body []byte, timeoutMS, maxBytes int) (int, []byte, error) {
	if timeoutMS <= 0 {
		timeoutMS = 15000
	}
	h := map[string]string{"Authorization": "Bearer " + token}
	if contentType != "" {
		h["Content-Type"] = contentType
	}
	status, raw, _, err := hfnet.Do(ctx, method, url, h, body, timeoutMS, maxBytes)
	return status, raw, err
}

// ErrMessage pulls the human message out of a Google API error envelope
// ({"error":{"message":...}}), falling back to a slice of the raw body bounded
// at limit bytes.
func ErrMessage(body []byte, limit int) string {
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &e); err == nil && e.Error.Message != "" {
		return e.Error.Message
	}
	if len(body) > limit {
		return string(body[:limit])
	}
	return string(body)
}
