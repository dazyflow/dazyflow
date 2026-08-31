// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/daemon"
)

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

func TestBuiltinProvider_Namespaced_UnprefixedRejected(t *testing.T) {
	p := daemon.NewBuiltinProvider()
	p.Namespaced = true
	p.Set("UNPREFIXED", "x")
	ctx := core.WithTenant(t.Context(), "acme")
	if _, err := p.Get(ctx, "UNPREFIXED"); err == nil {
		t.Fatal("unprefixed read should be rejected in namespaced mode")
	}
}

func TestBuiltinProvider_Namespaced_NoTenantRejected(t *testing.T) {
	p := daemon.NewBuiltinProvider()
	p.Namespaced = true
	p.Set("acme.token", "x")
	// No tenant in context.
	if _, err := p.Get(context.Background(), "acme.token"); err == nil {
		t.Fatal("missing-tenant read should be rejected")
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
