// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/daemon/internal/pgstore"
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

// WebAPILogoMode says where a catalog's brand mark comes from.
//
// Three modes rather than one nullable image, because "no mark" has two
// different meanings and a store that conflated them would fight the admin. A
// guess that found nothing must be retried — that is what makes pressing Save
// the retry. A globe the admin CHOSE must never be retried, or the wrong logo
// comes back on every save. And an uploaded mark must survive a change of
// address, which is exactly when a guess must not.
type WebAPILogoMode string

const (
	// WebAPILogoAuto takes the mark from the service's favicon, and is the
	// default for a catalog that has never said otherwise.
	WebAPILogoAuto WebAPILogoMode = "auto"
	// WebAPILogoCustom uses the image an admin chose. The resolver never runs.
	WebAPILogoCustom WebAPILogoMode = "custom"
	// WebAPILogoNone is the plain glyph, on purpose. Also the answer when the
	// guess landed on a shared platform's logo and the org has nothing of its
	// own to upload.
	WebAPILogoNone WebAPILogoMode = "none"
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
	Label string
	// Description is what the service IS, in the org's own words. It is the
	// prose the Apps page shows under the app's name, and the only description
	// of an org's own API that nobody else can write — a built-in integration's
	// blurb is curated in the app and there is nowhere to curate an org's.
	Description  string
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
	// Logo is the catalog's brand mark as a data: URI — guessed from the
	// service's favicon (engine/webapi/icon.go) or chosen by an admin, per
	// LogoMode — and then STORED.
	//
	// Stored rather than resolved on demand for two reasons. The reconcile loop
	// runs on every replica every 30s and must stay a query and a map compare,
	// not a fan-out of favicon requests; and a logo that changed on every pass
	// would be a manifest that changed on every pass. Empty means the globe,
	// which is the common case for an internal service.
	Logo string
	// LogoMode is where Logo came from. Empty reads as WebAPILogoAuto, which is
	// what every row stored before this field existed means.
	LogoMode WebAPILogoMode
	// SpecURL is where this catalog's operations were imported from, remembered
	// so a refresh can re-fetch without the admin finding the address again.
	// Empty for a hand-built catalog, which is not a lesser thing: the two front
	// ends produce the same descriptor and only this field tells them apart.
	SpecURL string
	// RunnerTags, when non-empty, moves this catalog's calls onto one of the
	// org's own machines — the only way to reach a service with no public
	// address, since the daemon refuses to dial private ranges. A machine
	// carrying ALL of these tags runs the request; see webapi.RunnerReach for
	// what the choice costs.
	RunnerTags []string
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

// logoMode is LogoMode with the stored default filled in.
func (w WebAPI) logoMode() WebAPILogoMode {
	if w.LogoMode == "" {
		return WebAPILogoAuto
	}
	return w.LogoMode
}

// HasAuth reports whether this catalog presents a credential.
func (w WebAPI) HasAuth() bool {
	return w.AuthKind == webapi.AuthBearer || w.AuthKind == webapi.AuthHeader
}

// Descriptor is the engine-facing view of a stored row.
func (w WebAPI) Descriptor() webapi.Descriptor {
	return webapi.Descriptor{
		Tenant: w.Tenant,
		Name:   w.Name,
		// The human name reaches the manifest here, and only here. Without it
		// every step this catalog contributes is captioned by its slug —
		// "order-service — get_order" where the admin typed "Order service".
		Label:        w.DisplayName(),
		Description:  w.Description,
		BaseURL:      w.BaseURL,
		Integration:  w.Integration,
		Auth:         webapi.Auth{Kind: w.AuthKind, Header: w.AuthHeader},
		Operations:   w.Operations,
		TimeoutMS:    w.TimeoutMS,
		MaxBodyBytes: w.MaxBodyBytes,
		Logo:         w.Logo,
		Runner:       webapi.RunnerReach{Tags: w.RunnerTags},
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
	// DeleteByTenant removes every catalog a tenant configured, returning the
	// count. The erasure-cascade entry point (GDPR Art. 17): erasure cannot go
	// name-by-name, because it does not know what an org configured over its
	// lifetime.
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
    description    TEXT NOT NULL DEFAULT '',
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
-- Added after the table shipped: the service's favicon, inlined as a data: URI.
-- A row predating this simply has no logo and picks one up on its next save.
ALTER TABLE tenant_web_apis ADD COLUMN IF NOT EXISTS logo TEXT NOT NULL DEFAULT '';
-- Added after the table shipped: the org's own blurb about the service, shown
-- on its page under Apps. Empty is normal — the page renders without it.
ALTER TABLE tenant_web_apis ADD COLUMN IF NOT EXISTS description TEXT NOT NULL DEFAULT '';
-- Where that logo came from. 'auto' is the default because it is what every row
-- written before the column existed did: took the guess, or took nothing.
ALTER TABLE tenant_web_apis ADD COLUMN IF NOT EXISTS logo_mode TEXT NOT NULL DEFAULT 'auto';
-- The reconcile loop reads every enabled row on a timer; without this it is a
-- sequential scan of the table on each pass.
-- Added after the table shipped: reach this catalog through a runner carrying
-- all of these tags, instead of dialling it from the daemon. Empty — the
-- default, and what every row written before this column means — is the direct
-- call the table has always described.
ALTER TABLE tenant_web_apis ADD COLUMN IF NOT EXISTS runner_tags TEXT[] NOT NULL DEFAULT '{}';
-- Added after the table shipped: where an imported catalog's spec came from, so
-- a refresh can re-fetch it. Empty means hand-built (or imported by paste), and
-- is what every row written before this column means.
ALTER TABLE tenant_web_apis ADD COLUMN IF NOT EXISTS spec_url TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS tenant_web_apis_enabled_idx ON tenant_web_apis (enabled);
`

// EnsurePgWebAPISchema creates the web-API table.
func EnsurePgWebAPISchema(ctx context.Context, pool *pgxpool.Pool) error {
	return pgstore.ApplySchema(ctx, pool, pgWebAPISchema)
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

// maxWebAPIDescriptionLen bounds the org's blurb about its service.
//
// Room for a real paragraph, because the Apps page renders it as one and the
// reader is deciding whether this is the app they want. Not room for a manual:
// the operations carry their own prose, and a page-long intro would push the
// connection form — the thing someone came here to fill in — off the screen.
const maxWebAPIDescriptionLen = 600

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

// maxWebAPIOpTitleLen bounds an operation's display name. It captions a palette
// row; the summary is where a sentence belongs, and that is a separate field.
const maxWebAPIOpTitleLen = 96

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
	// ResolveLogo guesses a catalog's brand mark from its base URL, returning a
	// data: URI or "". Nil means webapi.ResolveLogo, which is what production
	// wants; a test injects one to keep saves deterministic and off the network.
	ResolveLogo func(ctx context.Context, baseURL string) string

	// stepSourceRegistry records which rows this process currently has
	// registered, keyed by (tenant, name) and valued by the row's UpdatedAt.
	// See stepsources_live.go: the same bookkeeping serves MCP servers.
	stepSourceRegistry
}

func (m *WebAPIs) now() time.Time {
	if m == nil {
		return time.Now()
	}
	return nowOr(m.Now)
}

func (m *WebAPIs) resolveLogo(ctx context.Context, baseURL string) string {
	if m != nil && m.ResolveLogo != nil {
		return m.ResolveLogo(ctx, baseURL)
	}
	return webapi.ResolveLogo(ctx, baseURL)
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
	Name string
	// Description is the org's blurb about the service. A pointer, so an API
	// caller that sends only a new base URL does not erase prose it never sent —
	// the same care Label gets, by a different mechanism because blank is a
	// legitimate value here (an org clearing the paragraph) rather than a
	// fallback.
	Description  *string
	BaseURL      string
	Integration  string
	AuthKind     webapi.AuthKind
	AuthHeader   string
	Operations   []webapi.Operation
	TimeoutMS    int
	MaxBodyBytes int
	// SpecURL is where the operations were imported from. A pointer for the
	// usual reason: omitted means "keep what is stored", so an edit of anything
	// else does not make a catalog forget where it came from.
	SpecURL *string
	// RunnerTags moves this catalog's calls onto the org's own machines. Nil
	// means "not sent" and keeps whatever is stored — the same protection Label
	// and Description get, and for the same reason: an API caller changing only
	// the base URL must not silently move a catalog back onto the direct path.
	// An explicitly empty (non-nil) slice is how you turn it off.
	RunnerTags []string
	// Enabled defaults true for a new catalog.
	Enabled bool
	// LogoMode chooses where the brand mark comes from. Nil keeps the stored
	// choice: an API caller editing an operation must not silently move a
	// catalog back to guessing.
	LogoMode *WebAPILogoMode
	// Logo is the mark itself, a data: URI, for WebAPILogoCustom. Nil keeps the
	// stored image, so an edit that touched something else cannot blank it.
	//
	// Sending an image and no mode means "use this": see resolveWebAPILogo.
	Logo *string
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
		if len([]rune(strings.TrimSpace(op.Title))) > maxWebAPIOpTitleLen {
			return WebAPI{}, fmt.Errorf("operation %q has a name longer than %d characters — put the sentence in its summary instead",
				op.ID, maxWebAPIOpTitleLen)
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
	// The blurb gets the same protection by a different mechanism: blank is a
	// legitimate value here (an org clearing the paragraph), so "not sent" has to
	// be a nil pointer rather than an empty string.
	description := existing.Description
	if in.Description != nil {
		description = strings.TrimSpace(*in.Description)
	}
	if len([]rune(description)) > maxWebAPIDescriptionLen {
		return WebAPI{}, fmt.Errorf("description too long (max %d characters) — the operations carry their own prose",
			maxWebAPIDescriptionLen)
	}
	// Where this catalog's calls are made from, resolved beside the other two
	// keep-what-is-stored fields and for the same reason: nil means "not sent",
	// so an API caller changing only the base URL cannot silently move a
	// catalog off its runner and onto a direct call the network will refuse.
	// An explicitly empty (non-nil) slice is how a caller turns it off.
	//
	// Normalised here rather than at the edge so every writer — the admin page
	// and any API caller — gets the lower-cased, de-duplicated tags a machine
	// actually matches on.
	runnerTags := existing.RunnerTags
	if in.RunnerTags != nil {
		runnerTags = webapi.NormalizeRunnerTags(in.RunnerTags)
	}
	specURL := existing.SpecURL
	if in.SpecURL != nil {
		specURL = strings.TrimSpace(*in.SpecURL)
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

	logo, logoMode, err := m.resolveWebAPILogo(ctx, in, existing, baseURL)
	if err != nil {
		return WebAPI{}, err
	}

	now := m.now()
	row := WebAPI{
		Tenant:       tenant,
		Name:         name,
		Label:        label,
		Description:  description,
		BaseURL:      baseURL,
		Integration:  integration,
		AuthKind:     kind,
		AuthHeader:   header,
		Operations:   in.Operations,
		TimeoutMS:    in.TimeoutMS,
		MaxBodyBytes: in.MaxBodyBytes,
		Enabled:      in.Enabled,
		Logo:         logo,
		LogoMode:     logoMode,
		RunnerTags:   runnerTags,
		SpecURL:      specURL,
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
		m.forget(stepSourceKey{tenant, name})
	}
	row.LastError = ""
	return row, nil
}

// resolveWebAPILogo settles the catalog's brand mark: which of the three
// sources it comes from, and the image itself.
//
// This is the only place in the feature that reaches the network — see the
// package comment, which promised a save performs no I/O and now promises
// slightly less: a save performs no I/O it can FAIL on.
func (m *WebAPIs) resolveWebAPILogo(ctx context.Context, in WebAPIInput, existing WebAPI, baseURL string) (string, WebAPILogoMode, error) {
	mode := existing.logoMode()
	switch {
	case in.LogoMode != nil:
		mode = *in.LogoMode
	case in.Logo != nil && strings.TrimSpace(*in.Logo) != "":
		// An image and no mode is a statement: use this. It saves an API caller
		// from a save that accepted the icon it uploaded and then ignored it,
		// which is the failure this branch exists to prevent.
		mode = WebAPILogoCustom
	}

	switch mode {
	case WebAPILogoNone:
		return "", WebAPILogoNone, nil

	case WebAPILogoCustom:
		chosen := existing.Logo
		if in.Logo != nil {
			chosen = strings.TrimSpace(*in.Logo)
		}
		if chosen == "" {
			return "", "", fmt.Errorf("choose an image for the icon, or let it be taken from the service")
		}
		// Validated through the engine's own normaliser, which re-encodes from
		// bytes it decoded itself: an admin is a likelier source of a mis-typed
		// or oversized image than a favicon host is, and a stored logo is
		// something every viewer of the flow then renders.
		normalized, err := webapi.NormalizeLogo(chosen)
		if err != nil {
			return "", "", fmt.Errorf("icon: %w", err)
		}
		return normalized, WebAPILogoCustom, nil

	case WebAPILogoAuto:
		logo := existing.Logo
		if existing.logoMode() != WebAPILogoAuto {
			// Coming BACK to automatic. The stored image is the admin's old
			// upload rather than a guess, so it is not something to keep — the
			// point of choosing automatic is to see what the service publishes.
			logo = ""
		}
		// Kept when the address has not moved, so an ordinary edit (a relabel,
		// one more operation, a timeout bump) costs no outbound requests and
		// cannot be delayed by someone else's web server. A catalog with no
		// logo to keep does try again, which is deliberate: it makes "press
		// Save" the retry for a service whose site was down the first time, and
		// there is nothing else on this page that could be.
		if logo == "" || existing.BaseURL != baseURL {
			logo = m.resolveLogo(ctx, baseURL)
		}
		return logo, WebAPILogoAuto, nil

	default:
		return "", "", fmt.Errorf("unknown icon source %q (want %q, %q or %q)",
			mode, WebAPILogoAuto, WebAPILogoCustom, WebAPILogoNone)
	}
}

// register files a row in the live catalog and remembers what was applied.
func (m *WebAPIs) register(row WebAPI) error {
	if err := m.Catalog.Register(row.Descriptor()); err != nil {
		return err
	}
	m.remember(stepSourceKey{row.Tenant, row.Name}, row.UpdatedAt)
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
	m.forget(stepSourceKey{tenant, name})
	return nil
}

// DeleteByTenant removes every web-API catalog an org configured and takes each
// one out of the live palette, returning the count. The erasure cascade's hook
// (GDPR Art. 17); see deleteOrgData in gdpr.go.
//
// Lists then deletes, for the reason MCPServers.DeleteByTenant does: a row
// dropped straight from under the catalog leaves this process still serving the
// org's steps until a restart.
func (m *WebAPIs) DeleteByTenant(ctx context.Context, tenant string) (int, error) {
	if err := m.ready(); err != nil {
		return 0, err
	}
	apis, err := m.Store.List(ctx, tenant)
	if err != nil {
		return 0, fmt.Errorf("list web apis for %q: %w", tenant, err)
	}
	for _, a := range apis {
		m.Catalog.Unregister(tenant, a.Name)
		m.forget(stepSourceKey{tenant, a.Name})
	}
	n, err := m.Store.DeleteByTenant(ctx, tenant)
	if err != nil {
		return 0, fmt.Errorf("delete web apis for %q: %w", tenant, err)
	}
	return n, nil
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
	reconcileStepSources(ctx, &m.stepSourceRegistry, rows, stepSourcePlan[WebAPI]{
		key:        func(r WebAPI) stepSourceKey { return stepSourceKey{r.Tenant, r.Name} },
		enabled:    func(r WebAPI) bool { return r.Enabled },
		updatedAt:  func(r WebAPI) time.Time { return r.UpdatedAt },
		apply:      m.applyRow,
		unregister: m.Catalog.Unregister,
	})
	return nil
}

// applyRow registers one stored row and keeps LastError in step with the
// outcome.
//
// A stored descriptor the current code refuses — see WebAPI.LastError — is
// RECORDED rather than logged and forgotten, because the org needs to be able
// to see why its steps are missing. A row that registers cleanly has any stale
// error cleared.
func (m *WebAPIs) applyRow(ctx context.Context, row WebAPI) {
	if err := m.register(row); err != nil {
		_ = m.Store.SetError(ctx, row.Tenant, row.Name, err.Error())
		return
	}
	if row.LastError != "" {
		_ = m.Store.SetError(ctx, row.Tenant, row.Name, "")
	}
}

// RunReconciler reconciles until ctx ends. Started by cmd/dzd.
func (m *WebAPIs) RunReconciler(ctx context.Context, logf func(string, ...any)) {
	if err := m.ready(); err != nil {
		return
	}
	runStepSourceReconciler(ctx, "web apis", m.Reconcile, logf)
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
