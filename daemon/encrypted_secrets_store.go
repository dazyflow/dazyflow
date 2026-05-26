package daemon

import (
	"context"
	"sort"
	"sync"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ---- in-memory backend ----------------------------------------------
//
// MemSecretsStore lives in the daemon process — no disk, no DB. Used
// by tests and by single-binary dev runs without a Postgres
// dependency. Production deployments wire PgSecretsStore.

type memSecretRow struct {
	ciphertext []byte
	nonce      []byte
}

type memDEKRow struct {
	wrapped []byte
	nonce   []byte
}

// NewMemSecretsStore returns an empty in-memory store. The returned
// value is safe for concurrent use.
func NewMemSecretsStore() *MemSecretsStore {
	return &MemSecretsStore{
		secrets: map[string]map[string]memSecretRow{},
		deks:    map[string]memDEKRow{},
	}
}

// MemSecretsStore implements secretsStore against in-process maps.
type MemSecretsStore struct {
	mu      sync.Mutex
	secrets map[string]map[string]memSecretRow // tenant → name → row
	deks    map[string]memDEKRow               // tenant → DEK
}

func (m *MemSecretsStore) putSecret(_ context.Context, tenant, name string, ct, nonce []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.secrets[tenant]; !ok {
		m.secrets[tenant] = map[string]memSecretRow{}
	}
	// Copy slices so callers can reuse their buffers.
	m.secrets[tenant][name] = memSecretRow{
		ciphertext: append([]byte(nil), ct...),
		nonce:      append([]byte(nil), nonce...),
	}
	return nil
}

func (m *MemSecretsStore) getSecret(_ context.Context, tenant, name string) ([]byte, []byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if t, ok := m.secrets[tenant]; ok {
		if row, ok := t[name]; ok {
			return append([]byte(nil), row.ciphertext...),
				append([]byte(nil), row.nonce...), nil
		}
	}
	return nil, nil, ErrSecretNotFound
}

func (m *MemSecretsStore) deleteSecret(_ context.Context, tenant, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if t, ok := m.secrets[tenant]; ok {
		delete(t, name)
	}
	return nil
}

func (m *MemSecretsStore) listSecretNames(_ context.Context, tenant string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.secrets[tenant]
	if !ok {
		return nil, nil
	}
	names := make([]string, 0, len(t))
	for k := range t {
		names = append(names, k)
	}
	sort.Strings(names)
	return names, nil
}

func (m *MemSecretsStore) getWrappedDEK(_ context.Context, tenant string) ([]byte, []byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	row, ok := m.deks[tenant]
	if !ok {
		return nil, nil, ErrSecretNotFound
	}
	return append([]byte(nil), row.wrapped...),
		append([]byte(nil), row.nonce...), nil
}

func (m *MemSecretsStore) setWrappedDEK(_ context.Context, tenant string, wrapped, nonce []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.deks[tenant]; ok {
		// Don't overwrite — preserves the race-safe behavior the
		// provider depends on (loser of the provisioning race
		// re-reads and sees the winner's DEK).
		return nil
	}
	m.deks[tenant] = memDEKRow{
		wrapped: append([]byte(nil), wrapped...),
		nonce:   append([]byte(nil), nonce...),
	}
	return nil
}

// ---- Postgres backend -----------------------------------------------
//
// PgSecretsStore persists secrets and tenant DEKs to two tables in
// the same Postgres the daemon already uses for its JobStore. The
// schema is applied at OpenPgSecretsStore time; production
// deployments can also manage it through normal migration tooling.

const pgSecretsSchema = `
CREATE TABLE IF NOT EXISTS encrypted_secrets (
    tenant       TEXT NOT NULL,
    name         TEXT NOT NULL,
    ciphertext   BYTEA NOT NULL,
    nonce        BYTEA NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant, name)
);

CREATE TABLE IF NOT EXISTS encrypted_secret_deks (
    tenant       TEXT PRIMARY KEY,
    wrapped_dek  BYTEA NOT NULL,
    nonce        BYTEA NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
`

// PgSecretsStore implements secretsStore against a Postgres pool.
type PgSecretsStore struct {
	pool *pgxpool.Pool
}

// NewPgSecretsStore wraps a pgxpool.Pool and ensures the schema
// exists. Callers can share the pool with the JobStore (same
// connection budget) — pgxpool is already a concurrent pool so the
// extra usage is fine.
func NewPgSecretsStore(ctx context.Context, pool *pgxpool.Pool) (*PgSecretsStore, error) {
	if pool == nil {
		return nil, ErrSecretNotFound
	}
	if _, err := pool.Exec(ctx, pgSecretsSchema); err != nil {
		return nil, err
	}
	return &PgSecretsStore{pool: pool}, nil
}

func (p *PgSecretsStore) putSecret(ctx context.Context, tenant, name string, ct, nonce []byte) error {
	const q = `
		INSERT INTO encrypted_secrets (tenant, name, ciphertext, nonce, updated_at)
		VALUES ($1, $2, $3, $4, now())
		ON CONFLICT (tenant, name) DO UPDATE
		  SET ciphertext = EXCLUDED.ciphertext,
		      nonce      = EXCLUDED.nonce,
		      updated_at = now()
	`
	_, err := p.pool.Exec(ctx, q, tenant, name, ct, nonce)
	return err
}

func (p *PgSecretsStore) getSecret(ctx context.Context, tenant, name string) ([]byte, []byte, error) {
	const q = `SELECT ciphertext, nonce FROM encrypted_secrets WHERE tenant=$1 AND name=$2`
	var ct, nonce []byte
	err := p.pool.QueryRow(ctx, q, tenant, name).Scan(&ct, &nonce)
	if err != nil {
		// pgx returns its own no-rows error; we normalize so callers
		// can errors.Is against a single sentinel regardless of the
		// backend in play.
		if isPgNoRows(err) {
			return nil, nil, ErrSecretNotFound
		}
		return nil, nil, err
	}
	return ct, nonce, nil
}

func (p *PgSecretsStore) deleteSecret(ctx context.Context, tenant, name string) error {
	_, err := p.pool.Exec(ctx, `DELETE FROM encrypted_secrets WHERE tenant=$1 AND name=$2`, tenant, name)
	return err
}

func (p *PgSecretsStore) listSecretNames(ctx context.Context, tenant string) ([]string, error) {
	rows, err := p.pool.Query(ctx, `SELECT name FROM encrypted_secrets WHERE tenant=$1 ORDER BY name`, tenant)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (p *PgSecretsStore) getWrappedDEK(ctx context.Context, tenant string) ([]byte, []byte, error) {
	const q = `SELECT wrapped_dek, nonce FROM encrypted_secret_deks WHERE tenant=$1`
	var w, n []byte
	if err := p.pool.QueryRow(ctx, q, tenant).Scan(&w, &n); err != nil {
		if isPgNoRows(err) {
			return nil, nil, ErrSecretNotFound
		}
		return nil, nil, err
	}
	return w, n, nil
}

func (p *PgSecretsStore) setWrappedDEK(ctx context.Context, tenant string, wrapped, nonce []byte) error {
	// ON CONFLICT DO NOTHING preserves the existing DEK if two
	// goroutines race — the loser sees the winner's DEK on retry.
	const q = `
		INSERT INTO encrypted_secret_deks (tenant, wrapped_dek, nonce)
		VALUES ($1, $2, $3)
		ON CONFLICT (tenant) DO NOTHING
	`
	_, err := p.pool.Exec(ctx, q, tenant, wrapped, nonce)
	return err
}

// isPgNoRows detects pgx's no-rows sentinel without importing pgx
// in the test file (and with one fewer alias to remember).
func isPgNoRows(err error) bool {
	return err != nil && err.Error() == "no rows in result set"
}
