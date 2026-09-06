// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"fmt"
	"testing"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/engine"
	"github.com/dazyflow/dazyflow/engine/jobstore"
)

// The sidebar badge counts server-side while the inbox lists. The two must
// answer from the same query, or the badge sends people to a page that
// disagrees with it — including at the 200 ceiling, where the list stops and
// so must the count.
func TestCountPendingApprovalsMatchesTheInbox(t *testing.T) {
	for _, parked := range []int{0, 1, 25, 200, 260} {
		t.Run(fmt.Sprintf("parked=%d", parked), func(t *testing.T) {
			store := jobstore.NewMemory()
			ctx := t.Context()
			for i := range parked {
				runID := fmt.Sprintf("run-%03d", i)
				if err := store.Enqueue(ctx, core.JobRecord{
					ID: NodeJobID(runID, "gate"), Kind: core.JobKindNode,
					GraphRunID: runID, GraphID: "refunds", NodeID: "gate",
					Tenant: "acme", Workspace: "default", Status: core.JobStatusAwaiting,
					Result: &core.Result{Status: core.StatusAwaiting, Output: map[string]core.Ref{
						"pending_url": {Inline: "https://app.example/approve/" + runID + "/gate"},
						"prompt":      {Inline: "Refund?"},
					}},
				}); err != nil {
					t.Fatalf("seed %d: %v", i, err)
				}
			}
			// A subgraph caller is also parked, and must count for neither:
			// it has no pending_url, which is what tells the two apart.
			if err := store.Enqueue(ctx, core.JobRecord{
				ID: NodeJobID("run-sub", "call"), Kind: core.JobKindNode,
				GraphRunID: "run-sub", GraphID: "refunds", NodeID: "call",
				Tenant: "acme", Workspace: "default", Status: core.JobStatusAwaiting,
				Result: &core.Result{Status: core.StatusAwaiting, Output: map[string]core.Ref{
					"pending_child_graph_id": {Inline: "run-child"},
				}},
			}); err != nil {
				t.Fatalf("seed subgraph: %v", err)
			}
			svc := &Service{Jobs: store, Bus: NewMemoryBus(), Engine: &engine.Engine{
				Resolver: &engine.NodeResolver{Native: engine.Default},
			}}

			list, err := svc.ListPendingApprovals(ctx, acmePrincipal, "", "")
			if err != nil {
				t.Fatalf("ListPendingApprovals: %v", err)
			}
			n, err := svc.CountPendingApprovals(ctx, acmePrincipal, "", "")
			if err != nil {
				t.Fatalf("CountPendingApprovals: %v", err)
			}
			if n != len(list) {
				t.Errorf("badge says %d, inbox shows %d", n, len(list))
			}
			// Pin the number itself as well, or the agreement above is
			// satisfied by both reads being broken the same way. 200 is the
			// shared ceiling; the subgraph caller is never counted.
			if want := min(parked, 200); n != want {
				t.Errorf("count = %d, want %d", n, want)
			}
		})
	}
}

// The badge must not count another org's parked approvals.
func TestCountPendingApprovalsIsTenantScoped(t *testing.T) {
	store := jobstore.NewMemory()
	ctx := t.Context()
	for _, tenant := range []string{"acme", "other"} {
		if err := store.Enqueue(ctx, core.JobRecord{
			ID: NodeJobID("run-"+tenant, "gate"), Kind: core.JobKindNode,
			GraphRunID: "run-" + tenant, GraphID: "refunds", NodeID: "gate",
			Tenant: tenant, Workspace: "default", Status: core.JobStatusAwaiting,
			Result: &core.Result{Status: core.StatusAwaiting, Output: map[string]core.Ref{
				"pending_url": {Inline: "https://app.example/approve"},
			}},
		}); err != nil {
			t.Fatalf("seed %s: %v", tenant, err)
		}
	}
	svc := &Service{Jobs: store, Bus: NewMemoryBus(), Engine: &engine.Engine{
		Resolver: &engine.NodeResolver{Native: engine.Default},
	}}
	n, err := svc.CountPendingApprovals(ctx, acmePrincipal, "", "")
	if err != nil {
		t.Fatalf("CountPendingApprovals: %v", err)
	}
	if n != 1 {
		t.Errorf("count = %d, want 1 — the other org's approval leaked in", n)
	}
}
