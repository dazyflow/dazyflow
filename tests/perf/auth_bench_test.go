// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package perf

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/dazyflow/dazyflow/auth"
	"github.com/dazyflow/dazyflow/core"
	"github.com/jackc/pgx/v5/pgxpool"
)

// benchPool gives the auth benchmarks a real Postgres, because what they
// measure is round trips: an in-memory store would report the cost of a
// map lookup and hide the thing under test.
func benchPool(b *testing.B) (*pgxpool.Pool, context.Context) {
	b.Helper()
	url := os.Getenv("DAZYFLOW_TEST_DB")
	if url == "" {
		b.Skip("set DAZYFLOW_TEST_DB to run the auth benchmarks")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		b.Fatalf("pgxpool.New: %v", err)
	}
	b.Cleanup(pool.Close)
	if err := auth.EnsurePgAuthSchema(ctx, pool); err != nil {
		b.Fatalf("EnsurePgAuthSchema: %v", err)
	}
	if err := auth.EnsurePgOrgsSchema(ctx, pool); err != nil {
		b.Fatalf("EnsurePgOrgsSchema: %v", err)
	}
	return pool, ctx
}

// authFixture builds the chain cmd/dzd wires — session lookup behind the
// caching store, then the moderation gate — with one live user, org and
// session, and returns the session token a request would carry.
func authFixture(b *testing.B, ttl time.Duration) (auth.Authenticator, string) {
	pool, ctx := benchPool(b)

	users, err := auth.NewPgUserStore(ctx, pool)
	if err != nil {
		b.Fatalf("NewPgUserStore: %v", err)
	}
	orgs, err := auth.NewPgOrgProfileStore(ctx, pool)
	if err != nil {
		b.Fatalf("NewPgOrgProfileStore: %v", err)
	}
	sessions, err := auth.NewPgSessionStore(ctx, pool)
	if err != nil {
		b.Fatalf("NewPgSessionStore: %v", err)
	}

	const email = "bench@example.test"
	const tenant = "benchorg"
	u := auth.User{
		Email: email, Subject: email, Tenant: tenant, Workspace: "main",
		PasswordHash: []byte("x"),
		Roles:        []core.Role{{Name: "admin", Permissions: []core.Permission{core.PermOrganizationAdmin}}},
		CreatedAt:    time.Now().UTC(),
	}
	if err := users.PutUser(ctx, u); err != nil {
		b.Fatalf("PutUser: %v", err)
	}
	if err := orgs.PutOrgProfile(ctx, auth.OrgProfile{Tenant: tenant, DisplayName: "Bench Org"}); err != nil {
		b.Fatalf("PutOrgProfile: %v", err)
	}

	cached := auth.NewCachingSessionStore(sessions, 5*time.Second, 50_000)
	_, token, err := auth.IssueSession(ctx, cached, u, time.Hour)
	if err != nil {
		b.Fatalf("IssueSession: %v", err)
	}

	chain := auth.Chain{&auth.SessionAuthenticator{Store: cached}}
	return &auth.ModerationGate{
		Inner: chain, Users: users, Orgs: orgs, CacheTTL: ttl,
	}, token
}

// BenchmarkAuthenticateSession measures what every authenticated HTTP and
// gRPC request pays before its handler runs.
func BenchmarkAuthenticateSession(b *testing.B) {
	a, token := authFixture(b, 0)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		p, err := a.Authenticate(ctx, token)
		if err != nil {
			b.Fatalf("Authenticate: %v", err)
		}
		if p.Tenant == "" {
			b.Fatal("empty principal")
		}
	}
}

// BenchmarkAuthenticateSessionParallel is the same path under the
// concurrency a browser produces, where the pool is the contended resource.
func BenchmarkAuthenticateSessionParallel(b *testing.B) {
	a, token := authFixture(b, 0)
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		ctx := context.Background()
		for pb.Next() {
			if _, err := a.Authenticate(ctx, token); err != nil {
				b.Fatalf("Authenticate: %v", err)
			}
		}
	})
}

// BenchmarkAuthenticateSessionNoGate isolates the attribution: the same
// request with the moderation gate's two reads removed. The difference
// between this and BenchmarkAuthenticateSession is what the gate costs.
func BenchmarkAuthenticateSessionNoGate(b *testing.B) {
	a, token := authFixture(b, 0)
	inner := a.(*auth.ModerationGate).Inner
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if _, err := inner.Authenticate(ctx, token); err != nil {
			b.Fatalf("Authenticate: %v", err)
		}
	}
}

// BenchmarkAuthenticateSessionCached is the same chain with the
// moderation gate's memo window on — what a request pays once the two
// lockout reads stop being per-request round trips.
func BenchmarkAuthenticateSessionCached(b *testing.B) {
	a, token := authFixture(b, 15*time.Second)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		p, err := a.Authenticate(ctx, token)
		if err != nil {
			b.Fatalf("Authenticate: %v", err)
		}
		if p.Tenant == "" {
			b.Fatal("empty principal")
		}
	}
}

// BenchmarkAuthenticateSessionCachedParallel is the cached path under the
// concurrency a browser produces.
func BenchmarkAuthenticateSessionCachedParallel(b *testing.B) {
	a, token := authFixture(b, 15*time.Second)
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		ctx := context.Background()
		for pb.Next() {
			if _, err := a.Authenticate(ctx, token); err != nil {
				b.Fatalf("Authenticate: %v", err)
			}
		}
	})
}
