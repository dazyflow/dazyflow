// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"sort"
	"sync"

	"git.sr.ht/~klahr/dazyflow/core"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PgWebAPIStore is the durable WebAPIStore.
//
// A catalog must outlive the daemon for the same reason an MCP registration
// must: the org's flows reference api:<name>:<operation> by id, so forgetting a
// catalog on restart does not degrade those flows, it breaks them.
type PgWebAPIStore struct {
	pool *pgxpool.Pool
}

func NewPgWebAPIStore(ctx context.Context, pool *pgxpool.Pool) (*PgWebAPIStore, error) {
	if err := EnsurePgWebAPISchema(ctx, pool); err != nil {
		return nil, err
	}
	return &PgWebAPIStore{pool: pool}, nil
}

// webAPIColumns is the select list every read shares, in scan order. Unlike the
// MCP store there is no column withheld: this table holds no credential.
const webAPIColumns = `tenant, name, label, description, base_url, integration, auth_kind, auth_header,
	operations, timeout_ms, max_body_bytes, enabled, logo, logo_mode, last_error,
	created_by, created_at, updated_at`

func scanWebAPI(row pgx.Row) (WebAPI, error) {
	var w WebAPI
	var ops []byte
	if err := row.Scan(&w.Tenant, &w.Name, &w.Label, &w.Description, &w.BaseURL, &w.Integration,
		&w.AuthKind, &w.AuthHeader, &ops, &w.TimeoutMS, &w.MaxBodyBytes,
		&w.Enabled, &w.Logo, &w.LogoMode, &w.LastError, &w.CreatedBy, &w.CreatedAt, &w.UpdatedAt); err != nil {
		return WebAPI{}, err
	}
	parsed, err := unmarshalOperations(ops)
	if err != nil {
		// A row whose JSON will not parse is reported rather than skipped: the
		// alternative is an org's steps quietly missing from the palette with
		// nothing anywhere saying why.
		return WebAPI{}, err
	}
	w.Operations = parsed
	return w, nil
}

func (s *PgWebAPIStore) List(ctx context.Context, tenant string) ([]WebAPI, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+webAPIColumns+` FROM tenant_web_apis WHERE tenant = $1
		  ORDER BY COALESCE(NULLIF(label, ''), name), name`, tenant)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return collectWebAPIs(rows)
}

func (s *PgWebAPIStore) ListAll(ctx context.Context) ([]WebAPI, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+webAPIColumns+` FROM tenant_web_apis ORDER BY tenant, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return collectWebAPIs(rows)
}

func collectWebAPIs(rows pgx.Rows) ([]WebAPI, error) {
	out := []WebAPI{}
	for rows.Next() {
		one, err := scanWebAPI(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, one)
	}
	return out, rows.Err()
}

func (s *PgWebAPIStore) Get(ctx context.Context, tenant, name string) (WebAPI, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT `+webAPIColumns+` FROM tenant_web_apis WHERE tenant = $1 AND name = $2`, tenant, name)
	one, err := scanWebAPI(row)
	if err != nil {
		if isPgNoRows(err) {
			return WebAPI{}, ErrWebAPINotFound
		}
		return WebAPI{}, err
	}
	return one, nil
}

// Put inserts or replaces the whole configuration.
//
// Every column is overwritten from the row, last_error included: a save is a
// statement that this configuration is current, and a stale error left beside
// it would report a problem the admin has just fixed.
func (s *PgWebAPIStore) Put(ctx context.Context, w WebAPI) error {
	ops, err := marshalOperations(w.Operations)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO tenant_web_apis
			(tenant, name, label, description, base_url, integration, auth_kind, auth_header,
			 operations, timeout_ms, max_body_bytes, enabled, logo, logo_mode, last_error,
			 created_by, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, '', $15, $16, $17)
		ON CONFLICT (tenant, name) DO UPDATE SET
			label          = EXCLUDED.label,
			description    = EXCLUDED.description,
			base_url       = EXCLUDED.base_url,
			integration    = EXCLUDED.integration,
			auth_kind      = EXCLUDED.auth_kind,
			auth_header    = EXCLUDED.auth_header,
			operations     = EXCLUDED.operations,
			timeout_ms     = EXCLUDED.timeout_ms,
			max_body_bytes = EXCLUDED.max_body_bytes,
			enabled        = EXCLUDED.enabled,
			logo           = EXCLUDED.logo,
			logo_mode      = EXCLUDED.logo_mode,
			last_error     = '',
			updated_at     = EXCLUDED.updated_at`,
		w.Tenant, w.Name, w.Label, w.Description, w.BaseURL, w.Integration, string(w.AuthKind), w.AuthHeader,
		ops, w.TimeoutMS, w.MaxBodyBytes, w.Enabled, w.Logo, string(w.logoMode()),
		w.CreatedBy, w.CreatedAt, w.UpdatedAt)
	return err
}

func (s *PgWebAPIStore) Delete(ctx context.Context, tenant, name string) error {
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM tenant_web_apis WHERE tenant = $1 AND name = $2`, tenant, name)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrWebAPINotFound
	}
	return nil
}

