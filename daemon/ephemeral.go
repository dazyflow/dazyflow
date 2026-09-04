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

// Helpers for the short-lived, single-use auth state the sign-in flows mint on
// one request and redeem on the next. See auth.EphemeralStore for why it is not
// in process memory.

// putEphemeral stores v under token until expiresAt.
func putEphemeral[T any](ctx context.Context, s auth.EphemeralStore, kind, token string, v T, expiresAt time.Time) error {
	if s == nil {
		// Never reached in a wired gateway — mountRoutes installs a default —
		// so this is an error rather than a panic reaching a sign-in handler.
		return errors.New("no ephemeral auth state store configured")
	}
	payload, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return s.Put(ctx, kind, token, payload, expiresAt)
}

// consumeEphemeral reads a token and removes it in the same breath — these are
// all single-use, and a replayed one must not be honoured twice.
//
// The delete happens even when the payload fails to decode: a token that cannot
// be understood is one that will never be usable, and leaving it behind only
// invites the replay.
func consumeEphemeral[T any](ctx context.Context, s auth.EphemeralStore, kind, token string) (T, bool) {
	var zero T
	if s == nil || token == "" {
		return zero, false
	}
	payload, _, err := s.Get(ctx, kind, token)
	if err != nil {
		return zero, false
	}
	if derr := s.Delete(ctx, kind, token); derr != nil {
		// The read succeeded but the consume did not, so the token is still
		// live. Refusing is the safe half of the choice: a user retries a
		// sign-in, where honouring it would leave a replayable token.
		return zero, false
	}
	var v T
	if err := json.Unmarshal(payload, &v); err != nil {
		return zero, false
	}
	return v, true
}
