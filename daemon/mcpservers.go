// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	hfnet "git.sr.ht/~klahr/dazyflow/drops/net"
	"git.sr.ht/~klahr/dazyflow/engine/mcp"
	"github.com/jackc/pgx/v5/pgxpool"
)

// An MCP server is a catalog of steps an org brings with it.
//
// This is the answer to the objection every automation tool meets — "you don't
// integrate with the thing I use". An org points Dazyflow at an MCP endpoint
// and every tool that server publishes becomes a step in the palette, named
// mcp:<server>:<tool>, wired like any other. Nobody had to write a connector.
//
// Two constraints shape everything below, and both are about what a TENANT is
// allowed to make the daemon do:
//
//	HTTP only. The stdio transport starts a process on the daemon host; letting
//	an org admin choose that command is remote code execution, and on a hosted
//	instance it is remote code execution by any customer. Operators keep stdio
//	via DAZYFLOW_MCP_SERVERS; orgs get a URL.
//
//	The URL is still an outbound request the daemon makes on a tenant's behalf,
//	so it goes through the same SSRF guard as http_request — a server pointed at
//	169.254.169.254 or at a database on the daemon's own network is refused at
//	dial time, after DNS resolution, so a hostname that resolves inward is
//	caught with a literal one.

var (
	// ErrMCPServerNotFound is returned when no server is configured under a name.
	ErrMCPServerNotFound = errors.New("mcp server not found")
	// ErrMCPServersUnconfigured means this deployment has no store wired.
	ErrMCPServersUnconfigured = errors.New("mcp servers are not configured")
)

// MCPAuthKind is how a server's credential is presented.
type MCPAuthKind string

const (
	// MCPAuthNone is a server that needs no credential.
	MCPAuthNone MCPAuthKind = "none"
	// MCPAuthBearer sends Authorization: Bearer <token>.
	MCPAuthBearer MCPAuthKind = "bearer"
	// MCPAuthHeader sends the token under a header the server names — for the
	// vendors that use X-Api-Key rather than Authorization.
	MCPAuthHeader MCPAuthKind = "header"
)

// MCPServer is one HTTP MCP endpoint an org has configured.
//
// The token is deliberately NOT a field. It is sealed under the tenant's DEK
// and lives only in the store, so an MCPServer can be logged, returned to the
// UI, and put in an audit record without anyone having to remember to strip
// a credential first.
type MCPServer struct {
	Tenant string
	// Name is what flows reference: a tool from this server is the step
	// mcp:<name>:<tool>. Renaming is therefore not an edit — it is a new
	// server, and the old ids stop resolving. The UI says so.
	Name       string
	URL        string
	AuthKind   MCPAuthKind
	AuthHeader string
	// Enabled false keeps the row and its credential but takes the steps out
	// of the palette — the reversible half of deleting, for a server an org
	// wants to stop calling while it works out why it misbehaved.
	Enabled   bool
	CreatedBy string
	CreatedAt time.Time
	UpdatedAt time.Time
	// ToolCount, LastError and LastConnected are the outcome of the last
	// connection attempt, persisted so the list can explain a server that is
	// not working without every page load re-handshaking with it.
	ToolCount     int
	LastError     string
	LastConnected time.Time
}

// HasAuth reports whether this server presents a credential.
func (s MCPServer) HasAuth() bool { return s.AuthKind == MCPAuthBearer || s.AuthKind == MCPAuthHeader }

// MCPServerStore persists an org's MCP server registrations.
type MCPServerStore interface {
	List(ctx context.Context, tenant string) ([]MCPServer, error)
	// ListAll spans every tenant, for the reconcile loop that reconnects the
	// fleet at boot and picks up another replica's edits.
	ListAll(ctx context.Context) ([]MCPServer, error)
	Get(ctx context.Context, tenant, name string) (MCPServer, error)
	// Put inserts or replaces. A nil sealedToken KEEPS the stored credential,
	// which is what an edit that did not retype the token means; to clear one,
	// save with AuthKind none.
	Put(ctx context.Context, s MCPServer, sealedToken []byte) error
	Delete(ctx context.Context, tenant, name string) error
	// SealedToken returns the stored credential blob, still sealed.
	SealedToken(ctx context.Context, tenant, name string) ([]byte, error)
	// SetStatus records the outcome of a connection attempt. Deliberately
	// separate from Put: a status write must not disturb the configuration,
	// and the reconcile loop writes status far more often than anyone edits.
	SetStatus(ctx context.Context, tenant, name string, toolCount int, lastErr string, at time.Time) error
}

