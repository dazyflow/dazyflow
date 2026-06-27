// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"log"
	"strings"

	"git.sr.ht/~klahr/dazyflow/core"
)

// SystemPrincipal builds the synthetic principal that trigger- and
// system-driven runs act under. Every such site (form, webhook,
// scheduler, timeout, subgraph, and the github/slack/stripe event
// fan-outs) needs the same shape: a single role granting PermGraphRun
// (to fire/cancel runs) plus PermGraphAdmin (to bypass per-flow
// visibility, since possession of the trigger secret or schedule
// already proves authorization). The role name is derived from the
// subject by stripping the "dazyflow-" prefix, matching the historical
// per-site names exactly (e.g. subject "dazyflow-webhook" → role
// "webhook").
func SystemPrincipal(subject, tenant, workspace string) core.Principal {
	return core.Principal{
		Subject:   subject,
		Tenant:    tenant,
		Workspace: workspace,
		Roles: []core.Role{{
			Name:        strings.TrimPrefix(subject, "dazyflow-"),
			Permissions: []core.Permission{core.PermGraphRun, core.PermGraphAdmin},
		}},
	}
}

// fanoutSeed implements the shared trigger fan-out the github, slack,
// and stripe event handlers all need: walk every workspace under the
// tenant, load each graph's published-or-head revision, skip disabled
// flows, seed every node the match predicate selects, and submit a run
// under a system principal. The per-handler logger is threaded through
// so each event source keeps its own log prefix; subject names the
// system principal; seedLabel is the human-readable trigger name used
// in the "fired … (N <label> seed(s))" line.
//
// match decides which nodes in a graph receive the seed — github and
// stripe match purely on module ID; slack additionally checks the
// node's channel filter. Returning true seeds that node.
func fanoutSeed(
	ctx context.Context,
	svc *Service,
	logger *log.Logger,
	subject, tenant, seedLabel string,
	seed core.Result,
	match func(core.Node) bool,
) {
	workspaces, err := svc.Workspaces.List(tenant)
	if err != nil {
		logger.Printf("list workspaces for %s: %v", tenant, err)
		return
	}
	principal := SystemPrincipal(subject, tenant, "")
	for _, ws := range workspaces {
		store, err := svc.Workspaces.Open(tenant, ws)
		if err != nil {
			logger.Printf("open %s/%s: %v", tenant, ws, err)
			continue
		}
		ids, err := store.ListGraphs()
		if err != nil {
			logger.Printf("list graphs %s/%s: %v", tenant, ws, err)
			continue
		}
		principal.Workspace = ws
		for _, id := range ids {
			// Match + run the published revision (HEAD fallback for
			// never-published flows): an external event fires the version
			// that was deliberately published, not a draft.
			g, err := store.LoadPublishedOrHead(id)
			if err != nil {
				logger.Printf("load %s/%s/%s: %v", tenant, ws, id, err)
				continue
			}
			// A paused flow must not fire on inbound events — same rule the
			// /trigger webhook enforces. Skip disabled graphs entirely.
			if g.Disabled {
				continue
			}
			seeds := map[string]core.Result{}
			for _, n := range g.Nodes {
				if match(n) {
					seeds[n.ID] = seed
				}
			}
			if len(seeds) == 0 {
				continue
			}
			runID, err := svc.SubmitGraphWithSeed(ctx, principal, g, seeds)
			if err != nil {
				logger.Printf("submit %s/%s/%s: %v", tenant, ws, id, err)
				continue
			}
			logger.Printf("fired %s/%s/%s → %s (%d %s seed(s))",
				tenant, ws, id, runID, len(seeds), seedLabel)
		}
	}
}
