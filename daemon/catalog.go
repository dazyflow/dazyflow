// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package-internal handlers for the /api/v1/catalog/* and /api/v1
// discovery surface — the self-describing entry points an LLM agent
// (or a curl-wielding human) hits before composing flows. They wrap
// the existing module registry; the spec lives in openapi.yaml and
// is served verbatim (converted to JSON once at startup) from
// /api/v1/openapi.json.

package daemon

import (
	"context"
	_ "embed"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/core/buildinfo"
	yaml "go.yaml.in/yaml/v3"
)

//go:embed openapi.yaml
var openapiYAML []byte

// openapiJSON is the parsed OpenAPI spec, marshalled to JSON once at
// init time so the /api/v1/openapi.json route serves cached bytes
// without re-parsing on every hit. Set at startup; nil indicates the
// embedded spec was unparseable (we panic in init to fail loud).
var openapiJSON []byte

func init() {
	// yaml.v3 decodes into map[string]interface{} keyed by string, but
	// for arbitrary YAML the safest target is `any` and then a
	// recursive shape-fix so json.Marshal accepts it (yaml.v3 produces
	// map[any]any for nested maps in some shapes; we coerce to
	// map[string]any).
	var doc any
	if err := yaml.Unmarshal(openapiYAML, &doc); err != nil {
		panic("openapi.yaml is unparseable: " + err.Error())
	}
	doc = normalizeYAMLForJSON(doc)
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		panic("openapi.yaml → json: " + err.Error())
	}
	openapiJSON = b
}

// normalizeYAMLForJSON walks a YAML-decoded interface and rewrites any
// map[any]any to map[string]any. yaml.v3 prefers map[string]any when
// keys are strings, but older yaml decoders or non-string keys can
// produce map[any]any which json.Marshal refuses with
// "unsupported type". This is defensive — under yaml.v3's default
// behavior with a string-keyed spec, the cast normally isn't needed,
// but the cost of the walk is negligible at startup.
func normalizeYAMLForJSON(v any) any {
	switch x := v.(type) {
	case map[any]any:
		out := make(map[string]any, len(x))
		for k, vv := range x {
			out[toString(k)] = normalizeYAMLForJSON(vv)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, vv := range x {
			out[k] = normalizeYAMLForJSON(vv)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, vv := range x {
			out[i] = normalizeYAMLForJSON(vv)
		}
		return out
	default:
		return v
	}
}

func toString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	// Numeric keys (rare in OpenAPI) get coerced via fmt.
	return jsonStringOf(v)
}

func jsonStringOf(v any) string {
	b, _ := json.Marshal(v)
	return strings.Trim(string(b), `"`)
}

// IntegrationSummary is the wire shape for the integrations list. It
// groups all manifests that share Manifest.Integration into one
// catalog entry — a non-tech owner looking at "Slack" sees one card,
// not six.
type IntegrationSummary struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	Provider  string `json:"provider,omitempty"`
	Summary   string `json:"summary,omitempty"`
	DropCount int    `json:"drop_count"`
	BrandLogo string `json:"brand_logo,omitempty"`
	Icon      string `json:"icon,omitempty"`
}

// Integration is the full per-integration page: who provides it, what
// drops it exposes, how it authenticates. The auth.kind is inferred
// from drop params (oauth providers referenced, secret placeholders)
// rather than carried as a separate manifest field — keeps the
// integration as a derived view rather than a parallel data model.
type Integration struct {
	IntegrationSummary
	Drops []IntegrationDrop `json:"drops"`
	Links map[string]string `json:"links,omitempty"`
}

// IntegrationDrop is the slim drop entry inside an Integration. The
// role is derived from the drop's category: "trigger" → trigger,
// otherwise "action" (or "transformation" if category=transformation).
type IntegrationDrop struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Role  string `json:"role"`
}

// CatalogSummary is the one-page overview. Sized to fit comfortably
// in one LLM context window — a few hundred lines of JSON regardless
// of how many drops are installed.
type CatalogSummary struct {
	Integrations []struct {
		ID    string `json:"id"`
		Label string `json:"label"`
		Drops int    `json:"drops"`
	} `json:"integrations"`
	DropCount  int               `json:"drop_count"`
	Categories []string          `json:"categories"`
	Links      map[string]string `json:"links"`
}