const pgMCPServerSchema = `
CREATE TABLE IF NOT EXISTS tenant_mcp_servers (
    tenant         TEXT NOT NULL,
    name           TEXT NOT NULL,
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
`

// EnsurePgMCPServerSchema creates the MCP server table.
func EnsurePgMCPServerSchema(ctx context.Context, pool *pgxpool.Pool) error {
	return applyPgSchema(ctx, pool, pgMCPServerSchema)
}

// mcpSecretDomain scopes sealed MCP credentials in the payload AAD, so a blob
// sealed for an MCP server cannot be opened as, say, a runner's script.
const mcpSecretDomain = "mcp_server"

// ---- validation -------------------------------------------------------

// maxMCPServersPerTenant bounds how many an org may configure.
//
// Each one is a live handshake at boot and on every reconcile pass, so the
// cost of a thousand is paid by the daemon, not by the org that added them.
// High enough that nobody legitimate meets it.
const maxMCPServersPerTenant = 50

// validMCPServerName keeps a name usable inside a step id and readable in a
// palette.
//
// Stricter than it looks for one reason: the name goes into "mcp:<name>:<tool>",
// so a colon in it would produce an id that splits two ways. Lowercase letters,
// digits, hyphen and underscore only — the same shape a runner name takes, so
// an admin does not have to learn two rules.
func validMCPServerName(name string) error {
	if name == "" {
		return fmt.Errorf("name is empty")
	}
	if len(name) > 48 {
		return fmt.Errorf("name too long (max 48)")
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return fmt.Errorf("name may use lowercase letters, digits, - and _ only")
		}
	}
	return nil
}

// validMCPServerURL refuses what should never be dialed before the request is
// ever made.
//
// The dial guard is the real defence — it sees the resolved IP and so catches
// a hostname pointed inward — but a URL is worth refusing at SAVE time when it
// can be: an admin who typos gets told immediately rather than watching a
// server sit permanently in error.
//
// Cleartext http is refused because the credential rides in a header. The one
// exception is a deployment that opted into private egress, which is how a
// developer reaches an MCP server on their own laptop; on such a deployment
// the operator has already said the network is trusted.
func validMCPServerURL(raw string) error {
	if strings.TrimSpace(raw) == "" {
		return fmt.Errorf("URL is empty")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("URL is not valid: %w", err)
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !hfnet.PrivateEgressAllowed() {
			return fmt.Errorf("URL must be https — a token sent over http is readable in transit")
		}
	default:
		return fmt.Errorf("URL must start with https://")
	}
	if u.Host == "" {
		return fmt.Errorf("URL has no host")
	}
	return nil
}

// validMCPAuth checks the credential fields hang together.
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

// validHeaderName keeps a tenant from smuggling a second header (or a whole
// request line) in through the header-name field.
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

// ---- service ----------------------------------------------------------

// MCPServers is the service the API talks to. It owns the mapping between
// stored rows and live catalog registrations.
type MCPServers struct {
	Store   MCPServerStore
	Catalog *mcp.Catalog
	// Secrets seals and opens the stored credential. Required: without it a
	// token could only be stored in the clear, so Save refuses instead.
	Secrets *EncryptedSecrets
	// Now is overridable for tests; nil means time.Now.
	Now func() time.Time

	// mu guards applied.
	mu sync.Mutex
	// applied records the UpdatedAt of the row behind each live registration.
	//
	// This is what makes reconcile cheap and correct across replicas: a row
	// whose UpdatedAt still matches is already connected with the current
	// configuration and is left alone, while an edit made on ANOTHER replica
	// carries a newer UpdatedAt and so re-registers here on the next pass.
	applied map[mcpKey]time.Time
}

type mcpKey struct {
	tenant string
	name   string
}

