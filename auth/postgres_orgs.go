// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"git.sr.ht/~klahr/dazyflow/core"
)

// Postgres-backed implementations of the four org-level stores that
// were previously JSON-file only: memberships, invitations, per-org
// auth config, per-org profile. Selecting these (via DAZYFLOW_POSTGRES_DSN)
// is what lets a deploy fully drop the on-disk state/ directory.

const pgOrgsSchema = `
CREATE TABLE IF NOT EXISTS memberships (
    user_email TEXT NOT NULL,
    tenant     TEXT NOT NULL,
    workspace  TEXT NOT NULL,
    roles      JSONB NOT NULL DEFAULT '[]',
    invited_by TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_email, tenant)
);
CREATE INDEX IF NOT EXISTS memberships_email_idx  ON memberships (user_email);
CREATE INDEX IF NOT EXISTS memberships_tenant_idx ON memberships (tenant);

CREATE TABLE IF NOT EXISTS invitations (
    token       TEXT PRIMARY KEY,
    email       TEXT NOT NULL,
    tenant      TEXT NOT NULL,
    workspace   TEXT NOT NULL,
    roles       JSONB NOT NULL DEFAULT '[]',
    invited_by  TEXT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL,
    expires_at  TIMESTAMPTZ NOT NULL,
    accepted_at TIMESTAMPTZ,
    revoked_at  TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS invitations_tenant_idx ON invitations (tenant);
CREATE INDEX IF NOT EXISTS invitations_email_idx  ON invitations (email);

CREATE TABLE IF NOT EXISTS org_auth (
    tenant                  TEXT PRIMARY KEY,
    google_client_id        TEXT NOT NULL DEFAULT '',
    google_client_secret    TEXT NOT NULL DEFAULT '',
    google_workspace_domain TEXT NOT NULL DEFAULT '',
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS org_profiles (
    tenant       TEXT PRIMARY KEY,
    display_name TEXT NOT NULL,
    icon         TEXT NOT NULL DEFAULT '',
    subdomain    TEXT NOT NULL DEFAULT '',
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- Platform-admin moderation, mirroring users.status.
    status         TEXT NOT NULL DEFAULT 'active',
    suspended_at   TIMESTAMPTZ,
    suspend_reason TEXT NOT NULL DEFAULT ''
);
-- Backfill columns for databases created before they existed.
ALTER TABLE org_profiles ADD COLUMN IF NOT EXISTS icon TEXT NOT NULL DEFAULT '';
ALTER TABLE org_profiles ADD COLUMN IF NOT EXISTS subdomain TEXT NOT NULL DEFAULT '';
ALTER TABLE org_profiles ADD COLUMN IF NOT EXISTS status TEXT NOT NULL DEFAULT 'active';
ALTER TABLE org_profiles ADD COLUMN IF NOT EXISTS suspended_at TIMESTAMPTZ;
ALTER TABLE org_profiles ADD COLUMN IF NOT EXISTS suspend_reason TEXT NOT NULL DEFAULT '';
-- One org per subdomain: a partial unique index on the lowercased label,
-- skipping the empty (unclaimed) default so any number of orgs can have no
-- subdomain. lower() makes the uniqueness case-insensitive.
CREATE UNIQUE INDEX IF NOT EXISTS org_profiles_subdomain_key
    ON org_profiles (lower(subdomain)) WHERE subdomain <> '';
`

// EnsurePgOrgsSchema creates the four org-level tables if they don't
// exist. Each Pg*Store constructor calls it so opening any one of
// them provisions all four (idempotent, like EnsurePgAuthSchema).
func EnsurePgOrgsSchema(ctx context.Context, pool *pgxpool.Pool) error {
	if pool == nil {
		return fmt.Errorf("nil pool")
	}
	_, err := pool.Exec(ctx, pgOrgsSchema)
	return err
}