// ServiceDescriptor is the GET /api/v1 response — the single entry
// point. An LLM lands here and follows links to everything else.
type ServiceDescriptor struct {
	Service string `json:"service"`
	// Version is the API contract version (apiVersion). It moves only
	// when the HTTP surface changes shape — distinct from the daemon
	// build below, which advances every release.
	Version string `json:"version"`
	// Build is the running binary's release metadata, stamped at build
	// time (see core/buildinfo). The web UI reads it for its footer and
	// operators can curl GET /api/v1 to confirm which version is live.
	Build struct {
		Version string `json:"version"` // git describe, "dev" if unstamped
		Commit  string `json:"commit"`
		Date    string `json:"date"`
	} `json:"build"`
	Auth struct {
		Scheme  string `json:"scheme"`
		IssueAt string `json:"issue_at"`
	} `json:"auth"`
	Links map[string]string `json:"links"`
}

const (
	apiVersion = "1.0.0"
	apiService = "dazyflow"
)

// serviceDescriptor handles GET /api/v1.
func (h *HTTPGateway) serviceDescriptor(rw http.ResponseWriter, _ *http.Request) {
	d := ServiceDescriptor{
		Service: apiService,
		Version: apiVersion,
	}
	d.Build.Version = buildinfo.Version
	d.Build.Commit = buildinfo.Commit
	d.Build.Date = buildinfo.Date
	d.Auth.Scheme = "Bearer"
	d.Auth.IssueAt = "/api/v1/me/api-keys"
	d.Links = map[string]string{
		"self":         "/api/v1",
		"openapi":      "/api/v1/openapi.json",
		"catalog":      "/api/v1/catalog",
		"integrations": "/api/v1/catalog/integrations",
		"drops":        "/api/v1/catalog/drops",
		"me":           "/api/v1/me",
		"flows":        "/api/v1/me/flows",
		"runs":         "/api/v1/me/runs",
	}
	writeJSON(rw, http.StatusOK, d)
}

// openAPISpec serves the cached JSON form of openapi.yaml. Public —
// the LLM client may not yet have a token when it asks for the spec.
func (h *HTTPGateway) openAPISpec(rw http.ResponseWriter, _ *http.Request) {
	rw.Header().Set("Content-Type", "application/json")
	_, _ = rw.Write(openapiJSON)
}

