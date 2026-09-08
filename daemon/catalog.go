// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

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

type catalogAPI struct {
	auditor
	svc    *Service
	whoami func(rw http.ResponseWriter, r *http.Request, p core.Principal)
	// Mirrors the gateway's opt-out for already-compressed payloads.
	noCompression bool
}

func (h *HTTPGateway) catalogAPI() *catalogAPI {
	return &catalogAPI{auditor: h.auditor(), svc: h.svc, whoami: h.authAPI().whoami, noCompression: h.DisableCompression}
}

//go:embed openapi.yaml
var openapiYAML []byte

// Marshalled once at startup: it is served on every schema request.
var openapiJSON []byte

func init() {
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
	return jsonStringOf(v)
}

func jsonStringOf(v any) string {
	b, _ := json.Marshal(v)
	return strings.Trim(string(b), `"`)
}

// The list shape; the per-integration page carries the rest.
type IntegrationSummary struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	Provider  string `json:"provider,omitempty"`
	Summary   string `json:"summary,omitempty"`
	DropCount int    `json:"drop_count"`
	BrandLogo string `json:"brand_logo,omitempty"`
	Icon      string `json:"icon,omitempty"`
}

type Integration struct {
	IntegrationSummary
	Drops []IntegrationDrop `json:"drops"`
	Links map[string]string `json:"links,omitempty"`
}

type IntegrationDrop struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Role  string `json:"role"`
}

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

type ServiceDescriptor struct {
	Service string `json:"service"`
	Version string `json:"version"`
	Build   struct {
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

func (h *catalogAPI) serviceDescriptor(rw http.ResponseWriter, _ *http.Request) {
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

func (h *catalogAPI) openAPISpec(rw http.ResponseWriter, _ *http.Request) {
	rw.Header().Set("Content-Type", "application/json")
	_, _ = rw.Write(openapiJSON)
}

func (h *catalogAPI) catalogSummary(rw http.ResponseWriter, r *http.Request, p core.Principal) {
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

func (h *catalogAPI) listIntegrationsHandler(rw http.ResponseWriter, r *http.Request, p core.Principal) {
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

func (h *catalogAPI) getIntegrationHandler(rw http.ResponseWriter, r *http.Request, p core.Principal) {
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

func (h *catalogAPI) listDropsHandler(rw http.ResponseWriter, r *http.Request, p core.Principal) {
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
	search.IncludeDisabled = isTruthyQuery(r.URL.Query().Get("include_disabled"))
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
	writeCachedJSON(rw, r, map[string]any{
		"items": results,
		"page":  map[string]any{"next": nil, "size": len(results), "total": len(results)},
	}, !h.noCompression)
}

func (h *catalogAPI) getDropHandler(rw http.ResponseWriter, r *http.Request, p core.Principal) {
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

type integrationGroup struct {
	IntegrationSummary
	Drops          []IntegrationDrop
	dropCategories []string
}

// Curated in the app rather than on the manifest, so it can be translated and
// edited without releasing a drop.
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

func (h *catalogAPI) collectCatalog(ctx context.Context, p core.Principal) (
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
		if g.BrandLogo == "" {
			g.BrandLogo = m.BrandLogo
		}
		if g.Provider == "" {
			g.Provider = m.Provider
		}
		if g.Icon == "" {
			g.Icon = m.Icon
		}
		// An ORG's own integration has no curated blurb: only that org can write it.
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

func (h *catalogAPI) meHandler(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	h.whoami(rw, r, p)
}

func (h *catalogAPI) listMyAPIKeysHandler(rw http.ResponseWriter, r *http.Request, p core.Principal) {
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

func (h *catalogAPI) issueMyAPIKeyHandler(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	params, ok := decodeRequestJSONOptional[SelfIssueAPIKeyParams](rw, r)
	if !ok {
		return
	}
	issued, err := h.svc.IssueOwnAPIKey(r.Context(), p, params)
	if err != nil {
		// A permission overflow is the caller's fault, so 403 rather than 500.
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

func (h *catalogAPI) revokeMyAPIKeyHandler(rw http.ResponseWriter, r *http.Request, p core.Principal) {
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

func (h *catalogAPI) triggerKindsHandler(rw http.ResponseWriter, _ *http.Request, _ core.Principal) {
	writeJSON(rw, http.StatusOK, map[string]any{"kinds": triggerKinds()})
}

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
			"summary": "Deprecated graph-level webhook. Inbound HTTP now lives on steps: Webhook (acknowledge and return), Request (answer the caller with a Reply step), Form (a page Dazyflow hosts). Add the matching trigger node instead.",
			"fields": map[string]any{
				"type": map[string]any{"const": "webhook"},
				"secret": map[string]any{
					"type":        "string",
					"description": "Optional. When set, callers must send `Authorization: Bearer <secret>`. Surfaced to the user in the save response's `endpoints[].auth` field.",
				},
			},
			"examples": []map[string]any{
				{"title": "Secret-protected webhook", "trigger": map[string]any{"type": "webhook", "secret": "${secret.STRIPE_WEBHOOK_SECRET}"}},
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
