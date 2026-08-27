// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/engine/webapi"
	"github.com/jackc/pgx/v5/pgxpool"
)

// A web API is a catalog of steps an org describes for its OWN service.
//
// The shape mirrors Admin → MCP servers deliberately (see stepsources.go for
// the rules both obey), but two things are genuinely different and both make
// this the simpler feature:
//
//   - **No credential at rest here.** Auth rides on the synthesized manifest's
//     ConnectionFields, so the token lives in the tenant connection store the
//     Apps page already owns, and the engine injects it into the step's params
//     at run time. Nothing in this file seals, opens, or holds a secret — which
//     is why WebAPI has no token field and needs none of MCPServer's
//     write-only-column care.
//   - **No handshake.** A described API is a document, so registering one
//     performs no I/O and cannot fail for connectivity. There is no
//     LastConnected to report and no reconnect to schedule; "does it work?" is
//     answered by a run, not by a save.

var (
	// ErrWebAPINotFound is returned when no catalog is configured under a name.
	ErrWebAPINotFound = errors.New("web api not found")
	// ErrWebAPIsUnconfigured means this deployment has no store wired.
	ErrWebAPIsUnconfigured = errors.New("web apis are not configured")
)

// WebAPI is one org's described HTTP API.
//
// Every field is safe to log, return to a browser, and put in an audit record —
// not by convention but by construction, since the credential is not part of
// this feature's storage at all.
type WebAPI struct {
	Tenant string
	// Name is what flows reference: an operation from this catalog is the step
	// api:<name>:<operation>. Renaming is therefore not an edit — it is a new
	// catalog, and the old ids stop resolving. The UI says so.
	Name string
	// Label is the display name. Free-form; nothing references it.
	Label        string
	BaseURL      string
	Integration  string
	AuthKind     webapi.AuthKind
	AuthHeader   string
	Operations   []webapi.Operation
	TimeoutMS    int
	MaxBodyBytes int
	// Enabled false keeps the row but takes the steps out of the palette — the
	// reversible half of deleting, for a catalog an org wants to stop calling
	// while it works out why it misbehaved.
	Enabled bool
	// LastError is set when the RECONCILE loop could not register a stored row.
	// Save validates before writing, so a row is always registerable when it is
	// written; this exists for the one case that survives that — validation
	// tightened by a later release, leaving a stored descriptor the current code
	// refuses. Without it the org's steps would vanish with no explanation.
	LastError string
	CreatedBy string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// DisplayName is the label, falling back to the id for a row saved by an API
// caller that supplied no label.
func (w WebAPI) DisplayName() string {
	if strings.TrimSpace(w.Label) != "" {
		return w.Label
	}
	return w.Name
}

// HasAuth reports whether this catalog presents a credential.
func (w WebAPI) HasAuth() bool {
	return w.AuthKind == webapi.AuthBearer || w.AuthKind == webapi.AuthHeader
}

// Descriptor is the engine-facing view of a stored row.
func (w WebAPI) Descriptor() webapi.Descriptor {
	return webapi.Descriptor{
		Tenant:       w.Tenant,
		Name:         w.Name,
		BaseURL:      w.BaseURL,
		Integration:  w.Integration,
		Auth:         webapi.Auth{Kind: w.AuthKind, Header: w.AuthHeader},
		Operations:   w.Operations,
		TimeoutMS:    w.TimeoutMS,
		MaxBodyBytes: w.MaxBodyBytes,
	}
}

// StepIDs lists the step ids this catalog contributes.
func (w WebAPI) StepIDs() []string {
	out := make([]string, 0, len(w.Operations))
	for _, op := range w.Operations {
		out = append(out, webapi.StepID(w.Name, op.ID))
	}
	return out
}

// WebAPIStore persists an org's described APIs.
type WebAPIStore interface {
	List(ctx context.Context, tenant string) ([]WebAPI, error)
	// ListAll spans every tenant, for the reconcile loop that rebuilds the
	// catalog at boot and picks up another replica's edits.
	ListAll(ctx context.Context) ([]WebAPI, error)
	Get(ctx context.Context, tenant, name string) (WebAPI, error)
	Put(ctx context.Context, w WebAPI) error
	Delete(ctx context.Context, tenant, name string) error
	// SetError records why a stored row could not be registered. Deliberately
	// separate from Put: it must not disturb UpdatedAt, which is what every
	// replica's reconcile compares against.
	SetError(ctx context.Context, tenant, name, lastErr string) error
}

const pgWebAPISchema = `
CREATE TABLE IF NOT EXISTS tenant_web_apis (
    tenant         TEXT NOT NULL,
    name           TEXT NOT NULL,
    label          TEXT NOT NULL DEFAULT '',
    base_url       TEXT NOT NULL,
    integration    TEXT NOT NULL DEFAULT '',
    auth_kind      TEXT NOT NULL DEFAULT 'none',
    auth_header    TEXT NOT NULL DEFAULT '',
    -- The described operations, as stored JSON. No credential column: this
    -- feature's secret lives in the tenant connection store (see the package
    -- comment), so there is nothing here to seal.
    operations     JSONB NOT NULL,
    timeout_ms     INTEGER NOT NULL DEFAULT 0,
    max_body_bytes INTEGER NOT NULL DEFAULT 0,
    enabled        BOOLEAN NOT NULL DEFAULT TRUE,
    last_error     TEXT NOT NULL DEFAULT '',
    created_by     TEXT NOT NULL DEFAULT '',
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant, name)
);
-- The reconcile loop reads every enabled row on a timer; without this it is a
-- sequential scan of the table on each pass.
CREATE INDEX IF NOT EXISTS tenant_web_apis_enabled_idx ON tenant_web_apis (enabled);
`

// EnsurePgWebAPISchema creates the web-API table.
func EnsurePgWebAPISchema(ctx context.Context, pool *pgxpool.Pool) error {
	return applyPgSchema(ctx, pool, pgWebAPISchema)
}

// ---- validation -------------------------------------------------------

// maxWebAPIsPerTenant bounds how many catalogs an org may configure.
//
// Cheaper than an MCP server (no connection, no handshake, no goroutine), so
// the number is higher — but not unbounded: every operation is a manifest in
// the map handed to the palette and to the flow generator, and a generator
// prompt is a finite thing.
const maxWebAPIsPerTenant = 100

// maxWebAPILabelLen bounds the display name, as maxMCPServerLabelLen does.
const maxWebAPILabelLen = 96

// maxWebAPIIntegrationLen bounds the Apps-page grouping label.
const maxWebAPIIntegrationLen = 64

// maxWebAPIOperations bounds one catalog.
//
// This is the CURATION guard the design note argues for, and it matters more
// than it looks: an operation is a node in the palette and a manifest in the
// generator's grounding, so a spec import that filed nine hundred of them would
// not "work slowly", it would drown both. A hand-built catalog never comes near
// this; the OpenAPI importer is expected to meet it and must offer selection
// rather than raising it.
const maxWebAPIOperations = 60

// maxWebAPIArgs bounds one operation's arguments. Twelve become ports and the
// rest are params, so this is a bound on the params form — generous, and still
// a bound.
const maxWebAPIArgs = 40

// fallbackWebAPIName is the base used when a label slugs to nothing.
const fallbackWebAPIName = "web-api"

// ---- service ----------------------------------------------------------

// WebAPIs is the service the API talks to. It owns the mapping between stored
// rows and live catalog registrations.
type WebAPIs struct {
	Store   WebAPIStore
	Catalog *webapi.Catalog
	// ReservedIntegration reports that a connection slug is already owned by a
	// built-in integration.
	//
	// This is the check that stops a tenant naming their catalog's integration
	// "Gmail". Connection fields are found by SLUG, over the tenant's whole
	// manifest map, first match wins (connectionFieldsForSlug) — and Go's map
	// order is random, so a colliding integration would make the Apps page for
	// Gmail show that org's web-API fields on some requests and Gmail's on
	// others. Refusing the name is the only fix that is not a coin toss.
	//
	// Follows engine.RemoteCatalog.Reserved: a hook rather than a registry
	// dependency, so the daemon does not have to reach into the drop registry
	// from here. Nil disables the check, which is what a unit test with no
	// registry wants.
	ReservedIntegration func(slug string) bool
	// Now is overridable for tests; nil means time.Now.
	Now func() time.Time

	// mu guards applied.
	mu sync.Mutex
	// applied records the UpdatedAt of the row behind each live registration,
	// so a reconcile pass can skip a row that is already current and re-register
	// one another replica edited.
	applied map[webAPIKey]time.Time
}

type webAPIKey struct {
	tenant string
	name   string
}

func (m *WebAPIs) now() time.Time {
	if m != nil && m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

func (m *WebAPIs) ready() error {
	if m == nil || m.Store == nil || m.Catalog == nil {
		return ErrWebAPIsUnconfigured
	}
	return nil
}

// WebAPIInput is what an admin submits.
type WebAPIInput struct {
	// Label is what the admin typed. On a NEW catalog it is what the id is
	// derived from; on an edit it is the one part of the identity that can still
	// change. Empty on an edit means "keep the stored one".
	Label string
	// Name is the id. Empty on a create means "derive it from Label", which is
	// what the UI sends. On an edit it identifies the row and is never changed.
	Name         string
	BaseURL      string
	Integration  string
	AuthKind     webapi.AuthKind
	AuthHeader   string
	Operations   []webapi.Operation
	TimeoutMS    int
	MaxBodyBytes int
	// Enabled defaults true for a new catalog.
	Enabled bool
}

// List returns an org's catalogs.
func (m *WebAPIs) List(ctx context.Context, tenant string) ([]WebAPI, error) {
	if err := m.ready(); err != nil {
		return nil, err
	}
	return m.Store.List(ctx, tenant)
}

// Save validates, persists, and registers.
//
// Unlike MCPServers.Save there is nothing to connect to, so a successful save
// means the steps are in the palette — full stop. The validation is therefore
// the whole of the feedback an admin gets at save time, which is why it is
// thorough and why every message names a field.
func (m *WebAPIs) Save(ctx context.Context, tenant, actor string, in WebAPIInput) (WebAPI, error) {
	if err := m.ready(); err != nil {
		return WebAPI{}, err
	}
	if tenant == "" {
		return WebAPI{}, fmt.Errorf("web api: tenant required")
	}
	label := strings.TrimSpace(in.Label)
	if len([]rune(label)) > maxWebAPILabelLen {
		return WebAPI{}, fmt.Errorf("name too long (max %d characters)", maxWebAPILabelLen)
	}
	name := strings.ToLower(strings.TrimSpace(in.Name))
	if name == "" {
		if label == "" {
			return WebAPI{}, fmt.Errorf("name is empty")
		}
		derived, err := m.uniqueWebAPIName(ctx, tenant, slugStepSourceName(label))
		if err != nil {
			return WebAPI{}, err
		}
		name = derived
	} else if err := validStepSourceName(name); err != nil {
		return WebAPI{}, err
	}
	return m.save(ctx, tenant, actor, in, name, label)
}

func (m *WebAPIs) save(ctx context.Context, tenant, actor string, in WebAPIInput, name, label string) (WebAPI, error) {
	baseURL := strings.TrimSpace(in.BaseURL)
	// Policy first, so an http:// address is refused with the reason a reader
	// can act on rather than by the engine's laxer assemblability check.
	if err := validStepSourceURL(baseURL); err != nil {
		return WebAPI{}, err
	}
	if len(in.Operations) == 0 {
		return WebAPI{}, fmt.Errorf("add at least one operation — a catalog with none contributes no steps")
	}
	if len(in.Operations) > maxWebAPIOperations {
		return WebAPI{}, fmt.Errorf("%d operations is more than one catalog may hold (max %d) — split it, or select fewer",
			len(in.Operations), maxWebAPIOperations)
	}
	for _, op := range in.Operations {
		if len(op.Args) > maxWebAPIArgs {
			return WebAPI{}, fmt.Errorf("operation %q declares %d arguments (max %d)", op.ID, len(op.Args), maxWebAPIArgs)
		}
	}

	kind := in.AuthKind
	if kind == "" {
		kind = webapi.AuthNone
	}
	header := strings.TrimSpace(in.AuthHeader)
	if kind == webapi.AuthHeader && !validHeaderName(header) {
		// Checked here as well as in the descriptor because the message differs:
		// the engine says "not a usable header name" about an argument, and an
		// admin filling in the auth field needs to be told about THIS field.
		return WebAPI{}, fmt.Errorf("header name may use letters, digits and - only")
	}

	existing, err := m.Store.Get(ctx, tenant, name)
	isNew := errors.Is(err, ErrWebAPINotFound)
	if err != nil && !isNew {
		return WebAPI{}, err
	}
	// Blank keeps the stored label: an API caller changing only the base URL
	// must not erase a display name it never sent.
	if label == "" {
		label = existing.Label
	}

	// The Apps page this catalog is connected on. Resolved AFTER the label above,
	// and only when there is nothing stored to keep — moving it is moving where
	// the org's address and credential are looked up, so a save that did not ask
	// to move it must not. (An edit sending only a new base URL would otherwise
	// re-derive from an empty label, land on the id, and quietly orphan the
	// connection the org had already filled in.) A relabelled catalog keeps its
	// app name for the same reason: renaming what people see costs nothing, and
	// that is only true if it costs nothing here.
	integration := existing.Integration
	if strings.TrimSpace(in.Integration) != "" || integration == "" {
		integration, err = m.resolveIntegration(ctx, tenant, name, label, in.Integration)
		if err != nil {
			return WebAPI{}, err
		}
	}
	if isNew {
		rows, err := m.Store.List(ctx, tenant)
		if err != nil {
			return WebAPI{}, err
		}
		if len(rows) >= maxWebAPIsPerTenant {
			return WebAPI{}, fmt.Errorf("this org already has %d web APIs (the maximum)", maxWebAPIsPerTenant)
		}
	}

	now := m.now()
	row := WebAPI{
		Tenant:       tenant,
		Name:         name,
		Label:        label,
		BaseURL:      baseURL,
		Integration:  integration,
		AuthKind:     kind,
		AuthHeader:   header,
		Operations:   in.Operations,
		TimeoutMS:    in.TimeoutMS,
		MaxBodyBytes: in.MaxBodyBytes,
		Enabled:      in.Enabled,
		CreatedBy:    actor,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if !isNew {
		row.CreatedBy = existing.CreatedBy
		row.CreatedAt = existing.CreatedAt
	}

	// Validated through the ENGINE's own descriptor check, not a copy of it:
	// the rules about placeholders, duplicate arguments and reserved names live
	// where they are enforced, so a row that saves is a row that registers.
	if err := row.Descriptor().Validate(); err != nil {
		return WebAPI{}, err
	}

	if err := m.Store.Put(ctx, row); err != nil {
		return WebAPI{}, err
	}
	if row.Enabled {
		if err := m.register(row); err != nil {
			return WebAPI{}, err
		}
	} else {
		m.Catalog.Unregister(tenant, name)
		m.forget(webAPIKey{tenant, name})
	}
	row.LastError = ""
	return row, nil
}

// register files a row in the live catalog and remembers what was applied.
func (m *WebAPIs) register(row WebAPI) error {
	if err := m.Catalog.Register(row.Descriptor()); err != nil {
		return err
	}
	m.remember(webAPIKey{row.Tenant, row.Name}, row.UpdatedAt)
	return nil
}

// Delete removes a catalog and takes its steps out of the palette.
//
// Flows referencing api:<name>:<operation> do not stop being valid graphs —
// they stop RESOLVING, and a run fails with "no transport registered". That is
// the honest outcome: the org removed the thing the step called.
func (m *WebAPIs) Delete(ctx context.Context, tenant, name string) error {
	if err := m.ready(); err != nil {
		return err
	}
	if err := m.Store.Delete(ctx, tenant, name); err != nil {
		return err
	}
	m.Catalog.Unregister(tenant, name)
	m.forget(webAPIKey{tenant, name})
	return nil
}

// Reconcile makes this process's catalog match the store.
//
// Cheap by design: with no handshake to run, a pass over unchanged rows costs
// one query and a map comparison. A row whose UpdatedAt still matches what this
// replica applied is left alone; an edit made on ANOTHER replica carries a newer
// UpdatedAt and re-registers here.
func (m *WebAPIs) Reconcile(ctx context.Context) error {
	if err := m.ready(); err != nil {
		return err
	}
	rows, err := m.Store.ListAll(ctx)
	if err != nil {
		return err
	}
	desired := make(map[webAPIKey]struct{}, len(rows))
	for _, row := range rows {
		key := webAPIKey{row.Tenant, row.Name}
		if !row.Enabled {
			continue
		}
		desired[key] = struct{}{}
		if at, ok := m.appliedAt(key); ok && at.Equal(row.UpdatedAt) {
			continue
		}
		if err := m.register(row); err != nil {
			// A stored descriptor the current code refuses — see WebAPI.LastError.
			// Recorded rather than logged and forgotten, because the org needs to
			// be able to see why its steps are missing.
			_ = m.Store.SetError(ctx, row.Tenant, row.Name, err.Error())
			continue
		}
		if row.LastError != "" {
			_ = m.Store.SetError(ctx, row.Tenant, row.Name, "")
		}
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

// WebAPIReconcileInterval is how long a change made on another replica may take
// to appear here. Matches MCPReconcileInterval: the two features are edited on
// the same page-flow and a user should not learn two different latencies.
const WebAPIReconcileInterval = 30 * time.Second

// RunReconciler reconciles until ctx ends. Started by cmd/dzd.
func (m *WebAPIs) RunReconciler(ctx context.Context, logf func(string, ...any)) {
	if err := m.ready(); err != nil {
		return
	}
	if err := m.Reconcile(ctx); err != nil && logf != nil {
		logf("web api reconcile: %v", err)
	}
	t := time.NewTicker(WebAPIReconcileInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := m.Reconcile(ctx); err != nil && logf != nil {
				logf("web api reconcile: %v", err)
			}
		}
	}
}

// resolveIntegration settles which Apps page this catalog is connected on.
//
// Stored explicitly rather than left to the engine's name fallback, so what a
// connection attaches to is a value someone can read in the row — and so the
// collision rules below have something to check.
//
// An EXPLICIT name is taken as a statement and refused if it collides. A DERIVED
// one falls back to the catalog's own id, because a duplicate label is legal
// (that is what the numbered ids are for) and refusing the second "Order
// service" would make the id derivation pointless.
func (m *WebAPIs) resolveIntegration(ctx context.Context, tenant, name, label, requested string) (string, error) {
	explicit := strings.TrimSpace(requested)
	candidates := []string{explicit}
	if explicit == "" {
		candidates = []string{label, name}
	}
	var firstErr error
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		if err := m.checkIntegration(ctx, tenant, name, candidate); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		return candidate, nil
	}
	if firstErr != nil {
		return "", firstErr
	}
	return "", fmt.Errorf("app name is empty — it names the page where this API is connected")
}

// checkIntegration applies the three rules an Apps-page name must satisfy.
func (m *WebAPIs) checkIntegration(ctx context.Context, tenant, name, integration string) error {
	if len([]rune(integration)) > maxWebAPIIntegrationLen {
		return fmt.Errorf("app name too long (max %d characters)", maxWebAPIIntegrationLen)
	}
	slug := core.ConnectionSlug(integration)
	// The slug lands inside a secret name (core.ConnectionSecretKey →
	// conn.<slug>.<field>), and that validator allows only [A-Za-z0-9_.-]. A
	// name like "Ordrar!" slugs to "ordrar!" and would be stored happily here,
	// then refused the moment someone tried to CONNECT it — an error about
	// secret names, on a different page, for a field they cannot see. Refuse it
	// where the name is typed. Dots are excluded too, though the validator
	// allows them: conn.<slug>.<field> is read by position, and a dotted slug
	// makes that ambiguous.
	if slug == "" {
		return fmt.Errorf("app name %q has nothing usable in it — it names the page where this API is connected", integration)
	}
	for _, r := range slug {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return fmt.Errorf("app name %q may use only letters, digits, spaces, - and _ — it becomes the key this API's connection is stored under", integration)
		}
	}
	if m.ReservedIntegration != nil && m.ReservedIntegration(slug) {
		return fmt.Errorf("%q is the name of an app Dazyflow already has — pick another, or its connection page would show the wrong fields", integration)
	}
	rows, err := m.Store.List(ctx, tenant)
	if err != nil {
		return err
	}
	for _, r := range rows {
		if r.Name == name {
			continue // this row, being replaced
		}
		if core.ConnectionSlug(r.Integration) == slug {
			return fmt.Errorf("%q already uses that app name — two APIs cannot share one connection", r.DisplayName())
		}
	}
	return nil
}

func (m *WebAPIs) uniqueWebAPIName(ctx context.Context, tenant, base string) (string, error) {
	rows, err := m.Store.List(ctx, tenant)
	if err != nil {
		return "", err
	}
	// Only the org's own names. Unlike an MCP server there is no instance-wide
	// population to collide with, and an `api:` id cannot collide with a native
	// drop by construction.
	taken := make(map[string]bool, len(rows))
	for _, r := range rows {
		taken[r.Name] = true
	}
	return uniqueStepSourceName(base, fallbackWebAPIName, taken, maxWebAPIsPerTenant)
}

func (m *WebAPIs) remember(k webAPIKey, updated time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.applied == nil {
		m.applied = map[webAPIKey]time.Time{}
	}
	m.applied[k] = updated
}

func (m *WebAPIs) forget(k webAPIKey) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.applied, k)
}

func (m *WebAPIs) appliedAt(k webAPIKey) (time.Time, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	at, ok := m.applied[k]
	return at, ok
}

func (m *WebAPIs) appliedKeys() []webAPIKey {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]webAPIKey, 0, len(m.applied))
	for k := range m.applied {
		out = append(out, k)
	}
	return out
}

// marshalOperations is the store's encoding of the described operations. Kept
// here rather than in the pg file because the memory store must agree with it —
// a test that round-trips through memory has to exercise the same encoding a
// deployment does, or a JSON tag mismatch would only ever fail in production.
func marshalOperations(ops []webapi.Operation) ([]byte, error) {
	if ops == nil {
		return []byte(`[]`), nil
	}
	return json.Marshal(ops)
}

func unmarshalOperations(raw []byte) ([]webapi.Operation, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var ops []webapi.Operation
	if err := json.Unmarshal(raw, &ops); err != nil {
		return nil, fmt.Errorf("stored operations are not readable: %w", err)
	}
	return ops, nil
}
