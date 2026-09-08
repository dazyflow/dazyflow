// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/dazyflow/dazyflow/daemon/internal/pgstore"
	"github.com/dazyflow/dazyflow/engine/mcp"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrMCPServerNotFound      = errors.New("mcp server not found")
	ErrMCPServersUnconfigured = errors.New("mcp servers are not configured")
)

// Two constraints shape this file, both about what a TENANT may make the daemon
// do. HTTP only: the stdio transport starts a process on the daemon host, so
// letting an org admin choose that command is remote code execution — operators
// keep stdio via DAZYFLOW_MCP_SERVERS, orgs get a URL. And the URL is still an
// outbound request the daemon makes on a tenant's behalf, so it goes through the
// same SSRF guard as http_request, applied at dial time AFTER DNS resolution so a
// hostname resolving inward is caught like a literal one.

type MCPAuthKind string

const (
	MCPAuthNone   MCPAuthKind = "none"
	MCPAuthBearer MCPAuthKind = "bearer"
	MCPAuthHeader MCPAuthKind = "header"
)

// The token is deliberately NOT a field: it is sealed under the tenant's DEK and
// lives only in the store, so an MCPServer can be logged, returned to the UI and
// put in an audit record without anyone remembering to strip a credential.

type MCPServer struct {
	Tenant        string
	Name          string
	Label         string
	URL           string
	AuthKind      MCPAuthKind
	AuthHeader    string
	Enabled       bool
	CreatedBy     string
	CreatedAt     time.Time
	UpdatedAt     time.Time
	Snapshot      MCPSnapshot
	ToolCount     int
	LastError     string
	LastConnected time.Time
}

// Stored as JSON rather than as synthesized manifests: a manifest is DERIVED, and
// its shape changes between releases, so a stored one would pin an old version's
// idea of the ports.

type MCPSnapshot struct {
	Tools []mcp.Tool        `json:"tools,omitempty"`
	Logos map[string]string `json:"logos,omitempty"`
}

func (s MCPSnapshot) Empty() bool { return len(s.Tools) == 0 }

func (s MCPServer) HasAuth() bool { return s.AuthKind == MCPAuthBearer || s.AuthKind == MCPAuthHeader }

func (s MCPServer) DisplayName() string {
	if s.Label != "" {
		return s.Label
	}
	return s.Name
}

type MCPServerStore interface {
	List(ctx context.Context, tenant string) ([]MCPServer, error)
	ListAll(ctx context.Context) ([]MCPServer, error)
	Get(ctx context.Context, tenant, name string) (MCPServer, error)
	Put(ctx context.Context, s MCPServer, sealedToken []byte) error
	Delete(ctx context.Context, tenant, name string) error
	DeleteByTenant(ctx context.Context, tenant string) (int, error)
	AnonymizeSubject(ctx context.Context, ident string) (int, error)
	SealedToken(ctx context.Context, tenant, name string) ([]byte, error)
	SetSnapshot(ctx context.Context, tenant, name string, snap MCPSnapshot) error
	SetStatus(ctx context.Context, tenant, name string, toolCount int, lastErr string, at time.Time) error
}

const pgMCPServerSchema = `
CREATE TABLE IF NOT EXISTS tenant_mcp_servers (
    tenant         TEXT NOT NULL,
    name           TEXT NOT NULL,
    label          TEXT NOT NULL DEFAULT '',
    snapshot       JSONB NOT NULL DEFAULT '{}'::jsonb,
    url            TEXT NOT NULL,
    auth_kind      TEXT NOT NULL DEFAULT 'none',
    auth_header    TEXT NOT NULL DEFAULT '',
    -- Sealed under the tenant's DEK with (domain, name) as AAD, so a blob
    -- cannot be relocated into another org's row and read back there.
    auth_secret    BYTEA,
    enabled        BOOLEAN NOT NULL DEFAULT TRUE,
    tool_count     INTEGER NOT NULL DEFAULT 0,
    last_error     TEXT NOT NULL DEFAULT '',
    last_connected TIMESTAMPTZ,
    created_by     TEXT NOT NULL DEFAULT '',
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant, name)
);
-- The reconcile loop reads every enabled row on a timer; without this it is a
-- sequential scan of the table on each pass.
CREATE INDEX IF NOT EXISTS tenant_mcp_servers_enabled_idx ON tenant_mcp_servers (enabled);
-- Added after the table shipped: name used to be both the id and the display
-- name. Existing rows keep an empty label and render by name.
ALTER TABLE tenant_mcp_servers ADD COLUMN IF NOT EXISTS label TEXT NOT NULL DEFAULT '';
-- The last tool list the server was seen publishing, so its steps stay
-- described while it is unreachable. Added after the table shipped; an
-- existing row starts with none and gets one on its next successful connect.
ALTER TABLE tenant_mcp_servers ADD COLUMN IF NOT EXISTS snapshot JSONB NOT NULL DEFAULT '{}'::jsonb;
`

