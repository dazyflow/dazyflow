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

// Paginate walks a nextPageToken-paged list. fetch runs one call for pageToken
// ("" for the first page) and is told how many items are still wanted (0 when
// limit <= 0, meaning no limit). The walk stops at the last page, at limit
// items, or after maxPages calls; more reports that a token was left unfollowed,
// so a caller that must not silently truncate can refuse instead.
func Paginate[T any](fetch func(pageToken string, want int) ([]T, string, error), limit, maxPages int) (items []T, more bool, err error) {
	token := ""
	for page := 0; page < maxPages; page++ {
		want := 0
		if limit > 0 {
			if len(items) >= limit {
				break
			}
			want = limit - len(items)
		}
		got, next, err := fetch(token, want)
		if err != nil {
			return nil, false, err
		}
		items = append(items, got...)
		if next == "" {
			token = ""
			break
		}
		token = next
	}
	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}
	return items, token != "", nil
}
