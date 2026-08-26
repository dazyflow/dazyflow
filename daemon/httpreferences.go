// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"

	"git.sr.ht/~klahr/dazyflow/core"
)

// referenceItem is one insertable ${…} token the reference picker offers,
// plus the metadata the UI shows beside it. Kind-specific fields are
// omitempty so each group's items stay lean.
type referenceItem struct {
	Token string `json:"token"`           // the literal ${…} to insert
	Label string `json:"label,omitempty"` // human description

	Name      string `json:"name,omitempty"`       // secrets, resources
	Scope     string `json:"scope,omitempty"`      // secrets: flow|tenant
	NodeID    string `json:"node_id,omitempty"`    // upstream
	NodeLabel string `json:"node_label,omitempty"` // upstream
	Port      string `json:"port,omitempty"`       // upstream
	Field     string `json:"field,omitempty"`      // trigger
}

// referenceGroups mirrors the web's parseFieldRefs taxonomy so the picker
// can render one section per kind. resources is populated in Phase 4.
type referenceGroups struct {
	Secrets   []referenceItem `json:"secrets"`
	Upstream  []referenceItem `json:"upstream"`
	Trigger   []referenceItem `json:"trigger"`
	Resources []referenceItem `json:"resources"`
}

// listReferences answers GET /api/v1/me/flows/{flow_id}/references?node=ID:
// everything a param on `node` can reference — secrets, the outputs of
// upstream nodes, the trigger/form fields, and (Phase 4) flow resources.
// Access is gated by LoadGraph (visibility/ownership) exactly like the
// other /me/flows reads.
func (h *HTTPGateway) listReferences(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	_, _, id, g, ok := h.loadFlowForRequest(rw, r, p, "")
	if !ok {
		return
	}
	node := r.URL.Query().Get("node")

	// Tenant + flow ride on ctx so live row-source fetches (Sheets headers,
	// Form questions) can resolve the right OAuth account — same scoping as
	// the input-fields endpoint.
	ctx := core.WithFlow(core.WithTenant(r.Context(), p.Tenant), id)
	groups := referenceGroups{
		Secrets:   h.secretRefs(ctx, p, id),
		Upstream:  h.upstreamRefs(ctx, p, g, node),
		Trigger:   triggerFieldTokens(g),
		Resources: h.resourceRefs(ctx, p, id),
	}
	writeJSON(rw, http.StatusOK, map[string]any{
		"flow":   id,
		"node":   node,
		"groups": groups,
	})
}

