package daemon

import (
	"context"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/internal/llm"
)

// TestPickProvider_Cov covers pickProvider: no connected providers, the
// default-to-first choice, and an explicit want that matches a connected one.
func TestPickProvider_Cov(t *testing.T) {
	h := newSecretsHarness(t)
	ctx := core.WithTenant(context.Background(), "t")

	// No providers connected yet -> empty.
	if chosen, conn := h.gw.pickProvider(ctx, ""); conn != nil || chosen.info.Name != "" {
		t.Fatalf("no-connection pick = %+v / %v, want empty", chosen, conn)
	}

	// Register two test providers and store an api_key for each so both count
	// as connected.
	llm.Register(llm.ProviderInfo{Name: "testprov_a", Integration: "TestProvA"})
	llm.Register(llm.ProviderInfo{Name: "testprov_b", Integration: "TestProvB"})
	for _, p := range []struct{ integ, key string }{
		{"TestProvA", "key-a"}, {"TestProvB", "key-b"},
	} {
		if err := h.gw.EncryptedSecrets.PutScoped(ctx, "t", "", ScopeTenant,
			core.ConnectionSecretKey(p.integ, "api_key"), p.key); err != nil {
			t.Fatalf("seed %s: %v", p.integ, err)
		}
	}

	// At least our two providers are connected; default picks the first
	// connected one in registration order.
	chosen, conn := h.gw.pickProvider(ctx, "")
	if len(conn) < 2 {
		t.Fatalf("connected providers = %d, want >=2", len(conn))
	}
	if chosen.info.Name == "" || chosen.key == "" {
		t.Fatalf("default pick is empty: %+v", chosen)
	}

	// Explicit want selects the matching provider.
	want, _ := h.gw.pickProvider(ctx, "testprov_b")
	if want.info.Name != "testprov_b" || want.key != "key-b" {
		t.Fatalf("want=testprov_b pick = %+v", want)
	}
}
