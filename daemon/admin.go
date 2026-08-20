// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"git.sr.ht/~klahr/dazyflow/auth"
	"git.sr.ht/~klahr/dazyflow/core"
)

// APIKeySummary is the redacted view of an API key that's safe to
// surface to admin UIs. The hash + salt are stripped; the secret was
// never persisted in the first place. Includes status fields so the UI
// can render "active / expired / revoked" badges without re-deriving.
type APIKeySummary struct {
	ID        string      `json:"id"`
	Subject   string      `json:"subject"`
	Tenant    string      `json:"tenant"`
	Workspace string      `json:"workspace"`
	Roles     []core.Role `json:"roles"`
	ExpiresAt *time.Time  `json:"expires_at,omitempty"`
	RevokedAt *time.Time  `json:"revoked_at,omitempty"`
	Status    string      `json:"status"` // active | expired | revoked
}

// IssueAPIKeyParams is what the admin sends to mint a new key. ID is
// optional — when empty the service derives one. Workspace defaults
// to the admin's own workspace when blank. Tenant defaults to the
// admin's own tenant; only platform admins may specify a different
// one (used when bootstrapping new customer tenants on a shared dzd).
type IssueAPIKeyParams struct {
	ID        string      `json:"id"`
	Subject   string      `json:"subject"`
	Tenant    string      `json:"tenant"`
	Workspace string      `json:"workspace"`
	Roles     []core.Role `json:"roles"`
	// ExpiresAt is optional. nil/zero = the key never expires (the
	// current behavior for operator-issued long-lived tokens). When
	// set, the authenticator rejects the key after this time and the
	// admin UI surfaces the date next to the key's row.
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

// IssuedAPIKey is what comes back from a successful issue. The Secret
// field is the only place the resolved bearer token ever appears; the
// admin UI must show it once and never persist it again.
type IssuedAPIKey struct {
	APIKeySummary
	Secret string `json:"secret"`
}

// Sentinels the admin surface returns so handlers classify by TYPE, never by
// message text. adminError used to substring-match these strings, which meant
// rewording a user-facing message silently changed the HTTP status — and
// errors that wrapped core.ErrUnauthorized instead of using the exact phrase
// "requires permission" already fell through to 500 where they should have
// been 403.
var (
	// errAdminNotConfigured: the deployment has no API-key admin store
	// wired, so the endpoint cannot work here at all (501, not 500).
	errAdminNotConfigured = errors.New("api key admin not configured")
	// errAdminBadRequest: the caller's input is missing or malformed (400).
	errAdminBadRequest = errors.New("invalid request")
)

// ListAPIKeys returns every key in the scoped tenant. Requires
// organization:admin (within own tenant) or platform:admin (which can pass
// any tenant). When tenant=="", uses the principal's own tenant.
// Hash + salt are never exposed.
func (s *Service) ListAPIKeys(ctx context.Context, p core.Principal, tenant string) ([]APIKeySummary, error) {
	if err := requireAdmin(p); err != nil {
		return nil, err
	}
	if s.AdminKeys == nil {
		return nil, errAdminNotConfigured
	}
	scope, err := resolveAdminTenant(p, tenant)
	if err != nil {
		return nil, err
	}
	keys, err := s.AdminKeys.ListByTenant(ctx, scope)
	if err != nil {
		return nil, err
	}
	out := make([]APIKeySummary, 0, len(keys))
	now := time.Now()
	for _, k := range keys {
		out = append(out, redactKey(k, now))
	}
	return out, nil
}

// IssueAPIKey mints a new key. Default tenant is the principal's own;
// platform admins may specify any tenant via params.Tenant. The
// returned IssuedAPIKey contains the secret — callers MUST surface it
// to the user exactly once.
func (s *Service) IssueAPIKey(ctx context.Context, p core.Principal, params IssueAPIKeyParams) (IssuedAPIKey, error) {
	if err := requireAdmin(p); err != nil {
		return IssuedAPIKey{}, err
	}
	if s.AdminKeys == nil {
		return IssuedAPIKey{}, errAdminNotConfigured
	}
	if params.Subject == "" {
		return IssuedAPIKey{}, fmt.Errorf("%w: subject is required", errAdminBadRequest)
	}
	if len(params.Roles) == 0 {
		return IssuedAPIKey{}, fmt.Errorf("%w: at least one role is required", errAdminBadRequest)
	}
	tenant, err := resolveAdminTenant(p, params.Tenant)
	if err != nil {
		return IssuedAPIKey{}, err
	}
	// Block the cross-tenant escalation: only a platform admin may mint a key
	// carrying platform:admin. A tenant admin is the administrator of their own
	// org and legitimately delegates lesser permissions (graph:*, secret:*,
	// even organization:admin) within it — resolveAdminTenant already pins those keys
	// to the caller's own tenant — but must never be able to grant the
	// cross-tenant super-admin role and break out of that tenant.
	if !isPlatformAdmin(p) {
		for _, r := range params.Roles {
			if r.Has(core.PermPlatformAdmin) {
				return IssuedAPIKey{}, fmt.Errorf("%w: only a platform admin may grant %q", core.ErrUnauthorized, core.PermPlatformAdmin)
			}
		}
	}
	if err := s.reserveKeyID(ctx, params.ID, tenant); err != nil {
		return IssuedAPIKey{}, err
	}
	id, err := resolveKeyID(params.ID)
	if err != nil {
		return IssuedAPIKey{}, err
	}
	workspace := params.Workspace
	if workspace == "" {
		workspace = p.Workspace
	}
	key, secret, err := auth.IssueAPIKey(s.AdminKeys, ctx, id, tenant, workspace, params.Subject, params.Roles, params.ExpiresAt)
	if err != nil {
		return IssuedAPIKey{}, err
	}
	return IssuedAPIKey{
		APIKeySummary: redactKey(key, time.Now()),
		Secret:        secret,
	}, nil
}

// SelfIssueAPIKeyParams is the body for POST /api/v1/me/api-keys.
// Unlike IssueAPIKeyParams, it doesn't carry subject / tenant /
// workspace — those are taken verbatim from the caller's principal.
// Roles defaults to a claude-mcp role suitable for the Connect MCP
// flow if omitted; specifying explicit roles is allowed but every
// permission must be a subset of the caller's own permissions.
type SelfIssueAPIKeyParams struct {
	ID        string      `json:"id"`
	Roles     []core.Role `json:"roles,omitempty"`
	ExpiresAt *time.Time  `json:"expires_at,omitempty"`
}

// defaultSelfIssueRole is what gets attached when the caller doesn't
// specify roles — the narrow role the Connect MCP modal wants: enough
// to author and run flows in the caller's workspace, nothing else.
var defaultSelfIssueRole = core.Role{
	Name: "claude-mcp",
	Permissions: []core.Permission{
		core.PermGraphRun,
		core.PermGraphEdit,
	},
}

// IssueOwnAPIKey mints a key scoped to the caller. No admin permission
// is required — a key holder can always derive a sub-scope of their
// own permissions. The key's subject/tenant/workspace match the
// principal's verbatim; requested role permissions are capped by the
// caller's permissions (the engine will refuse a key it doesn't have
// the right to mint regardless, but failing here gives a clearer error).
//
// Used by the Connect MCP modal — lets any signed-in user issue a key
// for Claude without needing organization:admin on the AdminAPIKeys page.
func (s *Service) IssueOwnAPIKey(ctx context.Context, p core.Principal, params SelfIssueAPIKeyParams) (IssuedAPIKey, error) {
	if s.AdminKeys == nil {
		return IssuedAPIKey{}, errAdminNotConfigured
	}
	if p.Subject == "" {
		return IssuedAPIKey{}, fmt.Errorf("%w: principal has no subject", errAdminBadRequest)
	}
	if p.Tenant == "" {
		return IssuedAPIKey{}, fmt.Errorf("%w: principal has no tenant", errAdminBadRequest)
	}

	callerPerms := principalPermissions(p)

	roles := params.Roles
	if len(roles) == 0 {
		// Default (Connect-an-assistant) path: take the claude-mcp role
		// but CAP it to what the caller actually holds — an assistant can
		// never exceed its user. A viewer (graph:run only) gets a run-only
		// key rather than an error; an editor gets run+edit. Capping (not
		// rejecting) is what makes the flow usable by any member.
		capped := make([]core.Permission, 0, len(defaultSelfIssueRole.Permissions))
		for _, perm := range defaultSelfIssueRole.Permissions {
			if _, ok := callerPerms[perm]; ok {
				capped = append(capped, perm)
			}
		}
		if len(capped) == 0 {
			return IssuedAPIKey{}, fmt.Errorf("%w: your account has no permissions an assistant could use", errAdminBadRequest)
		}
		roles = []core.Role{{Name: defaultSelfIssueRole.Name, Permissions: capped}}
	} else {
		// Explicit roles: reject (don't silently cap) any permission the
		// caller lacks. The authenticator would refuse the key at use time,
		// but failing here gives a clearer error message ("you can't grant
		// secret:write to yourself") and avoids minting a broken key.
		for _, r := range roles {
			for _, perm := range r.Permissions {
				if _, ok := callerPerms[perm]; !ok {
					return IssuedAPIKey{}, fmt.Errorf("%w: requested permission %q exceeds caller's own permissions", core.ErrUnauthorized, perm)
				}
			}
		}
	}

	if err := s.reserveKeyID(ctx, params.ID, p.Tenant); err != nil {
		return IssuedAPIKey{}, err
	}
	id, err := resolveKeyID(params.ID)
	if err != nil {
		return IssuedAPIKey{}, err
	}

	key, secret, err := auth.IssueAPIKey(s.AdminKeys, ctx, id, p.Tenant, p.Workspace, p.Subject, roles, params.ExpiresAt)
	if err != nil {
		return IssuedAPIKey{}, err
	}
	return IssuedAPIKey{
		APIKeySummary: redactKey(key, time.Now()),
		Secret:        secret,
	}, nil
}

// reserveKeyID guards the ON CONFLICT (id) upsert in PutKey, which
// overwrites every column on a key-id collision. A caller-supplied ID
// that already belongs to a *different* tenant must be rejected — else
// issuing would silently hijack or revoke that tenant's key (key IDs are
// not secret; they travel in the dzk_<id>_... wire format). An empty ID is
// always server-generated, so it's free by construction.
// resolveKeyID returns the caller's requested key ID, or a freshly
// generated "k"-prefixed one when none was supplied. Key IDs are not
// secret (they travel in the dzk_<id>_... wire format), so a random
// 12-char suffix is enough to avoid collisions.
func resolveKeyID(requested string) (string, error) {
	if requested != "" {
		return requested, nil
	}
	generated, err := newID()
	if err != nil {
		return "", fmt.Errorf("generate key id: %w", err)
	}
	return "k" + generated[:12], nil
}

func (s *Service) reserveKeyID(ctx context.Context, id, tenant string) error {
	if id == "" {
		return nil
	}
	existing, err := s.AdminKeys.GetKey(ctx, id)
	if err != nil {
		if errors.Is(err, auth.ErrInvalidCredential) {
			return nil // no key with that ID — it's free to use
		}
		return err
	}
	if existing.Tenant != tenant {
		return fmt.Errorf("%w: key id %q is already in use", core.ErrUnauthorized, id)
	}
	return nil
}

// principalPermissions flattens the principal's roles into a perm set
// for membership checks. Returned as a map so callers can do O(1)
// lookups without re-walking the role slice per permission.
func principalPermissions(p core.Principal) map[core.Permission]struct{} {
	out := map[core.Permission]struct{}{}
	for _, role := range p.Roles {
		for _, perm := range role.Permissions {
			out[perm] = struct{}{}
		}
	}
	return out
}

// resolveAdminTenant centralizes the "did the caller specify a tenant
// they're allowed to act on?" check used by ListAPIKeys, ListUsers,
// and IssueAPIKey. Platform admins can specify any tenant; everyone
// else is force-scoped to their own.
func resolveAdminTenant(p core.Principal, requested string) (string, error) {
	if requested == "" {
		if p.Tenant == "" {
			return "", fmt.Errorf("%w: tenant is required (principal has no tenant binding)", errAdminBadRequest)
		}
		return p.Tenant, nil
	}
	if isPlatformAdmin(p) || requested == p.Tenant {
		return requested, nil
	}
	return "", fmt.Errorf("%w: principal cannot act on tenant %q (not own tenant, not platform admin)", core.ErrUnauthorized, requested)
}

// UserSummary is the per-subject roll-up the Admin users view uses.
// "User" isn't a first-class entity in Dazyflow today — we derive
// one synthetic record per distinct Subject across the tenant's keys.
// The aggregate Permissions union is what the principal would
// effectively get if all their active keys were combined.
type UserSummary struct {
	Subject       string            `json:"subject"`
	Tenant        string            `json:"tenant"`
	ActiveKeys    int               `json:"active_keys"`
	RevokedKeys   int               `json:"revoked_keys"`
	Permissions   []core.Permission `json:"permissions"`
	RoleNames     []string          `json:"role_names"`
	KeyIDs        []string          `json:"key_ids"`
	LastWorkspace string            `json:"last_workspace,omitempty"`
}

// ListUsers groups the tenant's API keys by subject, returning one
// record per distinct user. Roll-up rules:
//   - Permissions = union over the user's ACTIVE keys
//   - ActiveKeys / RevokedKeys count each key's status
//   - RoleNames is the dedup'd set of role names the active keys carry
//   - KeyIDs lets the UI link to a focused list
//
// Sorted by Subject for stable ordering.
func (s *Service) ListUsers(ctx context.Context, p core.Principal, tenant string) ([]UserSummary, error) {
	if err := requireAdmin(p); err != nil {
		return nil, err
	}
	if s.AdminKeys == nil {
		return nil, errAdminNotConfigured
	}
	scope, err := resolveAdminTenant(p, tenant)
	if err != nil {
		return nil, err
	}
	keys, err := s.AdminKeys.ListByTenant(ctx, scope)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	bySubject := map[string]*UserSummary{}
	permSeen := map[string]map[core.Permission]struct{}{}
	roleSeen := map[string]map[string]struct{}{}
	for _, k := range keys {
		sum, ok := bySubject[k.Subject]
		if !ok {
			sum = &UserSummary{Subject: k.Subject, Tenant: k.Tenant}
			bySubject[k.Subject] = sum
			permSeen[k.Subject] = map[core.Permission]struct{}{}
			roleSeen[k.Subject] = map[string]struct{}{}
		}
		sum.KeyIDs = append(sum.KeyIDs, k.ID)
		if k.Workspace != "" {
			sum.LastWorkspace = k.Workspace
		}
		active := k.RevokedAt == nil && (k.ExpiresAt == nil || k.ExpiresAt.After(now))
		if active {
			sum.ActiveKeys++
			for _, r := range k.Roles {
				roleSeen[k.Subject][r.Name] = struct{}{}
				for _, perm := range r.Permissions {
					permSeen[k.Subject][perm] = struct{}{}
				}
			}
		} else {
			sum.RevokedKeys++
		}
	}
	out := make([]UserSummary, 0, len(bySubject))
	for subject, sum := range bySubject {
		for perm := range permSeen[subject] {
			sum.Permissions = append(sum.Permissions, perm)
		}
		for name := range roleSeen[subject] {
			sum.RoleNames = append(sum.RoleNames, name)
		}
		sort.Slice(sum.Permissions, func(i, j int) bool {
			return sum.Permissions[i] < sum.Permissions[j]
		})
		sort.Strings(sum.RoleNames)
		sort.Strings(sum.KeyIDs)
		out = append(out, *sum)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Subject < out[j].Subject })
	return out, nil
}

