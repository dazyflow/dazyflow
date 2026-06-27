// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"git.sr.ht/~klahr/dazyflow/core"
)

// Persisted OAuth provider credentials live in the same encrypted
// secret store tenants use for their own tokens, but under a fixed
// pseudo-tenant — the credentials are deployment-global (one Google
// OAuth app for the whole install), not per-tenant. The pseudo-tenant
// name is intentionally unlikely to collide with a real tenant ID
// (real ones look like `usr_<hex>` or admin-named org slugs that
// don't start with an underscore).
const (
	providerStoreTenant = "_oauth_providers"
	providerStorePrefix = "oauth_provider/"
)

// providerCreds is the persisted shape — just the deployment-set
// credentials, no provider metadata (URLs/scopes come from the
// hardcoded defaults table). UpdatedAt is informational, surfaced in
// the admin UI so an operator can tell when the credentials were
// last rotated.
type providerCreds struct {
	ClientID     string    `json:"client_id"`
	ClientSecret string    `json:"client_secret"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// loadProviderCreds reads the persisted (client_id, client_secret) for
// a provider. Returns (nil, nil) when nothing has been written —
// distinguishing "no creds" from "decryption failed" so the boot
// hydration loop doesn't treat a fresh install as an error.
func loadProviderCreds(ctx context.Context, secrets *EncryptedSecrets, name string) (*providerCreds, error) {
	if secrets == nil {
		return nil, nil
	}
	raw, err := secrets.Get(core.WithTenant(ctx, providerStoreTenant), providerStorePrefix+name)
	if err != nil {
		// "Never written" is a normal state on a fresh install, not an
		// error — Get wraps ErrSecretNotFound for it. A decryption or
		// store failure is real and propagates.
		if errors.Is(err, ErrSecretNotFound) {
			return nil, nil
		}
		return nil, err
	}
	var c providerCreds
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		return nil, fmt.Errorf("decode provider creds for %q: %w", name, err)
	}
	return &c, nil
}

// saveProviderCreds writes credentials for a provider, encrypting
// through the same KEK-derived DEK tenant secrets use. Overwrites any
// existing entry — there's only one set of (client_id, secret) per
// provider per deployment.
func saveProviderCreds(ctx context.Context, secrets *EncryptedSecrets, name string, c providerCreds) error {
	if secrets == nil {
		return fmt.Errorf("encrypted secret store not configured")
	}
	if c.UpdatedAt.IsZero() {
		c.UpdatedAt = time.Now().UTC()
	}
	payload, err := json.Marshal(c)
	if err != nil {
		return fmt.Errorf("encode provider creds: %w", err)
	}
	return secrets.Put(ctx, providerStoreTenant, providerStorePrefix+name, string(payload))
}

// deleteProviderCreds clears the persisted credentials for a provider.
// The registry still keeps the in-memory entry until the next restart
// — callers that need the live unregister also remove from the
// registry (see admin handler).
func deleteProviderCreds(ctx context.Context, secrets *EncryptedSecrets, name string) error {
	if secrets == nil {
		return fmt.Errorf("encrypted secret store not configured")
	}
	return secrets.Delete(ctx, providerStoreTenant, providerStorePrefix+name)
}

// listConfiguredProviders returns the provider names that have a
// persisted credential record. Sorted for deterministic boot order.
func listConfiguredProviders(ctx context.Context, secrets *EncryptedSecrets) ([]string, error) {
	if secrets == nil {
		return nil, nil
	}
	all, err := secrets.List(ctx, providerStoreTenant)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(all))
	for _, n := range all {
		if name, ok := strings.CutPrefix(n, providerStorePrefix); ok && name != "" {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out, nil
}

// HydrateOAuthProvidersFromStore reads every persisted set of provider
// credentials and re-registers them in r. Called once at boot, after
// the env-var registration in cmd/dzd — persisted entries win, which
// is what makes the admin UI's "set credentials here" overrideable at
// runtime without requiring an operator to also clear the env var.
//
// Returns (configured, errors). Configured names are listed so the
// boot log can confirm what hydrated; errors are returned alongside
// rather than failing the whole boot — a single decryption failure
// shouldn't take the daemon down.
func HydrateOAuthProvidersFromStore(ctx context.Context, r *OAuthRegistry, secrets *EncryptedSecrets) (configured []string, errs []error) {
	if r == nil || secrets == nil {
		return nil, nil
	}
	names, err := listConfiguredProviders(ctx, secrets)
	if err != nil {
		return nil, []error{fmt.Errorf("list persisted providers: %w", err)}
	}
	for _, name := range names {
		def := providerDefault(name)
		if def == nil {
			// Persisted creds for a provider the daemon no longer knows.
			// Skip — admin can delete it via the UI.
			errs = append(errs, fmt.Errorf("persisted credentials for unknown provider %q; ignoring", name))
			continue
		}
		c, err := loadProviderCreds(ctx, secrets, name)
		if err != nil {
			errs = append(errs, fmt.Errorf("load %q: %w", name, err))
			continue
		}
		if c == nil || c.ClientID == "" || c.ClientSecret == "" {
			continue
		}
		r.Register(def.toProvider(c.ClientID, c.ClientSecret))
		configured = append(configured, name)
	}
	return configured, errs
}
