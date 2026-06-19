package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"git.sr.ht/~klahr/dazyflow/core"
)

// Postgres-backed implementations of the three auth stores
// (APIKeyStore/AdminKeyStore, SessionStore, UserStore). These replace
// the in-memory / JSON-file stores for production: API keys, sessions,
// and user records all survive a daemon restart. All three share one
// pgxpool with the JobStore and secret store.
//
// Roles are stored as JSONB — they're small, read whole, and never
// queried by their interior, so a relational role table would be
// overkill here.

const pgAuthSchema = `
CREATE TABLE IF NOT EXISTS api_keys (
    id          TEXT PRIMARY KEY,
    tenant      TEXT NOT NULL,
    workspace   TEXT NOT NULL,
    subject     TEXT NOT NULL,
    roles       JSONB NOT NULL DEFAULT '[]',
    salt        BYTEA NOT NULL,
    hash        BYTEA NOT NULL,
    expires_at  TIMESTAMPTZ,
    revoked_at  TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS api_keys_tenant_idx ON api_keys (tenant);

CREATE TABLE IF NOT EXISTS sessions (
    id          TEXT PRIMARY KEY,
    subject     TEXT NOT NULL,
    tenant      TEXT NOT NULL,
    workspace   TEXT NOT NULL,
    roles       JSONB NOT NULL DEFAULT '[]',
    created_at  TIMESTAMPTZ NOT NULL,
    expires_at  TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS sessions_expires_idx ON sessions (expires_at);

CREATE TABLE IF NOT EXISTS users (
    email         TEXT PRIMARY KEY,
    password_hash BYTEA NOT NULL,
    subject       TEXT NOT NULL,
    tenant        TEXT NOT NULL,
    workspace     TEXT NOT NULL,
    roles         JSONB NOT NULL DEFAULT '[]',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- TOTP 2FA. Added after the initial users table shipped, so the
    -- ALTERs below carry installs that predate 2FA. totp_secret_enc is
    -- the AES-256-GCM ciphertext (see auth/totp.go); recovery_codes is a
    -- JSONB array of bcrypt hashes of the unused single-use codes.
    totp_secret_enc  BYTEA,
    totp_enabled     BOOLEAN NOT NULL DEFAULT FALSE,
    totp_enrolled_at TIMESTAMPTZ,
    recovery_codes   JSONB NOT NULL DEFAULT '[]',
    -- Last successfully-consumed TOTP time-step, for replay protection.
    totp_last_step   BIGINT NOT NULL DEFAULT 0
);
-- Idempotent migrations for users tables created before 2FA landed.
ALTER TABLE users ADD COLUMN IF NOT EXISTS totp_secret_enc  BYTEA;
ALTER TABLE users ADD COLUMN IF NOT EXISTS totp_enabled     BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE users ADD COLUMN IF NOT EXISTS totp_enrolled_at TIMESTAMPTZ;
ALTER TABLE users ADD COLUMN IF NOT EXISTS recovery_codes   JSONB NOT NULL DEFAULT '[]';
ALTER TABLE users ADD COLUMN IF NOT EXISTS totp_last_step    BIGINT NOT NULL DEFAULT 0;
ALTER TABLE users ADD COLUMN IF NOT EXISTS verified_at       TIMESTAMPTZ;
ALTER TABLE users ADD COLUMN IF NOT EXISTS verify_token_hash BYTEA;
ALTER TABLE users ADD COLUMN IF NOT EXISTS verify_expires_at TIMESTAMPTZ;
ALTER TABLE users ADD COLUMN IF NOT EXISTS reset_token_hash  BYTEA;
ALTER TABLE users ADD COLUMN IF NOT EXISTS reset_expires_at  TIMESTAMPTZ;
`

// EnsurePgAuthSchema creates the api_keys / sessions / users tables if
// they don't exist. Each Pg*Store constructor calls it, so opening any
// one of them is enough to provision all three (idempotent).
func EnsurePgAuthSchema(ctx context.Context, pool *pgxpool.Pool) error {
	if pool == nil {
		return fmt.Errorf("nil pool")
	}
	_, err := pool.Exec(ctx, pgAuthSchema)
	return err
}

func marshalRoles(roles []core.Role) ([]byte, error) {
	if roles == nil {
		return []byte("[]"), nil
	}
	return json.Marshal(roles)
}

func unmarshalRoles(b []byte) ([]core.Role, error) {
	if len(b) == 0 {
		return nil, nil
	}
	var roles []core.Role
	if err := json.Unmarshal(b, &roles); err != nil {
		return nil, err
	}
	return roles, nil
}