func (m *MCPServers) now() time.Time {
	if m != nil && m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

func (m *MCPServers) ready() error {
	if m == nil || m.Store == nil || m.Catalog == nil {
		return ErrMCPServersUnconfigured
	}
	return nil
}

// MCPServerInput is what an admin submits. Token is separate from MCPServer
// precisely because MCPServer is the shape that gets returned and logged.
type MCPServerInput struct {
	Name       string
	URL        string
	AuthKind   MCPAuthKind
	AuthHeader string
	// Token is the credential. Empty on an edit means "keep the stored one";
	// to remove it, save with AuthKind none.
	Token string
	// Enabled defaults true for a new server.
	Enabled bool
}

// List returns an org's servers.
func (m *MCPServers) List(ctx context.Context, tenant string) ([]MCPServer, error) {
	if err := m.ready(); err != nil {
		return nil, err
	}
	return m.Store.List(ctx, tenant)
}

// Save validates, persists, and immediately connects.
//
// Connecting as part of the save is the point: an admin who has just pasted a
// URL and a token wants to know NOW whether it works, and the failure they
// need to see ("the server refused the credential") is only knowable by
// trying. A server that fails to connect is still saved — with the error on
// the row — so the fix is an edit rather than retyping everything.
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
	name := strings.ToLower(strings.TrimSpace(in.Name))
	if err := validMCPServerName(name); err != nil {
		return MCPServer{}, err
	}
	rawURL := strings.TrimSpace(in.URL)
	if err := validMCPServerURL(rawURL); err != nil {
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
	if isNew {
		rows, err := m.Store.List(ctx, tenant)
		if err != nil {
			return MCPServer{}, err
		}
		if len(rows) >= maxMCPServersPerTenant {
			return MCPServer{}, fmt.Errorf("this org already has %d MCP servers (the maximum)", maxMCPServersPerTenant)
		}
	}
	// A blank token means "keep the stored one" — but there is only something
	// to keep when the server ALREADY had auth. Switching a credential-free
	// server to bearer without supplying one would otherwise save happily and
	// then fail to connect, blaming the endpoint for a field the form never
	// asked for.
	if kind != MCPAuthNone && in.Token == "" && (isNew || !existing.HasAuth()) {
		return MCPServer{}, fmt.Errorf("a token is required for %s auth", kind)
	}

	now := m.now()
	row := MCPServer{
		Tenant:     tenant,
		Name:       name,
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
	}

	// nil means keep. Auth cleared to none drops the stored blob outright
	// rather than leaving a credential nothing references.
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

	// Connect (or take down, when disabled) before returning, so the caller's
	// response already carries the outcome.
	if row.Enabled {
		row = m.connect(ctx, row)
	} else {
		m.Catalog.Unregister(tenant, name)
		m.forget(mcpKey{tenant, name})
		row.ToolCount = 0
	}
	return row, nil
}

// Delete removes a server and takes its steps out of the palette.
//
// Flows referencing mcp:<name>:<tool> do not stop being valid graphs — they
// stop RESOLVING, and a run fails with "no transport registered". That is the
// same bargain deleting a runner makes, and the UI warns before it happens.
func (m *MCPServers) Delete(ctx context.Context, tenant, name string) error {
	if err := m.ready(); err != nil {
		return err
	}
	if err := m.Store.Delete(ctx, tenant, name); err != nil {
		return err
	}
	m.Catalog.Unregister(tenant, name)
	m.forget(mcpKey{tenant, name})
	return nil
}

// Refresh re-handshakes with a server and re-reads its tool list.
//
// The button exists because a tool list is a snapshot: an MCP server that gains
// a tool after registration would otherwise not offer it until the daemon
// restarted or the reconcile interval elapsed.
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

// connect handshakes with one server and records the outcome on the row.
//
// It never returns an error: a server that will not connect is a STATE an
// admin needs to see on the list, not a failure of the operation that touched
// it. The error text is what the page shows.
func (m *MCPServers) connect(ctx context.Context, row MCPServer) MCPServer {
	desc, err := m.descriptor(ctx, row)
	if err == nil {
		err = m.Catalog.RegisterHTTP(desc)
	}
	now := m.now()
	if err != nil {
		// Unregister as well as record: a server that WAS working and has just
		// had its token rotated away must stop offering steps, not keep serving
		// from a connection that no longer authenticates.
		//
		// forget rather than remember, so the next reconcile pass tries again.
		// A failing server is retried every interval, indefinitely — which is
		// what makes it come back on its own when the vendor's outage ends,
		// without anyone revisiting the page.
		m.Catalog.Unregister(row.Tenant, row.Name)
		m.forget(mcpKey{row.Tenant, row.Name})
		row.LastError = err.Error()
		row.ToolCount = 0
		_ = m.Store.SetStatus(ctx, row.Tenant, row.Name, 0, row.LastError, now)
		return row
	}
	row.ToolCount = m.toolCount(row.Tenant, row.Name)
	row.LastError = ""
	row.LastConnected = now
	m.remember(mcpKey{row.Tenant, row.Name}, row.UpdatedAt)
	_ = m.Store.SetStatus(ctx, row.Tenant, row.Name, row.ToolCount, "", now)
	return row
}

func (m *MCPServers) toolCount(tenant, name string) int {
	for _, st := range m.Catalog.ServersFor(tenant) {
		if st.Name == name && st.Tenant == tenant {
			return len(st.ToolIDs)
		}
	}
	return 0
}

// descriptor builds the live connection parameters, opening the sealed token.
func (m *MCPServers) descriptor(ctx context.Context, row MCPServer) (mcp.HTTPDescriptor, error) {
	desc := mcp.HTTPDescriptor{
		Name:   row.Name,
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

	// A token entered as ${secret.NAME} resolves through the org's secret
	// store, so a credential an org already manages (and rotates) in one place
	// does not have to be pasted here a second time.
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

// secretReference recognises the ${secret.NAME} form the rest of the product
// uses for a stored secret, so this field accepts what an author would type
// anywhere else.
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

func (m *MCPServers) remember(k mcpKey, updated time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.applied == nil {
		m.applied = map[mcpKey]time.Time{}
	}
	m.applied[k] = updated
}

func (m *MCPServers) forget(k mcpKey) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.applied, k)
}

func (m *MCPServers) appliedAt(k mcpKey) (time.Time, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.applied[k]
	return t, ok
}

func (m *MCPServers) appliedKeys() []mcpKey {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]mcpKey, 0, len(m.applied))
	for k := range m.applied {
		out = append(out, k)
	}
	return out
}

