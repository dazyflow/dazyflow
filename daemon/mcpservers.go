// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

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
	// mcp:<name>:<tool>. It is DERIVED from Label at creation and never
	// changes afterwards — renaming would silently re-key every step id the
	// org's flows hold.
	Name string
	// Label is what a human called this server, kept verbatim: "MCP Test",
	// "Kunddatabas (test)". Free to edit, because nothing references it — it
	// is display only, and Name carries the identity.
	//
	// Empty on rows created before labels existed. Display reads through
	// DisplayName so those still render as something.
	Label      string
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
	// Snapshot is the tool list this server was last seen publishing.
	//
	// Persisted for one reason: a tool's manifest is what tells the editor a
	// step's PORTS, so losing it when the endpoint goes down makes every flow
	// wired into that step look like it lost its edges. With the snapshot the
	// steps stay fully described and merely unavailable. It outlives a daemon
	// restart because "the server is still down after a deploy" is exactly
	// when an author would otherwise open a flow that looks broken.
	Snapshot MCPSnapshot
	// ToolCount, LastError and LastConnected are the outcome of the last
	// connection attempt, persisted so the list can explain a server that is
	// not working without every page load re-handshaking with it.
	ToolCount     int
	LastError     string
	LastConnected time.Time
}

// MCPSnapshot is what a server was last seen publishing: enough to describe
// its steps with no connection open.
//
// Stored as JSON on the row rather than as synthesized manifests, because a
// manifest is DERIVED — the shape synthesizeManifest produces changes between
// versions, and a stored one would pin an old release's idea of the ports.
type MCPSnapshot struct {
	Tools []mcp.Tool `json:"tools,omitempty"`
	// Logos are resolved icons by tool name. Kept alongside the tools so a
	// disconnected step keeps its identity as well as its shape, and so a
	// reconcile pass over a dead server costs no icon fetches.
	Logos map[string]string `json:"logos,omitempty"`
}

// Empty reports whether there is nothing to describe.
func (s MCPSnapshot) Empty() bool { return len(s.Tools) == 0 }

// HasAuth reports whether this server presents a credential.
func (s MCPServer) HasAuth() bool { return s.AuthKind == MCPAuthBearer || s.AuthKind == MCPAuthHeader }

// DisplayName is what to show a human: the label when there is one, and the
// id when there is not. Rows written before labels existed have no label, and
// so do rows created through the API by id alone.
func (s MCPServer) DisplayName() string {
	if s.Label != "" {
		return s.Label
	}
	return s.Name
}

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
	// DeleteByTenant removes every server a tenant configured, returning the
	// count. The erasure-cascade entry point (GDPR Art. 17): erasure cannot
	// go name-by-name, because it does not know what an org configured over
	// its lifetime.
	DeleteByTenant(ctx context.Context, tenant string) (int, error)
	// AnonymizeSubject replaces an erased person's identifier wherever it
	// appears in this store's rows, returning the rows changed.
	//
	// The rows belong to an ORG and outlive the person, so their identifier is
	// pseudonymised rather than deleted — the same treatment the audit trail
	// gets. Deleting an org takes these rows anyway; this is the OTHER path,
	// where a member of a shared org erases their account and the org carries
	// on with their address still in it.
	AnonymizeSubject(ctx context.Context, ident string) (int, error)
	// SealedToken returns the stored credential blob, still sealed.
	SealedToken(ctx context.Context, tenant, name string) ([]byte, error)
	// SetSnapshot records what the server was last seen publishing. Separate
	// from Put for the same reason SetStatus is: it is written by the connect
	// path, not by an admin's edit, and must not disturb the configuration.
	SetSnapshot(ctx context.Context, tenant, name string, snap MCPSnapshot) error
	// SetStatus records the outcome of a connection attempt. Deliberately
	// separate from Put: a status write must not disturb the configuration,
	// and the reconcile loop writes status far more often than anyone edits.
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

// fallbackMCPServerName is the base used when a label slugs to nothing — a name
// in a script with no Latin letters, or one made only of punctuation. The
// uniqueness pass then makes it mcp-server, mcp-server-2, and so on.
const fallbackMCPServerName = "mcp-server"

// uniqueMCPServerName picks a free id, deciding what "taken" means for an MCP
// server: the org's own rows AND the instance-wide servers an operator
// registered, because the catalog refuses a tenant name that collides with one
// of those — better to pick a free id here than to save a row that can never
// connect.
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

