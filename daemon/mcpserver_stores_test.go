// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"errors"
	"testing"
	"time"
)

// One suite, both stores.
//
// The memory store and the Postgres one have to be indistinguishable: the
// daemon picks between them at boot and every other test in this package runs
// against the memory one, so a behaviour that holds only in memory is a
// behaviour that does not hold in production.
//
// Two rules here are enforced by SQL in one implementation and by Go under a
// mutex in the other, and both fail SILENTLY when wrong — which is what makes
// them worth a contract rather than a unit test on either side:
//
//	A nil sealed token KEEPS the stored credential. Get it wrong and every edit
//	that did not retype the token blanks it, and the server stops connecting
//	for a reason nothing on the page explains.
//
//	SetStatus does not touch updated_at. Get it wrong and every replica sees a
//	newer timestamp than it applied on every pass, so the whole fleet
//	re-handshakes with every server every thirty seconds, forever.
func mcpServerStoreContract(t *testing.T, store MCPServerStore) {
	t.Helper()
	ctx := context.Background()

	row := func(tenant, name, url string) MCPServer {
		at := time.Now().UTC().Truncate(time.Millisecond)
		return MCPServer{
			Tenant: tenant, Name: name, URL: url,
			AuthKind: MCPAuthBearer, Enabled: true,
			CreatedBy: "admin@" + tenant, CreatedAt: at, UpdatedAt: at,
		}
	}

	t.Run("a missing server is not found", func(t *testing.T) {
		if _, err := store.Get(ctx, "acme", "nope"); !errors.Is(err, ErrMCPServerNotFound) {
			t.Fatalf("err = %v, want ErrMCPServerNotFound", err)
		}
		if err := store.Delete(ctx, "acme", "nope"); !errors.Is(err, ErrMCPServerNotFound) {
			t.Fatalf("delete err = %v, want ErrMCPServerNotFound", err)
		}
	})

	t.Run("put then read back", func(t *testing.T) {
		want := row("acme", "vendor", "https://vendor.test/mcp")
		if err := store.Put(ctx, want, []byte("sealed-1")); err != nil {
			t.Fatalf("Put: %v", err)
		}
		got, err := store.Get(ctx, "acme", "vendor")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got.URL != want.URL || got.AuthKind != want.AuthKind || !got.Enabled {
			t.Fatalf("got %+v, want %+v", got, want)
		}
		if got.CreatedBy != "admin@acme" {
			t.Errorf("created_by = %q", got.CreatedBy)
		}
		blob, err := store.SealedToken(ctx, "acme", "vendor")
		if err != nil {
			t.Fatalf("SealedToken: %v", err)
		}
		if string(blob) != "sealed-1" {
			t.Errorf("sealed token = %q", blob)
		}
	})

	t.Run("a nil token keeps the stored one", func(t *testing.T) {
		edited := row("acme", "vendor", "https://moved.test/mcp")
		if err := store.Put(ctx, edited, nil); err != nil {
			t.Fatalf("Put: %v", err)
		}
		got, _ := store.Get(ctx, "acme", "vendor")
		if got.URL != "https://moved.test/mcp" {
			t.Errorf("url = %q, want the edit applied", got.URL)
		}
		blob, _ := store.SealedToken(ctx, "acme", "vendor")
		if string(blob) != "sealed-1" {
			t.Fatalf("sealed token = %q, want the stored one kept", blob)
		}
	})

	t.Run("an empty token clears the stored one", func(t *testing.T) {
		cleared := row("acme", "vendor", "https://moved.test/mcp")
		cleared.AuthKind = MCPAuthNone
		if err := store.Put(ctx, cleared, []byte{}); err != nil {
			t.Fatalf("Put: %v", err)
		}
		blob, _ := store.SealedToken(ctx, "acme", "vendor")
		if len(blob) != 0 {
			t.Fatalf("sealed token = %q, want it cleared", blob)
		}
	})

	t.Run("set status leaves the configuration alone", func(t *testing.T) {
		before, _ := store.Get(ctx, "acme", "vendor")
		at := time.Now().UTC().Truncate(time.Millisecond)
		if err := store.SetStatus(ctx, "acme", "vendor", 7, "", at); err != nil {
			t.Fatalf("SetStatus: %v", err)
		}
		after, _ := store.Get(ctx, "acme", "vendor")
		if after.ToolCount != 7 || after.LastError != "" {
			t.Errorf("status not recorded: %+v", after)
		}
		if after.LastConnected.IsZero() {
			t.Error("last_connected not set on a successful attempt")
		}
		// The reconcile loop compares against updated_at on every pass, so a
		// status write moving it would make the fleet re-handshake forever.
		if !after.UpdatedAt.Equal(before.UpdatedAt) {
			t.Fatalf("updated_at moved on a status write: %v -> %v", before.UpdatedAt, after.UpdatedAt)
		}
		if after.URL != before.URL {
			t.Errorf("url changed on a status write: %q -> %q", before.URL, after.URL)
		}
	})

	t.Run("a failure keeps the last good connection time", func(t *testing.T) {
		before, _ := store.Get(ctx, "acme", "vendor")
		at := time.Now().UTC().Add(time.Minute).Truncate(time.Millisecond)
		if err := store.SetStatus(ctx, "acme", "vendor", 0, "refused the credential", at); err != nil {
			t.Fatalf("SetStatus: %v", err)
		}
		after, _ := store.Get(ctx, "acme", "vendor")
		if after.LastError == "" || after.ToolCount != 0 {
			t.Errorf("failure not recorded: %+v", after)
		}
		// "Last worked at…" is the useful half of a failing server's story, so
		// a failure must not overwrite it with the time it failed.
		if !after.LastConnected.Equal(before.LastConnected) {
			t.Fatalf("last_connected moved on a failure: %v -> %v", before.LastConnected, after.LastConnected)
		}
	})

	t.Run("listing is scoped to one tenant", func(t *testing.T) {
		if err := store.Put(ctx, row("globex", "vendor", "https://other.test/mcp"), []byte("x")); err != nil {
			t.Fatalf("Put: %v", err)
		}
		acme, err := store.List(ctx, "acme")
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		for _, s := range acme {
			if s.Tenant != "acme" {
				t.Fatalf("another org's row is in acme's list: %+v", s)
			}
		}
		// The same NAME in two orgs is two different servers, and neither is
		// reachable from the other.
		globex, _ := store.List(ctx, "globex")
		if len(globex) != 1 || globex[0].URL != "https://other.test/mcp" {
			t.Fatalf("globex list = %+v", globex)
		}
		all, err := store.ListAll(ctx)
		if err != nil {
			t.Fatalf("ListAll: %v", err)
		}
		if len(all) < len(acme)+len(globex) {
			t.Fatalf("ListAll returned %d rows, fewer than the per-tenant lists", len(all))
		}
	})

	t.Run("delete removes the credential too", func(t *testing.T) {
		if err := store.Delete(ctx, "globex", "vendor"); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		if _, err := store.SealedToken(ctx, "globex", "vendor"); !errors.Is(err, ErrMCPServerNotFound) {
			t.Fatalf("err = %v, want the credential gone with the row", err)
		}
	})
}