// secretRefs lists the flow-scoped then organization-scoped secret names
// as ${secret.NAME} tokens, deduped (a flow secret shadows an org one of
// the same name, but the picker only needs the name once). Returns an
// empty slice when the encrypted store isn't configured.
func (h *HTTPGateway) secretRefs(ctx context.Context, p core.Principal, flow string) []referenceItem {
	out := []referenceItem{}
	if h.EncryptedSecrets == nil || p.Tenant == "" {
		return out
	}
	seen := map[string]bool{}
	add := func(names []string, scope SecretScope) {
		for _, n := range names {
			if seen[n] {
				continue
			}
			seen[n] = true
			out = append(out, referenceItem{
				Token: "${secret." + n + "}",
				Name:  n,
				Scope: string(scope),
			})
		}
	}
	if flow != "" {
		if names, err := h.EncryptedSecrets.ListScoped(ctx, p.Tenant, flow, ScopeFlow); err == nil {
			add(names, ScopeFlow)
		}
	}
	if names, err := h.EncryptedSecrets.ListScoped(ctx, p.Tenant, "", ScopeTenant); err == nil {
		add(names, ScopeTenant)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// resourceRefs lists the flow's configured resources as ${resource.NAME}
// tokens, plus typed sub-paths (a google_sheet offers .rows and .headers),
// deduped flow-then-organization. Empty when the store isn't configured.
func (h *HTTPGateway) resourceRefs(ctx context.Context, p core.Principal, flow string) []referenceItem {
	out := []referenceItem{}
	if h.EncryptedSecrets == nil || p.Tenant == "" {
		return out
	}
	seen := map[string]bool{}
	add := func(scope SecretScope) {
		names, err := h.resourceStorageNames(ctx, p.Tenant, flow, scope)
		if err != nil {
			return
		}
		for resName, storage := range names {
			if seen[resName] {
				continue
			}
			seen[resName] = true
			raw, err := h.EncryptedSecrets.GetExact(ctx, p.Tenant, storage)
			if err != nil {
				continue
			}
			var def core.ResourceDef
			_ = json.Unmarshal([]byte(raw), &def)
			out = append(out, referenceItem{
				Token: "${resource." + resName + "}",
				Name:  resName,
				Label: def.Type,
			})
			for _, sub := range resourceSubpaths(def.Type) {
				out = append(out, referenceItem{
					Token: "${resource." + resName + "." + sub + "}",
					Name:  resName,
					Label: sub,
				})
			}
		}
	}
	if flow != "" {
		add(ScopeFlow)
	}
	add(ScopeTenant)
	sort.Slice(out, func(i, j int) bool { return out[i].Token < out[j].Token })
	return out
}

// resourceSubpaths returns the well-known sub-paths a resource type exposes,
// so the picker can offer e.g. ${resource.leads.rows} directly. One table
// entry per type.
func resourceSubpaths(typ string) []string {
	switch typ {
	case "google_sheet":
		return []string{"rows", "headers"}
	default:
		return nil
	}
}

// upstreamRefs lists ${upstream.<id>.<port>} for every output port of every
// node that can reach `node` (its ancestors) — so a param can only pull
// from a node that may already have run. With no/unknown node it falls back
// to every node in the flow.
func (h *HTTPGateway) upstreamRefs(ctx context.Context, p core.Principal, g core.Graph, node string) []referenceItem {
	out := []referenceItem{}
	scope := g
	if node != "" {
		if sub, ok := g.UpstreamSubset(node); ok {
			scope = sub
		}
	}
	manifests, err := h.svc.ListDrops(ctx, p)
	if err != nil {
		manifests = nil // degrade: still list nodes, just without port labels
	}
	for _, n := range scope.Nodes {
		if n.ID == node {
			continue // a node can't reference its own (not-yet-produced) output
		}
		m, hasManifest := manifests[n.Module]
		// What to call the step in the picker: the author's own name when they
		// gave it one, else the drop's. A reference reads as "<step> · <port>",
		// and naming the step differently here to the way it is named on the
		// canvas is how you end up hunting for a step that is right in front of
		// you.
		nodeLabel := n.Module
		if hasManifest && m.Label != "" {
			nodeLabel = m.Label
		}
		if n.Label != "" {
			nodeLabel = n.Label
		}
		ports := m.Outputs
		if len(ports) == 0 {
			// No declared outputs (or no manifest) — offer the node itself
			// so the user can still hand-complete the path.
			out = append(out, referenceItem{
				Token:     "${upstream." + n.ID + "}",
				NodeID:    n.ID,
				NodeLabel: nodeLabel,
			})
			continue
		}
		for _, port := range ports {
			out = append(out, referenceItem{
				Token:     "${upstream." + n.ID + "." + port.Port + "}",
				Label:     port.Label,
				NodeID:    n.ID,
				NodeLabel: nodeLabel,
				Port:      port.Port,
			})
			// When this port carries a record LIST (the node is a registered
			// row source), also offer the first row's fields as ready-made
			// tokens — e.g. "Matching emails → first → id" inserting
			// ${upstream.search.messages[0].id}. Spares non-techies from
			// hand-typing the [0].field indexing syntax. Best-effort: a
			// failed live fetch (Sheets headers etc.) just adds nothing.
			if src, isSource := rowSources[n.Module]; isSource && port.Port == src.listPort {
				fields, ferr := src.fields(ctx, n)
				if ferr != nil {
					continue
				}
				portName := port.Label
				if portName == "" {
					portName = port.Port
				}
				for _, f := range fields {
					out = append(out, referenceItem{
						Token:     "${upstream." + n.ID + "." + port.Port + "[0]." + f + "}",
						Label:     portName + " → first → " + f,
						NodeID:    n.ID,
						NodeLabel: nodeLabel,
						Port:      port.Port,
					})
				}
			}
		}
	}
	return out
}

// triggerFieldTokens lists ${trigger.body.<field>} for a flow whose trigger
// seeds the run from a hosted form — the fields the form renders. Other
// triggers (cron/poll) seed nothing; the google_form_trigger surfaces its
// responses through the upstream group like any other node. Best-effort:
// new trigger kinds extend this one function.
func triggerFieldTokens(g core.Graph) []referenceItem {
	fields := hostedFormFields(g)
	out := make([]referenceItem, 0, len(fields))
	for _, f := range fields {
		out = append(out, referenceItem{
			Token: "${trigger.body." + f + "}",
			Field: f,
		})
	}
	return out
}

// hostedFormFields collects the form field names a flow's hosted form
// exposes, from a public_form graph trigger or a webhook_input node with
// public_form set, deduped. Empty when the flow has no hosted form.
func hostedFormFields(g core.Graph) []string {
	seen := map[string]bool{}
	var fields []string
	add := func(fs []string) {
		if len(fs) == 0 {
			fs = defaultFormFields
		}
		for _, f := range fs {
			if f == "" || seen[f] {
				continue
			}
			seen[f] = true
			fields = append(fields, f)
		}
	}
	for _, t := range g.Triggers {
		if t.PublicForm {
			add(t.FormFields)
		}
	}
	for _, n := range g.Nodes {
		if n.Module != "webhook_input" {
			continue
		}
		if pf, _ := n.Params["public_form"].(bool); !pf {
			continue
		}
		add(stringSliceParam(n.Params, "form_fields"))
	}
	return fields
}

// stringSliceParam reads a []string-ish node param (JSON decodes a string
// array as []any of string).
func stringSliceParam(p map[string]any, key string) []string {
	raw, ok := p[key]
	if !ok {
		return nil
	}
	switch v := raw.(type) {
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}
