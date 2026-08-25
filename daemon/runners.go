// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// A runner is an org's own code, running on the org's own hardware, reachable
// as a step in that org's flows. This file owns how one is remembered.
//
// The material splits in two, and the split is the point:
//
//	Public — endpoint, the runner's certificate, our client certificate. These
//	are identities, not secrets; they live in the tenant_runners table where an
//	admin UI can list them and an operator can read them in psql.
//
//	Private — the client PRIVATE key, which is what proves the daemon is the
//	daemon. It never touches that table. It goes through EncryptedSecrets, so
//	it inherits the envelope encryption, the AAD row-binding, and the master-key
//	rotation path that already exist for tenant secrets.
//
// See runnerKeyTenant below for why it is not stored in the tenant's own
// secret namespace, which would have been the obvious thing to do and is wrong.

// ErrRunnerNotFound is returned when no runner is registered under a name.
var ErrRunnerNotFound = errors.New("runner not found")

// Runner is one registered runner endpoint.
//
// ClientKeyPEM is write-only: it is supplied on Put and is always empty on
// read, because reads come from the table and the key is not in the table.
type Runner struct {
	Tenant   string
	Name     string
	Endpoint string

	// ServerCAPEM is the certificate the runner must present. Pinned, not
	// chained to a public CA: for a link between two parties who already know
	// each other, trusting one known certificate is stricter than trusting
	// anything a CA has signed.
	ServerCAPEM []byte
	// ClientCertPEM is what the daemon presents to the runner.
	ClientCertPEM []byte
	// ClientKeyPEM is accepted on write and never returned on read.
	ClientKeyPEM []byte

	// RecvTimeout bounds the gap between two events on an Execute stream.
	// Zero means the engine default.
	RecvTimeout time.Duration
	Enabled     bool

	// NotAfter is the client certificate's expiry, parsed once at registration
	// so the admin list can warn before a flow starts failing with a TLS error
	// nobody reads as "the certificate expired".
	NotAfter time.Time

	CreatedBy string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// runnerKeyTenant is the pseudo-tenant a runner's client key is stored under.
//
// The obvious placement — the tenant's own secret namespace, under a reserved
// name — is unsafe. Secret names permit dots, and a flow resolves
// ${secret.NAME} against its own tenant, so a member who guessed the name
// could read the key straight out through an ordinary flow. The reserved-name
// check in httpsecrets.go guards WRITES, not reads.
//
// A per-tenant pseudo-tenant closes that: no flow ever executes with this as
// its tenant, so no ${secret.…} can reach it. Real tenants are `usr_<hex>` or
// admin-named org slugs, neither of which starts with an underscore — the same
// reasoning the OAuth provider store relies on.
//
// A side effect worth having: the key ends up under its own DEK, separate from
// the DEK protecting that tenant's ordinary secrets.
func runnerKeyTenant(tenant string) string { return "_runner:" + tenant }

// runnerKeyName is the secret name a runner's client key is stored under
// within its pseudo-tenant. Just the runner name — the tenant is already the
// namespace, so nothing has to be encoded into the name.
func runnerKeyName(name string) string { return "client_key/" + name }

const pgRunnerSchema = `
CREATE TABLE IF NOT EXISTS tenant_runners (
    tenant           TEXT NOT NULL,
    name             TEXT NOT NULL,
    endpoint         TEXT NOT NULL,
    server_ca_pem    BYTEA NOT NULL,
    client_cert_pem  BYTEA NOT NULL,
    recv_timeout_ms  BIGINT NOT NULL DEFAULT 0,
    enabled          BOOLEAN NOT NULL DEFAULT TRUE,
    not_after        TIMESTAMPTZ,
    created_by       TEXT NOT NULL DEFAULT '',
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant, name)
);
`

// EnsurePgRunnerSchema creates the tenant_runners table.
func EnsurePgRunnerSchema(ctx context.Context, pool *pgxpool.Pool) error {
	return applyPgSchema(ctx, pool, pgRunnerSchema)
}

// RunnerStore persists the public half of a runner registration.
type RunnerStore interface {
	Put(ctx context.Context, r Runner) error
	Get(ctx context.Context, tenant, name string) (Runner, error)
	List(ctx context.Context, tenant string) ([]Runner, error)
	// ListAll is the boot path: every tenant's runners, so the catalog can be
	// rebuilt after a restart.
	ListAll(ctx context.Context) ([]Runner, error)
	Delete(ctx context.Context, tenant, name string) error
}

// ---- in-memory backend ------------------------------------------------

// MemRunnerStore implements RunnerStore against a map, for tests and for
// single-binary dev runs with no Postgres.
type MemRunnerStore struct {
	mu   sync.Mutex
	rows map[string]map[string]Runner // tenant → name → row
}

func NewMemRunnerStore() *MemRunnerStore {
	return &MemRunnerStore{rows: map[string]map[string]Runner{}}
}

func (m *MemRunnerStore) Put(_ context.Context, r Runner) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.rows[r.Tenant] == nil {
		m.rows[r.Tenant] = map[string]Runner{}
	}
	r.ClientKeyPEM = nil // never held here, mirroring the table
	if prev, ok := m.rows[r.Tenant][r.Name]; ok {
		r.CreatedAt = prev.CreatedAt
	}
	m.rows[r.Tenant][r.Name] = r
	return nil
}

