package daemon

import (
	"context"
	"testing"

	"git.sr.ht/~klahr/hazyflow/core"
)

func resolveCtx(tenant, ws, flow string) context.Context {
	ctx := core.WithTenant(context.Background(), tenant)
	ctx = core.WithWorkspace(ctx, ws)
	ctx = core.WithFlow(ctx, flow)
	return ctx
}

// TestSecretScope_CascadePrecedence proves ${secret.NAME} resolves
// flow → workspace → tenant, nearest scope winning, and that a name present
// only at tenant resolves identically regardless of workspace/flow.
func TestSecretScope_CascadePrecedence(t *testing.T) {
	es, err := NewEncryptedSecrets(randomKey(t), NewMemSecretsStore())
	if err != nil {
		t.Fatal(err)
	}
	put := es.PutScoped
	ctx := context.Background()
	if err := put(ctx, "acme", "", "", ScopeTenant, "TOKEN", "tenant-val"); err != nil {
		t.Fatal(err)
	}
	if err := put(ctx, "acme", "ws1", "", ScopeWorkspace, "TOKEN", "ws-val"); err != nil {
		t.Fatal(err)
	}
	if err := put(ctx, "acme", "ws1", "flowA", ScopeFlow, "TOKEN", "flow-val"); err != nil {
		t.Fatal(err)
	}
	// A tenant-only secret to prove back-compat.
	if err := put(ctx, "acme", "", "", ScopeTenant, "SHARED", "shared-val"); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name, ws, flow, secret, want string
	}{
		{"flow wins", "ws1", "flowA", "TOKEN", "flow-val"},
		{"workspace wins when no flow secret", "ws1", "flowZ", "TOKEN", "ws-val"},
		{"workspace wins when no flow in ctx", "ws1", "", "TOKEN", "ws-val"},
		{"tenant when no ws/flow secret", "wsZ", "flowZ", "TOKEN", "tenant-val"},
		{"tenant when ctx bare", "", "", "TOKEN", "tenant-val"},
		{"tenant-only name ignores scope (ctx full)", "ws1", "flowA", "SHARED", "shared-val"},
		{"tenant-only name ignores scope (ctx bare)", "", "", "SHARED", "shared-val"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := es.Get(resolveCtx("acme", tc.ws, tc.flow), tc.secret)
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if got != tc.want {
				t.Errorf("Get(%s, ws=%q flow=%q) = %q, want %q", tc.secret, tc.ws, tc.flow, got, tc.want)
			}
		})
	}
}

// TestSecretScope_FlowIsolation proves one flow cannot resolve another flow's
// flow-scoped secret via the cascade — it's keyed by the running flow's ID, so
// flowB never sees flowA's value. This is the blast-radius guard.
func TestSecretScope_FlowIsolation(t *testing.T) {
	es, err := NewEncryptedSecrets(randomKey(t), NewMemSecretsStore())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	// Only flowA has a flow-scoped DB secret — nothing at workspace/tenant.
	if err := es.PutScoped(ctx, "acme", "ws1", "flowA", ScopeFlow, "DB", "flowA-db"); err != nil {
		t.Fatal(err)
	}
	// flowA resolves it.
	if got, err := es.Get(resolveCtx("acme", "ws1", "flowA"), "DB"); err != nil || got != "flowA-db" {
		t.Fatalf("flowA ${secret.DB} = %q, %v; want flowA-db", got, err)
	}
	// flowB must NOT — its cascade has no flow/workspace/tenant DB, so it errors.
	if got, err := es.Get(resolveCtx("acme", "ws1", "flowB"), "DB"); err == nil {
		t.Fatalf("cascade leaked flowA's secret to flowB: %q", got)
	}
}

// TestSecretScope_ListScoped proves each scope lists only its own names (with
// the prefix stripped) and the tenant scope hides every reserved namespace.
func TestSecretScope_ListScoped(t *testing.T) {
	es, err := NewEncryptedSecrets(randomKey(t), NewMemSecretsStore())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	_ = es.PutScoped(ctx, "acme", "", "", ScopeTenant, "TEN", "x")
	_ = es.PutScoped(ctx, "acme", "ws1", "", ScopeWorkspace, "WSP", "x")
	_ = es.PutScoped(ctx, "acme", "ws1", "flowA", ScopeFlow, "FLW", "x")
	// A reserved connection secret must not appear in the tenant list.
	_ = es.Put(ctx, "acme", "conn.slack.token", "x")

	tenantList, _ := es.ListScoped(ctx, "acme", "", "", ScopeTenant)
	if !equalStrSet(tenantList, []string{"TEN"}) {
		t.Errorf("tenant list = %v, want [TEN] (ws./flow./conn. hidden)", tenantList)
	}
	wsList, _ := es.ListScoped(ctx, "acme", "ws1", "", ScopeWorkspace)
	if !equalStrSet(wsList, []string{"WSP"}) {
		t.Errorf("workspace list = %v, want [WSP]", wsList)
	}
	flowList, _ := es.ListScoped(ctx, "acme", "ws1", "flowA", ScopeFlow)
	if !equalStrSet(flowList, []string{"FLW"}) {
		t.Errorf("flow list = %v, want [FLW]", flowList)
	}
}

func equalStrSet(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	m := map[string]int{}
	for _, s := range got {
		m[s]++
	}
	for _, s := range want {
		m[s]--
	}
	for _, v := range m {
		if v != 0 {
			return false
		}
	}
	return true
}
