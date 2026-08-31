// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"errors"
	"strings"
	"time"
)

// OrgAuthConfig is the per-tenant sign-in policy. Today it carries
// Google Workspace SSO config; future providers (Microsoft Entra,
// Okta, SAML) extend the same record.
//
// GoogleClientSecret is plaintext ON THIS STRUCT but must never be
// persisted that way. daemon.EncryptedOrgAuthStore decorates every
// OrgAuthStore and keeps the secret in the per-tenant encrypted secret
// store, writing an empty string to the org_auth column — so a database
// dump exposes no client secrets. Implementations of this interface should
// therefore treat the field as "whatever the decorator handed me" and not
// assume the column is the system of record.
//
// WorkspaceDomain restricts which Google accounts can sign into this
// org: the hd= claim on Google's response must match. Empty means
// any Google account whose email matches a member of the org may
// sign in (less strict, useful for personal-Gmail-using small teams).
type OrgAuthConfig struct {
	Tenant                string    `json:"tenant"`
	GoogleClientID        string    `json:"google_client_id"`
	GoogleClientSecret    string    `json:"google_client_secret"`
	GoogleWorkspaceDomain string    `json:"google_workspace_domain,omitempty"`
	UpdatedAt             time.Time `json:"updated_at"`
}

// GoogleEnabled reports whether enough Google fields are populated
// for the sign-in handler to attempt an OAuth round-trip. Both
// client_id and secret are required; the domain is optional and
// enforced only when set.
func (c OrgAuthConfig) GoogleEnabled() bool {
	return strings.TrimSpace(c.GoogleClientID) != "" &&
		strings.TrimSpace(c.GoogleClientSecret) != ""
}

// OrgAuthStore is the lookup boundary. Operations are keyed on
// tenant ID; a tenant with no config returns ErrUnknownOrgAuth and
// the sign-in flow falls back to password-only.
type OrgAuthStore interface {
	GetOrgAuth(ctx context.Context, tenant string) (OrgAuthConfig, error)
	PutOrgAuth(ctx context.Context, cfg OrgAuthConfig) error
	DeleteOrgAuth(ctx context.Context, tenant string) error
}

var ErrUnknownOrgAuth = errors.New("no auth config for tenant")