func (m *MemRunnerStore) Get(_ context.Context, tenant, name string) (Runner, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.rows[tenant][name]
	if !ok {
		return Runner{}, ErrRunnerNotFound
	}
	return r, nil
}

func (m *MemRunnerStore) List(_ context.Context, tenant string) ([]Runner, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Runner, 0, len(m.rows[tenant]))
	for _, r := range m.rows[tenant] {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (m *MemRunnerStore) ListAll(_ context.Context) ([]Runner, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Runner
	for _, byName := range m.rows {
		for _, r := range byName {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Tenant != out[j].Tenant {
			return out[i].Tenant < out[j].Tenant
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

func (m *MemRunnerStore) Delete(_ context.Context, tenant, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.rows[tenant], name)
	return nil
}

// ---- Postgres backend -------------------------------------------------

// PgRunnerStore implements RunnerStore against a Postgres pool.
type PgRunnerStore struct{ pool *pgxpool.Pool }

func NewPgRunnerStore(ctx context.Context, pool *pgxpool.Pool) (*PgRunnerStore, error) {
	if pool == nil {
		return nil, fmt.Errorf("nil pool")
	}
	if err := EnsurePgRunnerSchema(ctx, pool); err != nil {
		return nil, fmt.Errorf("runner schema: %w", err)
	}
	return &PgRunnerStore{pool: pool}, nil
}

func (p *PgRunnerStore) Put(ctx context.Context, r Runner) error {
	_, err := p.pool.Exec(ctx, `
INSERT INTO tenant_runners
    (tenant, name, endpoint, server_ca_pem, client_cert_pem, recv_timeout_ms, enabled, not_after, created_by)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
ON CONFLICT (tenant, name) DO UPDATE SET
    endpoint        = EXCLUDED.endpoint,
    server_ca_pem   = EXCLUDED.server_ca_pem,
    client_cert_pem = EXCLUDED.client_cert_pem,
    recv_timeout_ms = EXCLUDED.recv_timeout_ms,
    enabled         = EXCLUDED.enabled,
    not_after       = EXCLUDED.not_after,
    updated_at      = now()`,
		r.Tenant, r.Name, r.Endpoint, r.ServerCAPEM, r.ClientCertPEM,
		r.RecvTimeout.Milliseconds(), r.Enabled, nullTime(r.NotAfter), r.CreatedBy)
	return err
}

func (p *PgRunnerStore) Get(ctx context.Context, tenant, name string) (Runner, error) {
	row := p.pool.QueryRow(ctx, `
SELECT tenant, name, endpoint, server_ca_pem, client_cert_pem, recv_timeout_ms,
       enabled, not_after, created_by, created_at, updated_at
  FROM tenant_runners WHERE tenant=$1 AND name=$2`, tenant, name)
	r, err := scanRunner(row)
	if isPgNoRows(err) {
		return Runner{}, ErrRunnerNotFound
	}
	return r, err
}

func (p *PgRunnerStore) List(ctx context.Context, tenant string) ([]Runner, error) {
	return p.query(ctx, `
SELECT tenant, name, endpoint, server_ca_pem, client_cert_pem, recv_timeout_ms,
       enabled, not_after, created_by, created_at, updated_at
  FROM tenant_runners WHERE tenant=$1 ORDER BY name`, tenant)
}

func (p *PgRunnerStore) ListAll(ctx context.Context) ([]Runner, error) {
	return p.query(ctx, `
SELECT tenant, name, endpoint, server_ca_pem, client_cert_pem, recv_timeout_ms,
       enabled, not_after, created_by, created_at, updated_at
  FROM tenant_runners ORDER BY tenant, name`)
}

func (p *PgRunnerStore) query(ctx context.Context, sql string, args ...any) ([]Runner, error) {
	rows, err := p.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Runner
	for rows.Next() {
		r, err := scanRunner(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (p *PgRunnerStore) Delete(ctx context.Context, tenant, name string) error {
	_, err := p.pool.Exec(ctx, `DELETE FROM tenant_runners WHERE tenant=$1 AND name=$2`, tenant, name)
	return err
}

// scanner is satisfied by both pgx.Row and pgx.Rows.
type scanner interface{ Scan(dest ...any) error }

func scanRunner(s scanner) (Runner, error) {
	var (
		r        Runner
		ms       int64
		notAfter *time.Time
	)
	err := s.Scan(&r.Tenant, &r.Name, &r.Endpoint, &r.ServerCAPEM, &r.ClientCertPEM,
		&ms, &r.Enabled, &notAfter, &r.CreatedBy, &r.CreatedAt, &r.UpdatedAt)
	if err != nil {
		return Runner{}, err
	}
	r.RecvTimeout = time.Duration(ms) * time.Millisecond
	if notAfter != nil {
		r.NotAfter = *notAfter
	}
	return r, nil
}

func nullTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

// ---- registry: table + key, as one thing ------------------------------

// Runners couples the table with the encrypted key so a caller registers a
// runner in one call and cannot half-do it.
type Runners struct {
	Store   RunnerStore
	Secrets *EncryptedSecrets
}

// validRunnerName keeps names to something that reads well in a palette and
// cannot be confused with a path segment or a drop id.
func validRunnerName(name string) error {
	if name == "" {
		return fmt.Errorf("name is empty")
	}
	if len(name) > 64 {
		return fmt.Errorf("name too long (max 64)")
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '-' || r == '_':
		default:
			return fmt.Errorf("name may only contain [a-z0-9_-]")
		}
	}
	return nil
}

// Put registers or replaces a runner.
//
// The key is written FIRST. If the table write then fails the key is orphaned,
// which is harmless — nothing reads it without a row. The reverse order would
// leave a row whose key is missing, which is a runner that exists in the UI and
// cannot connect.
func (rs *Runners) Put(ctx context.Context, r Runner) error {
	if rs == nil || rs.Store == nil || rs.Secrets == nil {
		return fmt.Errorf("runners: not configured")
	}
	if r.Tenant == "" {
		return fmt.Errorf("runner: tenant required")
	}
	if err := validRunnerName(r.Name); err != nil {
		return fmt.Errorf("runner name: %w", err)
	}
	if strings.TrimSpace(r.Endpoint) == "" {
		return fmt.Errorf("runner %q: endpoint required", r.Name)
	}
	if len(r.ServerCAPEM) == 0 {
		return fmt.Errorf("runner %q: the runner's certificate is required — "+
			"a runner is trusted by pinning its certificate, not by a public CA", r.Name)
	}
	if len(r.ClientCertPEM) == 0 || len(r.ClientKeyPEM) == 0 {
		return fmt.Errorf("runner %q: a client certificate and key are required — "+
			"they are how the daemon proves itself to your runner", r.Name)
	}
	// Parse now rather than at first connect: a malformed pair should be
	// rejected while an admin is looking at the form, not hours later inside a
	// run. This also yields the expiry the admin list warns on.
	notAfter, err := clientCertNotAfter(r.ClientCertPEM, r.ClientKeyPEM)
	if err != nil {
		return fmt.Errorf("runner %q: %w", r.Name, err)
	}
	r.NotAfter = notAfter

	if err := rs.Secrets.Put(ctx, runnerKeyTenant(r.Tenant), runnerKeyName(r.Name), string(r.ClientKeyPEM)); err != nil {
		return fmt.Errorf("runner %q: store client key: %w", r.Name, err)
	}
	if err := rs.Store.Put(ctx, r); err != nil {
		return fmt.Errorf("runner %q: %w", r.Name, err)
	}
	return nil
}

// Delete removes a runner and its key. The key goes last: a row without a key
// is a broken runner, but a key without a row is inert.
func (rs *Runners) Delete(ctx context.Context, tenant, name string) error {
	if err := rs.Store.Delete(ctx, tenant, name); err != nil {
		return err
	}
	if err := rs.Secrets.Delete(ctx, runnerKeyTenant(tenant), runnerKeyName(name)); err != nil && !errors.Is(err, ErrSecretNotFound) {
		return fmt.Errorf("runner %q: delete client key: %w", name, err)
	}
	return nil
}

// clientKey reads back the stored private key.
func (rs *Runners) clientKey(ctx context.Context, tenant, name string) ([]byte, error) {
	// GetExact, not Get: Get resolves flow→org precedence for a running flow,
	// which has no meaning for a key that no flow may reach.
	pemStr, err := rs.Secrets.GetExact(ctx, runnerKeyTenant(tenant), runnerKeyName(name))
	if err != nil {
		return nil, err
	}
	return []byte(pemStr), nil
}

// clientCertNotAfter validates that cert and key are a usable pair and returns
// the certificate's expiry.
func clientCertNotAfter(certPEM, keyPEM []byte) (time.Time, error) {
	if _, err := tlsKeyPair(certPEM, keyPEM); err != nil {
		return time.Time{}, err
	}
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return time.Time{}, fmt.Errorf("client certificate is not PEM")
	}
	parsed, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return time.Time{}, fmt.Errorf("client certificate: %w", err)
	}
	return parsed.NotAfter, nil
}
