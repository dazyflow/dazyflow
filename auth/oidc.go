package auth

import (
	"context"
	"errors"
	"fmt"

	"git.sr.ht/~klahr/hazyflow/core"
)

// OIDCConfig configures the (scaffold) OIDC authenticator.
type OIDCConfig struct {
	Issuer   string
	Audience string
	ClientID string
	// TenantClaim names the claim that maps to a Hazyflow tenant ID
	// (default "tenant"). Different IdPs use different conventions; for
	// Microsoft Entra it's typically "tid", for Google Workspace "hd".
	TenantClaim string
	RolesClaim  string
}
//
// OIDCAuthenticator is a scaffold. Production use requires:
//
//   - `github.com/coreos/go-oidc/v3/oidc` for JWKS-backed verifier
//   - `golang.org/x/oauth2` for the device flow
//
// The shape below is what the rest of the system depends on; swap the body
// of Verify() once the libraries are wired. Tests for the OIDC path
// belong in a tagged integration suite that points at a real IdP.

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

// rolePermissions maps role names to permission sets. Production should
// load this from configuration so customers can define custom roles.
func rolePermissions(role string) []core.Permission {
	switch role {
	case "admin":
		return []core.Permission{
			core.PermOrganizationAdmin, core.PermGraphRun, core.PermGraphEdit,
			core.PermGraphAdmin, core.PermModuleRegister,
			core.PermSecretRead, core.PermSecretWrite,
		}
	case "editor":
		return []core.Permission{core.PermGraphRun, core.PermGraphEdit, core.PermSecretRead}
	case "operator":
		return []core.Permission{core.PermGraphRun, core.PermSecretRead}
	case "viewer":
		return []core.Permission{core.PermSecretRead}
	default:
		return nil
	}
}