// Reconcile makes this process's live registrations match the store.
//
// It runs at boot and then on a timer, and it is what makes the feature work
// on more than one dzd. A server added on replica A is a row in Postgres, not
// a message: replica B notices it on its next pass, connects, and starts
// resolving that org's steps. A deletion propagates the same way, and an edit
// does too — the row's UpdatedAt is newer than what this replica applied, so
// it re-registers rather than keeping a stale URL or a rotated-away token.
//
// Errors connecting one server are recorded on its row and do not stop the
// pass: one org's expired token must not keep every other org's servers down.
func (m *MCPServers) Reconcile(ctx context.Context) error {
	if err := m.ready(); err != nil {
		return err
	}
	rows, err := m.Store.ListAll(ctx)
	if err != nil {
		return err
	}
	desired := make(map[mcpKey]struct{}, len(rows))
	for _, row := range rows {
		key := mcpKey{row.Tenant, row.Name}
		if !row.Enabled {
			continue
		}
		desired[key] = struct{}{}
		if at, ok := m.appliedAt(key); ok && at.Equal(row.UpdatedAt) {
			continue
		}
		m.connect(ctx, row)
	}
	// Anything this replica holds that the store no longer wants: deleted or
	// disabled, here or on another node.
	for _, key := range m.appliedKeys() {
		if _, want := desired[key]; want {
			continue
		}
		m.Catalog.Unregister(key.tenant, key.name)
		m.forget(key)
	}
	return nil
}

// MCPReconcileInterval is how long a change made on another replica may take
// to appear here.
//
// A compromise, and worth naming as one: shorter means a colleague's new
// server shows up in your palette sooner, longer means fewer needless list
// queries. Thirty seconds is well under the time it takes someone to add a
// server and then go looking for its steps.
const MCPReconcileInterval = 30 * time.Second

// RunReconciler reconciles until ctx ends. Started by cmd/dzd.
func (m *MCPServers) RunReconciler(ctx context.Context, logf func(string, ...any)) {
	if err := m.ready(); err != nil {
		return
	}
	ticker := time.NewTicker(MCPReconcileInterval)
	defer ticker.Stop()
	for {
		if err := m.Reconcile(ctx); err != nil && logf != nil && ctx.Err() == nil {
			logf("mcp servers: reconcile: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