func EnsurePgMCPServerSchema(ctx context.Context, pool *pgxpool.Pool) error {
	return pgstore.ApplySchema(ctx, pool, pgMCPServerSchema)
}

const mcpSecretDomain = "mcp_server"

const maxMCPServersPerTenant = 50

const fallbackMCPServerName = "mcp-server"

func (m *MCPServers) uniqueMCPServerName(ctx context.Context, tenant, base string) (string, error) {
	rows, err := m.Store.List(ctx, tenant)
	if err != nil {
		return "", err
	}
	taken := make(map[string]bool, len(rows))
	for _, r := range rows {
		taken[r.Name] = true
	}
	if m.Catalog != nil {
		for _, st := range m.Catalog.ServersFor(tenant) {
			if st.Tenant == "" {
				taken[st.Name] = true
			}
		}
	}
	return uniqueStepSourceName(base, fallbackMCPServerName, taken, maxMCPServersPerTenant)
}

const maxMCPServerLabelLen = 96

func validMCPAuth(kind MCPAuthKind, header string) error {
	switch kind {
	case MCPAuthNone, MCPAuthBearer:
		return nil
	case MCPAuthHeader:
		if strings.TrimSpace(header) == "" {
			return fmt.Errorf("header name is required when auth is a custom header")
		}
		if !validHeaderName(header) {
			return fmt.Errorf("header name may use letters, digits and - only")
		}
		return nil
	default:
		return fmt.Errorf("auth must be one of none, bearer, header")
	}
}

func validHeaderName(h string) bool {
	if h == "" || len(h) > 64 {
		return false
	}
	for _, r := range h {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-':
		default:
			return false
		}
	}
	return true
}

type MCPServers struct {
	Store   MCPServerStore
	Catalog *mcp.Catalog
	Secrets *EncryptedSecrets
	Now     func() time.Time

	stepSourceRegistry
}

func (m *MCPServers) now() time.Time {
	if m == nil {
		return time.Now()
	}
	return nowOr(m.Now)
}

func (m *MCPServers) ready() error {
	if m == nil || m.Store == nil || m.Catalog == nil {
		return ErrMCPServersUnconfigured
	}
	return nil
}

type MCPServerInput struct {
	Label      string
	Name       string
	URL        string
	AuthKind   MCPAuthKind
	AuthHeader string
	Token      string
	Enabled    bool
}

func (m *MCPServers) List(ctx context.Context, tenant string) ([]MCPServer, error) {
	if err := m.ready(); err != nil {
		return nil, err
	}
	return m.Store.List(ctx, tenant)
}

func (m *MCPServers) Save(ctx context.Context, tenant, actor string, in MCPServerInput) (MCPServer, error) {
	if err := m.ready(); err != nil {
		return MCPServer{}, err
	}
	if tenant == "" {
		return MCPServer{}, fmt.Errorf("mcp server: tenant required")
	}
	if m.Secrets == nil && in.Token != "" {
		return MCPServer{}, fmt.Errorf("this deployment has no encrypted secret store, so a token cannot be stored safely")
	}
	label := strings.TrimSpace(in.Label)
	if len([]rune(label)) > maxMCPServerLabelLen {
		return MCPServer{}, fmt.Errorf("name too long (max %d characters)", maxMCPServerLabelLen)
	}
	name := strings.ToLower(strings.TrimSpace(in.Name))
	if name == "" {
		if label == "" {
			return MCPServer{}, fmt.Errorf("name is empty")
		}
		name, err := m.uniqueMCPServerName(ctx, tenant, slugStepSourceName(label))
		if err != nil {
			return MCPServer{}, err
		}
		return m.save(ctx, tenant, actor, in, name, label)
	}
	if err := validStepSourceName(name); err != nil {
		return MCPServer{}, err
	}
	return m.save(ctx, tenant, actor, in, name, label)
}