// MigrateLegacyOrgAdminPerm rewrites the pre-rename "tenant:admin" permission
// to "organization:admin" in stored role sets, in both the users and
// memberships tables. Accounts created before the rename still carry the dead
// string in their JSONB roles, so the org-admin checks (which look for
// "organization:admin") reject them even though they're org owners. This is
// idempotent — only rows still containing the old string are touched — and
// safe to run on every boot. Returns the number of rows updated.
func MigrateLegacyOrgAdminPerm(ctx context.Context, pool *pgxpool.Pool) (int64, error) {
	if pool == nil {
		return 0, fmt.Errorf("nil pool")
	}
	const (
		legacy  = `"tenant:admin"`
		current = `"organization:admin"`
	)
	var total int64
	// Table names are a fixed allowlist (never user input) — they can't be
	// bound as parameters, so they're concatenated from constants here.
	for _, tbl := range []string{"users", "memberships"} {
		ct, err := pool.Exec(ctx,
			`UPDATE `+tbl+` SET roles = REPLACE(roles::text, $1, $2)::jsonb `+
				`WHERE roles::text LIKE '%' || $1 || '%'`,
			legacy, current)
		if err != nil {
			return total, fmt.Errorf("migrate %s roles: %w", tbl, err)
		}
		total += ct.RowsAffected()
	}
	return total, nil
}

// ---- memberships ----------------------------------------------------

type PgMembershipStore struct {
	pool *pgxpool.Pool
}

func NewPgMembershipStore(ctx context.Context, pool *pgxpool.Pool) (*PgMembershipStore, error) {
	if err := EnsurePgOrgsSchema(ctx, pool); err != nil {
		return nil, err
	}
	return &PgMembershipStore{pool: pool}, nil
}

