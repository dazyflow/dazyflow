// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import "context"

// WriteDedupeStore remembers the successful result of a non-idempotent
// external write keyed by its node-record job ID, so a re-execution of the
// SAME job (an expired-lease reclaim or crash recovery after the first attempt
// already fired the write) can return the prior result instead of firing the
// side effect again. It guards drops whose upstream API has no idempotency key
// (see Manifest.DedupeWrites) — Twilio SMS, Gmail send, Discord webhook,
// Sheets append, Home Assistant call_service.
//
// The guarantee is at-least-once: Put happens AFTER the write succeeds, so a
// crash in the window between the API returning success and Put committing can
// still re-fire. Recording before the call would instead risk at-most-once
// (skipping a write that never actually happened), which silently drops
// messages — the worse failure for these connectors.
//
// Implementations must be safe for concurrent use (several workers share one
// store) and must bound their memory.
type WriteDedupeStore interface {
	// Get returns a previously recorded successful result for key, and true,
	// or a zero Result and false when none is recorded.
	Get(ctx context.Context, key string) (Result, bool)

	// Put records a successful result for key. Best-effort: a Put that fails
	// to persist just means a re-execution re-fires (at-least-once holds).
	Put(ctx context.Context, key string, result Result)
}
