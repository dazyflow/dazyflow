// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/coreos/go-oidc/v3/oidc"
)

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
