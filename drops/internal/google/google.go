// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package google

import (
	"context"
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

// Do runs one authenticated Google API call through net.DoBearer. It returns
// the status and capped body; callers classify non-2xx via ErrMessage.
// maxBytes is the connector's own per-API response cap, and a timeoutMS <= 0
// falls back to 15s.
func Do(ctx context.Context, method, url, token, contentType string, body []byte, timeoutMS, maxBytes int) (int, []byte, error) {
	status, raw, _, err := hfnet.DoBearer(ctx, method, url, token, contentType, nil, body, timeoutMS, maxBytes)
	return status, raw, err
}

// ErrMessage pulls the human message out of a Google API error envelope
// ({"error":{"message":...}}), falling back to a slice of the raw body bounded
// at limit bytes.
func ErrMessage(body []byte, limit int) string {
	return params.EnvelopeMessage(body, "", limit)
}

// Paginate walks a nextPageToken-paged Google list. fetch makes one call with
// the page token ("" for the first page) and the number of items still wanted
// (0 when limit is unbounded), returning that page's items and the next token.
//
// The walk stops when a page carries no token, once limit items are collected
// (limit <= 0 means no limit), or after maxPages calls — a guard against an API
// that keeps handing back tokens. The result is trimmed to limit. next is the
// token the walk stopped short of: "" when the listing was exhausted, so a
// caller that must see everything can tell a capped walk from a complete one.
func Paginate[T any](ctx context.Context, fetch func(pageToken string, remaining int) ([]T, string, error), limit, maxPages int) (items []T, next string, err error) {
	for page := 0; page < maxPages; page++ {
		if err := ctx.Err(); err != nil {
			return nil, "", err
		}
		remaining := 0
		if limit > 0 {
			remaining = limit - len(items)
		}
		got, tok, err := fetch(next, remaining)
		if err != nil {
			return nil, "", err
		}
		items = append(items, got...)
		next = tok
		if next == "" || (limit > 0 && len(items) >= limit) {
			break
		}
	}
	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}
	return items, next, nil
}