// AnonymizeSubject replaces an erased person's email where it appears as the
// INVITER on someone else's membership row. See PgInvitationStore's method.
func (s *PgMembershipStore) AnonymizeSubject(ctx context.Context, ident string) (int, error) {
	ident = strings.ToLower(strings.TrimSpace(ident))
	if ident == "" {
		return 0, nil
	}
	tag, err := s.pool.Exec(ctx,
		`UPDATE memberships SET invited_by = $2 WHERE lower(invited_by) = $1`, ident, core.ErasedIdentity)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

func (s *PgMembershipStore) PutMembership(ctx context.Context, m Membership) error {
	email := strings.ToLower(strings.TrimSpace(m.UserEmail))
	if email == "" {
		return fmt.Errorf("user_email required")
	}
	if m.Tenant == "" {
		return fmt.Errorf("tenant required")
	}
	roles, err := marshalRoles(m.Roles)
	if err != nil {
		return err
	}
	createdAt := m.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	const q = `
        INSERT INTO memberships (user_email, tenant, workspace, roles, invited_by, created_at)
        VALUES ($1,$2,$3,$4,$5,$6)
        ON CONFLICT (user_email, tenant) DO UPDATE SET
            workspace  = EXCLUDED.workspace,
            roles      = EXCLUDED.roles,
            invited_by = EXCLUDED.invited_by`
	_, err = s.pool.Exec(ctx, q, email, m.Tenant, m.Workspace, roles, nullable(m.InvitedBy), createdAt)
	return err
}

func (s *PgMembershipStore) DeleteMembership(ctx context.Context, email, tenant string) error {
	email = strings.ToLower(strings.TrimSpace(email))
	_, err := s.pool.Exec(ctx, `DELETE FROM memberships WHERE user_email=$1 AND tenant=$2`, email, tenant)
	return err
}

// DeleteByEmail removes every membership for a user (erasure, Art. 17).
func (s *PgMembershipStore) DeleteByEmail(ctx context.Context, email string) (int, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	return deleteWhere(ctx, s.pool, "memberships", "user_email", email)
}

// DeleteByTenant removes every membership in an org (org deletion).
func (s *PgMembershipStore) DeleteByTenant(ctx context.Context, tenant string) (int, error) {
	return deleteWhere(ctx, s.pool, "memberships", "tenant", tenant)
}

func (s *PgMembershipStore) GetMembership(ctx context.Context, email, tenant string) (Membership, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	row := s.pool.QueryRow(ctx,
		`SELECT user_email, tenant, workspace, roles, COALESCE(invited_by,''), created_at
         FROM memberships WHERE user_email=$1 AND tenant=$2`, email, tenant)
	m, err := scanMembership(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Membership{}, ErrUnknownMembership
	}
	return m, err
}

func (s *PgMembershipStore) ListByEmail(ctx context.Context, email string) ([]Membership, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	return queryRows(ctx, s.pool, scanMembership,
		`SELECT user_email, tenant, workspace, roles, COALESCE(invited_by,''), created_at
         FROM memberships WHERE user_email=$1
         ORDER BY tenant`, email)
}

func (s *PgMembershipStore) ListByTenant(ctx context.Context, tenant string) ([]Membership, error) {
	return queryRows(ctx, s.pool, scanMembership,
		`SELECT user_email, tenant, workspace, roles, COALESCE(invited_by,''), created_at
         FROM memberships WHERE tenant=$1
         ORDER BY user_email`, tenant)
}

func scanMembership(row rowScanner) (Membership, error) {
	var (
		m        Membership
		rolesRaw []byte
	)
	if err := row.Scan(&m.UserEmail, &m.Tenant, &m.Workspace, &rolesRaw, &m.InvitedBy, &m.CreatedAt); err != nil {
		return Membership{}, err
	}
	roles, err := jsonOrZero[[]core.Role](rolesRaw)
	if err != nil {
		return Membership{}, err
	}
	m.Roles = roles
	return m, nil
}

// ---- invitations ----------------------------------------------------

type PgInvitationStore struct {
	pool *pgxpool.Pool
}

func NewPgInvitationStore(ctx context.Context, pool *pgxpool.Pool) (*PgInvitationStore, error) {
	if err := EnsurePgOrgsSchema(ctx, pool); err != nil {
		return nil, err
	}
	return &PgInvitationStore{pool: pool}, nil
}

// AnonymizeSubject replaces an erased person's email where it appears as the
// INVITER, returning the rows changed.
//
// The row belongs to somebody else — the person invited — and survives the
// inviter's erasure, so the identifier is pseudonymised rather than deleted.
// Probed by the erasure cascade rather than declared on the interface, matching
// how DeleteByEmail is already handled for this store.
func (s *PgInvitationStore) AnonymizeSubject(ctx context.Context, ident string) (int, error) {
	ident = strings.ToLower(strings.TrimSpace(ident))
	if ident == "" {
		return 0, nil
	}
	tag, err := s.pool.Exec(ctx,
		`UPDATE invitations SET invited_by = $2 WHERE lower(invited_by) = $1`, ident, core.ErasedIdentity)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

func (s *PgInvitationStore) PutInvitation(ctx context.Context, inv Invitation) error {
	if inv.Token == "" {
		return fmt.Errorf("token required")
	}
	if inv.Tenant == "" {
		return fmt.Errorf("tenant required")
	}
	inv.Email = strings.ToLower(strings.TrimSpace(inv.Email))
	roles, err := marshalRoles(inv.Roles)
	if err != nil {
		return err
	}
	const q = `
        INSERT INTO invitations (token, email, tenant, workspace, roles, invited_by, created_at, expires_at, accepted_at, revoked_at)
        VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
        ON CONFLICT (token) DO UPDATE SET
            email       = EXCLUDED.email,
            tenant      = EXCLUDED.tenant,
            workspace   = EXCLUDED.workspace,
            roles       = EXCLUDED.roles,
            invited_by  = EXCLUDED.invited_by,
            created_at  = EXCLUDED.created_at,
            expires_at  = EXCLUDED.expires_at,
            accepted_at = EXCLUDED.accepted_at,
            revoked_at  = EXCLUDED.revoked_at`
	_, err = s.pool.Exec(ctx, q,
		inv.Token, inv.Email, inv.Tenant, inv.Workspace, roles,
		inv.InvitedBy, inv.CreatedAt, inv.ExpiresAt, inv.AcceptedAt, inv.RevokedAt)
	return err
}

func (s *PgInvitationStore) GetByToken(ctx context.Context, token string) (Invitation, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT token, email, tenant, workspace, roles, invited_by, created_at, expires_at, accepted_at, revoked_at
         FROM invitations WHERE token=$1`, token)
	inv, err := scanInvitation(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Invitation{}, ErrUnknownInvitation
	}
	return inv, err
}

func (s *PgInvitationStore) ListByTenant(ctx context.Context, tenant string) ([]Invitation, error) {
	return queryRows(ctx, s.pool, scanInvitation,
		`SELECT token, email, tenant, workspace, roles, invited_by, created_at, expires_at, accepted_at, revoked_at
         FROM invitations WHERE tenant=$1
         ORDER BY created_at DESC`, tenant)
}

// ListByEmail returns every invitation addressed to an email (export).
func (s *PgInvitationStore) ListByEmail(ctx context.Context, email string) ([]Invitation, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	return queryRows(ctx, s.pool, scanInvitation,
		`SELECT token, email, tenant, workspace, roles, invited_by, created_at, expires_at, accepted_at, revoked_at
         FROM invitations WHERE email=$1
         ORDER BY created_at DESC`, email)
}

// DeleteByEmail hard-deletes every invitation to an email (erasure).
func (s *PgInvitationStore) DeleteByEmail(ctx context.Context, email string) (int, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	return deleteWhere(ctx, s.pool, "invitations", "email", email)
}

// DeleteByTenant hard-deletes every invitation in an org (org deletion).
func (s *PgInvitationStore) DeleteByTenant(ctx context.Context, tenant string) (int, error) {
	return deleteWhere(ctx, s.pool, "invitations", "tenant", tenant)
}

func (s *PgInvitationStore) MarkAccepted(ctx context.Context, token string, at time.Time) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE invitations SET accepted_at=$2 WHERE token=$1`, token, at)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrUnknownInvitation
	}
	return nil
}