// ---- API keys -------------------------------------------------------

// PgKeyStore is the Postgres AdminKeyStore (and therefore APIKeyStore).
type PgKeyStore struct {
	pool *pgxpool.Pool
}

func NewPgKeyStore(ctx context.Context, pool *pgxpool.Pool) (*PgKeyStore, error) {
	if err := EnsurePgAuthSchema(ctx, pool); err != nil {
		return nil, err
	}
	return &PgKeyStore{pool: pool}, nil
}

func (s *PgKeyStore) PutKey(ctx context.Context, k APIKey) error {
	roles, err := marshalRoles(k.Roles)
	if err != nil {
		return err
	}
	const q = `
		INSERT INTO api_keys (id, tenant, workspace, subject, roles, salt, hash, expires_at, revoked_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		ON CONFLICT (id) DO UPDATE SET
		  tenant=EXCLUDED.tenant, workspace=EXCLUDED.workspace,
		  subject=EXCLUDED.subject, roles=EXCLUDED.roles,
		  salt=EXCLUDED.salt, hash=EXCLUDED.hash,
		  expires_at=EXCLUDED.expires_at, revoked_at=EXCLUDED.revoked_at
	`
	_, err = s.pool.Exec(ctx, q, k.ID, k.Tenant, k.Workspace, k.Subject, roles, k.Salt, k.Hash, k.ExpiresAt, k.RevokedAt)
	return err
}

func (s *PgKeyStore) GetKey(ctx context.Context, id string) (APIKey, error) {
	const q = `
		SELECT id, tenant, workspace, subject, roles, salt, hash, expires_at, revoked_at
		FROM api_keys WHERE id=$1
	`
	k, err := scanKey(s.pool.QueryRow(ctx, q, id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return APIKey{}, ErrInvalidCredential
		}
		return APIKey{}, err
	}
	return k, nil
}

func (s *PgKeyStore) Revoke(ctx context.Context, id string, at time.Time) error {
	tag, err := s.pool.Exec(ctx, `UPDATE api_keys SET revoked_at=$2 WHERE id=$1`, id, at)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrInvalidCredential
	}
	return nil
}

func (s *PgKeyStore) ListByTenant(ctx context.Context, tenant string) ([]APIKey, error) {
	const q = `
		SELECT id, tenant, workspace, subject, roles, salt, hash, expires_at, revoked_at
		FROM api_keys WHERE tenant=$1 ORDER BY id
	`
	return s.queryKeys(ctx, q, tenant)
}

func (s *PgKeyStore) ListAll(ctx context.Context) ([]APIKey, error) {
	const q = `
		SELECT id, tenant, workspace, subject, roles, salt, hash, expires_at, revoked_at
		FROM api_keys ORDER BY id
	`
	return s.queryKeys(ctx, q)
}

// ListBySubject returns every key issued to a principal subject, across
// tenants — used by the data-export path (GDPR Art. 15/20).
func (s *PgKeyStore) ListBySubject(ctx context.Context, subject string) ([]APIKey, error) {
	const q = `
		SELECT id, tenant, workspace, subject, roles, salt, hash, expires_at, revoked_at
		FROM api_keys WHERE subject=$1 ORDER BY id
	`
	return s.queryKeys(ctx, q, subject)
}

// DeleteBySubject hard-deletes every key for a subject (erasure, Art. 17).
// Returns the number removed.
func (s *PgKeyStore) DeleteBySubject(ctx context.Context, subject string) (int, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM api_keys WHERE subject=$1`, subject)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

// DeleteByTenant hard-deletes every key in a tenant — for org deletion.
func (s *PgKeyStore) DeleteByTenant(ctx context.Context, tenant string) (int, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM api_keys WHERE tenant=$1`, tenant)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

func (s *PgKeyStore) queryKeys(ctx context.Context, q string, args ...any) ([]APIKey, error) {
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]APIKey, 0)
	for rows.Next() {
		k, err := scanKey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// rowScanner unifies pgx.Row (QueryRow) and pgx.Rows (Query) so one
// scanKey helper serves both the single-get and list paths.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanKey(row rowScanner) (APIKey, error) {
	var (
		k        APIKey
		rolesRaw []byte
	)
	if err := row.Scan(&k.ID, &k.Tenant, &k.Workspace, &k.Subject, &rolesRaw, &k.Salt, &k.Hash, &k.ExpiresAt, &k.RevokedAt); err != nil {
		return APIKey{}, err
	}
	roles, err := unmarshalRoles(rolesRaw)
	if err != nil {
		return APIKey{}, err
	}
	k.Roles = roles
	return k, nil
}

