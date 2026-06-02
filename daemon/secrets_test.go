package daemon_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.sr.ht/~klahr/hazyflow/core"
	"git.sr.ht/~klahr/hazyflow/daemon"
)

func TestEnvProvider_ResolvesAndRejectsMissing(t *testing.T) {
	t.Setenv("HZ_TEST_SECRET", "value-from-env")
	p := daemon.EnvProvider{}
	got, err := p.Get(t.Context(), "HZ_TEST_SECRET")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "value-from-env" {
		t.Errorf("got %q, want value-from-env", got)
	}

	if _, err := p.Get(t.Context(), "DEFINITELY_NOT_SET_12345"); err == nil {
		t.Error("expected error for missing env var")
	}

	t.Setenv("HZ_TEST_EMPTY", "")
	if _, err := p.Get(t.Context(), "HZ_TEST_EMPTY"); err == nil {
		t.Error("expected error for empty env var")
	}
}

func TestEnvProvider_Scheme(t *testing.T) {
	p := daemon.EnvProvider{}
	if s := p.Scheme(); s != "env" {
		t.Errorf("scheme = %q", s)
	}
}

func TestBuiltinProvider_FromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secrets.json")
	contents, _ := json.Marshal(map[string]string{
		"stripe.key": "sk_test_xyz",
		"smtp.pass":  "hunter2",
	})
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	p, err := daemon.NewBuiltinProviderFromFile(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	v, _ := p.Get(t.Context(), "stripe.key")
	if v != "sk_test_xyz" {
		t.Errorf("stripe.key = %q", v)
	}
	if _, err := p.Get(context.Background(), "nope"); err == nil {
		t.Error("expected error for missing key")
	}
}

func TestBuiltinProvider_BadFile(t *testing.T) {
	if _, err := daemon.NewBuiltinProviderFromFile("/nonexistent/path/here"); err == nil {
		t.Error("expected error for missing file")
	}
	bad := filepath.Join(t.TempDir(), "bad.json")
	_ = os.WriteFile(bad, []byte("not json"), 0o600)
	_, err := daemon.NewBuiltinProviderFromFile(bad)
	if err == nil || !strings.Contains(err.Error(), "parse") {
		t.Errorf("err = %v", err)
	}
}

func TestBuiltinProvider_Set(t *testing.T) {
	p := daemon.NewBuiltinProvider()
	p.Set("k", "v")
	v, err := p.Get(t.Context(), "k")
	if err != nil || v != "v" {
		t.Errorf("Get after Set: v=%q err=%v", v, err)
	}
}

// ---- Namespaced (per-tenant ACL) mode ------------------------------
//
// In Namespaced mode the provider rejects cross-tenant reads. The
// security property: tenant "acme" cannot resolve a secret that
// tenant "globex" has the right to read, even if both names are
// known to the operator.

func TestEnvProvider_Namespaced_RequiresTenantPrefix(t *testing.T) {
	t.Setenv("acme.token", "shared-but-acme-only")
	p := daemon.EnvProvider{Namespaced: true}
	ctx := core.WithTenant(t.Context(), "acme")

	got, err := p.Get(ctx, "acme.token")
	if err != nil {
		t.Fatalf("Get with matching prefix: %v", err)
	}
	if got != "shared-but-acme-only" {
		t.Errorf("got %q", got)
	}
}

func TestEnvProvider_Namespaced_CrossTenantRejected(t *testing.T) {
	t.Setenv("acme.token", "acme-secret")
	p := daemon.EnvProvider{Namespaced: true}
	// Caller is globex but tries to read acme's secret.
	ctx := core.WithTenant(t.Context(), "globex")
	_, err := p.Get(ctx, "acme.token")
	if err == nil {
		t.Fatal("cross-tenant read should be rejected")
	}
	if !strings.Contains(err.Error(), "does not match") {
		t.Errorf("error should explain the tenant mismatch, got: %v", err)
	}
}

func TestEnvProvider_Namespaced_UnprefixedRejected(t *testing.T) {
	t.Setenv("UNPREFIXED", "x")
	p := daemon.EnvProvider{Namespaced: true}
	ctx := core.WithTenant(t.Context(), "acme")
	_, err := p.Get(ctx, "UNPREFIXED")
	if err == nil {
		t.Fatal("unprefixed read should be rejected in namespaced mode")
	}
}

func TestEnvProvider_Namespaced_NoTenantRejected(t *testing.T) {
	t.Setenv("acme.token", "x")
	p := daemon.EnvProvider{Namespaced: true}
	// No tenant in context.
	_, err := p.Get(context.Background(), "acme.token")
	if err == nil {
		t.Fatal("missing-tenant read should be rejected")
	}
}

func TestEnvProvider_NotNamespaced_PreservesBackcompat(t *testing.T) {
	// Default mode (Namespaced=false) MUST keep working without a
	// tenant in context — single-tenant deployments depend on this.
	t.Setenv("GLOBAL_KEY", "v")
	p := daemon.EnvProvider{}
	got, err := p.Get(context.Background(), "GLOBAL_KEY")
	if err != nil {
		t.Fatalf("backcompat Get failed: %v", err)
	}
	if got != "v" {
		t.Errorf("got %q", got)
	}
}

func TestBuiltinProvider_Namespaced_OK(t *testing.T) {
	p := daemon.NewBuiltinProvider()
	p.Namespaced = true
	p.Set("acme.api_key", "sk_acme")
	ctx := core.WithTenant(t.Context(), "acme")
	got, err := p.Get(ctx, "acme.api_key")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "sk_acme" {
		t.Errorf("got %q", got)
	}
}

func TestBuiltinProvider_Namespaced_CrossTenantRejected(t *testing.T) {
	p := daemon.NewBuiltinProvider()
	p.Namespaced = true
	p.Set("acme.api_key", "sk_acme")
	p.Set("globex.api_key", "sk_globex")
	// Globex shouldn't see acme's even though both are in the store.
	ctx := core.WithTenant(t.Context(), "globex")
	_, err := p.Get(ctx, "acme.api_key")
	if err == nil {
		t.Fatal("cross-tenant read should be rejected")
	}
}
