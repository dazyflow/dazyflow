// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/dazyflow/dazyflow/auth"
	"github.com/dazyflow/dazyflow/core"
)

// Redacted: the secret is never in it.
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

type IssueAPIKeyParams struct {
	ID        string      `json:"id"`
	Subject   string      `json:"subject"`
	Tenant    string      `json:"tenant"`
	Workspace string      `json:"workspace"`
	Roles     []core.Role `json:"roles"`
	// nil means the key never expires.
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

// The Secret is returned exactly once and never stored in cleartext.
type IssuedAPIKey struct {
	APIKeySummary
	Secret string `json:"secret"`
}

// Classified by type, never by message text.
var (
	errAdminNotConfigured = errors.New("api key admin not configured")
	errAdminBadRequest    = errors.New("invalid request")
)

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

// Defaults to the caller's own tenant.
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
	// Only a platform admin may mint a key for another tenant.
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

type SelfIssueAPIKeyParams struct {
	ID        string      `json:"id"`
	Roles     []core.Role `json:"roles,omitempty"`
	ExpiresAt *time.Time  `json:"expires_at,omitempty"`
}

var defaultSelfIssueRole = core.Role{
	Name: "claude-mcp",
	Permissions: []core.Permission{
		core.PermGraphRun,
		core.PermGraphEdit,
	},
}

// No admin permission needed: the key can do no more than the caller already can,
// which is why the roles are capped to the caller's own.
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
		// The Connect-an-assistant path takes a fixed narrow role.
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
		// REJECT rather than silently cap: a quietly weakened key fails mysteriously later.
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

// Guards the ON CONFLICT (id) upsert: without it, a colliding id would overwrite
// another tenant's key rather than being refused.
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

func principalPermissions(p core.Principal) map[core.Permission]struct{} {
	out := map[core.Permission]struct{}{}
	for _, role := range p.Roles {
		for _, perm := range role.Permissions {
			out[perm] = struct{}{}
		}
	}
	return out
}

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

// Grouped by subject: there is no user table behind an API key.
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
	// Revoke keys only on id, so the tenant must be checked before calling it.
	key, err := s.AdminKeys.GetKey(ctx, id)
	if err != nil {
		return s.AdminKeys.Revoke(ctx, id, time.Now())
	}
	if !isPlatformAdmin(p) && key.Tenant != p.Tenant {
		return auth.ErrInvalidCredential
	}
	return s.AdminKeys.Revoke(ctx, id, time.Now())
}

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

func requireAdmin(p core.Principal) error {
	if core.CanAdminOrg(p) {
		return nil
	}
	return fmt.Errorf("%w: requires permission %q", core.ErrUnauthorized, core.PermOrganizationAdmin)
}

// Instance-wide settings, so tenant admin is not enough.
func requirePlatformAdmin(p core.Principal) error {
	if p.Has(core.PermPlatformAdmin) {
		return nil
	}
	return fmt.Errorf("%w: requires permission %q", core.ErrUnauthorized, core.PermPlatformAdmin)
}

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

type adminCheck struct {
	allowlist []string
	grants    PlatformAdminStore
}

// Either layer: the env allowlist or a runtime grant.
func (a adminCheck) isPlatformAdmin(email string) bool {
	return a.isPlatformAdminEmail(email) || a.isPlatformAdminGranted(email)
}

func (a adminCheck) isPlatformAdminEmail(email string) bool {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return false
	}
	for _, a := range a.allowlist {
		if a == email {
			return true
		}
	}
	return false
}

func (a adminCheck) isPlatformAdminGranted(email string) bool {
	return a.grants != nil && a.grants.Granted(email)
}

func (h *HTTPGateway) admins() adminCheck {
	return adminCheck{allowlist: h.PlatformAdmins, grants: h.PlatformAdminGrants}
}
