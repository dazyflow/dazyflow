// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"context"
	"sync"

	"git.sr.ht/~klahr/dazyflow/core"
)

// ConnectionVerifier tests whether a candidate service connection actually
// works — a live reachability/auth check, not just "a secret is present".
// conn maps each ConnectionField.Key to its plaintext value (the daemon
// resolves secret fields before calling). A nil return means the connection
// is usable; a non-nil error's message is shown to the user, so it must
// describe the failure without leaking the credential itself.
//
// Verifiers run on the user's request (connect / "Test connection") with a
// short timeout the caller imposes via ctx — keep them cheap (a Ping, a
// HEAD, a free read endpoint), never a token-costing or mutating call.
type ConnectionVerifier func(ctx context.Context, conn map[string]string) error

var (
	verifierMu sync.RWMutex
	// verifiers is keyed by integration slug (core.ConnectionSlug of the
	// Manifest.Integration label) so the daemon can look one up from the
	// /apps/<slug> URL without knowing which package registered it.
	verifiers = map[string]ConnectionVerifier{}
)

// RegisterConnectionVerifier registers fn as the verifier for an integration,
// named by its Manifest.Integration label (e.g. "Postgres"). Call from a
// drop package's init(). Panics on a duplicate slug — like Register, a
// copy-paste mistake should fail loud at startup rather than silently pick a
// winner. Not every integration needs one; the connect flow simply skips
// verification when no verifier is registered.
func RegisterConnectionVerifier(integration string, fn ConnectionVerifier) {
	slug := core.ConnectionSlug(integration)
	verifierMu.Lock()
	defer verifierMu.Unlock()
	if _, dup := verifiers[slug]; dup {
		panic("connection verifier already registered for integration " + slug)
	}
	verifiers[slug] = fn
}

// ConnectionVerifierFor returns the verifier registered for an integration
// slug, and whether one exists. The daemon uses the boolean to advertise
// whether a connection is testable (the Apps page only shows a "Test
// connection" affordance when it is).
func ConnectionVerifierFor(slug string) (ConnectionVerifier, bool) {
	verifierMu.RLock()
	defer verifierMu.RUnlock()
	fn, ok := verifiers[slug]
	return fn, ok
}
