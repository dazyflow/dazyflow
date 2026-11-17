// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package google

import "context"

// Paginate walks a Google token-paged listing (pageToken in, nextPageToken
// out). fetch is called with the token to continue from ("" for the first
// page) and how many items are still wanted (0 when limit <= 0, meaning no
// limit), and returns that page's items plus the next token.
//
// The walk stops at the last page, once limit items are collected, or after
// maxPages calls — the cap that keeps an API that keeps handing back tokens
// from looping without bound. more reports that a next token was left
// unfollowed, so a caller that must see everything (a watermark poll) can tell
// a complete listing from a truncated one. The result is trimmed to limit.
func Paginate[T any](ctx context.Context, limit, maxPages int, fetch func(pageToken string, want int) ([]T, string, error)) (items []T, more bool, err error) {
	items = []T{}
	token := ""
	for page := 0; page < maxPages; page++ {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		want := 0
		if limit > 0 {
			want = limit - len(items)
		}
		got, next, err := fetch(token, want)
		if err != nil {
			return nil, false, err
		}
		items = append(items, got...)
		if next == "" {
			more = false
			break
		}
		token, more = next, true
		if limit > 0 && len(items) >= limit {
			break
		}
	}
	if limit > 0 && len(items) > limit {
		items, more = items[:limit], true
	}
	return items, more, nil
}
