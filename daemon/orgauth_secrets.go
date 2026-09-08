// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"errors"
	"fmt"
	"log"

	"github.com/dazyflow/dazyflow/auth"
)

// A reserved name, so a user secret cannot shadow it.
const orgAuthGoogleSecretName = "orgauth.google.client_secret"

// Keeps the client secret out of the org_auth row: the row is read on paths that
// log and serialize it, and a credential there would leak by default.
type EncryptedOrgAuthStore struct {
	inner   auth.OrgAuthStore
	secrets *EncryptedSecrets
}

func NewEncryptedOrgAuthStore(inner auth.OrgAuthStore, secrets *EncryptedSecrets) auth.OrgAuthStore {
	if inner == nil || secrets == nil {
		return inner
	}
	return &EncryptedOrgAuthStore{inner: inner, secrets: secrets}
}

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
		// Fail closed: an empty secret would make SSO fail as "wrong credentials".
		return auth.OrgAuthConfig{}, fmt.Errorf("read org-auth client secret for %q: %w", tenant, encErr)
	}

	if plaintextInRow == "" {
		return cfg, nil
	}
	cfg.GoogleClientSecret = plaintextInRow
	if err := s.migrateRow(ctx, cfg); err != nil {
		// Best-effort: SSO must keep working on the plaintext already in hand.
		log.Printf("WARNING: could not migrate org-auth client secret for %q to the encrypted store (it remains in cleartext in org_auth): %v", tenant, err)
	}
	return cfg, nil
}

// One-way and idempotent: a migrated row has an empty plaintext column.
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

func (s *EncryptedOrgAuthStore) PutOrgAuth(ctx context.Context, cfg auth.OrgAuthConfig) error {
	secret := cfg.GoogleClientSecret
	cfg.GoogleClientSecret = ""

	if secret == "" {
		if err := s.secrets.Delete(ctx, cfg.Tenant, orgAuthGoogleSecretName); err != nil &&
			!errors.Is(err, ErrSecretNotFound) {
			return fmt.Errorf("clear org-auth client secret for %q: %w", cfg.Tenant, err)
		}
		return s.inner.PutOrgAuth(ctx, cfg)
	}
	// Secret FIRST: the reverse order leaves a config nothing can authenticate with.
	if err := s.secrets.Put(ctx, cfg.Tenant, orgAuthGoogleSecretName, secret); err != nil {
		return fmt.Errorf("store org-auth client secret for %q: %w", cfg.Tenant, err)
	}
	return s.inner.PutOrgAuth(ctx, cfg)
}

// Both, or the ciphertext outlives the org that owned it.
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