func (s *PgWebAPIStore) AnonymizeSubject(ctx context.Context, ident string) (int, error) {
	if ident == "" {
		return 0, nil
	}
	tag, err := s.pool.Exec(ctx,
		`UPDATE tenant_web_apis SET created_by = $2 WHERE created_by = $1`, ident, core.ErasedIdentity)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

func (s *PgWebAPIStore) DeleteByTenant(ctx context.Context, tenant string) (int, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM tenant_web_apis WHERE tenant = $1`, tenant)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

// SetError records why a stored row could not be registered. It deliberately
// does NOT touch updated_at: that column is what every replica's reconcile
// compares against to decide whether a registration is current, so a status
// write moving it would make every node re-register on every pass.
func (s *PgWebAPIStore) SetError(ctx context.Context, tenant, name, lastErr string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE tenant_web_apis SET last_error = $3 WHERE tenant = $1 AND name = $2`,
		tenant, name, lastErr)
	return err
}

// ---- in-memory store --------------------------------------------------

// MemWebAPIStore implements WebAPIStore in process, for tests.
//
// Test-only, like MemMCPServerStore: a restart with this store would silently
// drop an org's steps out of the palette.
type MemWebAPIStore struct {
	mu   sync.Mutex
	rows map[webAPIKey]WebAPI
}

func NewMemWebAPIStore() *MemWebAPIStore {
	return &MemWebAPIStore{rows: map[webAPIKey]WebAPI{}}
}

func (s *MemWebAPIStore) List(_ context.Context, tenant string) ([]WebAPI, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []WebAPI{}
	for k, v := range s.rows {
		if k.tenant == tenant {
			out = append(out, v)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (s *MemWebAPIStore) ListAll(_ context.Context) ([]WebAPI, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []WebAPI{}
	for _, v := range s.rows {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Tenant != out[j].Tenant {
			return out[i].Tenant < out[j].Tenant
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

func (s *MemWebAPIStore) Get(_ context.Context, tenant, name string) (WebAPI, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.rows[webAPIKey{tenant, name}]
	if !ok {
		return WebAPI{}, ErrWebAPINotFound
	}
	return v, nil
}

// Put round-trips the operations through the SAME encoding Postgres stores, so
// a JSON tag that would orphan stored rows fails in a memory-store test too
// rather than only in a deployment.
func (s *MemWebAPIStore) Put(_ context.Context, w WebAPI) error {
	raw, err := marshalOperations(w.Operations)
	if err != nil {
		return err
	}
	ops, err := unmarshalOperations(raw)
	if err != nil {
		return err
	}
	w.Operations = ops
	w.LastError = ""
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows[webAPIKey{w.Tenant, w.Name}] = w
	return nil
}

func (s *MemWebAPIStore) Delete(_ context.Context, tenant, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := webAPIKey{tenant, name}
	if _, ok := s.rows[k]; !ok {
		return ErrWebAPINotFound
	}
	delete(s.rows, k)
	return nil
}

func (s *MemWebAPIStore) AnonymizeSubject(_ context.Context, ident string) (int, error) {
	if ident == "" {
		return 0, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for k, v := range s.rows {
		if v.CreatedBy == ident {
			v.CreatedBy = core.ErasedIdentity
			s.rows[k] = v
			n++
		}
	}
	return n, nil
}

func (s *MemWebAPIStore) DeleteByTenant(_ context.Context, tenant string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for k := range s.rows {
		if k.tenant == tenant {
			delete(s.rows, k)
			n++
		}
	}
	return n, nil
}

func (s *MemWebAPIStore) SetError(_ context.Context, tenant, name, lastErr string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := webAPIKey{tenant, name}
	row, ok := s.rows[k]
	if !ok {
		return ErrWebAPINotFound
	}
	row.LastError = lastErr
	s.rows[k] = row
	return nil
}
