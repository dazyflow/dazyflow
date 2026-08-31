// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"encoding/json"
	"sort"
	"sync"
	"time"

	"github.com/dazyflow/dazyflow/core"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PgMCPServerStore is the durable MCPServerStore.
//
// A registration must outlive the daemon for the same reason a runner's does:
// the org's flows reference mcp:<server>:<tool> by id, so forgetting a server
// on restart does not degrade those flows, it breaks them.
type PgMCPServerStore struct {
	pool *pgxpool.Pool
}

func NewPgMCPServerStore(ctx context.Context, pool *pgxpool.Pool) (*PgMCPServerStore, error) {
	if err := EnsurePgMCPServerSchema(ctx, pool); err != nil {
		return nil, err
	}
	return &PgMCPServerStore{pool: pool}, nil
}

// mcpServerColumns is the select list every read shares, in MCPServer field
// order. auth_secret is absent on purpose: a credential leaves the table only
// through SealedToken, so no ordinary list or lookup can carry one into a log
// line or an API response by accident.
const mcpServerColumns = `tenant, name, label, url, auth_kind, auth_header, enabled, snapshot,
	created_by, created_at, updated_at, tool_count, last_error, last_connected`

func scanMCPServer(row pgx.Row) (MCPServer, error) {
	var s MCPServer
	var lastConnected *time.Time
	var snapshot []byte
	if err := row.Scan(&s.Tenant, &s.Name, &s.Label, &s.URL, &s.AuthKind, &s.AuthHeader, &s.Enabled, &snapshot,
		&s.CreatedBy, &s.CreatedAt, &s.UpdatedAt, &s.ToolCount, &s.LastError, &lastConnected); err != nil {
		return MCPServer{}, err
	}
	// A snapshot that will not parse is treated as absent: it is a cache, and
	// refusing to load the whole row over it would take the server out of the
	// palette entirely — the opposite of what the cache is for.
	if len(snapshot) > 0 {
		_ = json.Unmarshal(snapshot, &s.Snapshot)
	}
	if lastConnected != nil {
		s.LastConnected = *lastConnected
	}
	return s, nil
}

func (s *PgMCPServerStore) List(ctx context.Context, tenant string) ([]MCPServer, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+mcpServerColumns+` FROM tenant_mcp_servers WHERE tenant = $1 ORDER BY COALESCE(NULLIF(label, ''), name), name`, tenant)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return collectMCPServers(rows)
}

func (s *PgMCPServerStore) ListAll(ctx context.Context) ([]MCPServer, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+mcpServerColumns+` FROM tenant_mcp_servers ORDER BY tenant, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return collectMCPServers(rows)
}

func collectMCPServers(rows pgx.Rows) ([]MCPServer, error) {
	out := []MCPServer{}
	for rows.Next() {
		one, err := scanMCPServer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, one)
	}
	return out, rows.Err()
}

func (s *PgMCPServerStore) Get(ctx context.Context, tenant, name string) (MCPServer, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT `+mcpServerColumns+` FROM tenant_mcp_servers WHERE tenant = $1 AND name = $2`, tenant, name)
	one, err := scanMCPServer(row)
	if err != nil {
		if isPgNoRows(err) {
			return MCPServer{}, ErrMCPServerNotFound
		}
		return MCPServer{}, err
	}
	return one, nil
}

// Put inserts or replaces the configuration.
//
// A nil sealedToken keeps whatever is stored — COALESCE on the excluded value
// rather than a read-modify-write, so two admins saving at once cannot have
// one of them blank the other's freshly pasted credential. An empty (non-nil)
// slice clears it, which is what switching auth to none means.
func (s *PgMCPServerStore) Put(ctx context.Context, m MCPServer, sealedToken []byte) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO tenant_mcp_servers
			(tenant, name, label, url, auth_kind, auth_header, auth_secret, enabled,
			 created_by, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		-- snapshot is deliberately absent: an edit must not clear the cached
		-- tool list, or saving a URL typo would strip every flow's ports until
		-- the next successful handshake.
		ON CONFLICT (tenant, name) DO UPDATE SET
			label       = EXCLUDED.label,
			url         = EXCLUDED.url,
			auth_kind   = EXCLUDED.auth_kind,
			auth_header = EXCLUDED.auth_header,
			auth_secret = COALESCE(EXCLUDED.auth_secret, tenant_mcp_servers.auth_secret),
			enabled     = EXCLUDED.enabled,
			updated_at  = EXCLUDED.updated_at`,
		m.Tenant, m.Name, m.Label, m.URL, string(m.AuthKind), m.AuthHeader, sealedToken, m.Enabled,
		m.CreatedBy, m.CreatedAt, m.UpdatedAt)
	return err
}

func (s *PgMCPServerStore) Delete(ctx context.Context, tenant, name string) error {
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM tenant_mcp_servers WHERE tenant = $1 AND name = $2`, tenant, name)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrMCPServerNotFound
	}
	return nil
}

