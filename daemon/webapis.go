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

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/daemon/internal/pgstore"
	"github.com/dazyflow/dazyflow/engine/webapi"
	"github.com/jackc/pgx/v5/pgxpool"
)

// A catalog of steps an org describes for its own service. The credential is
// never a field here: it is stored as a tenant connection, so a WebAPI row can be
// logged and returned to the UI without stripping anything.

var (
	ErrWebAPINotFound      = errors.New("web api not found")
	ErrWebAPIsUnconfigured = errors.New("web apis are not configured")
)

type WebAPILogoMode string

const (
	WebAPILogoAuto   WebAPILogoMode = "auto"
	WebAPILogoCustom WebAPILogoMode = "custom"
	WebAPILogoNone   WebAPILogoMode = "none"
)

type WebAPI struct {
	Tenant       string
	Name         string
	Label        string
	Description  string
	BaseURL      string
	Integration  string
	AuthKind     webapi.AuthKind
	AuthHeader   string
	Operations   []webapi.Operation
	TimeoutMS    int
	MaxBodyBytes int
	Enabled      bool
	// A data: URI only: the app's CSP will not load a third-party image.
	Logo       string
	LogoMode   WebAPILogoMode
	SpecURL    string
	RunnerTags []string
	// Set by the RECONCILE loop, so a stored row that will not register says why.
	LastError string
	CreatedBy string
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (w WebAPI) DisplayName() string {
	if strings.TrimSpace(w.Label) != "" {
		return w.Label
	}
	return w.Name
}

func (w WebAPI) logoMode() WebAPILogoMode {
	if w.LogoMode == "" {
		return WebAPILogoAuto
	}
	return w.LogoMode
}

func (w WebAPI) HasAuth() bool {
	return w.AuthKind == webapi.AuthBearer || w.AuthKind == webapi.AuthHeader
}

func (w WebAPI) Descriptor() webapi.Descriptor {
	return webapi.Descriptor{
		Tenant: w.Tenant,
		Name:   w.Name,
		// The only place the human name reaches the manifest.
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

func (w WebAPI) StepIDs() []string {
	out := make([]string, 0, len(w.Operations))
	for _, op := range w.Operations {
		out = append(out, webapi.StepID(w.Name, op.ID))
	}
	return out
}

type WebAPIStore interface {
	List(ctx context.Context, tenant string) ([]WebAPI, error)
	ListAll(ctx context.Context) ([]WebAPI, error)
	Get(ctx context.Context, tenant, name string) (WebAPI, error)
	Put(ctx context.Context, w WebAPI) error
	Delete(ctx context.Context, tenant, name string) error
	DeleteByTenant(ctx context.Context, tenant string) (int, error)
	AnonymizeSubject(ctx context.Context, ident string) (int, error)
	// Deliberately does not touch the row's own fields.
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

func EnsurePgWebAPISchema(ctx context.Context, pool *pgxpool.Pool) error {
	return pgstore.ApplySchema(ctx, pool, pgWebAPISchema)
}

// Bounds how many catalogs an org may configure.
const maxWebAPIsPerTenant = 100

const maxWebAPILabelLen = 96

const maxWebAPIDescriptionLen = 600

const maxWebAPIIntegrationLen = 64

// Bounds one catalog. Each operation becomes a manifest carried in full by the
// editor's catalog response, so this multiplies by the logo size.
const maxWebAPIOperations = 60

const maxWebAPIOpTitleLen = 96

const maxWebAPIArgs = 40

const fallbackWebAPIName = "web-api"

type WebAPIs struct {
	Store   WebAPIStore
	Catalog *webapi.Catalog
	// A slug collision would let an org's catalog read a built-in integration's
	// stored credential, so the name is refused rather than resolved by precedence.
	ReservedIntegration func(slug string) bool
	Now                 func() time.Time
	ResolveLogo         func(ctx context.Context, baseURL string) string

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

type WebAPIInput struct {
	Label        string
	Name         string
	Description  *string
	BaseURL      string
	Integration  string
	AuthKind     webapi.AuthKind
	AuthHeader   string
	Operations   []webapi.Operation
	TimeoutMS    int
	MaxBodyBytes int
	SpecURL      *string
	// Moves the calls onto the org's own machines, which bypasses the daemon's SSRF
	// guard and egress allowlist. Nil keeps the stored value.
	RunnerTags []string
	Enabled    bool
	LogoMode   *WebAPILogoMode
	Logo       *string
}

func (m *WebAPIs) List(ctx context.Context, tenant string) ([]WebAPI, error) {
	if err := m.ready(); err != nil {
		return nil, err
	}
	return m.Store.List(ctx, tenant)
}

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
		return WebAPI{}, fmt.Errorf("header name may use letters, digits and - only")
	}

	existing, err := m.Store.Get(ctx, tenant, name)
	isNew := errors.Is(err, ErrWebAPINotFound)
	if err != nil && !isNew {
		return WebAPI{}, err
	}
	if label == "" {
		label = existing.Label
	}
	description := existing.Description
	if in.Description != nil {
		description = strings.TrimSpace(*in.Description)
	}
	if len([]rune(description)) > maxWebAPIDescriptionLen {
		return WebAPI{}, fmt.Errorf("description too long (max %d characters) — the operations carry their own prose",
			maxWebAPIDescriptionLen)
	}
	// Resolved beside the label and integration, so one save cannot half-apply.
	runnerTags := existing.RunnerTags
	if in.RunnerTags != nil {
		runnerTags = webapi.NormalizeRunnerTags(in.RunnerTags)
	}
	specURL := existing.SpecURL
	if in.SpecURL != nil {
		specURL = strings.TrimSpace(*in.SpecURL)
	}

	// Resolved AFTER the label, which it can fall back to.
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

func (m *WebAPIs) resolveWebAPILogo(ctx context.Context, in WebAPIInput, existing WebAPI, baseURL string) (string, WebAPILogoMode, error) {
	mode := existing.logoMode()
	switch {
	case in.LogoMode != nil:
		mode = *in.LogoMode
	case in.Logo != nil && strings.TrimSpace(*in.Logo) != "":
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
		normalized, err := webapi.NormalizeLogo(chosen)
		if err != nil {
			return "", "", fmt.Errorf("icon: %w", err)
		}
		return normalized, WebAPILogoCustom, nil

	case WebAPILogoAuto:
		logo := existing.Logo
		if existing.logoMode() != WebAPILogoAuto {
			logo = ""
		}
		// Kept when the address has not moved, so a relabel does not refetch the logo.
		if logo == "" || existing.BaseURL != baseURL {
			logo = m.resolveLogo(ctx, baseURL)
		}
		return logo, WebAPILogoAuto, nil

	default:
		return "", "", fmt.Errorf("unknown icon source %q (want %q, %q or %q)",
			mode, WebAPILogoAuto, WebAPILogoCustom, WebAPILogoNone)
	}
}

func (m *WebAPIs) register(row WebAPI) error {
	if err := m.Catalog.Register(row.Descriptor()); err != nil {
		return err
	}
	m.remember(stepSourceKey{row.Tenant, row.Name}, row.UpdatedAt)
	return nil
}

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

// Keeps LastError in step with the outcome, so the admin page never lies.
func (m *WebAPIs) applyRow(ctx context.Context, row WebAPI) {
	if err := m.register(row); err != nil {
		_ = m.Store.SetError(ctx, row.Tenant, row.Name, err.Error())
		return
	}
	if row.LastError != "" {
		_ = m.Store.SetError(ctx, row.Tenant, row.Name, "")
	}
}

func (m *WebAPIs) RunReconciler(ctx context.Context, logf func(string, ...any)) {
	if err := m.ready(); err != nil {
		return
	}
	runStepSourceReconciler(ctx, "web apis", m.Reconcile, logf)
}

// Settles which Apps page the catalog is connected on.
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

func (m *WebAPIs) checkIntegration(ctx context.Context, tenant, name, integration string) error {
	if len([]rune(integration)) > maxWebAPIIntegrationLen {
		return fmt.Errorf("app name too long (max %d characters)", maxWebAPIIntegrationLen)
	}
	slug := core.ConnectionSlug(integration)
	// The slug lands inside a secret name, so it must survive that validator and
	// must not collide with a built-in's.
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
	// Only the org's own: there is no instance-wide population here.
	taken := make(map[string]bool, len(rows))
	for _, r := range rows {
		taken[r.Name] = true
	}
	return uniqueStepSourceName(base, fallbackWebAPIName, taken, maxWebAPIsPerTenant)
}

// The stored encoding; a field renamed without its tag orphans every row.
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
