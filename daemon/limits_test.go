package daemon_test

import (
	"context"
	"errors"
	"testing"

	"git.sr.ht/~klahr/hazy-flow/core"
)

func TestSubmitGraph_RejectsTooManyNodes(t *testing.T) {
	h := newVisibilityHarness(t)
	h.svc.MaxGraphNodes = 2
	ctx := context.Background()

	tooBig := core.Graph{
		ID: "big", Tenant: "t", Workspace: "ws", Visibility: core.VisibilityOrg,
		Nodes: []core.Node{
			{ID: "a", Module: "noop"},
			{ID: "b", Module: "noop"},
			{ID: "c", Module: "noop"},
		},
	}
	if _, err := h.svc.SubmitGraph(ctx, h.alice, tooBig); !errors.Is(err, core.ErrGraphTooLarge) {
		t.Fatalf("3-node submit under limit 2: err = %v, want ErrGraphTooLarge", err)
	}

	// At the limit, the node-count guard must not be what stops it.
	atLimit := core.Graph{
		ID: "ok", Tenant: "t", Workspace: "ws", Visibility: core.VisibilityOrg,
		Nodes: []core.Node{
			{ID: "a", Module: "noop"},
			{ID: "b", Module: "noop"},
		},
	}
	if _, err := h.svc.SubmitGraph(ctx, h.alice, atLimit); errors.Is(err, core.ErrGraphTooLarge) {
		t.Fatalf("2-node submit at limit 2 rejected as too large: %v", err)
	}
}