func (s *PgMCPServerStore) AnonymizeSubject(ctx context.Context, ident string) (int, error) {
	if ident == "" {
		return 0, nil
	}
	tag, err := s.pool.Exec(ctx,
		`UPDATE tenant_mcp_servers SET created_by = $2 WHERE created_by = $1`, ident, core.ErasedIdentity)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

func (s *PgMCPServerStore) DeleteByTenant(ctx context.Context, tenant string) (int, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM tenant_mcp_servers WHERE tenant = $1`, tenant)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

func (s *PgMCPServerStore) SealedToken(ctx context.Context, tenant, name string) ([]byte, error) {
	var blob []byte
	err := s.pool.QueryRow(ctx,
		`SELECT auth_secret FROM tenant_mcp_servers WHERE tenant = $1 AND name = $2`, tenant, name).Scan(&blob)
	if err != nil {
		if isPgNoRows(err) {
			return nil, ErrMCPServerNotFound
		}
		return nil, err
	}
	return blob, nil
}

// SetSnapshot stores what the server was last seen publishing. Like SetStatus
// it leaves updated_at alone: this is an outcome of connecting, not an edit,
// and moving that column would make every replica re-handshake on every pass.
func (s *PgMCPServerStore) SetSnapshot(ctx context.Context, tenant, name string, snap MCPSnapshot) error {
	blob, err := json.Marshal(snap)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `
		UPDATE tenant_mcp_servers
		   SET snapshot = $3
		 WHERE tenant = $1 AND name = $2`, tenant, name, blob)
	return err
}

// SetStatus records a connection outcome. It deliberately does NOT touch
// updated_at: that column is what every replica's reconcile compares against
// to decide whether a registration is current, so a status write moving it
// would make every node re-handshake on every pass.
func (s *PgMCPServerStore) SetStatus(ctx context.Context, tenant, name string, toolCount int, lastErr string, at time.Time) error {
	var connected *time.Time
	if lastErr == "" {
		connected = &at
	}
	_, err := s.pool.Exec(ctx, `
		UPDATE tenant_mcp_servers
		   SET tool_count = $3,
		       last_error = $4,
		       last_connected = COALESCE($5, last_connected)
		 WHERE tenant = $1 AND name = $2`, tenant, name, toolCount, lastErr, connected)
	return err
}

// ---- in-memory store --------------------------------------------------

// MemMCPServerStore implements MCPServerStore in process, for tests.
//
// Unlike the runner store's memory twin this one is genuinely test-only: an
// MCP registration has no agent to notice it was forgotten, so a restart with
// this store would silently drop an org's steps out of the palette.
type MemMCPServerStore struct {
	mu   sync.Mutex
	rows map[stepSourceKey]MCPServer
	toks map[stepSourceKey][]byte
}

func NewMemMCPServerStore() *MemMCPServerStore {
	return &MemMCPServerStore{rows: map[stepSourceKey]MCPServer{}, toks: map[stepSourceKey][]byte{}}
}

func (s *MemMCPServerStore) List(_ context.Context, tenant string) ([]MCPServer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []MCPServer{}
	for k, v := range s.rows {
		if k.tenant == tenant {
			out = append(out, v)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (s *MemMCPServerStore) ListAll(_ context.Context) ([]MCPServer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []MCPServer{}
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

func (s *MemMCPServerStore) Get(_ context.Context, tenant, name string) (MCPServer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.rows[stepSourceKey{tenant, name}]
	if !ok {
		return MCPServer{}, ErrMCPServerNotFound
	}
	return v, nil
}

func (s *MemMCPServerStore) Put(_ context.Context, m MCPServer, sealedToken []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := stepSourceKey{m.Tenant, m.Name}
	if old, ok := s.rows[k]; ok {
		m.ToolCount, m.LastError, m.LastConnected = old.ToolCount, old.LastError, old.LastConnected
		// An edit must not clear the cached tool list.
		m.Snapshot = old.Snapshot
	}
	s.rows[k] = m
	// nil keeps, matching the Postgres COALESCE.
	if sealedToken != nil {
		s.toks[k] = sealedToken
	}
	return nil
}

func (s *MemMCPServerStore) Delete(_ context.Context, tenant, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := stepSourceKey{tenant, name}
	if _, ok := s.rows[k]; !ok {
		return ErrMCPServerNotFound
	}
	delete(s.rows, k)
	delete(s.toks, k)
	return nil
}

func (s *MemMCPServerStore) AnonymizeSubject(_ context.Context, ident string) (int, error) {
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

func (s *MemMCPServerStore) DeleteByTenant(_ context.Context, tenant string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for k := range s.rows {
		if k.tenant == tenant {
			delete(s.rows, k)
			delete(s.toks, k)
			n++
		}
	}
	return n, nil
}

func (s *MemMCPServerStore) SealedToken(_ context.Context, tenant, name string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := stepSourceKey{tenant, name}
	if _, ok := s.rows[k]; !ok {
		return nil, ErrMCPServerNotFound
	}
	return s.toks[k], nil
}

func (s *MemMCPServerStore) SetSnapshot(_ context.Context, tenant, name string, snap MCPSnapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := stepSourceKey{tenant, name}
	row, ok := s.rows[k]
	if !ok {
		return ErrMCPServerNotFound
	}
	row.Snapshot = snap
	s.rows[k] = row
	return nil
}

func (s *MemMCPServerStore) SetStatus(_ context.Context, tenant, name string, toolCount int, lastErr string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := stepSourceKey{tenant, name}
	row, ok := s.rows[k]
	if !ok {
		return ErrMCPServerNotFound
	}
	row.ToolCount = toolCount
	row.LastError = lastErr
	if lastErr == "" {
		row.LastConnected = at
	}
	s.rows[k] = row
	return nil
}
