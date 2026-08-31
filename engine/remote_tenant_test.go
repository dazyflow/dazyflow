// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/dazyflow/dazyflow/core"
)

// A remote belongs to exactly one tenant.
//
// This is not ordinary scoping. By the time the engine hands a Job to a
// transport, Job.Params carry RESOLVED secrets — the ${secret.…} references
// have already been expanded into real API keys and credentials. A remote any
// tenant could reach would therefore be a place one org's secrets could be
// sent by another org's flow.
//
// The catalog is keyed by (tenant, id) rather than filtered on read, so these
// tests are asserting something the map cannot skip. They exist anyway,
// because the guarantee is invisible when it works: nothing about a passing
// run tells you the isolation is still there.

// fakeRemote puts a transport into the catalog without a gRPC server. The dial
// path is covered by TestRemoteCatalog_Register_InsecureDialAndHandshake; what
// matters here is purely which key it lands under.
func fakeRemote(c *RemoteCatalog, tenant, id string) {
	c.nodes[remoteKey{tenant: tenant, id: id}] = &RemoteTransport{
		Descriptor: RemoteDescriptor{ID: id, Tenant: tenant},
		manifest:   core.Manifest{ID: id},
	}
}

func TestRemoteCatalog_GetIsScopedToOwningTenant(t *testing.T) {
	c := NewRemoteCatalog()
	fakeRemote(c, "acme", "invoices")

	if _, ok := c.Get("acme", "invoices"); !ok {
		t.Fatal("owning tenant cannot reach its own remote")
	}
	// The whole point: same id, different tenant.
	if _, ok := c.Get("globex", "invoices"); ok {
		t.Fatal("another tenant reached a remote it does not own")
	}
}

// Two tenants naming a runner the same thing is expected, not exceptional —
// "invoices" is what everyone calls it. They must not collide.
func TestRemoteCatalog_SameIdDifferentTenants(t *testing.T) {
	c := NewRemoteCatalog()
	fakeRemote(c, "acme", "invoices")
	fakeRemote(c, "globex", "invoices")

	acme, ok := c.Get("acme", "invoices")
	if !ok {
		t.Fatal("acme lost its remote")
	}
	globex, ok := c.Get("globex", "invoices")
	if !ok {
		t.Fatal("globex lost its remote")
	}
	if acme == globex {
		t.Fatal("both tenants resolved to the SAME transport — the id collided")
	}
}

// An empty tenant means "nobody", never "anybody". A background task, a
// migration, or a test that forgot core.WithTenant must not reach a runner.
func TestRemoteCatalog_EmptyTenantMatchesNothing(t *testing.T) {
	c := NewRemoteCatalog()
	fakeRemote(c, "acme", "invoices")

	if _, ok := c.Get("", "invoices"); ok {
		t.Fatal("an empty tenant resolved a remote — it must match nothing at all")
	}
}

// The guard in Get, specifically.
//
// The (tenant, id) key already yields nothing for an empty tenant, so the
// explicit `if tenant == ""` check looks redundant — and a test that only asks
// "can an empty tenant reach acme's remote?" passes with the check deleted,
// which was verified. What the check actually defends is the case where
// something DID land under the empty key: Register refuses that today, but a
// future config path, a migration, or a direct map write would not be refused
// by the type system. This test creates exactly that state and asserts an
// empty tenant still reaches nothing.
func TestRemoteCatalog_EmptyTenantCannotReachAnEmptyKeyedRemote(t *testing.T) {
	c := NewRemoteCatalog()
	fakeRemote(c, "", "orphan") // bypasses Register on purpose

	if _, ok := c.Get("", "orphan"); ok {
		t.Fatal("an empty tenant resolved an empty-keyed remote — " +
			"the guard in Get is what stops '' from becoming a usable namespace")
	}
}

// Registering under no tenant would store something nothing can ever resolve.
// Failing at registration turns that into a startup error instead of a drop
// that silently never works.
func TestRemoteCatalog_RegisterRequiresTenant(t *testing.T) {
	c := NewRemoteCatalog()
	err := c.Register(RemoteDescriptor{ID: "invoices", Endpoint: "127.0.0.1:1", Insecure: true})
	if err == nil {
		t.Fatal("Register with no tenant: want an error")
	}
	if !strings.Contains(err.Error(), "Tenant required") {
		t.Fatalf("err = %v, want one naming the missing tenant", err)
	}
}

// The end-to-end shape: what the engine actually calls. Resolve reads the
// tenant off the context (set by core.WithTenant before every node executes)
// and passes it down to the catalog.
func TestResolve_RemoteIsScopedToContextTenant(t *testing.T) {
	c := NewRemoteCatalog()
	fakeRemote(c, "acme", "invoices")
	r := &NodeResolver{Native: NewRegistry(), Remote: c}

	if _, err := r.Resolve(core.WithTenant(context.Background(), "acme"), "invoices"); err != nil {
		t.Fatalf("owning tenant: %v", err)
	}

	_, err := r.Resolve(core.WithTenant(context.Background(), "globex"), "invoices")
	if err == nil {
		t.Fatal("another tenant resolved a remote it does not own")
	}
	// It should look like an unknown module, not like a permission error:
	// globex has no business learning that acme has a runner called invoices.
	if !strings.Contains(err.Error(), "no transport") {
		t.Fatalf("err = %v, want the plain 'no transport' form", err)
	}
}

// A context with no tenant at all — the case a missing core.WithTenant
// produces. Fails closed, and reports the same way an unknown module does.
func TestResolve_RemoteRefusedWithoutTenant(t *testing.T) {
	c := NewRemoteCatalog()
	fakeRemote(c, "acme", "invoices")
	r := &NodeResolver{Native: NewRegistry(), Remote: c}

	if _, err := r.Resolve(context.Background(), "invoices"); err == nil {
		t.Fatal("a tenantless context resolved a remote")
	}
}

// Native drops are instance-wide and must NOT have been narrowed by any of
// this: threading a tenant through lookup is meant to constrain remotes only.
// A positive control — without it, a lookup that refused everything would pass
// every test above.
func TestResolve_NativeUnaffectedByTenant(t *testing.T) {
	reg := NewRegistry()
	if err := reg.Register(NativeDrop{
		Manifest: core.Manifest{
			ID:       "native_drop",
			Summary:  "control for the tenant-scoping tests",
			Examples: []core.ParamsExample{{Title: "default"}},
		},
		Execute: noopExecute,
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	r := &NodeResolver{Native: reg, Remote: NewRemoteCatalog()}

	for _, ctx := range []context.Context{
		context.Background(),
		core.WithTenant(context.Background(), "acme"),
		core.WithTenant(context.Background(), "globex"),
	} {
		if _, err := r.Resolve(ctx, "native_drop"); err != nil {
			t.Fatalf("native drop refused: %v", err)
		}
	}
}