// RevokeAPIKey marks a key revoked. Idempotent — revoking an already-
// revoked key is a no-op (and not an error).
func (s *Service) RevokeAPIKey(ctx context.Context, p core.Principal, id string) error {
	if err := requireAdmin(p); err != nil {
		return err
	}
	if s.AdminKeys == nil {
		return errAdminNotConfigured
	}
	if id == "" {
		return fmt.Errorf("%w: id is required", errAdminBadRequest)
	}
	// Revoke() keys only on id — the row's tenant isn't in its WHERE — so
	// scope the revoke to the caller's tenant here, otherwise a tenant
	// admin could revoke another tenant's key by id (a cross-tenant denial
	// of service). Platform admins legitimately cross tenant boundaries.
	// A key in another tenant returns the same not-found error as an
	// unknown id so this can't be used to probe for foreign key ids.
	key, err := s.AdminKeys.GetKey(ctx, id)
	if err != nil {
		// Unknown id (or unreadable). Defer to the store, which is
		// idempotent and returns the canonical not-found error.
		return s.AdminKeys.Revoke(ctx, id, time.Now())
	}
	if !isPlatformAdmin(p) && key.Tenant != p.Tenant {
		return auth.ErrInvalidCredential
	}
	return s.AdminKeys.Revoke(ctx, id, time.Now())
}