// maxMCPServerLabelLen bounds the display name. Long enough for a sentence
// fragment an admin would actually type, short enough that the list stays a
// list.
const maxMCPServerLabelLen = 96

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
	// Label is what the admin typed — free-form. On a NEW server it is what
	// the id is derived from; on an edit it is the one part of the identity
	// that can still change. Empty on an edit means "keep the stored one", so
	// a caller that only wants to change the URL cannot blank it by omission.
	Label string
	// Name is the id. Empty on a create means "derive it from Label", which is
	// what the UI sends. A caller that supplies one is choosing its own id and
	// gets it validated as before. On an edit it identifies the row and is
	// never changed.
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
	label := strings.TrimSpace(in.Label)
	if len([]rune(label)) > maxMCPServerLabelLen {
		return MCPServer{}, fmt.Errorf("name too long (max %d characters)", maxMCPServerLabelLen)
	}
	name := strings.ToLower(strings.TrimSpace(in.Name))
	// A create with no explicit id derives one from the label. This is the
	// path the UI takes, and the reason an admin can call a server "MCP Test"
	// while its steps stay addressable as mcp:mcp-test:<tool>.
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

// save is Save once the identity is settled: name is final, whether it came
// from the caller or from the label. Split out so the derivation above reads as
// one decision instead of threading a flag through eighty lines.
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
	// Blank keeps the stored label, for the same reason a blank token keeps
	// the stored credential: an API caller changing only the URL must not
	// erase a display name it never sent.
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
		// Carried, not re-derived: the connect below may fail (a URL typo is
		// the usual way), and it needs the cached tool list to fall back on.
		// Without this, fixing one field would strip every flow's ports until
		// the next successful handshake. The stores keep it on Put for the same
		// reason; this is the in-memory half of that promise.
		row.Snapshot = existing.Snapshot
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

// DeleteByTenant removes every MCP server an org configured and takes each one
// out of the live catalog, returning the count. The erasure cascade's hook
// (GDPR Art. 17); see deleteOrgData in gdpr.go.
//
// It lists and then deletes one at a time rather than issuing a single
// tenant-wide statement, because unregistering is half the job: a row deleted
// straight from under the catalog would leave this process still holding the
// org's transports — and the sealed token in memory — until a restart. Erasure
// is rare enough that the extra queries cost nothing worth having.
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
		m.forget(mcpKey{tenant, s.Name})
	}
	// The tenant-wide delete still runs, and its count is what we report: a
	// row written between the List and here is caught by it, and must be —
	// this is the erasure path, and "all but the one that raced" is not
	// erasure. Such a row's catalog entry is dropped by the next reconcile.
	n, err := m.Store.DeleteByTenant(ctx, tenant)
	if err != nil {
		return 0, fmt.Errorf("delete mcp servers for %q: %w", tenant, err)
	}
	return n, nil
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
		// The live registration goes: a server whose token has just been
		// rotated away must stop SERVING, not keep answering from a connection
		// that no longer authenticates.
		//
		// What replaces it is a description, not a connection. The steps stay
		// in the catalog, fully specified and marked unavailable, so a flow
		// wired into them keeps its ports and its edges; running one fails
		// with the reason. Without this the manifests vanish and every such
		// flow opens looking like it lost its wiring — see engine/mcp/offline.go.
		//
		// forget rather than remember, so the next reconcile pass tries again.
		// A failing server is retried every interval, indefinitely — which is
		// what makes it come back on its own when the vendor's outage ends,
		// without anyone revisiting the page.
		m.forget(mcpKey{row.Tenant, row.Name})
		row.LastError = err.Error()
		row.ToolCount = m.describeOffline(row, row.LastError)
		_ = m.Store.SetStatus(ctx, row.Tenant, row.Name, row.ToolCount, row.LastError, now)
		return row
	}
	row.ToolCount = m.toolCount(row.Tenant, row.Name)
	row.LastError = ""
	row.LastConnected = now
	m.remember(mcpKey{row.Tenant, row.Name}, row.UpdatedAt)
	// Snapshot what it is publishing NOW, so the next failure has something
	// current to describe. Written only on success: a snapshot taken from a
	// failed handshake would be empty, which is the state this exists to avoid.
	if tools, logos, ok := m.Catalog.SnapshotFor(row.Tenant, row.Name); ok && len(tools) > 0 {
		snap := MCPSnapshot{Tools: tools, Logos: logos}
		row.Snapshot = snap
		_ = m.Store.SetSnapshot(ctx, row.Tenant, row.Name, snap)
	}
	_ = m.Store.SetStatus(ctx, row.Tenant, row.Name, row.ToolCount, "", now)
	return row
}

// describeOffline re-registers a failed server's cached tools as unavailable
// and reports how many it described.
//
// A server that has never connected has no snapshot; there is nothing to
// describe and it is unregistered outright, which is the old behaviour and the
// right one — inventing placeholder steps for a URL that has never answered
// would put fiction in the palette.
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

// descriptor builds the live connection parameters, opening the sealed token.
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
