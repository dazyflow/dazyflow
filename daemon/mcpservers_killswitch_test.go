// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/engine"
	"git.sr.ht/~klahr/dazyflow/engine/mcp"
)

// The platform killswitch has to reach a tenant's MCP tool.
//
// This is the surface a platform admin uses when an org's server misbehaves —
// and until MCP tools were keyed by tenant it was the one place they could not
// be seen, because the instance-wide manifest map does not contain them by
// construction. DropGate would happily have enforced a switch on such an id;
// there was simply no way to create one.
//
// Two things are asserted here that no unit test on either side can: the drop
// appears on the page WITH its owning org, and the resolver then refuses it.

func TestPlatformKillswitch_ListsATenantsMCPTool(t *testing.T) {
	h, _, _, _, _, _ := platformHarness(t)

	srv := (&fakeMCPEndpoint{toolNames: []string{"create_issue"}}).start(t)
	cat := mcp.NewCatalog()
	t.Cleanup(func() { _ = cat.Close() })
	if err := cat.RegisterHTTP(mcp.HTTPDescriptor{Name: "vendor", Tenant: "acme", URL: srv.URL}); err != nil {
		t.Fatalf("RegisterHTTP: %v", err)
	}
	h.svc.Engine.Resolver = &engine.NodeResolver{Native: engine.Default, MCP: cat}

	rw := platformRaw(t, h, "GET", "/api/v1/admin/platform/drops", nil)
	if rw.Code != 200 {
		t.Fatalf("code %d body %s", rw.Code, rw.Body)
	}
	var got struct {
		Drops []struct {
			ID             string   `json:"id"`
			OwnedByTenants []string `json:"owned_by_tenants"`
		} `json:"drops"`
	}
	if err := json.Unmarshal(rw.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	var found bool
	for _, d := range got.Drops {
		if d.ID != "mcp:vendor:create_issue" {
			continue
		}
		found = true
		// Without the owning org the page cannot say whose server this is, so
		// a platform admin would be switching off an id with no context.
		if len(d.OwnedByTenants) != 1 || d.OwnedByTenants[0] != "acme" {
			t.Errorf("owned_by_tenants = %v, want [acme]", d.OwnedByTenants)
		}
	}
	if !found {
		t.Fatal("a tenant's MCP tool is absent from the only page that can disable it")
	}
}

// TestPlatformKillswitch_DisablesATenantsMCPTool drives the whole path: the
// admin disables the id, and the resolver refuses it for that org afterwards.
func TestPlatformKillswitch_DisablesATenantsMCPTool(t *testing.T) {
	h, _, _, _, _, ds := platformHarness(t)

	srv := (&fakeMCPEndpoint{toolNames: []string{"create_issue"}}).start(t)
	cat := mcp.NewCatalog()
	t.Cleanup(func() { _ = cat.Close() })
	if err := cat.RegisterHTTP(mcp.HTTPDescriptor{Name: "vendor", Tenant: "acme", URL: srv.URL}); err != nil {
		t.Fatalf("RegisterHTTP: %v", err)
	}
	resolver := &engine.NodeResolver{
		Native: engine.Default, MCP: cat,
		DropGate: func(_ context.Context, dropID, tenant string) error {
			if ds.Disabled(dropID, tenant) {
				return fmt.Errorf("step %q is disabled by platform policy", dropID)
			}
			return nil
		},
	}
	h.svc.Engine.Resolver = resolver

	const id = "mcp:vendor:create_issue"
	ctx := core.WithTenant(context.Background(), "acme")

	// Resolvable to begin with.
	if _, err := resolver.Resolve(ctx, id); err != nil {
		t.Fatalf("Resolve before disabling: %v", err)
	}

	// The id contains colons, so it reaches the handler percent-encoded — the
	// one detail that would have made this route unusable for MCP ids.
	path := "/api/v1/admin/platform/drops/" + url.PathEscape(id) + "/disable"
	rw := platformRaw(t, h, "POST", path, []byte(`{"tenant":"acme","reason":"leaking"}`))
	if rw.Code != 204 {
		t.Fatalf("disable code %d body %s", rw.Code, rw.Body)
	}

	_, err := resolver.Resolve(ctx, id)
	if err == nil {
		t.Fatal("the disabled MCP step still resolves")
	}
	if !strings.Contains(err.Error(), "disabled by platform policy") {
		t.Fatalf("unexpected refusal: %v", err)
	}
	// Another org's flows are untouched by a per-tenant switch.
	if _, err := resolver.Resolve(core.WithTenant(context.Background(), "globex"), "text"); err != nil {
		t.Fatalf("an unrelated org's built-in step broke: %v", err)
	}
}
