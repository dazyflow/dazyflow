package daemon

import (
	"context"
	"encoding/json"
	"testing"

	"git.sr.ht/~klahr/hazyflow/core"
)

func newTestResourceProvider(t *testing.T) (*ResourceProvider, *int) {
	t.Helper()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 7)
	}
	es, err := NewEncryptedSecrets(key, NewMemSecretsStore())
	if err != nil {
		t.Fatalf("NewEncryptedSecrets: %v", err)
	}
	calls := 0
	rp := &ResourceProvider{
		Secrets: es,
		Fetchers: map[string]ResourceFetcher{
			"google_sheet": func(_ context.Context, def core.ResourceDef) (any, error) {
				calls++
				return map[string]any{"id": def.Config["spreadsheet_id"]}, nil
			},
		},
	}
	return rp, &calls
}

func putDef(t *testing.T, es *EncryptedSecrets, tenant, flow string, scope SecretScope, def core.ResourceDef) {
	t.Helper()
	raw, _ := json.Marshal(def)
	if err := es.PutScoped(context.Background(), tenant, flow, scope, secretResourcePrefix+def.Name, string(raw)); err != nil {
		t.Fatalf("put def: %v", err)
	}
}

func TestResourceProvider_ResolvesViaFetcher(t *testing.T) {
	rp, calls := newTestResourceProvider(t)
	putDef(t, rp.Secrets, "acme", "f1", ScopeFlow, core.ResourceDef{
		Name: "leads", Type: "google_sheet", Config: map[string]any{"spreadsheet_id": "S1"},
	})
	ctx := core.WithFlow(core.WithTenant(context.Background(), "acme"), "f1")
	v, err := rp.Resolve(ctx, "leads")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	m, ok := v.(map[string]any)
	if !ok || m["id"] != "S1" {
		t.Errorf("resolved = %+v", v)
	}
	if *calls != 1 {
		t.Errorf("fetcher calls = %d, want 1", *calls)
	}
}

func TestResourceProvider_FlowOverridesOrg(t *testing.T) {
	rp, _ := newTestResourceProvider(t)
	putDef(t, rp.Secrets, "acme", "", ScopeTenant, core.ResourceDef{
		Name: "leads", Type: "google_sheet", Config: map[string]any{"spreadsheet_id": "ORG"},
	})
	putDef(t, rp.Secrets, "acme", "f1", ScopeFlow, core.ResourceDef{
		Name: "leads", Type: "google_sheet", Config: map[string]any{"spreadsheet_id": "FLOW"},
	})
	// With the flow on ctx, the flow-scoped def wins.
	ctx := core.WithFlow(core.WithTenant(context.Background(), "acme"), "f1")
	v, _ := rp.Resolve(ctx, "leads")
	if v.(map[string]any)["id"] != "FLOW" {
		t.Errorf("flow scope should win, got %+v", v)
	}
	// With no flow, the org def is used.
	v, _ = rp.Resolve(core.WithTenant(context.Background(), "acme"), "leads")
	if v.(map[string]any)["id"] != "ORG" {
		t.Errorf("org fallback, got %+v", v)
	}
}

func TestResourceProvider_UnknownTypeAndMissing(t *testing.T) {
	rp, _ := newTestResourceProvider(t)
	putDef(t, rp.Secrets, "acme", "", ScopeTenant, core.ResourceDef{
		Name: "weird", Type: "airtable", Config: map[string]any{},
	})
	ctx := core.WithTenant(context.Background(), "acme")
	if _, err := rp.Resolve(ctx, "weird"); err == nil {
		t.Error("unknown type should error")
	}
	if _, err := rp.Resolve(ctx, "nope"); err == nil {
		t.Error("missing resource should error")
	}
}