func TestMemMCPServerStore_Contract(t *testing.T) {
	mcpServerStoreContract(t, NewMemMCPServerStore())
}

func TestPgMCPServerStore_Contract(t *testing.T) {
	pool := pgRunnerPool(t)
	ctx := context.Background()
	store, err := NewPgMCPServerStore(ctx, pool)
	if err != nil {
		t.Fatalf("NewPgMCPServerStore: %v", err)
	}
	if _, err := pool.Exec(ctx, "TRUNCATE tenant_mcp_servers"); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	mcpServerStoreContract(t, store)
}

// A registration has to survive the process that created it: the org's flows
// reference mcp:<server>:<tool> by id, so forgetting a server on restart does
// not degrade those flows, it breaks them.
func TestPgMCPServerStore_SurvivesARestart(t *testing.T) {
	pool := pgRunnerPool(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, "TRUNCATE tenant_mcp_servers"); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	first, err := NewPgMCPServerStore(ctx, pool)
	if err != nil {
		t.Fatalf("NewPgMCPServerStore: %v", err)
	}
	at := time.Now().UTC().Truncate(time.Millisecond)
	if err := first.Put(ctx, MCPServer{
		Tenant: "acme", Name: "vendor", URL: "https://vendor.test/mcp",
		AuthKind: MCPAuthBearer, Enabled: true, CreatedAt: at, UpdatedAt: at,
	}, []byte("sealed")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	// A second store over the same pool is what the next boot sees.
	second, err := NewPgMCPServerStore(ctx, pool)
	if err != nil {
		t.Fatalf("re-open: %v", err)
	}
	got, err := second.Get(ctx, "acme", "vendor")
	if err != nil {
		t.Fatalf("Get after restart: %v", err)
	}
	if got.URL != "https://vendor.test/mcp" || !got.Enabled {
		t.Fatalf("row did not survive: %+v", got)
	}
	blob, _ := second.SealedToken(ctx, "acme", "vendor")
	if string(blob) != "sealed" {
		t.Fatalf("credential did not survive: %q", blob)
	}
}
