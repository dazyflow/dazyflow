// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"errors"
	"fmt"

	"github.com/dazyflow/dazyflow/core"
)

type OIDCConfig struct {
	Issuer      string
	Audience    string
	ClientID    string
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

type Claims struct {
	// Subject is the raw IdP `sub`, unique only within Issuer.
	Subject string
	Issuer  string
	Tenant  string
	Roles   []string
	Extras  map[string]any
}

func (a *OIDCAuthenticator) Authenticate(ctx context.Context, credential string) (core.Principal, error) {
	if a.Verifier == nil {
		return core.Principal{}, errors.New("OIDC verifier not configured")
	}
	if !looksLikeJWT(credential) {
		return core.Principal{}, ErrInvalidCredential
	}
	claims, err := a.Verifier.Verify(ctx, credential)
	if err != nil {
		return core.Principal{}, fmt.Errorf("%w: %s", ErrInvalidCredential, err.Error())
	}
	if claims.Subject == "" {
		return core.Principal{}, fmt.Errorf("%w: token has no sub claim", ErrInvalidCredential)
	}
	roles := make([]core.Role, 0, len(claims.Roles))
	for _, name := range claims.Roles {
		roles = append(roles, core.Role{Name: name, Permissions: rolePermissions(name)})
	}
	issuer := claims.Issuer
	if issuer == "" {
		issuer = a.Config.Issuer
	}
	return core.Principal{
		Subject: OIDCSubject(issuer, claims.Subject),
		Tenant:  claims.Tenant,
		Roles:   roles,
		Extras:  claims.Extras,
	}, nil
}

// OIDCSubject is the principal Subject for an IdP-authenticated bearer token.
//
// The raw `sub` is NOT used: it is chosen by the IdP and shares a namespace with
// local subjects, which are emails — invites, memberships, moderation and flow
// ownership all key on them. An IdP whose sub happened to be "alice@corp" would
// otherwise act as the local user alice@corp (accepting her invites, editing and
// seeing her private flows). The issuer-qualified form cannot equal any
// email-keyed subject, because nothing on those paths matches a string with the
// "oidc:" prefix. The sub is also not replaced by the email claim: that would
// hand every trusted IdP the ability to act as any local account.
func OIDCSubject(issuer, sub string) string {
	return "oidc:" + issuer + "#" + sub
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
