// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"net/http"

	"github.com/dazyflow/dazyflow/core"
)

type RowFieldFunc func(ctx context.Context, node core.Node) ([]string, error)

type rowSource struct {
	listPort string
	fields   RowFieldFunc
}

var rowSources = map[string]rowSource{}

func RegisterRowSource(module, listPort string, fn RowFieldFunc) {
	rowSources[module] = rowSource{listPort: listPort, fields: fn}
}

// googleFormFieldFetcher, when set by cmd/dzd (which can reach the gform
// drop), fetches a Google Form's live question-title fields. It's injected
// rather than imported so the daemon package stays free of connector
// dependencies — the same looseness the OAuth token + resource hooks use.
// Nil in minimal builds and tests, where the Google Form source falls back
// to the structural keys.
var googleFormFieldFetcher func(ctx context.Context, node core.Node) ([]string, error)

func SetGoogleFormFieldFetcher(fn func(ctx context.Context, node core.Node) ([]string, error)) {
	googleFormFieldFetcher = fn
}

var googleFormStructuralKeys = []string{"responseId", "submittedTime"}

var sheetsFieldFetcher func(ctx context.Context, node core.Node) ([]string, error)

func SetSheetsFieldFetcher(fn func(ctx context.Context, node core.Node) ([]string, error)) {
	sheetsFieldFetcher = fn
}

func init() {
	// Built-in row sources:
	//  - the hosted form, whose fields are its declared form_fields;
	//  - the Google Form trigger, which fetches its question titles live via
	//    the injected fetcher (falling back to the structural keys);
	//  - Gmail search, whose match stubs always carry id + threadId;
	//  - Sheets read range, whose fields are the sheet's live header row.
	RegisterRowSource(core.FormInputModule, "body", func(_ context.Context, n core.Node) ([]string, error) {
		fs := stringSliceParam(n.Params, "form_fields")
		if len(fs) == 0 {
			fs = defaultFormFields
		}
		return fs, nil
	})
	RegisterRowSource("google_form_trigger", "responses", func(ctx context.Context, n core.Node) ([]string, error) {
		if googleFormFieldFetcher != nil {
			if fields, err := googleFormFieldFetcher(ctx, n); err == nil && len(fields) > 0 {
				return fields, nil
			}
			// Live fetch unavailable/failed (no token, form not shared, …):
			// degrade to the structural keys rather than erroring.
		}
		return googleFormStructuralKeys, nil
	})
	RegisterRowSource("gmail_search_messages", "messages", func(_ context.Context, _ core.Node) ([]string, error) {
		return []string{"date", "from", "subject", "body", "id"}, nil
	})
	RegisterRowSource("sheets_read_range", "rows", func(ctx context.Context, n core.Node) ([]string, error) {
		if sheetsFieldFetcher == nil {
			return nil, nil
		}
		return sheetsFieldFetcher(ctx, n)
	})
}

type rowSourceInfo struct {
	NodeID string `json:"node_id"`
	Module string `json:"module"`
	Label  string `json:"label,omitempty"`
}

func (h *flowAPI) listInputFields(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	tenant, workspace, id, ok := readFlowID(rw, r, p)
	if !ok {
		return
	}
	node := r.URL.Query().Get("node")
	port := r.URL.Query().Get("port")
	if port == "" {
		port = "rows"
	}
	g, err := h.svc.LoadGraph(r.Context(), p, tenant, workspace, id, "")
	if err != nil {
		writeAPIError(rw, http.StatusNotFound, "flow_not_found", flowNotFoundMessage(tenant, workspace, id))
		return
	}

	// Tenant + flow ride on ctx so a live source fetch (Google Forms) can
	// resolve the right OAuth account, mirroring how the engine scopes
	// resolution.
	ctx := core.WithFlow(core.WithTenant(r.Context(), p.Tenant), id)
	src, fields := h.inputFieldsFor(ctx, p, g, node, port)
	if fields == nil {
		fields = []string{}
	}
	writeJSON(rw, http.StatusOK, map[string]any{"source": src, "fields": fields})
}

func (h *flowAPI) inputFieldsFor(ctx context.Context, p core.Principal, g core.Graph, target, port string) (*rowSourceInfo, []string) {
	var fromID string
	for _, e := range g.Edges {
		if e.To == target && e.ToPort == port {
			fromID = e.From
			break
		}
	}
	if fromID == "" {
		return nil, nil
	}
	n, ok := g.Node(fromID)
	if !ok {
		return nil, nil
	}
	info := &rowSourceInfo{NodeID: fromID, Module: n.Module, Label: n.Module}
	// One module's label, so one lookup — ListDrops here built the whole
	// tenant-filtered catalog, 182 manifests cloned and walked twice, to read
	// a single string off one of them.
	if m, ok := h.svc.manifestsForModules(p.Tenant, n.Module)[n.Module]; ok && m.Label != "" {
		info.Label = m.Label
	}
	src, ok := rowSources[n.Module]
	if !ok {
		return info, nil
	}
	fields, err := src.fields(ctx, n)
	if err != nil {
		return info, nil
	}
	return info, fields
}
