// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import "context"

// Remembers a non-idempotent external write under its node-record job ID, so
// re-executing the SAME job replays the result rather than firing the side effect
// again. Guards the drops whose upstream API has no idempotency key.
//
// At-least-once: Put happens AFTER the write succeeds, so a crash between the API
// returning and Put committing can re-fire. Recording first would risk
// at-most-once — silently dropping messages, the worse failure here.
//
// Implementations must be concurrency-safe and must bound their memory.
type WriteDedupeStore interface {
	Get(ctx context.Context, key string) (Result, bool)

	Put(ctx context.Context, key string, result Result)
}
