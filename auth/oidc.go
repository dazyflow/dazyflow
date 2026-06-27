// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"errors"
	"fmt"

	"git.sr.ht/~klahr/dazyflow/core"
)

// OIDCConfig configures the OIDC bearer-token authenticator.
type OIDCConfig struct {
	Issuer   string
	Audience string
	ClientID string
	// TenantClaim names the claim that maps to a Dazyflow tenant ID
	// (default "tenant"). Different IdPs use different conventions; for
	// Microsoft Entra it's typically "tid", for Google Workspace "hd".
	TenantClaim string
	RolesClaim  string
	// AllowedTenants optionally constrains which tenant claim values this
	// issuer may assert. The verifier checks sig/iss/aud/exp, but the
	// tenant comes straight from the (issuer-controlled) token body with
	// no binding back to Dazyflow — so a single trusted issuer can mint a
	// token for ANY tenant id. When AllowedTenants is non-empty, a token
	// whose tenant claim is not in this list is rejected. When empty,
	// behavior is unchanged (any tenant the issuer asserts is honored),
	// preserving single-trusted-issuer setups. Comparison is exact.
	AllowedTenants []string
}

// OIDCAuthenticator accepts IdP-issued bearer JWTs on the API: a token
// minted by Microsoft Entra / Okta / Google Workspace (any OIDC issuer)
// authenticates a request without a Dazyflow session or API key —
// machine-to-machine and SSO-backed automation. It slots into the auth
// Chain after the API-key and session authenticators; non-JWT
// credentials fall through untouched. The production Verifier comes
// from NewOIDCVerifier (go-oidc discovery + JWKS, see oidc_verifier.go),
// wired by dzd when DAZYFLOW_OIDC_ISSUER is set.

type OIDCAuthenticator struct {
	Config   OIDCConfig
	Verifier IDTokenVerifier
}

// IDTokenVerifier exists so tests can inject a fake without pulling in the
// full go-oidc library.
type IDTokenVerifier interface {
	Verify(ctx context.Context, rawIDToken string) (Claims, error)
}

// Claims is the subset the system needs from the verified ID token.
type Claims struct {
	Subject string
	Tenant  string
	Roles   []string
	Extras  map[string]any
}

func (a *OIDCAuthenticator) Authenticate(ctx context.Context, credential string) (core.Principal, error) {
	if a.Verifier == nil {
		return core.Principal{}, errors.New("OIDC verifier not configured")
	}
	// JWTs use three dot-separated base64 segments. A naive prefix check
	// lets the Chain authenticator skip OIDC for non-JWT credentials.
	if !looksLikeJWT(credential) {
		return core.Principal{}, ErrInvalidCredential
	}
	claims, err := a.Verifier.Verify(ctx, credential)
	if err != nil {
		return core.Principal{}, fmt.Errorf("%w: %s", ErrInvalidCredential, err.Error())
	}
	roles := make([]core.Role, 0, len(claims.Roles))
	for _, name := range claims.Roles {
		roles = append(roles, core.Role{Name: name, Permissions: rolePermissions(name)})
	}
	return core.Principal{
		Subject: claims.Subject,
		Tenant:  claims.Tenant,
		Roles:   roles,
		Extras:  claims.Extras,
	}, nil
}

func looksLikeJWT(s string) bool {
	dots := 0
	for _, c := range s {
		if c == '.' {
			dots++
		}
	}
	return dots == 2
}

// rolePermissions resolves an IdP role/group name through the canonical
// team catalog (viewer / editor / admin — core.TeamRoleByName), so an
// Entra group named "editor" grants exactly what an invited editor
// gets. Unknown names carry no permissions: an unmapped IdP group must
// never grant access by accident.
func rolePermissions(role string) []core.Permission {
	if cat, ok := core.TeamRoleByName(role); ok {
		return cat.Permissions
	}
	return nil
}
