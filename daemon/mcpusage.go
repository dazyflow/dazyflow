// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"sort"
	"strings"

	"git.sr.ht/~klahr/dazyflow/core"
)

// Who is using an MCP server, for the admin about to delete one.
//
// Deleting is allowed and stays allowed — the same bargain deleting a runner
// makes. What was missing is the fact an admin needs to make that call: the
// page warned that flows "will stop running" whether or not any flow used the
// server, which is a warning nobody can act on. This answers the actual
// question.

// MCPServerUse is one flow that references a server's steps.
type MCPServerUse struct {
	Workspace string `json:"workspace"`
	FlowID    string `json:"flow_id"`
	// Name is the flow's title when it has one; the UI falls back to FlowID.
	Name string `json:"name,omitempty"`
	// Steps are the ids of the referencing nodes' modules, deduplicated —
	// "mcp:mcp-test:search". Which TOOL is used is what tells an admin whether
	// the flow is doing something they can replace.
	Steps []string `json:"steps"`
	// Published matters more than the rest: an unpublished draft breaking is
	// an inconvenience, a published flow breaking is an outage.
	Published bool `json:"published"`
}

// MCPServerUsage is the whole answer for one server.
type MCPServerUsage struct {
	Flows []MCPServerUse `json:"flows"`
	// Hidden counts flows that use the server but that this principal may not
	// view. They are counted and never named: an admin needs to know the blast
	// radius, which is not a reason to show them a private flow's title.
	Hidden int `json:"hidden"`
}

// InUse reports whether anything at all would break.
func (u MCPServerUsage) InUse() bool { return len(u.Flows) > 0 || u.Hidden > 0 }

// FlowsUsingMCPServer scans a tenant's workspaces for flows referencing
// mcp:<name>:<tool>.
//
// Loading every graph in the org is the honest way to answer this: node module
// ids are inside the graph body, and there is no index of them. It runs once,
// when an admin opens a delete confirmation — not on a hot path — and a
// per-org flow count is small enough that a scan is cheaper than an index that
// could go stale and under-report, which is the one failure mode that matters
// here.
func (s *Service) FlowsUsingMCPServer(ctx context.Context, p core.Principal, tenant, name string) (MCPServerUsage, error) {
	usage := MCPServerUsage{Flows: []MCPServerUse{}}
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
	prefix := "mcp:" + name + ":"

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
			steps := mcpStepsIn(g, prefix)
			if len(steps) == 0 {
				continue
			}
			if !isAdmin && core.AuthorizeGraphView(p, g) != nil {
				usage.Hidden++
				continue
			}
			pub, _ := store.PublishedCommit(id)
			usage.Flows = append(usage.Flows, MCPServerUse{
				Workspace: ws,
				FlowID:    id,
				Name:      g.Name,
				Steps:     steps,
				Published: pub != "",
			})
		}
	}
	// Published first, then by name: the flows whose breakage an admin most
	// needs to weigh are the ones at the top of the list.
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

// mcpStepsIn returns the distinct module ids in g that belong to the server,
// sorted. Empty when the flow does not touch it.
func mcpStepsIn(g core.Graph, prefix string) []string {
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
