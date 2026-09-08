// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"sort"
	"strings"

	"github.com/dazyflow/dazyflow/core"
)

type StepSourceUse struct {
	Workspace string   `json:"workspace"`
	FlowID    string   `json:"flow_id"`
	Name      string   `json:"name,omitempty"`
	Steps     []string `json:"steps"`
	Published bool     `json:"published"`
}

type StepSourceUsage struct {
	Flows []StepSourceUse `json:"flows"`
	// Hidden counts flows that use the server but that this principal may not
	// view. They are counted and never named: an admin needs to know the blast
	// radius, which is not a reason to show them a private flow's title.
	Hidden int `json:"hidden"`
}

// InUse reports whether anything at all would break.
func (u StepSourceUsage) InUse() bool { return len(u.Flows) > 0 || u.Hidden > 0 }

func (s *Service) FlowsUsingMCPServer(ctx context.Context, p core.Principal, tenant, name string) (StepSourceUsage, error) {
	return s.flowsUsingStepSource(ctx, p, tenant, "mcp", name)
}

func (s *Service) FlowsUsingWebAPI(ctx context.Context, p core.Principal, tenant, name string) (StepSourceUsage, error) {
	return s.flowsUsingStepSource(ctx, p, tenant, "api", name)
}

// flowsUsingStepSource scans a tenant's workspaces for flows referencing
// <scheme>:<name>:<operation>.
//
// Loading every graph in the org is the honest way to answer this: node module
// ids are inside the graph body, and there is no index of them. It runs once,
// when an admin opens a delete confirmation — not on a hot path — and a
// per-org flow count is small enough that a scan is cheaper than an index that
// could go stale and under-report, which is the one failure mode that matters
// here.
func (s *Service) flowsUsingStepSource(ctx context.Context, p core.Principal, tenant, scheme, name string) (StepSourceUsage, error) {
	usage := StepSourceUsage{Flows: []StepSourceUse{}}
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return usage, nil
	}
	if err := core.RequireTenant(p, tenant); err != nil {
		return usage, err
	}
	if s.Workspaces == nil {
		return usage, nil
	}
	// The trailing colon is what keeps "vendor" from matching "vendor-2": the
	// numbered ids the uniqueness pass hands out are neighbours in the same
	// namespace, and a prefix match without it would warn about the wrong
	// source's flows.
	prefix := scheme + ":" + name + ":"

	workspaces, err := s.Workspaces.List(tenant)
	if err != nil {
		return usage, err
	}
	isAdmin := core.IsFlowAdminPrincipal(p)
	for _, ws := range workspaces {
		store, err := s.Workspaces.Open(tenant, ws)
		if err != nil {
			continue
		}
		ids, err := store.ListGraphs()
		if err != nil {
			continue
		}
		for _, id := range ids {
			g, err := store.Load(id)
			if err != nil {
				// A graph that will not load cannot be shown to use the
				// server — and cannot be shown NOT to. Counting it as hidden
				// errs toward warning rather than toward silence.
				usage.Hidden++
				continue
			}
			steps := stepsIn(g, prefix)
			if len(steps) == 0 {
				continue
			}
			if !isAdmin && core.AuthorizeGraphView(p, g) != nil {
				usage.Hidden++
				continue
			}
			pub, _ := store.PublishedCommit(id)
			usage.Flows = append(usage.Flows, StepSourceUse{
				Workspace: ws,
				FlowID:    id,
				Name:      g.Name,
				Steps:     steps,
				Published: pub != "",
			})
		}
	}
	sort.SliceStable(usage.Flows, func(i, j int) bool {
		a, b := usage.Flows[i], usage.Flows[j]
		if a.Published != b.Published {
			return a.Published
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.FlowID < b.FlowID
	})
	return usage, nil
}

func stepsIn(g core.Graph, prefix string) []string {
	var steps []string
	seen := map[string]bool{}
	for _, n := range g.Nodes {
		if !strings.HasPrefix(n.Module, prefix) || seen[n.Module] {
			continue
		}
		seen[n.Module] = true
		steps = append(steps, n.Module)
	}
	sort.Strings(steps)
	return steps
}