func (s *PgInvitationStore) MarkRevoked(ctx context.Context, token string, at time.Time) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE invitations SET revoked_at=$2 WHERE token=$1`, token, at)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrUnknownInvitation
	}
	return nil
}

func scanInvitation(row rowScanner) (Invitation, error) {
	var (
		inv      Invitation
		rolesRaw []byte
	)
	if err := row.Scan(
		&inv.Token, &inv.Email, &inv.Tenant, &inv.Workspace, &rolesRaw,
		&inv.InvitedBy, &inv.CreatedAt, &inv.ExpiresAt, &inv.AcceptedAt, &inv.RevokedAt,
	); err != nil {
		return Invitation{}, err
	}
	roles, err := jsonOrZero[[]core.Role](rolesRaw)
	if err != nil {
		return Invitation{}, err
	}
	inv.Roles = roles
	return inv, nil
}

// ---- org_auth -------------------------------------------------------

type PgOrgAuthStore struct {
	pool *pgxpool.Pool
}

func NewPgOrgAuthStore(ctx context.Context, pool *pgxpool.Pool) (*PgOrgAuthStore, error) {
	if err := EnsurePgOrgsSchema(ctx, pool); err != nil {
		return nil, err
	}
	return &PgOrgAuthStore{pool: pool}, nil
}

func (s *PgOrgAuthStore) GetOrgAuth(ctx context.Context, tenant string) (OrgAuthConfig, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT tenant, google_client_id, google_client_secret, google_workspace_domain, updated_at
         FROM org_auth WHERE tenant=$1`, tenant)
	var c OrgAuthConfig
	err := row.Scan(&c.Tenant, &c.GoogleClientID, &c.GoogleClientSecret, &c.GoogleWorkspaceDomain, &c.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return OrgAuthConfig{}, ErrUnknownOrgAuth
	}
	return c, err
}

func (s *PgOrgAuthStore) PutOrgAuth(ctx context.Context, cfg OrgAuthConfig) error {
	if cfg.Tenant == "" {
		return fmt.Errorf("tenant required")
	}
	updated := cfg.UpdatedAt
	if updated.IsZero() {
		updated = time.Now().UTC()
	}
	const q = `
        INSERT INTO org_auth (tenant, google_client_id, google_client_secret, google_workspace_domain, updated_at)
        VALUES ($1,$2,$3,$4,$5)
        ON CONFLICT (tenant) DO UPDATE SET
            google_client_id        = EXCLUDED.google_client_id,
            google_client_secret    = EXCLUDED.google_client_secret,
            google_workspace_domain = EXCLUDED.google_workspace_domain,
            updated_at              = EXCLUDED.updated_at`
	_, err := s.pool.Exec(ctx, q,
		cfg.Tenant, cfg.GoogleClientID, cfg.GoogleClientSecret, cfg.GoogleWorkspaceDomain, updated)
	return err
}

