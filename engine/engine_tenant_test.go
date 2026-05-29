package engine

import (
	"context"
	"testing"

	"git.sr.ht/~klahr/hazy-flow/core"
)

// Connector token lookups (e.g. Gmail/Sheets OAuth via GetOAuthToken)
// read the tenant from the execution context. The engine must therefore
// put it there before calling a node's Execute — otherwise a live
// connected account fails with "get oauth token: no tenant in context".
func TestEngine_TenantInExecuteContext(t *testing.T) {
	var gotTenant string
	var present bool
	e := newEngineWith(t, NativeDrop{
		Manifest: noopManifest,
		Execute: func(ctx context.Context, _ core.Job, _ chan<- core.Progress) (core.Result, error) {
			gotTenant, present = core.TenantFromContext(ctx)
			return core.Result{Status: core.StatusOK, Output: map[string]core.Ref{"out": {Ref: "x"}}}, nil
		},
	})

	g := core.Graph{
		ID:     "g",
		Tenant: "acme",
		Nodes:  []core.Node{{ID: "a", Module: "noop"}},
	}
	if _, err := e.Run(t.Context(), g, nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !present || gotTenant != "acme" {
		t.Fatalf("tenant in Execute context = %q (present=%v), want \"acme\" — connector OAuth lookups depend on this", gotTenant, present)
	}
}