// catalogSummary handles GET /api/v1/catalog. Returns the one-page
// overview by walking the registry once.
func (h *HTTPGateway) catalogSummary(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	groups, manifests, cats, err := h.collectCatalog(r.Context(), p)
	if err != nil {
		writeAPIError(rw, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	out := CatalogSummary{
		DropCount:  len(manifests),
		Categories: cats,
		Links: map[string]string{
			"integration": "/api/v1/catalog/integrations/{id}",
			"drop":        "/api/v1/catalog/drops/{id}",
		},
	}
	out.Integrations = make([]struct {
		ID    string `json:"id"`
		Label string `json:"label"`
		Drops int    `json:"drops"`
	}, 0, len(groups))
	for _, g := range groups {
		out.Integrations = append(out.Integrations, struct {
			ID    string `json:"id"`
			Label string `json:"label"`
			Drops int    `json:"drops"`
		}{ID: g.ID, Label: g.Label, Drops: g.DropCount})
	}
	writeJSON(rw, http.StatusOK, out)
}

// listIntegrationsHandler handles GET /api/v1/catalog/integrations.
// Filtering supports ?q= (label/summary substring) and
// ?category= (any drop in the group has this category).
func (h *HTTPGateway) listIntegrationsHandler(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	groups, _, _, err := h.collectCatalog(r.Context(), p)
	if err != nil {
		writeAPIError(rw, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	cat := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("category")))
	out := make([]IntegrationSummary, 0, len(groups))
	for _, g := range groups {
		if q != "" {
			hay := strings.ToLower(g.Label + " " + g.Summary)
			if !strings.Contains(hay, q) {
				continue
			}
		}
		if cat != "" {
			ok := false
			for _, c := range g.dropCategories {
				if strings.EqualFold(c, cat) {
					ok = true
					break
				}
			}
			if !ok {
				continue
			}
		}
		out = append(out, g.IntegrationSummary)
	}
	writeJSON(rw, http.StatusOK, map[string]any{
		"items": out,
		"page":  map[string]any{"next": nil, "size": len(out), "total": len(out)},
	})
}

// getIntegrationHandler handles GET /api/v1/catalog/integrations/{id}.
func (h *HTTPGateway) getIntegrationHandler(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	id := r.PathValue("id")
	groups, _, _, err := h.collectCatalog(r.Context(), p)
	if err != nil {
		writeAPIError(rw, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	for _, g := range groups {
		if g.ID == id {
			writeJSON(rw, http.StatusOK, Integration{
				IntegrationSummary: g.IntegrationSummary,
				Drops:              g.Drops,
				Links: map[string]string{
					"self": "/api/v1/catalog/integrations/" + g.ID,
				},
			})
			return
		}
	}
	writeAPIError(rw, http.StatusNotFound, "integration_not_found", "no such integration: "+id)
}

// listDropsHandler handles GET /api/v1/catalog/drops.
func (h *HTTPGateway) listDropsHandler(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	search := DropSearch{
		Query: r.URL.Query().Get("q"),
	}
	if c := r.URL.Query()["category"]; len(c) > 0 {
		search.Categories = c
	}
	if pr := r.URL.Query()["provider"]; len(pr) > 0 {
		search.Providers = pr
	}
	if t := r.URL.Query()["tag"]; len(t) > 0 {
		search.Tags = t
	}
	// The editor opts in to seeing platform-disabled drops (rendered greyed-out
	// and un-pickable) rather than having them vanish from the palette.
	search.IncludeDisabled = isTruthyQuery(r.URL.Query().Get("include_disabled"))
	// Integration filter is not part of DropSearch (legacy), so apply
	// it here as a post-filter.
	integration := strings.TrimSpace(r.URL.Query().Get("integration"))

	results, err := h.svc.SearchDrops(r.Context(), p, search)
	if err != nil {
		writeAPIError(rw, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if integration != "" {
		filtered := results[:0]
		for _, m := range results {
			if strings.EqualFold(m.Integration, integration) {
				filtered = append(filtered, m)
			}
		}
		results = filtered
	}
	writeJSON(rw, http.StatusOK, map[string]any{
		"items": results,
		"page":  map[string]any{"next": nil, "size": len(results), "total": len(results)},
	})
}

// getDropHandler handles GET /api/v1/catalog/drops/{id}.
func (h *HTTPGateway) getDropHandler(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	id := r.PathValue("id")
	manifests, err := h.svc.ListDrops(r.Context(), p)
	if err != nil {
		writeAPIError(rw, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	m, ok := manifests[id]
	if !ok {
		writeAPIError(rw, http.StatusNotFound, "drop_not_found", "no such step: "+id)
		return
	}
	writeJSON(rw, http.StatusOK, m)
}

// integrationGroup is the intermediate shape used while collecting
// drops into integrations. dropCategories is kept separately so the
// listIntegrationsHandler can filter on it without recomputing.
type integrationGroup struct {
	IntegrationSummary
	Drops          []IntegrationDrop
	dropCategories []string
}

// integrationSummaries gives each integration a one-line blurb for the
// catalog list and the per-integration page (IntegrationSummary.Summary),
// which the LLM-facing API surfaces. Keyed by the exact Manifest.Integration
// label the drops set (the same string collectCatalog groups on); the
// synthetic "standard-library" key covers drops with no Integration.
//
// This is the Go/API counterpart to the web's integrationMeta.ts (the richer
// prose the Apps UI renders). The two are deliberately separate — different
// consumers, different surfaces — and kept short here so they don't drift far;
// a missing key just yields an empty Summary, which the API omits.
var integrationSummaries = map[string]string{
	"Stripe":           "Take payments and react to them — create customers, send invoices and payment links, issue refunds, and trigger flows on succeeded or failed charges and canceled subscriptions.",
	"Slack":            "Post messages to your workspace, and trigger flows when your bot is @-mentioned.",
	"GitHub":           "Create and comment on issues, and trigger flows on pushes or new pull requests.",
	"Gmail":            "Send email, search the inbox, and read message bodies — often paired with a polling trigger to react to new mail.",
	"Notion":           "Create pages and query databases.",
	"Fortnox":          "Manage customers and invoices in Fortnox, Sweden's leading SMB accounting platform — create customers and invoices, and poll invoices by status to react to newly paid or overdue ones.",
	"Klarna":           "Manage Klarna orders — look one up, capture it (fully or partially) when the goods ship, and refund it, for the Nordic buy-now-pay-later checkout.",
	"Google Sheets":    "Read rows from a spreadsheet, and append rows to it.",
	"Google Forms":     "Trigger flows when a Google Form receives new responses.",
	"Postgres":         "Insert, upsert, and query rows against a Postgres database.",
	"MySQL":            "Insert, upsert, and query rows against MySQL or MariaDB.",
	"SQLite":           "Insert, upsert, and query rows against a SQLite file in your workspace.",
	"Excel":            "Read .xlsx workbooks into rows, and write rows back out as a new workbook.",
	"Email":            "Send email through an SMTP server you configure.",
	"ntfy":             "Push notifications to your phone via ntfy.sh or a self-hosted server.",
	"HTTP":             "Make HTTP requests to any API that doesn't have a dedicated connector yet.",
	"Claude":           "Run prompts through Claude to summarize, classify, or generate text inside a flow.",
	"Git":              "Clone repositories and check out branches inside your workspace.",
	"Webhook":          "Send a fire-and-forget notification to any URL.",
	"Collections":      "Save rows to a built-in collection with no setup, then query them back — the storage behind the in-app Collections page.",
	"standard-library": "Everything that isn't a particular app: branching, looping, waiting for approval, files, tidying up lists, your own database, and starting a flow on a schedule or an incoming call.",
}

// collectCatalog walks the registry once, groups manifests by
// Manifest.Integration, and returns:
//   - groups sorted by label
//   - the flat manifests map (for the dropCount, categories, etc.)
//   - the unique sorted category list
//
// Drops with empty Integration fall under a synthetic "standard
// library" group so they remain reachable from the integrations
// endpoint instead of disappearing from the catalog.
func (h *HTTPGateway) collectCatalog(ctx context.Context, p core.Principal) (
	groups []integrationGroup,
	manifests map[string]core.Manifest,
	categories []string,
	err error,
) {
	manifests, err = h.svc.ListDrops(ctx, p)
	if err != nil {
		return nil, nil, nil, err
	}
	byInteg := map[string]*integrationGroup{}
	catSet := map[string]struct{}{}
	for _, m := range manifests {
		integID := m.Integration
		integLabel := m.Integration
		if integID == "" {
			integID = "standard-library"
			integLabel = "Standard library"
		}
		g, ok := byInteg[integID]
		if !ok {
			g = &integrationGroup{
				IntegrationSummary: IntegrationSummary{
					ID:        integID,
					Label:     integLabel,
					Provider:  m.Provider,
					BrandLogo: m.BrandLogo,
					Icon:      m.Icon,
					Summary:   integrationSummaries[integID],
				},
			}
			byInteg[integID] = g
		}
		g.DropCount++
		// Use the first non-empty BrandLogo/Provider/Icon we see for the
		// group — manifests in one integration usually agree on these,
		// but if they differ we prefer the first observation rather than
		// overwriting on every iteration.
		if g.BrandLogo == "" {
			g.BrandLogo = m.BrandLogo
		}
		if g.Provider == "" {
			g.Provider = m.Provider
		}
		if g.Icon == "" {
			g.Icon = m.Icon
		}
		// An integration an ORG created has no curated blurb and never will —
		// integrationSummaries is a table in this repo. So a manifest may carry
		// the prose itself, and the first module that does speaks for the group.
		// Curated text still wins: it is translated and edited without a release,
		// which is exactly what a first-party integration wants.
		if g.Summary == "" {
			g.Summary = m.IntegrationDescription
		}
		g.Drops = append(g.Drops, IntegrationDrop{
			ID:    m.ID,
			Label: m.Label,
			Role:  dropRole(m),
		})
		if m.Category != "" {
			g.dropCategories = append(g.dropCategories, m.Category)
			catSet[m.Category] = struct{}{}
		}
	}
	groups = make([]integrationGroup, 0, len(byInteg))
	for _, g := range byInteg {
		// Drops within an integration are sorted alphabetically by label
		// so the LLM sees a stable order; the UI may resort.
		sort.Slice(g.Drops, func(i, j int) bool { return g.Drops[i].Label < g.Drops[j].Label })
		groups = append(groups, *g)
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].Label < groups[j].Label })
	categories = make([]string, 0, len(catSet))
	for c := range catSet {
		categories = append(categories, c)
	}
	sort.Strings(categories)
	return groups, manifests, categories, nil
}

// meHandler is the GET /api/v1/me endpoint — the new canonical name
// for what /api/v1/whoami serves. We delegate to the existing handler
// rather than duplicate the principal-flattening logic; eventually
// /api/v1/whoami will be retired in favor of /me.
func (h *HTTPGateway) meHandler(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	h.whoami(rw, r, p)
}

// listMyAPIKeysHandler is GET /api/v1/me/api-keys — the caller's own
// keys. Today the underlying store doesn't index by subject, so we
// list the tenant and filter; a future indexing improvement on
// AdminKeyStore lands here without changing the wire shape.
func (h *HTTPGateway) listMyAPIKeysHandler(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if h.svc.AdminKeys == nil {
		writeAPIError(rw, http.StatusNotImplemented, "not_configured", "api key admin not configured")
		return
	}
	if p.Tenant == "" {
		writeAPIError(rw, http.StatusBadRequest, "missing_tenant", "principal has no tenant")
		return
	}
	keys, err := h.svc.AdminKeys.ListByTenant(r.Context(), p.Tenant)
	if err != nil {
		writeAPIError(rw, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	mine := make([]APIKeySummary, 0)
	now := time.Now()
	for _, k := range keys {
		if k.Subject != p.Subject {
			continue
		}
		mine = append(mine, redactKey(k, now))
	}
	writeJSON(rw, http.StatusOK, map[string]any{"items": mine})
}

// issueMyAPIKeyHandler is POST /api/v1/me/api-keys — the self-issue
// path used by the Connect MCP modal. No organization:admin required; the
// service caps requested permissions to a subset of the caller's
// own. Returns the secret exactly once.
func (h *HTTPGateway) issueMyAPIKeyHandler(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	params, ok := decodeRequestJSONOptional[SelfIssueAPIKeyParams](rw, r)
	if !ok {
		return
	}
	issued, err := h.svc.IssueOwnAPIKey(r.Context(), p, params)
	if err != nil {
		// Permission overflow is a 403 (caller-attributable). Everything
		// else maps via the legacy adminError helper for now — it still
		// writes the old {"error":"..."} string shape, which the web UI
		// reads. Migrating adminError lives in the gateway-wide rewrite.
		if strings.Contains(err.Error(), "exceeds caller's own permissions") {
			writeAPIError(rw, http.StatusForbidden, "permission_denied", err.Error())
			return
		}
		adminError(rw, err)
		return
	}
	h.audit(r.Context(), p, "apikey.issue.self", p.Subject, "")
	writeJSON(rw, http.StatusCreated, issued)
}

// revokeMyAPIKeyHandler is DELETE /api/v1/me/api-keys/{id} — caller
// revokes their own key. The revoke path on AdminKeys works for any
// key id, so we look up the key first and confirm subject match
// before delegating.
func (h *HTTPGateway) revokeMyAPIKeyHandler(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	id := r.PathValue("id")
	if h.svc.AdminKeys == nil {
		writeAPIError(rw, http.StatusNotImplemented, "not_configured", "api key admin not configured")
		return
	}
	k, err := h.svc.AdminKeys.GetKey(r.Context(), id)
	if err != nil {
		writeAPIError(rw, http.StatusNotFound, "key_not_found", "no such key: "+id)
		return
	}
	if k.Subject != p.Subject {
		// 404 (not 403) — don't acknowledge keys belonging to other users
		// to avoid leaking key-id existence.
		writeAPIError(rw, http.StatusNotFound, "key_not_found", "no such key: "+id)
		return
	}
	if err := h.svc.AdminKeys.Revoke(r.Context(), id, time.Now()); err != nil {
		writeAPIError(rw, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	h.audit(r.Context(), p, "apikey.revoke.self", id, "")
	rw.WriteHeader(http.StatusNoContent)
}

// triggerKindsHandler is GET /api/v1/catalog/trigger-kinds. Returns
// the typed schema for every supported trigger kind — what fields a
// GraphTrigger of that kind accepts, with worked examples. This is
// the LLM's discovery path for "how do I make this run on a schedule
// / accept a webhook / show a hosted form?" without scraping
// hardcoded knowledge from training.
func (h *HTTPGateway) triggerKindsHandler(rw http.ResponseWriter, _ *http.Request, _ core.Principal) {
	writeJSON(rw, http.StatusOK, map[string]any{"kinds": triggerKinds()})
}

// triggerKinds is the source of truth for the trigger catalog. Kept
// inline (rather than reflection over GraphTrigger) so the human-
// readable explanation lives next to the schema and stays in sync.
// When a new GraphTrigger field lands, update both this function and
// core.GraphTrigger in the same change.
func triggerKinds() []map[string]any {
	return []map[string]any{
		{
			"kind":    "cron",
			"summary": "Fire on a schedule using standard 5-field cron syntax.",
			"fields": map[string]any{
				"type": map[string]any{"const": "cron"},
				"cron": map[string]any{
					"type":        "string",
					"description": "Minute hour day month weekday. Validate with the validate_cron tool before saving.",
				},
			},
			"examples": []map[string]any{
				{"title": "Weekdays at 09:00", "trigger": map[string]any{"type": "cron", "cron": "0 9 * * 1-5"}},
				{"title": "Every 15 minutes", "trigger": map[string]any{"type": "cron", "cron": "*/15 * * * *"}},
			},
		},
		{
			"kind":    "webhook",
			"summary": "Accept POSTs from an external system. Optional secret authenticates the caller; optional public_form opts the flow into a hosted intake form.",
			"fields": map[string]any{
				"type": map[string]any{"const": "webhook"},
				"secret": map[string]any{
					"type":        "string",
					"description": "Optional. When set, callers must send `Authorization: Bearer <secret>`. Surfaced to the user in the save response's `endpoints[].auth` field.",
				},
				"public_form": map[string]any{
					"type":        "boolean",
					"description": "When true, the flow ALSO gets a hosted intake form at /form/<tenant>/<workspace>/<id>. The form is public — possession of the URL is the only credential.",
				},
				"form_fields": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"description": "Names of the fields the hosted form renders. Empty defaults to name/email/message. Field names become keys in the JSON delivered to the webhook_input node.",
				},
				"form_title": map[string]any{
					"type":        "string",
					"description": "Heading shown on the hosted form. Empty falls back to the flow's name.",
				},
			},
			"examples": []map[string]any{
				{"title": "Secret-protected webhook", "trigger": map[string]any{"type": "webhook", "secret": "${secret.STRIPE_WEBHOOK_SECRET}"}},
				{"title": "Public contact form", "trigger": map[string]any{"type": "webhook", "public_form": true, "form_fields": []string{"name", "email", "message"}, "form_title": "Get in touch"}},
			},
		},
		{
			"kind":    "poll",
			"summary": "Server-side polling — the scheduler fires the flow every N seconds, like a cron with relative spacing rather than wall-clock alignment.",
			"fields": map[string]any{
				"type":             map[string]any{"const": "poll"},
				"interval_seconds": map[string]any{"type": "integer", "minimum": 1},
			},
			"examples": []map[string]any{
				{"title": "Every 60 seconds", "trigger": map[string]any{"type": "poll", "interval_seconds": 60}},
			},
		},
	}
}

// dropRole maps a drop's category to the "role" the integration page
// surfaces. Triggers are special because they're what an LLM looks
// for first when composing a new flow ("how does this start?");
// transformation drops are the second-class verb; everything else is
// a plain action.
func dropRole(m core.Manifest) string {
	switch m.Category {
	case "trigger":
		return "trigger"
	case "transformation":
		return "transformation"
	default:
		return "action"
	}
}