// ---- Sessions -------------------------------------------------------

type PgSessionStore struct {
	pool *pgxpool.Pool
}

func NewPgSessionStore(ctx context.Context, pool *pgxpool.Pool) (*PgSessionStore, error) {
	if err := EnsurePgAuthSchema(ctx, pool); err != nil {
		return nil, err
	}
	return &PgSessionStore{pool: pool}, nil
}

func (s *PgSessionStore) GetSession(ctx context.Context, id string) (Session, error) {
	const q = `SELECT id, subject, tenant, workspace, roles, created_at, expires_at FROM sessions WHERE id=$1`
	var (
		sess     Session
		rolesRaw []byte
	)
	err := s.pool.QueryRow(ctx, q, id).Scan(
		&sess.ID, &sess.Subject, &sess.Tenant, &sess.Workspace, &rolesRaw, &sess.CreatedAt, &sess.ExpiresAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Session{}, ErrInvalidCredential
		}
		return Session{}, err
	}
	roles, err := unmarshalRoles(rolesRaw)
	if err != nil {
		return Session{}, err
	}
	sess.Roles = roles
	return sess, nil
}

func (s *PgSessionStore) PutSession(ctx context.Context, sess Session) error {
	roles, err := marshalRoles(sess.Roles)
	if err != nil {
		return err
	}
	const q = `
		INSERT INTO sessions (id, subject, tenant, workspace, roles, created_at, expires_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT (id) DO UPDATE SET
		  subject=EXCLUDED.subject, tenant=EXCLUDED.tenant,
		  workspace=EXCLUDED.workspace, roles=EXCLUDED.roles,
		  created_at=EXCLUDED.created_at, expires_at=EXCLUDED.expires_at
	`
	_, err = s.pool.Exec(ctx, q, sess.ID, sess.Subject, sess.Tenant, sess.Workspace, roles, sess.CreatedAt, sess.ExpiresAt)
	return err
}

func (s *PgSessionStore) DeleteSession(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM sessions WHERE id=$1`, id)
	return err
}

func (s *PgSessionStore) RevokeSubjectSessions(ctx context.Context, subject string) (int, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM sessions WHERE subject=$1`, subject)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

// ---- Users ----------------------------------------------------------

type PgUserStore struct {
	pool *pgxpool.Pool
}

func NewPgUserStore(ctx context.Context, pool *pgxpool.Pool) (*PgUserStore, error) {
	if err := EnsurePgAuthSchema(ctx, pool); err != nil {
		return nil, err
	}
	return &PgUserStore{pool: pool}, nil
}

// marshalRecoveryCodes / unmarshalRecoveryCodes move the bcrypt-hash
// array in and out of the recovery_codes JSONB column. nil → "[]" so the
// NOT NULL column always gets a value.
func marshalRecoveryCodes(codes []string) ([]byte, error) {
	if codes == nil {
		return []byte("[]"), nil
	}
	return json.Marshal(codes)
}

func unmarshalRecoveryCodes(b []byte) ([]string, error) {
	if len(b) == 0 {
		return nil, nil
	}
	var codes []string
	if err := json.Unmarshal(b, &codes); err != nil {
		return nil, err
	}
	return codes, nil
}

func (s *PgUserStore) GetByEmail(ctx context.Context, email string) (User, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	const q = `SELECT email, password_hash, subject, tenant, workspace, roles, created_at,
	                  totp_secret_enc, totp_enabled, totp_enrolled_at, recovery_codes,
	                  verified_at, verify_token_hash, verify_expires_at, totp_last_step,
	                  reset_token_hash, reset_expires_at
	             FROM users WHERE email=$1`
	var (
		u           User
		rolesRaw    []byte
		recoveryRaw []byte
	)
	err := s.pool.QueryRow(ctx, q, email).Scan(
		&u.Email, &u.PasswordHash, &u.Subject, &u.Tenant, &u.Workspace, &rolesRaw, &u.CreatedAt,
		&u.TOTPSecretEnc, &u.TOTPEnabled, &u.TOTPEnrolledAt, &recoveryRaw,
		&u.VerifiedAt, &u.VerifyTokenHash, &u.VerifyExpiresAt, &u.TOTPLastStep,
		&u.ResetTokenHash, &u.ResetExpiresAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return User{}, ErrUnknownUser
		}
		return User{}, err
	}
	roles, err := unmarshalRoles(rolesRaw)
	if err != nil {
		return User{}, err
	}
	u.Roles = roles
	codes, err := unmarshalRecoveryCodes(recoveryRaw)
	if err != nil {
		return User{}, err
	}
	u.RecoveryCodeHashes = codes
	return u, nil
}

