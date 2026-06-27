// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package secrets contains drops that interact with the encrypted
// secret store. The companion read path is template substitution —
// anywhere a string param accepts `${secret.name}`,
// the engine resolves it before Execute, so a dedicated read drop
// would be redundant. This package only ships the symmetric write
// surface (`secret_set`) and any future tenant-state writers.
//
// Cross-cutting hook: the secret-store implementation lives in
// daemon/encrypted_secrets.go; importing it here would invert the
// dependency direction (integrations is meant to be importable by
// daemon, not the other way around). Instead, dzd calls
// SetSecretWriter at startup with a closure that calls
// EncryptedSecrets.Put — mirroring the Slack/Gmail SetTokenLookup
// pattern.
package secrets

import (
	"context"
	"fmt"
	"sync"
)

// SecretWriter writes a value to the encrypted secret store under
// (tenant, name). Implementations should be idempotent — same
// (tenant, name, value) gives same result — and must enforce tenant
// isolation at the store layer.
type SecretWriter func(ctx context.Context, tenant, name, value string) error

var (
	writerMu sync.RWMutex
	writer   SecretWriter
)

// SetSecretWriter wires dzd's encrypted secret store into this
// package. Called once at daemon startup. nil clears the writer
// (drops then fail with a clear "secrets not configured" message
// instead of crashing or silently no-op'ing).
func SetSecretWriter(fn SecretWriter) {
	writerMu.Lock()
	defer writerMu.Unlock()
	writer = fn
}

func currentWriter() SecretWriter {
	writerMu.RLock()
	defer writerMu.RUnlock()
	return writer
}

// validSecretName mirrors daemon.validSecretName — kept private here
// to avoid an import cycle. Rules: 1..128 chars, [A-Za-z0-9_.-] only.
// Stops path-like names and shell-specials from landing in the store.
func validSecretName(name string) error {
	if name == "" {
		return fmt.Errorf("name is empty")
	}
	if len(name) > 128 {
		return fmt.Errorf("name too long (max 128)")
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '-' || r == '_' || r == '.':
		default:
			return fmt.Errorf("name may only contain [A-Za-z0-9_.-]")
		}
	}
	return nil
}
