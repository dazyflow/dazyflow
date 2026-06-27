package core

import (
	"context"
	"testing"
)

func TestWithTenant_AndFromContext(t *testing.T) {
	ctx := WithTenant(context.Background(), "acme")
	got, ok := TenantFromContext(ctx)
	if !ok || got != "acme" {
		t.Errorf("TenantFromContext = (%q, %v), want (acme, true)", got, ok)
	}
}

func TestWithTenant_EmptyIsNoop(t *testing.T) {
	ctx := WithTenant(context.Background(), "")
	if got, ok := TenantFromContext(ctx); ok || got != "" {
		t.Errorf("empty tenant should not be set: got (%q, %v)", got, ok)
	}
}

func TestTenantFromContext_Missing(t *testing.T) {
	if got, ok := TenantFromContext(context.Background()); ok || got != "" {
		t.Errorf("missing tenant should be (\"\", false): got (%q, %v)", got, ok)
	}
}

func TestWithFlow_AndFromContext(t *testing.T) {
	ctx := WithFlow(context.Background(), "flow-1")
	got, ok := FlowFromContext(ctx)
	if !ok || got != "flow-1" {
		t.Errorf("FlowFromContext = (%q, %v), want (flow-1, true)", got, ok)
	}
}

func TestWithFlow_EmptyIsNoop(t *testing.T) {
	ctx := WithFlow(context.Background(), "")
	if got, ok := FlowFromContext(ctx); ok || got != "" {
		t.Errorf("empty flow should not be set: got (%q, %v)", got, ok)
	}
}

func TestFlowFromContext_Missing(t *testing.T) {
	if got, ok := FlowFromContext(context.Background()); ok || got != "" {
		t.Errorf("missing flow should be (\"\", false): got (%q, %v)", got, ok)
	}
}
