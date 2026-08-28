// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"errors"
	"fmt"
	"log"

	"git.sr.ht/~klahr/dazyflow/auth"
)

// orgAuthGoogleSecretName is the reserved storage name an org's Google
// OAuth client secret lives under in EncryptedSecrets. It deliberately
// mirrors the oauth.{provider}.{account} shape the connector tokens use, so
// an operator reading a secret listing sees one naming convention rather
// than two.
const orgAuthGoogleSecretName = "orgauth.google.client_secret"

// EncryptedOrgAuthStore keeps an org's Google OAuth client secret out of the
// org_auth row and in the per-tenant encrypted secret store instead.
//
// Why this exists: every other credential in the system is encrypted at rest
// — connector tokens under a per-tenant DEK, TOTP seeds under AES-GCM,
// passwords under bcrypt — but org_auth.google_client_secret was a plain
// TEXT column, so a database dump handed over every org's live Google client
// secret in cleartext. Paired with the client_id in the same row (and the
// redirect URI, which is public), that is enough to impersonate the org's
// OAuth client.
//
// It is a decorator rather than a change inside auth.PgOrgAuthStore because
// EncryptedSecrets lives in package daemon and package daemon imports auth —
// encrypting inside the store would invert that dependency. The interface is
// unchanged, so every existing caller of GetOrgAuth/PutOrgAuth is unaffected
// and continues to see a plaintext GoogleClientSecret on the struct.
//
// Legacy rows are migrated lazily: the first GetOrgAuth that finds a
// plaintext secret in the row and no ciphertext in the store moves it across
// and blanks the column. No operator step, and no boot-time table scan.
type EncryptedOrgAuthStore struct {
	inner   auth.OrgAuthStore
	secrets *EncryptedSecrets
}

// NewEncryptedOrgAuthStore wraps inner so client secrets are stored
// encrypted. Returns inner untouched when there is no encrypted secret store
// — an install with no DAZYFLOW_MASTER_KEY has nowhere to put the
// ciphertext, and failing SSO closed there would break working deployments
// on upgrade. Those installs keep the old plaintext behaviour, which is why
// the master key is documented as required for production.
func NewEncryptedOrgAuthStore(inner auth.OrgAuthStore, secrets *EncryptedSecrets) auth.OrgAuthStore {
	if inner == nil || secrets == nil {
		return inner
	}
	return &EncryptedOrgAuthStore{inner: inner, secrets: secrets}
}

// GetOrgAuth returns the config with GoogleClientSecret populated from the
// encrypted store.
func (s *EncryptedOrgAuthStore) GetOrgAuth(ctx context.Context, tenant string) (auth.OrgAuthConfig, error) {
	cfg, err := s.inner.GetOrgAuth(ctx, tenant)
	if err != nil {
		return cfg, err
	}
	plaintextInRow := cfg.GoogleClientSecret
	cfg.GoogleClientSecret = ""

	enc, encErr := s.secrets.GetExact(ctx, tenant, orgAuthGoogleSecretName)
	switch {
	case encErr == nil:
		cfg.GoogleClientSecret = enc
		return cfg, nil
	case !errors.Is(encErr, ErrSecretNotFound):
		// Fail closed. Handing back a config with an empty secret would make
		// GoogleEnabled() report false and silently disable the org's SSO —
		// and worse, putOrgAuthConfig treats a blank secret as "keep the
		// existing one" by reading it back through this method, so an empty
		// value here could be persisted over a perfectly good secret.
		return auth.OrgAuthConfig{}, fmt.Errorf("read org-auth client secret for %q: %w", tenant, encErr)
	}

	// No ciphertext. Either this org has no secret at all, or the row
	// predates encryption and still holds the plaintext.
	if plaintextInRow == "" {
		return cfg, nil
	}
	cfg.GoogleClientSecret = plaintextInRow
	if err := s.migrateRow(ctx, cfg); err != nil {
		// Best-effort: SSO must keep working on the plaintext we already
		// hold. Logged loudly because the row is still exposed until a
		// later call succeeds.
		log.Printf("WARNING: could not migrate org-auth client secret for %q to the encrypted store (it remains in cleartext in org_auth): %v", tenant, err)
	}
	return cfg, nil
}

// migrateRow moves a legacy plaintext secret into the encrypted store and
// blanks the column. UpdatedAt is carried over unchanged so a migration
// doesn't look like an admin edit in the org's audit trail.
func (s *EncryptedOrgAuthStore) migrateRow(ctx context.Context, cfg auth.OrgAuthConfig) error {
	if err := s.secrets.Put(ctx, cfg.Tenant, orgAuthGoogleSecretName, cfg.GoogleClientSecret); err != nil {
		return fmt.Errorf("encrypt: %w", err)
	}
	blanked := cfg
	blanked.GoogleClientSecret = ""
	if err := s.inner.PutOrgAuth(ctx, blanked); err != nil {
		return fmt.Errorf("blank the plaintext column: %w", err)
	}
	log.Printf("migrated org-auth Google client secret for %q into the encrypted secret store", cfg.Tenant)
	return nil
}

// PutOrgAuth writes the secret to the encrypted store and the rest of the
// config to the row, with the secret column left empty.
func (s *EncryptedOrgAuthStore) PutOrgAuth(ctx context.Context, cfg auth.OrgAuthConfig) error {
	secret := cfg.GoogleClientSecret
	// Never let the plaintext reach the row, on any path below.
	cfg.GoogleClientSecret = ""

	if secret == "" {
		// An explicitly cleared secret must also clear the ciphertext,
		// otherwise the next Get would resurrect it.
		if err := s.secrets.Delete(ctx, cfg.Tenant, orgAuthGoogleSecretName); err != nil &&
			!errors.Is(err, ErrSecretNotFound) {
			return fmt.Errorf("clear org-auth client secret for %q: %w", cfg.Tenant, err)
		}
		return s.inner.PutOrgAuth(ctx, cfg)
	}
	// Secret first, row second. The reverse order would leave a config that
	// advertises SSO (client_id present) with no retrievable secret if the
	// second write failed; this order can only orphan a ciphertext, which is
	// inert and overwritten by the next successful save.
	if err := s.secrets.Put(ctx, cfg.Tenant, orgAuthGoogleSecretName, secret); err != nil {
		return fmt.Errorf("store org-auth client secret for %q: %w", cfg.Tenant, err)
	}
	return s.inner.PutOrgAuth(ctx, cfg)
}

// DeleteOrgAuth removes the row and the ciphertext. Both, because this is
// also the path GDPR org erasure takes — a leftover client secret would
// outlive the org it belonged to.
func (s *EncryptedOrgAuthStore) DeleteOrgAuth(ctx context.Context, tenant string) error {
	rowErr := s.inner.DeleteOrgAuth(ctx, tenant)
	secErr := s.secrets.Delete(ctx, tenant, orgAuthGoogleSecretName)
	if secErr != nil && errors.Is(secErr, ErrSecretNotFound) {
		secErr = nil
	}
	if rowErr != nil {
		return rowErr
	}
	if secErr != nil {
		return fmt.Errorf("delete org-auth client secret for %q: %w", tenant, secErr)
	}
	return nil
}
