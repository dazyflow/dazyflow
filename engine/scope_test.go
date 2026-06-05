package engine

import (
	"context"
	"testing"

	"git.sr.ht/~klahr/hazyflow/core"
)

// scopeCaptureProvider records the tenant/workspace/flow it saw on the
// resolution context, so a test can assert the engine threaded all three
// through to secret resolution.
type scopeCaptureProvider struct {
	tenant, workspace, flow string
}

func (p *scopeCaptureProvider) Scheme() string { return "secret" }
func (p *scopeCaptureProvider) Get(ctx context.Context, _ string) (string, error) {
	p.tenant, _ = core.TenantFromContext(ctx)
	p.workspace, _ = core.WorkspaceFromContext(ctx)
	p.flow, _ = core.FlowFromContext(ctx)
	return "resolved", nil
}

// TestRunNode_ThreadsScopeToSecretResolver proves Engine.RunNode wraps the
// resolution context with the graph's tenant, workspace, AND flow id — the
// plumbing the layered-secret cascade depends on.
func TestRunNode_ThreadsScopeToSecretResolver(t *testing.T) {
	cap := &scopeCaptureProvider{}
	e := newEngineWith(t, sinkDrop())
	e.Secrets = map[string]core.SecretProvider{"secret": cap}

	g := core.Graph{
		ID: "flow-123", Tenant: "acme", Workspace: "ws-1",
		Nodes: []core.Node{{ID: "n", Module: "sink", Params: map[string]any{"k": "${secret.KEY}"}}},
	}
	if _, err := e.RunNode(context.Background(), g, "run-1", "n", nil, nil); err != nil {
		t.Fatalf("RunNode: %v", err)
	}
	if cap.tenant != "acme" || cap.workspace != "ws-1" || cap.flow != "flow-123" {
		t.Fatalf("resolver saw tenant=%q workspace=%q flow=%q; want acme/ws-1/flow-123",
			cap.tenant, cap.workspace, cap.flow)
	}
}
