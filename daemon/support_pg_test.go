// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"git.sr.ht/~klahr/dazyflow/core"
)

// Gated on DAZYFLOW_TEST_DB (a real Postgres), like the jobstore/billing
// integration tests. Exercises the same lifecycle the in-memory tests cover, so
// the two impls stay behaviorally identical.
func TestPgGrantStore(t *testing.T) {
	url := os.Getenv("DAZYFLOW_TEST_DB")
	if url == "" {
		t.Skip("set DAZYFLOW_TEST_DB to run Postgres support tests")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(pool.Close)
	s, err := NewPgGrantStore(ctx, pool)
	if err != nil {
		t.Fatalf("NewPgGrantStore: %v", err)
	}
	if _, err := pool.Exec(ctx, "TRUNCATE access_grants"); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	now := time.Unix(1_700_000_000, 0).UTC()

	if err := s.Create(ctx, reqGrant("g1", "agent-a", now)); err != nil {
		t.Fatalf("create: %v", err)
	}
	// Duplicate create is rejected.
	if err := s.Create(ctx, reqGrant("g1", "agent-a", now)); !errors.Is(err, errGrantExists) {
		t.Errorf("duplicate create = %v, want errGrantExists", err)
	}
	// Requested → not active.
	if _, ok, _ := s.ActiveGrant(ctx, "agent-a", "acme", "daily-invoice", now); ok {
		t.Fatal("requested grant must not be active")
	}
	// Can't revoke a requested grant.
	if err := s.Revoke(ctx, "g1", "admin", now); !errors.Is(err, errGrantNotRevocable) {
		t.Errorf("revoke requested = %v, want errGrantNotRevocable", err)
	}
	// Approve with a 1h box.
	if err := s.Decide(ctx, "g1", core.GrantApproved, "admin-1", now, now.Add(time.Hour)); err != nil {
		t.Fatalf("decide approve: %v", err)
	}
	g, ok, _ := s.ActiveGrant(ctx, "agent-a", "acme", "daily-invoice", now)
	if !ok || g.DecidedBy != "admin-1" || !g.ExpiresAt.Equal(now.Add(time.Hour)) {
		t.Errorf("approved grant wrong: ok=%v %+v", ok, g)
	}
	// Past expiry → inactive.
	if _, ok, _ := s.ActiveGrant(ctx, "agent-a", "acme", "daily-invoice", now.Add(2*time.Hour)); ok {
		t.Error("expired grant must not be active")
	}
	// Double-decide rejected.
	if err := s.Decide(ctx, "g1", core.GrantDenied, "admin-2", now, now); !errors.Is(err, errGrantNotDecidable) {
		t.Errorf("double-decide = %v, want errGrantNotDecidable", err)
	}
	// Revoke ends it.
	if err := s.Revoke(ctx, "g1", "admin-1", now.Add(time.Minute)); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if _, ok, _ := s.ActiveGrant(ctx, "agent-a", "acme", "daily-invoice", now.Add(2*time.Minute)); ok {
		t.Error("revoked grant must not be active")
	}
	// Missing → ErrNotFound.
	if _, err := s.Get(ctx, "nope"); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("get missing = %v, want ErrNotFound", err)
	}
	list, _ := s.ListForTenant(ctx, "acme")
	if len(list) != 1 {
		t.Errorf("list acme = %d, want 1", len(list))
	}
}

func TestPgBundleStore(t *testing.T) {
	url := os.Getenv("DAZYFLOW_TEST_DB")
	if url == "" {
		t.Skip("set DAZYFLOW_TEST_DB to run Postgres support tests")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(pool.Close)
	s, err := NewPgBundleStore(ctx, pool)
	if err != nil {
		t.Fatalf("NewPgBundleStore: %v", err)
	}
	if _, err := pool.Exec(ctx, "TRUNCATE support_bundles"); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	now := time.Unix(1_700_000_000, 0).UTC()

	// Build a real redacted record so the payload round-trips through BYTEA.
	graph := core.Graph{
		ID: "daily-invoice", Tenant: "acme", Workspace: "main",
		Nodes: []core.Node{{ID: "charge", Module: "stripe_create_customer",
			Params: map[string]any{"api_key": "sk_live_abcdefgh12345678"}}},
	}
	b := core.BuildSupportBundle(graph, nil, nil, core.RedactStructureOnly)
	rec, err := core.NewSupportBundleRecord("b1", "agent-a", now, b)
	if err != nil {
		t.Fatalf("NewSupportBundleRecord: %v", err)
	}
	if err := s.Create(ctx, rec); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s.Create(ctx, rec); !errors.Is(err, errBundleExists) {
		t.Errorf("duplicate = %v, want errBundleExists", err)
	}
	got, err := s.Get(ctx, "b1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.FlowID != "daily-invoice" || got.Mode != core.RedactStructureOnly || string(got.Payload) != string(rec.Payload) {
		t.Errorf("round-trip mismatch: %+v", got)
	}
	if _, err := s.Get(ctx, "missing"); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("get missing = %v, want ErrNotFound", err)
	}
	list, _ := s.ListForTenant(ctx, "acme")
	if len(list) != 1 {
		t.Errorf("list acme = %d, want 1", len(list))
	}
}
