// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"

	"github.com/dazyflow/dazyflow/core"
)

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

type referenceGroups struct {
	Secrets   []referenceItem `json:"secrets"`
	Upstream  []referenceItem `json:"upstream"`
	Trigger   []referenceItem `json:"trigger"`
	Resources []referenceItem `json:"resources"`
}

func (h *secretsAPI) listReferences(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	_, _, id, g, ok := h.loadFlowForRequest(rw, r, p, "")
	if !ok {
		return
	}
	node := r.URL.Query().Get("node")

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
func (h *secretsAPI) secretRefs(ctx context.Context, p core.Principal, flow string) []referenceItem {
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

func (h *secretsAPI) resourceRefs(ctx context.Context, p core.Principal, flow string) []referenceItem {
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

func resourceSubpaths(typ string) []string {
	switch typ {
	case "google_sheet":
		return []string{"rows", "headers"}
	default:
		return nil
	}
}

func (h *secretsAPI) upstreamRefs(ctx context.Context, p core.Principal, g core.Graph, node string) []referenceItem {
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
		nodeLabel := n.Module
		if hasManifest && m.Label != "" {
			nodeLabel = m.Label
		}
		if n.Label != "" {
			nodeLabel = n.Label
		}
		ports := m.Outputs
		if len(ports) == 0 {
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
	for _, n := range g.Nodes {
		if n.Module != core.FormInputModule {
			continue
		}
		add(stringSliceParam(n.Params, "form_fields"))
	}
	return fields
}

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