// ListTenants returns the set of tenants that have at least one API
// key. Platform admins only — for everyone else, the answer is "just
// your own tenant", which the caller already knows.
//
// Derived view: there's no first-class tenants table. A tenant
// effectively exists when a key is issued against its name; this
// method walks the key store to surface them. Sorted alphabetically.
func (s *Service) ListTenants(ctx context.Context, p core.Principal) ([]string, error) {
	if !isPlatformAdmin(p) {
		return nil, fmt.Errorf("%w: requires permission %q", core.ErrUnauthorized, core.PermPlatformAdmin)
	}
	if s.AdminKeys == nil {
		return nil, errAdminNotConfigured
	}
	keys, err := s.AdminKeys.ListAll(ctx)
	if err != nil {
		return nil, err
	}
	seen := map[string]struct{}{}
	for _, k := range keys {
		if k.Tenant != "" {
			seen[k.Tenant] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for t := range seen {
		out = append(out, t)
	}
	sort.Strings(out)
	return out, nil
}

// requireAdmin verifies the principal carries organization:admin or
// platform:admin. We deliberately don't accept graph:admin here —
// graph admins can edit + run graphs but the API key surface affects
// identity and belongs in its own permission lane.
func requireAdmin(p core.Principal) error {
	if core.CanAdminOrg(p) {
		return nil
	}
	return fmt.Errorf("%w: requires permission %q", core.ErrUnauthorized, core.PermOrganizationAdmin)
}

// requirePlatformAdmin gates instance-wide settings that every tenant
// shares — e.g. the OAuth client_id/secret for the provider apps the
// whole install connects through. A tenant admin owns one org and must
// NOT read or change config that affects all orgs, so these surfaces
// require platform admin rather than the broader requireAdmin.
func requirePlatformAdmin(p core.Principal) error {
	if p.Has(core.PermPlatformAdmin) {
		return nil
	}
	return fmt.Errorf("%w: requires permission %q", core.ErrUnauthorized, core.PermPlatformAdmin)
}

// isPlatformAdmin is the override used by admin Service methods to
// decide whether the caller is allowed to specify a tenant other than
// their own.
func isPlatformAdmin(p core.Principal) bool {
	return p.Has(core.PermPlatformAdmin)
}

func redactKey(k auth.APIKey, now time.Time) APIKeySummary {
	s := APIKeySummary{
		ID:        k.ID,
		Subject:   k.Subject,
		Tenant:    k.Tenant,
		Workspace: k.Workspace,
		Roles:     k.Roles,
		ExpiresAt: k.ExpiresAt,
		RevokedAt: k.RevokedAt,
		Status:    "active",
	}
	switch {
	case k.RevokedAt != nil:
		s.Status = "revoked"
	case k.ExpiresAt != nil && k.ExpiresAt.Before(now):
		s.Status = "expired"
	}
	return s
}