func (s *PgUserStore) PutUser(ctx context.Context, u User) error {
	u.Email = strings.ToLower(strings.TrimSpace(u.Email))
	if u.Email == "" {
		return fmt.Errorf("email required")
	}
	roles, err := marshalRoles(u.Roles)
	if err != nil {
		return err
	}
	recovery, err := marshalRecoveryCodes(u.RecoveryCodeHashes)
	if err != nil {
		return err
	}
	created := u.CreatedAt
	if created.IsZero() {
		created = time.Now()
	}
	const q = `
		INSERT INTO users (email, password_hash, subject, tenant, workspace, roles, created_at,
		                   totp_secret_enc, totp_enabled, totp_enrolled_at, recovery_codes,
		                   verified_at, verify_token_hash, verify_expires_at, totp_last_step,
		                   reset_token_hash, reset_expires_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)
		ON CONFLICT (email) DO UPDATE SET
		  password_hash=EXCLUDED.password_hash, subject=EXCLUDED.subject,
		  tenant=EXCLUDED.tenant, workspace=EXCLUDED.workspace, roles=EXCLUDED.roles,
		  totp_secret_enc=EXCLUDED.totp_secret_enc, totp_enabled=EXCLUDED.totp_enabled,
		  totp_enrolled_at=EXCLUDED.totp_enrolled_at, recovery_codes=EXCLUDED.recovery_codes,
		  verified_at=EXCLUDED.verified_at, verify_token_hash=EXCLUDED.verify_token_hash,
		  verify_expires_at=EXCLUDED.verify_expires_at, totp_last_step=EXCLUDED.totp_last_step,
		  reset_token_hash=EXCLUDED.reset_token_hash, reset_expires_at=EXCLUDED.reset_expires_at
	`
	_, err = s.pool.Exec(ctx, q, u.Email, u.PasswordHash, u.Subject, u.Tenant, u.Workspace, roles, created,
		u.TOTPSecretEnc, u.TOTPEnabled, u.TOTPEnrolledAt, recovery,
		u.VerifiedAt, u.VerifyTokenHash, u.VerifyExpiresAt, u.TOTPLastStep,
		u.ResetTokenHash, u.ResetExpiresAt)
	return err
}

func (s *PgUserStore) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.pool.Query(ctx, `SELECT email, password_hash, subject, tenant, workspace, roles, created_at,
	                                       totp_secret_enc, totp_enabled, totp_enrolled_at, recovery_codes,
	                                       verified_at, verify_token_hash, verify_expires_at, totp_last_step,
	                                       reset_token_hash, reset_expires_at
	                                  FROM users ORDER BY email`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]User, 0)
	for rows.Next() {
		var (
			u           User
			rolesRaw    []byte
			recoveryRaw []byte
		)
		if err := rows.Scan(&u.Email, &u.PasswordHash, &u.Subject, &u.Tenant, &u.Workspace, &rolesRaw, &u.CreatedAt,
			&u.TOTPSecretEnc, &u.TOTPEnabled, &u.TOTPEnrolledAt, &recoveryRaw,
			&u.VerifiedAt, &u.VerifyTokenHash, &u.VerifyExpiresAt, &u.TOTPLastStep,
			&u.ResetTokenHash, &u.ResetExpiresAt); err != nil {
			return nil, err
		}
		roles, err := unmarshalRoles(rolesRaw)
		if err != nil {
			return nil, err
		}
		u.Roles = roles
		codes, err := unmarshalRecoveryCodes(recoveryRaw)
		if err != nil {
			return nil, err
		}
		u.RecoveryCodeHashes = codes
		out = append(out, u)
	}
	return out, rows.Err()
}

// DeleteUser hard-deletes the user row (erasure, Art. 17). Idempotent:
// a missing row is not an error, so the cascade can run repeatedly.
func (s *PgUserStore) DeleteUser(ctx context.Context, email string) error {
	email = strings.ToLower(strings.TrimSpace(email))
	_, err := s.pool.Exec(ctx, `DELETE FROM users WHERE email=$1`, email)
	return err
}
