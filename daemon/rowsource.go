// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"net/http"

	"github.com/dazyflow/dazyflow/core"
)

// A "row source" is a node that emits an array of record objects — the
// kind of thing wired into sheets_append_row's `rows` input. Each source
// knows the field names its records carry, so the mapping editor can offer
// them as suggestions instead of making the user guess. The registry below
// is the extensible seam: a new source is one RegisterRowSource call.
//
// This is deliberately decoupled from the drops that define the nodes — the
// extractors read only node params, so daemon needn't import the connector
// packages (same looseness as the OAuth token hooks).

// RowFieldFunc returns the record field names a source node emits. It takes
// a context (and may error) so a source can fetch its fields live — e.g. the
// Google Form source calls the Forms API for the question titles. Failures
// are treated as "no hints" by the caller, so the mapping box degrades to
// free-text rather than erroring.
type RowFieldFunc func(ctx context.Context, node core.Node) ([]string, error)

// rowSource pairs the field extractor with the OUTPUT port that carries the
// record list — so the reference picker can offer "first row" field tokens
// (${upstream.<id>.<port>[0].<field>}) for exactly that port.
type rowSource struct {
	listPort string
	fields   RowFieldFunc
}

var rowSources = map[string]rowSource{}

// RegisterRowSource registers a field extractor for a node module, naming
// the output port that emits the record list. Adding a source is exactly
// this one call plus the extractor — that's the whole extension surface.
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

// SetGoogleFormFieldFetcher installs the live Google Form field resolver.
func SetGoogleFormFieldFetcher(fn func(ctx context.Context, node core.Node) ([]string, error)) {
	googleFormFieldFetcher = fn
}

// googleFormStructuralKeys are the fields every Forms response carries,
// independent of the form's questions — the fallback when a live fetch
// isn't wired or fails.
var googleFormStructuralKeys = []string{"responseId", "submittedTime"}

// sheetsFieldFetcher, when set by cmd/dzd, fetches a Google Sheet's live
// header row so sheets_read_range can act as a row source. Injected for the
// same reason as the Forms fetcher: daemon stays free of connector imports.
var sheetsFieldFetcher func(ctx context.Context, node core.Node) ([]string, error)

// SetSheetsFieldFetcher installs the live Sheets header resolver.
func SetSheetsFieldFetcher(fn func(ctx context.Context, node core.Node) ([]string, error)) {
	sheetsFieldFetcher = fn
}

func init() {
	// Built-in row sources:
	//  - the hosted webhook form, whose fields are its declared form_fields;
	//  - the Google Form trigger, which fetches its question titles live via
	//    the injected fetcher (falling back to the structural keys);
	//  - Gmail search, whose match stubs always carry id + threadId;
	//  - Sheets read range, whose fields are the sheet's live header row.
	RegisterRowSource("webhook_input", "body", func(_ context.Context, n core.Node) ([]string, error) {
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
		// Search expands every match into a real email record — these
		// fields are structurally fixed, no live fetch needed.
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

// listInputFields answers
// GET /api/v1/me/flows/{flow_id}/input-fields?node=ID&port=rows:
// the candidate record fields of whatever node feeds `node`'s `port` input
// (default "rows"), so the mapping editor can suggest them. Returns an empty
// field list (not an error) when nothing is wired in or the producer isn't a
// known row source — the box just stays free-text.
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

// inputFieldsFor finds the node wired into target.<port> and, if its module
// is a registered row source, returns that source's field names.
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
	if manifests, err := h.svc.ListDrops(ctx, p); err == nil {
		if m, ok := manifests[n.Module]; ok && m.Label != "" {
			info.Label = m.Label
		}
	}
	src, ok := rowSources[n.Module]
	if !ok {
		return info, nil
	}
	fields, err := src.fields(ctx, n)
	if err != nil {
		// Hints are best-effort — a failed live fetch leaves the box free-text.
		return info, nil
	}
	return info, fields
}
