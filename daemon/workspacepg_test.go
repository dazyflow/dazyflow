// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/workspace"
)

func pgWorkspacesFixture(t *testing.T) (*PgWorkspaces, string, string) {
	t.Helper()
	dsn := os.Getenv("DAZYFLOW_TEST_DB")
	if dsn == "" {
		t.Skip("set DAZYFLOW_TEST_DB")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := workspace.EnsurePgWorkspaceSchema(ctx, pool); err != nil {
		t.Fatal(err)
	}
	cache := t.TempDir()
	p := NewPgWorkspaces(pool)
	p.SetMirrorCache(cache)
	tenant := fmt.Sprintf("erase-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		for _, tbl := range []string{"flow_revisions", "flow_heads", "flow_envs"} {
			_, _ = pool.Exec(context.Background(), "DELETE FROM "+tbl+" WHERE tenant=$1", tenant)
		}
	})
	return p, cache, tenant
}

func seedMirroredFlow(t *testing.T, p *PgWorkspaces, tenant, cache string) {
	t.Helper()
	s, err := p.Open(tenant, "main")
	if err != nil {
		t.Fatal(err)
	}
	g := core.Graph{ID: "f1", Tenant: tenant, Workspace: "main",
		Nodes: []core.Node{{ID: "a", Module: "noop"}}}
	if _, err := s.Save(g, "u"); err != nil {
		t.Fatal(err)
	}
	// Pushing to a bogus remote still synthesizes the repository first, which
	// is what puts the org's flows on disk.
	_, _ = s.Push(context.Background(), "file:///nonexistent-remote", nil)
	if _, err := os.Stat(filepath.Join(cache, tenant, "main", ".git")); err != nil {
		t.Fatalf("expected a synthesized mirror on disk: %v", err)
	}
}

// The synthesized mirror is a full copy of an org's flows. Erasing the org must
// take it too, or Art. 17 leaves the content on disk.
func TestPgWorkspaces_EraseRemovesTheSynthesizedMirror(t *testing.T) {
	p, cache, tenant := pgWorkspacesFixture(t)
	seedMirroredFlow(t, p, tenant, cache)

	if err := p.RemoveTenant(tenant); err != nil {
		t.Fatalf("RemoveTenant: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cache, tenant)); !os.IsNotExist(err) {
		t.Fatalf("the erased org's mirror is still on disk: %v", err)
	}
	ids, err := p.Open(tenant, "main")
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := ids.ListGraphs(); len(got) != 0 {
		t.Fatalf("flows survived erasure: %v", got)
	}
}

// The other replicas' copies: each clears its own on the next sweep.
func TestPgWorkspaces_PruneMirrorCacheClearsErasedTenants(t *testing.T) {
	p, cache, tenant := pgWorkspacesFixture(t)
	seedMirroredFlow(t, p, tenant, cache)

	// A second replica with its own cache, which also mirrored this org.
	other := NewPgWorkspaces(p.pool)
	otherCache := t.TempDir()
	other.SetMirrorCache(otherCache)
	seedMirroredFlow(t, other, tenant, otherCache)

	// Erasure runs on the first replica only.
	if err := p.RemoveTenant(tenant); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(otherCache, tenant)); err != nil {
		t.Fatalf("the second replica's copy should still be there before its sweep: %v", err)
	}

	n, err := other.PruneMirrorCache(context.Background())
	if err != nil {
		t.Fatalf("PruneMirrorCache: %v", err)
	}
	if n != 1 {
		t.Fatalf("pruned %d tenants, want 1", n)
	}
	if _, err := os.Stat(filepath.Join(otherCache, tenant)); !os.IsNotExist(err) {
		t.Fatalf("the second replica kept the erased org's mirror: %v", err)
	}
}

// A live org's mirror must survive the sweep — including one whose flows have
// all been deleted, which still has rows.
func TestPgWorkspaces_PruneMirrorCacheKeepsLiveTenants(t *testing.T) {
	p, cache, tenant := pgWorkspacesFixture(t)
	seedMirroredFlow(t, p, tenant, cache)

	s, err := p.Open(tenant, "main")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Delete("f1", "u"); err != nil {
		t.Fatal(err)
	}

	n, err := p.PruneMirrorCache(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("pruned %d tenants, want 0 — an org that merely deleted its flows is not erased", n)
	}
	if _, err := os.Stat(filepath.Join(cache, tenant)); err != nil {
		t.Fatalf("a live org's mirror was removed: %v", err)
	}
}
