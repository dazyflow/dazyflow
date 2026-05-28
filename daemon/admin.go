package daemon

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"git.sr.ht/~klahr/hazy-flow/auth"
	"git.sr.ht/~klahr/hazy-flow/core"
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
// one (used when bootstrapping new customer tenants on a shared hzd).
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

// ListAPIKeys returns every key in the scoped tenant. Requires
// tenant:admin (within own tenant) or platform:admin (which can pass
// any tenant). When tenant=="", uses the principal's own tenant.
// Hash + salt are never exposed.
func (s *Service) ListAPIKeys(ctx context.Context, p core.Principal, tenant string) ([]APIKeySummary, error) {
	if err := requireAdmin(p); err != nil {
		return nil, err
	}
	if s.AdminKeys == nil {
		return nil, errors.New("api key admin not configured")
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
		return IssuedAPIKey{}, errors.New("api key admin not configured")
	}
	if params.Subject == "" {
		return IssuedAPIKey{}, errors.New("subject is required")
	}
	if len(params.Roles) == 0 {
		return IssuedAPIKey{}, errors.New("at least one role is required")
	}
	tenant, err := resolveAdminTenant(p, params.Tenant)
	if err != nil {
		return IssuedAPIKey{}, err
	}
	id := params.ID
	if id == "" {
		generated, err := newID()
		if err != nil {
			return IssuedAPIKey{}, fmt.Errorf("generate key id: %w", err)
		}
		id = "k" + generated[:12]
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

// resolveAdminTenant centralizes the "did the caller specify a tenant
// they're allowed to act on?" check used by ListAPIKeys, ListUsers,
// and IssueAPIKey. Platform admins can specify any tenant; everyone
// else is force-scoped to their own.
func resolveAdminTenant(p core.Principal, requested string) (string, error) {
	if requested == "" {
		if p.Tenant == "" {
			return "", errors.New("tenant is required (principal has no tenant binding)")
		}
		return p.Tenant, nil
	}
	if isPlatformAdmin(p) || requested == p.Tenant {
		return requested, nil
	}
	return "", fmt.Errorf("principal cannot act on tenant %q (not own tenant, not platform admin)", requested)
}

// UserSummary is the per-subject roll-up the Admin users view uses.
// "User" isn't a first-class entity in Hazy Flow today — we derive
// one synthetic record per distinct Subject across the tenant's keys.
// The aggregate Permissions union is what the principal would
// effectively get if all their active keys were combined.
type UserSummary struct {
	Subject       string           `json:"subject"`
	Tenant        string           `json:"tenant"`
	ActiveKeys    int              `json:"active_keys"`
	RevokedKeys   int              `json:"revoked_keys"`
	Permissions   []core.Permission `json:"permissions"`
	RoleNames     []string         `json:"role_names"`
	KeyIDs        []string         `json:"key_ids"`
	LastWorkspace string           `json:"last_workspace,omitempty"`
}

// ListUsers groups the tenant's API keys by subject, returning one
// record per distinct user. Roll-up rules:
//   - Permissions = union over the user's ACTIVE keys
//   - ActiveKeys / RevokedKeys count each key's status
//   - RoleNames is the dedup'd set of role names the active keys carry
//   - KeyIDs lets the UI link to a focused list
// Sorted by Subject for stable ordering.
func (s *Service) ListUsers(ctx context.Context, p core.Principal, tenant string) ([]UserSummary, error) {
	if err := requireAdmin(p); err != nil {
		return nil, err
	}
	if s.AdminKeys == nil {
		return nil, errors.New("api key admin not configured")
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
		return errors.New("api key admin not configured")
	}
	if id == "" {
		return errors.New("id is required")
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
		return nil, fmt.Errorf("requires permission %q", core.PermPlatformAdmin)
	}
	if s.AdminKeys == nil {
		return nil, errors.New("api key admin not configured")
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

// requireAdmin verifies the principal carries tenant:admin or
// platform:admin. We deliberately don't accept graph:admin here —
// graph admins can edit + run graphs but the API key surface affects
// identity and belongs in its own permission lane.
func requireAdmin(p core.Principal) error {
	if p.Has(core.PermTenantAdmin) || p.Has(core.PermPlatformAdmin) {
		return nil
	}
	return fmt.Errorf("requires permission %q", core.PermTenantAdmin)
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
