// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/coreos/go-oidc/v3/oidc"
)

// NewOIDCVerifier builds the production IDTokenVerifier: OIDC discovery
// against cfg.Issuer (.well-known/openid-configuration) plus a
// JWKS-backed signature verifier with key rotation handled by go-oidc.
// Issuer, expiry, and audience are all enforced by the library; this
// wrapper's job is claim EXTRACTION — mapping whatever the IdP calls
// things onto the Claims shape the authenticator consumes.
//
// The ctx given here is used for the discovery fetch AND becomes the
// base context for background JWKS refreshes, so pass a long-lived one
// (dzd passes its root context), not a request context.
func NewOIDCVerifier(ctx context.Context, cfg OIDCConfig) (IDTokenVerifier, error) {
	if cfg.Issuer == "" {
		return nil, fmt.Errorf("oidc: issuer is required")
	}
	audience := cfg.Audience
	if audience == "" {
		audience = cfg.ClientID
	}
	if audience == "" {
		return nil, fmt.Errorf("oidc: an audience is required (client_id or audience)")
	}
	provider, err := oidc.NewProvider(ctx, cfg.Issuer)
	if err != nil {
		return nil, fmt.Errorf("oidc: discovery against %s: %w", cfg.Issuer, err)
	}
	return &oidcVerifier{
		cfg:      cfg,
		verifier: provider.VerifierContext(ctx, &oidc.Config{ClientID: audience}),
	}, nil
}

type oidcVerifier struct {
	cfg      OIDCConfig
	verifier *oidc.IDTokenVerifier
}

func (v *oidcVerifier) Verify(ctx context.Context, rawIDToken string) (Claims, error) {
	idToken, err := v.verifier.Verify(ctx, rawIDToken)
	if err != nil {
		return Claims{}, err
	}
	var all map[string]any
	if err := idToken.Claims(&all); err != nil {
		return Claims{}, fmt.Errorf("decode claims: %w", err)
	}

	tenantClaim := v.cfg.TenantClaim
	if tenantClaim == "" {
		tenantClaim = "tenant"
	}
	tenant, _ := all[tenantClaim].(string)

	// Optional issuer→tenant binding. The library has already verified the
	// signature/issuer/audience/expiry, but the tenant value itself is
	// asserted by the (single trusted) issuer with nothing tying it to a
	// specific Dazyflow tenant. When the operator pins an allowlist, fail
	// closed on any tenant outside it; when unset, accept whatever the
	// issuer asserts (unchanged behavior for single-trusted-issuer setups).
	if len(v.cfg.AllowedTenants) > 0 && !slices.Contains(v.cfg.AllowedTenants, tenant) {
		return Claims{}, fmt.Errorf("oidc: tenant %q is not in the issuer's allowed-tenants list", tenant)
	}

	rolesClaim := v.cfg.RolesClaim
	if rolesClaim == "" {
		rolesClaim = "roles"
	}
	return Claims{
		Subject: idToken.Subject,
		Tenant:  tenant,
		Roles:   stringList(all[rolesClaim]),
		Extras:  all,
	}, nil
}

// stringList renders a roles claim however the IdP shapes it: a JSON
// array of strings (Okta/Entra groups), a single string, or a
// space-separated scope-style string.
func stringList(v any) []string {
	switch t := v.(type) {
	case []any:
		out := make([]string, 0, len(t))
		for _, e := range t {
			if s, ok := e.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	case []string:
		return t
	case string:
		return strings.Fields(t)
	}
	return nil
}
