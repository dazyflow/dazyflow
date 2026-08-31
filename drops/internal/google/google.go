// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package google centralizes the plumbing shared by every native Google
// connector — gmail, sheets, gcal, drive, and the gform trigger. They all ride
// a single OAuth provider ("google"), so the token lookup, the guarded HTTP
// call, and the {error:{message}} envelope parsing were byte-identical copies
// in each package. They live here once; the daemon wires one SetTokenLookup,
// and each connector keeps only what is genuinely its own: API roots, the
// SetHTTPBase test seam, response shaping, and manifests.
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

// TokenLookup resolves a per-account Google OAuth access token.
type TokenLookup func(ctx context.Context, account string) (string, error)

var (
	tokenLookupMu sync.RWMutex
	tokenLookup   TokenLookup
)

// SetTokenLookup wires the daemon's per-account Google OAuth token resolver.
// One hook serves every Google connector (one provider), so cmd/dzd calls this
// once at startup instead of once per package.
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
