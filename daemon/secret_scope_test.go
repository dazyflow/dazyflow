package daemon

import (
	"context"
	"testing"

	"git.sr.ht/~klahr/hazyflow/core"
)

func resolveCtx(tenant, flow string) context.Context {
	return core.WithFlow(core.WithTenant(context.Background(), tenant), flow)
}

// TestSecretScope_CascadePrecedence proves ${secret.NAME} resolves
// flow → organization, the nearest scope winning, and that a name present only
// at the organization scope resolves identically regardless of the flow.
func TestSecretScope_CascadePrecedence(t *testing.T) {
	es, err := NewEncryptedSecrets(randomKey(t), NewMemSecretsStore())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := es.PutScoped(ctx, "acme", "", ScopeTenant, "TOKEN", "org-val"); err != nil {
		t.Fatal(err)
	}
	if err := es.PutScoped(ctx, "acme", "flowA", ScopeFlow, "TOKEN", "flow-val"); err != nil {
		t.Fatal(err)
	}
	// A organization-only secret.
	if err := es.PutScoped(ctx, "acme", "", ScopeTenant, "SHARED", "shared-val"); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name, flow, secret, want string
	}{
		{"flow overrides org", "flowA", "TOKEN", "flow-val"},
		{"org when no flow secret", "flowZ", "TOKEN", "org-val"},
		{"org when no flow in ctx", "", "TOKEN", "org-val"},
		{"org-only name, with flow ctx", "flowA", "SHARED", "shared-val"},
		{"org-only name, bare ctx", "", "SHARED", "shared-val"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := es.Get(resolveCtx("acme", tc.flow), tc.secret)
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if got != tc.want {
				t.Errorf("Get(%s, flow=%q) = %q, want %q", tc.secret, tc.flow, got, tc.want)
			}
		})
	}
}

// TestSecretScope_FlowIsolation proves one flow cannot resolve another flow's
// secret via the cascade — it's keyed by the running flow's ID, so flowB never
// sees flowA's value. This is the blast-radius guard.
func TestSecretScope_FlowIsolation(t *testing.T) {
	es, err := NewEncryptedSecrets(randomKey(t), NewMemSecretsStore())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	// Only flowA has a flow-scoped DB secret — nothing at the org level.
	if err := es.PutScoped(ctx, "acme", "flowA", ScopeFlow, "DB", "flowA-db"); err != nil {
		t.Fatal(err)
	}
	if got, err := es.Get(resolveCtx("acme", "flowA"), "DB"); err != nil || got != "flowA-db" {
		t.Fatalf("flowA ${secret.DB} = %q, %v; want flowA-db", got, err)
	}
	if got, err := es.Get(resolveCtx("acme", "flowB"), "DB"); err == nil {
		t.Fatalf("cascade leaked flowA's secret to flowB: %q", got)
	}
}

// TestSecretScope_ListScoped proves each scope lists only its own names (flow
// prefix stripped) and the organization scope hides every reserved namespace.
func TestSecretScope_ListScoped(t *testing.T) {
	es, err := NewEncryptedSecrets(randomKey(t), NewMemSecretsStore())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	_ = es.PutScoped(ctx, "acme", "", ScopeTenant, "ORG", "x")
	_ = es.PutScoped(ctx, "acme", "flowA", ScopeFlow, "FLW", "x")
	// A reserved connection secret must not appear in the organization list.
	_ = es.Put(ctx, "acme", "conn.slack.token", "x")

	orgList, _ := es.ListScoped(ctx, "acme", "", ScopeTenant)
	if !equalStrSet(orgList, []string{"ORG"}) {
		t.Errorf("organization list = %v, want [ORG] (flow./conn. hidden)", orgList)
	}
	flowList, _ := es.ListScoped(ctx, "acme", "flowA", ScopeFlow)
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