func (s *PgOrgAuthStore) DeleteOrgAuth(ctx context.Context, tenant string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM org_auth WHERE tenant=$1`, tenant)
	return err
}

// ---- org_profiles ---------------------------------------------------

type PgOrgProfileStore struct {
	pool *pgxpool.Pool
}

func NewPgOrgProfileStore(ctx context.Context, pool *pgxpool.Pool) (*PgOrgProfileStore, error) {
	if err := EnsurePgOrgsSchema(ctx, pool); err != nil {
		return nil, err
	}
	return &PgOrgProfileStore{pool: pool}, nil
}

// orgProfileColumns is the org_profiles SELECT/Scan column list, shared
// by every read path and (positionally) the PutOrgProfile INSERT so a new
// column stays in lockstep across all of them. Mirrors userColumns.
const orgProfileColumns = `tenant, display_name, icon, subdomain, updated_at,
	status, suspended_at, suspend_reason`

// scanOrgProfile scans one org_profiles row (in orgProfileColumns order).
func scanOrgProfile(row rowScanner) (OrgProfile, error) {
	var p OrgProfile
	if err := row.Scan(&p.Tenant, &p.DisplayName, &p.Icon, &p.Subdomain, &p.UpdatedAt,
		&p.Status, &p.SuspendedAt, &p.SuspendReason); err != nil {
		return OrgProfile{}, err
	}
	return p, nil
}

func (s *PgOrgProfileStore) GetOrgProfile(ctx context.Context, tenant string) (OrgProfile, error) {
	p, err := scanOrgProfile(s.pool.QueryRow(ctx,
		`SELECT `+orgProfileColumns+` FROM org_profiles WHERE tenant=$1`, tenant))
	if errors.Is(err, pgx.ErrNoRows) {
		return OrgProfile{}, ErrUnknownOrgProfile
	}
	return p, err
}

func (s *PgOrgProfileStore) GetOrgProfileBySubdomain(ctx context.Context, subdomain string) (OrgProfile, error) {
	subdomain = strings.ToLower(strings.TrimSpace(subdomain))
	if subdomain == "" {
		return OrgProfile{}, ErrUnknownOrgProfile
	}
	p, err := scanOrgProfile(s.pool.QueryRow(ctx,
		`SELECT `+orgProfileColumns+` FROM org_profiles WHERE lower(subdomain)=$1`, subdomain))
	if errors.Is(err, pgx.ErrNoRows) {
		return OrgProfile{}, ErrUnknownOrgProfile
	}
	return p, err
}

func (s *PgOrgProfileStore) PutOrgProfile(ctx context.Context, p OrgProfile) error {
	if p.Tenant == "" {
		return fmt.Errorf("tenant required")
	}
	updated := p.UpdatedAt
	if updated.IsZero() {
		updated = time.Now().UTC()
	}
	status := p.Status
	if status == "" {
		status = StatusActive
	}
	const q = `
        INSERT INTO org_profiles (` + orgProfileColumns + `)
        VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
        ON CONFLICT (tenant) DO UPDATE SET
            display_name   = EXCLUDED.display_name,
            icon           = EXCLUDED.icon,
            subdomain      = EXCLUDED.subdomain,
            updated_at     = EXCLUDED.updated_at,
            status         = EXCLUDED.status,
            suspended_at   = EXCLUDED.suspended_at,
            suspend_reason = EXCLUDED.suspend_reason`
	_, err := s.pool.Exec(ctx, q, p.Tenant, p.DisplayName, p.Icon, p.Subdomain, updated,
		status, p.SuspendedAt, p.SuspendReason)
	// A unique-index violation means another org already claimed this
	// subdomain — surface a typed error the handler maps to 409.
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return ErrSubdomainTaken
	}
	return err
}

func (s *PgOrgProfileStore) ListOrgProfiles(ctx context.Context, tenants []string) (map[string]OrgProfile, error) {
	out := make(map[string]OrgProfile)
	if len(tenants) == 0 {
		return out, nil
	}
	profiles, err := queryRows(ctx, s.pool, scanOrgProfile,
		`SELECT `+orgProfileColumns+` FROM org_profiles WHERE tenant = ANY($1)`, tenants)
	if err != nil {
		return nil, err
	}
	for _, p := range profiles {
		out[p.Tenant] = p
	}
	return out, nil
}

// ListAllOrgProfiles returns every org profile, newest-updated first —
// the platform-admin org roster. Unlike ListOrgProfiles it isn't scoped
// to a tenant set, so it's gated to platform admins at the handler.
func (s *PgOrgProfileStore) ListAllOrgProfiles(ctx context.Context) ([]OrgProfile, error) {
	return queryRows(ctx, s.pool, scanOrgProfile,
		`SELECT `+orgProfileColumns+` FROM org_profiles ORDER BY updated_at DESC`)
}

// DeleteOrgProfile removes an org's display profile (org deletion).
func (s *PgOrgProfileStore) DeleteOrgProfile(ctx context.Context, tenant string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM org_profiles WHERE tenant=$1`, tenant)
	return err
}

// ---- shared ---------------------------------------------------------

// nullable folds empty strings to nil so optional columns store NULL
// rather than the empty string. Keeps the schema legible (e.g.
// invited_by IS NULL is the obvious "no inviter" query).
func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}