func (m *MCPServers) save(ctx context.Context, tenant, actor string, in MCPServerInput, name, label string) (MCPServer, error) {
	rawURL := strings.TrimSpace(in.URL)
	if err := validStepSourceURL(rawURL); err != nil {
		return MCPServer{}, err
	}
	kind := in.AuthKind
	if kind == "" {
		kind = MCPAuthNone
	}
	header := strings.TrimSpace(in.AuthHeader)
	if err := validMCPAuth(kind, header); err != nil {
		return MCPServer{}, err
	}

	existing, err := m.Store.Get(ctx, tenant, name)
	isNew := errors.Is(err, ErrMCPServerNotFound)
	if err != nil && !isNew {
		return MCPServer{}, err
	}
	if label == "" {
		label = existing.Label
	}
	if isNew {
		rows, err := m.Store.List(ctx, tenant)
		if err != nil {
			return MCPServer{}, err
		}
		if len(rows) >= maxMCPServersPerTenant {
			return MCPServer{}, fmt.Errorf("this org already has %d MCP servers (the maximum)", maxMCPServersPerTenant)
		}
	}
	if kind != MCPAuthNone && in.Token == "" && (isNew || !existing.HasAuth()) {
		return MCPServer{}, fmt.Errorf("a token is required for %s auth", kind)
	}

	now := m.now()
	row := MCPServer{
		Tenant:     tenant,
		Name:       name,
		Label:      label,
		URL:        rawURL,
		AuthKind:   kind,
		AuthHeader: header,
		Enabled:    in.Enabled,
		CreatedBy:  actor,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	if !isNew {
		row.CreatedBy = existing.CreatedBy
		row.CreatedAt = existing.CreatedAt
		row.Snapshot = existing.Snapshot
	}

	var sealed []byte
	switch {
	case kind == MCPAuthNone:
		sealed = []byte{}
	case in.Token != "":
		sealed, err = m.Secrets.SealPayload(ctx, tenant, mcpSecretDomain, name, []byte(in.Token))
		if err != nil {
			return MCPServer{}, fmt.Errorf("seal token: %w", err)
		}
	}

	if err := m.Store.Put(ctx, row, sealed); err != nil {
		return MCPServer{}, err
	}

	if row.Enabled {
		row = m.connect(ctx, row)
	} else {
		m.Catalog.Unregister(tenant, name)
		m.forget(stepSourceKey{tenant, name})
		row.ToolCount = 0
	}
	return row, nil
}

func (m *MCPServers) Delete(ctx context.Context, tenant, name string) error {
	if err := m.ready(); err != nil {
		return err
	}
	if err := m.Store.Delete(ctx, tenant, name); err != nil {
		return err
	}
	m.Catalog.Unregister(tenant, name)
	m.forget(stepSourceKey{tenant, name})
	return nil
}

func (m *MCPServers) DeleteByTenant(ctx context.Context, tenant string) (int, error) {
	if err := m.ready(); err != nil {
		return 0, err
	}
	servers, err := m.Store.List(ctx, tenant)
	if err != nil {
		return 0, fmt.Errorf("list mcp servers for %q: %w", tenant, err)
	}
	for _, s := range servers {
		m.Catalog.Unregister(tenant, s.Name)
		m.forget(stepSourceKey{tenant, s.Name})
	}
	n, err := m.Store.DeleteByTenant(ctx, tenant)
	if err != nil {
		return 0, fmt.Errorf("delete mcp servers for %q: %w", tenant, err)
	}
	return n, nil
}

func (m *MCPServers) Refresh(ctx context.Context, tenant, name string) (MCPServer, error) {
	if err := m.ready(); err != nil {
		return MCPServer{}, err
	}
	row, err := m.Store.Get(ctx, tenant, name)
	if err != nil {
		return MCPServer{}, err
	}
	if !row.Enabled {
		return row, nil
	}
	return m.connect(ctx, row), nil
}

func (m *MCPServers) connect(ctx context.Context, row MCPServer) MCPServer {
	desc, err := m.descriptor(ctx, row)
	if err == nil {
		err = m.Catalog.RegisterHTTP(desc)
	}
	now := m.now()
	if err != nil {
		m.forget(stepSourceKey{row.Tenant, row.Name})
		row.LastError = err.Error()
		row.ToolCount = m.describeOffline(row, row.LastError)
		_ = m.Store.SetStatus(ctx, row.Tenant, row.Name, row.ToolCount, row.LastError, now)
		return row
	}
	row.ToolCount = m.toolCount(row.Tenant, row.Name)
	row.LastError = ""
	row.LastConnected = now
	m.remember(stepSourceKey{row.Tenant, row.Name}, row.UpdatedAt)
	if tools, logos, ok := m.Catalog.SnapshotFor(row.Tenant, row.Name); ok && len(tools) > 0 {
		snap := MCPSnapshot{Tools: tools, Logos: logos}
		row.Snapshot = snap
		_ = m.Store.SetSnapshot(ctx, row.Tenant, row.Name, snap)
	}
	_ = m.Store.SetStatus(ctx, row.Tenant, row.Name, row.ToolCount, "", now)
	return row
}

func (m *MCPServers) describeOffline(row MCPServer, reason string) int {
	if row.Snapshot.Empty() {
		m.Catalog.Unregister(row.Tenant, row.Name)
		return 0
	}
	err := m.Catalog.RegisterOffline(mcp.OfflineDescriptor{
		Tenant: row.Tenant,
		Name:   row.Name,
		Label:  row.DisplayName(),
		Tools:  row.Snapshot.Tools,
		Logos:  row.Snapshot.Logos,
		Reason: reason,
	})
	if err != nil {
		m.Catalog.Unregister(row.Tenant, row.Name)
		return 0
	}
	return m.toolCount(row.Tenant, row.Name)
}

func (m *MCPServers) toolCount(tenant, name string) int {
	for _, st := range m.Catalog.ServersFor(tenant) {
		if st.Name == name && st.Tenant == tenant {
			return len(st.ToolIDs)
		}
	}
	return 0
}

func (m *MCPServers) descriptor(ctx context.Context, row MCPServer) (mcp.HTTPDescriptor, error) {
	desc := mcp.HTTPDescriptor{
		Name:   row.Name,
		Label:  row.DisplayName(),
		Tenant: row.Tenant,
		URL:    row.URL,
		Header: http.Header{},
	}
	if !row.HasAuth() {
		return desc, nil
	}
	if m.Secrets == nil {
		return desc, fmt.Errorf("this deployment has no encrypted secret store, so the stored token cannot be read")
	}
	blob, err := m.Store.SealedToken(ctx, row.Tenant, row.Name)
	if err != nil {
		return desc, err
	}
	if len(blob) == 0 {
		return desc, fmt.Errorf("no token is stored for this server — edit it and paste the token again")
	}
	plain, err := m.Secrets.OpenPayload(ctx, row.Tenant, mcpSecretDomain, row.Name, blob)
	if err != nil {
		return desc, fmt.Errorf("open token: %w", err)
	}
	token := string(plain)

	if ref, ok := secretReference(token); ok {
		resolved, err := m.Secrets.GetExact(ctx, row.Tenant, ref)
		if err != nil {
			return desc, fmt.Errorf("secret %q: %w", ref, err)
		}
		token = resolved
	}

	switch row.AuthKind {
	case MCPAuthBearer:
		desc.Header.Set("Authorization", "Bearer "+token)
	case MCPAuthHeader:
		desc.Header.Set(row.AuthHeader, token)
	}
	return desc, nil
}

func secretReference(v string) (string, bool) {
	s := strings.TrimSpace(v)
	if !strings.HasPrefix(s, "${secret.") || !strings.HasSuffix(s, "}") {
		return "", false
	}
	name := strings.TrimSuffix(strings.TrimPrefix(s, "${secret."), "}")
	if name == "" || strings.ContainsAny(name, "${}") {
		return "", false
	}
	return name, true
}

func (m *MCPServers) Reconcile(ctx context.Context) error {
	if err := m.ready(); err != nil {
		return err
	}
	rows, err := m.Store.ListAll(ctx)
	if err != nil {
		return err
	}
	reconcileStepSources(ctx, &m.stepSourceRegistry, rows, stepSourcePlan[MCPServer]{
		key:        func(r MCPServer) stepSourceKey { return stepSourceKey{r.Tenant, r.Name} },
		enabled:    func(r MCPServer) bool { return r.Enabled },
		updatedAt:  func(r MCPServer) time.Time { return r.UpdatedAt },
		apply:      func(ctx context.Context, r MCPServer) { m.connect(ctx, r) },
		unregister: m.Catalog.Unregister,
	})
	return nil
}

func (m *MCPServers) RunReconciler(ctx context.Context, logf func(string, ...any)) {
	if err := m.ready(); err != nil {
		return
	}
	runStepSourceReconciler(ctx, "mcp servers", m.Reconcile, logf)
}
