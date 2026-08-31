// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"context"
	"testing"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/engine/webapi"
)

func webAPICatalog(t *testing.T, tenant string) *webapi.Catalog {
	t.Helper()
	cat := webapi.NewCatalog()
	err := cat.Register(webapi.Descriptor{
		Tenant:  tenant,
		Name:    "orders",
		BaseURL: "https://api.example.com",
		Operations: []webapi.Operation{{
			ID: "get_order", Method: "GET", Path: "/orders/{id}",
			Args: []webapi.Arg{{Name: "id", In: webapi.InPath, Type: "string", Required: true}},
		}},
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	return cat
}

// A web-API step resolves for the org that configured it, and for nobody else.
// The tenant travels on ctx, so this is the check that the resolver reads it.
func TestNodeResolver_WebAPIIsTenantScoped(t *testing.T) {
	r := &NodeResolver{Native: NewRegistry(), WebAPI: webAPICatalog(t, "acme")}

	if _, err := r.Resolve(core.WithTenant(context.Background(), "acme"), "api:orders:get_order"); err != nil {
		t.Fatalf("the owning tenant cannot resolve its own step: %v", err)
	}
	if _, err := r.Resolve(core.WithTenant(context.Background(), "other"), "api:orders:get_order"); err == nil {
		t.Error("another tenant resolved a step it does not own")
	}
	if _, err := r.Resolve(context.Background(), "api:orders:get_order"); err == nil {
		t.Error("a tenant-less caller resolved a tenant's step")
	}
}

// The palette must show exactly what will resolve: a step in ManifestsForTenant
// that Resolve then refuses is a step an author can place and never run.
func TestNodeResolver_WebAPIManifestsMatchResolution(t *testing.T) {
	r := &NodeResolver{Native: NewRegistry(), WebAPI: webAPICatalog(t, "acme")}

	got := r.ManifestsForTenant("acme")
	m, ok := got["api:orders:get_order"]
	if !ok {
		t.Fatalf("manifest missing from the tenant's palette: %v", keys(got))
	}
	if m.Label == "" || m.Summary == "" {
		t.Errorf("manifest = %+v, want it palette-ready", m)
	}
	// WithPassthrough is applied on the way out, as it is for every other
	// catalog — the pin the editor expects on a processing step.
	if _, hasPass := m.Output(core.PassPort); !hasPass {
		t.Error("no passthrough pin: ManifestsForTenant did not decorate this manifest like the others")
	}
	if len(r.ManifestsForTenant("other")) != 0 {
		t.Error("another org's palette shows a catalog it cannot resolve")
	}
	if len(r.ManifestsForTenant("")) != 0 {
		t.Error("a tenant-less listing shows a tenant's private steps")
	}
}

func keys(m map[string]core.Manifest) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
