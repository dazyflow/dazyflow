// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/dazyflow/dazyflow/auth"
)

func putEphemeral[T any](ctx context.Context, s auth.EphemeralStore, kind, token string, v T, expiresAt time.Time) error {
	if s == nil {
		return errors.New("no ephemeral auth state store configured")
	}
	payload, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return s.Put(ctx, kind, token, payload, expiresAt)
}

// consumeEphemeral reads a token and removes it in one atomic Take — these are
// all single-use, and a replayed one must not be honoured twice. A Get followed
// by a Delete let two concurrent redemptions both read the payload before
// either deleted it.
//
// The token is gone even when the payload fails to decode: a token that cannot
// be understood is one that will never be usable, and leaving it behind only
// invites the replay.
func consumeEphemeral[T any](ctx context.Context, s auth.EphemeralStore, kind, token string) (T, bool) {
	var zero T
	if s == nil || token == "" {
		return zero, false
	}
	payload, err := s.Take(ctx, kind, token)
	if err != nil {
		return zero, false
	}
	var v T
	if err := json.Unmarshal(payload, &v); err != nil {
		return zero, false
	}
	return v, true
}
