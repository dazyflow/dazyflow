// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/dazyflow/dazyflow/core"
)

// countingUsers / countingOrgs wrap the fakes with a read counter, which
// is what these tests actually assert on: the point of the cache is the
// round trips it removes, so "how many reads" is the behaviour, not an
// implementation detail.
type countingUsers struct {
	mu      sync.Mutex
	users   map[string]User
	reads   int
	failAll bool
}

func (c *countingUsers) GetByEmail(_ context.Context, email string) (User, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reads++
	if c.failAll {
		return User{}, errors.New("transient store failure")
	}
	u, ok := c.users[email]
	if !ok {
		return User{}, ErrUnknownUser
	}
	return u, nil
}
func (c *countingUsers) PutUser(_ context.Context, u User) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.users[u.Email] = u
	return nil
}
func (c *countingUsers) ListUsers(context.Context) ([]User, error) { return nil, nil }
func (c *countingUsers) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.reads
}

type countingOrgs struct {
	mu    sync.Mutex
	orgs  map[string]OrgProfile
	reads int
}

func (c *countingOrgs) GetOrgProfile(_ context.Context, tenant string) (OrgProfile, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reads++
	p, ok := c.orgs[tenant]
	if !ok {
		return OrgProfile{}, ErrUnknownOrgProfile
	}
	return p, nil
}
func (c *countingOrgs) PutOrgProfile(_ context.Context, p OrgProfile) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.orgs[p.Tenant] = p
	return nil
}
func (c *countingOrgs) ListOrgProfiles(context.Context, []string) (map[string]OrgProfile, error) {
	return nil, nil
}
func (c *countingOrgs) GetOrgProfileBySubdomain(context.Context, string) (OrgProfile, error) {
	return OrgProfile{}, ErrUnknownOrgProfile
}
func (c *countingOrgs) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.reads
}

func cachedGateFixture(ttl time.Duration) (*ModerationGate, *countingUsers, *countingOrgs) {
	const email, tenant = "alice@acme.test", "org_acme"
	users := &countingUsers{users: map[string]User{
		email: {Email: email, Subject: email, Status: StatusActive},
	}}
	orgs := &countingOrgs{orgs: map[string]OrgProfile{
		tenant: {Tenant: tenant, Status: StatusActive},
	}}
	g := &ModerationGate{
		Inner:    fakeAuthenticator{core.Principal{Subject: email, Tenant: tenant}},
		Users:    users,
		Orgs:     orgs,
		CacheTTL: ttl,
	}
	return g, users, orgs
}

func TestModerationCache_CollapsesRepeatedLookups(t *testing.T) {
	g, users, orgs := cachedGateFixture(time.Minute)
	ctx := context.Background()
	for range 50 {
		if _, err := g.Authenticate(ctx, "tok"); err != nil {
			t.Fatalf("Authenticate: %v", err)
		}
	}
	if users.count() != 1 || orgs.count() != 1 {
		t.Fatalf("50 requests should cost 1 read each, got users=%d orgs=%d",
			users.count(), orgs.count())
	}
}

func TestModerationCache_DisabledKeepsPerRequestReads(t *testing.T) {
	g, users, orgs := cachedGateFixture(0)
	ctx := context.Background()
	for range 5 {
		if _, err := g.Authenticate(ctx, "tok"); err != nil {
			t.Fatalf("Authenticate: %v", err)
		}
	}
	if users.count() != 5 || orgs.count() != 5 {
		t.Fatalf("caching off should read every request, got users=%d orgs=%d",
			users.count(), orgs.count())
	}
}

// A suspension applied on this instance must bite on the very next
// request, not after the window — this is the property the platform-admin
// handlers rely on when they call Invalidate.
func TestModerationCache_InvalidateEnforcesImmediately(t *testing.T) {
	g, users, orgs := cachedGateFixture(time.Hour)
	ctx := context.Background()
	if _, err := g.Authenticate(ctx, "tok"); err != nil {
		t.Fatalf("warm: %v", err)
	}

	u := users.users["alice@acme.test"]
	u.Status = StatusSuspended
	_ = users.PutUser(ctx, u)

	// Without invalidation the memo still says active.
	if _, err := g.Authenticate(ctx, "tok"); err != nil {
		t.Fatalf("within the window the memo should still pass: %v", err)
	}
	g.Invalidate("alice@acme.test", "")
	if _, err := g.Authenticate(ctx, "tok"); !errors.Is(err, ErrAccountSuspended) {
		t.Fatalf("after Invalidate want ErrAccountSuspended, got %v", err)
	}

	p := orgs.orgs["org_acme"]
	p.Status = StatusSuspended
	_ = orgs.PutOrgProfile(ctx, p)
	g.Invalidate("", "org_acme")
	if _, err := g.Authenticate(ctx, "tok"); !errors.Is(err, ErrAccountSuspended) {
		t.Fatalf("org suspend after Invalidate want ErrAccountSuspended, got %v", err)
	}
}

func TestModerationCache_ExpiresAfterTTL(t *testing.T) {
	g, users, _ := cachedGateFixture(time.Hour)
	ctx := context.Background()
	now := time.Now()
	g.initCaches()
	g.users.clock = func() time.Time { return now }
	g.orgs.clock = func() time.Time { return now }

	if _, err := g.Authenticate(ctx, "tok"); err != nil {
		t.Fatalf("warm: %v", err)
	}
	u := users.users["alice@acme.test"]
	u.Status = StatusSuspended
	_ = users.PutUser(ctx, u)

	now = now.Add(2 * time.Hour) // past the window, no Invalidate
	if _, err := g.Authenticate(ctx, "tok"); !errors.Is(err, ErrAccountSuspended) {
		t.Fatalf("past the window want ErrAccountSuspended, got %v", err)
	}
}

// A transient store error must fail open (as it did before the cache) but
// must NOT be remembered — otherwise one blip pins the gate open for the
// whole window.
func TestModerationCache_TransientErrorNotCached(t *testing.T) {
	g, users, _ := cachedGateFixture(time.Hour)
	ctx := context.Background()
	users.failAll = true
	if _, err := g.Authenticate(ctx, "tok"); err != nil {
		t.Fatalf("a store blip must fail open: %v", err)
	}
	users.failAll = false
	u := users.users["alice@acme.test"]
	u.Status = StatusSuspended
	_ = users.PutUser(ctx, u)
	if _, err := g.Authenticate(ctx, "tok"); !errors.Is(err, ErrAccountSuspended) {
		t.Fatalf("the blip must not have been cached, got %v", err)
	}
}

// An unknown user (every API-key service account) is a definitive answer
// and must be cached, or those principals pay a guaranteed-miss read on
// every request — the exact cost the cache exists to remove.
func TestModerationCache_UnknownSubjectIsCached(t *testing.T) {
	g, users, _ := cachedGateFixture(time.Hour)
	g.Inner = fakeAuthenticator{core.Principal{Subject: "svc-account", Tenant: "org_acme"}}
	ctx := context.Background()
	for range 10 {
		if _, err := g.Authenticate(ctx, "tok"); err != nil {
			t.Fatalf("Authenticate: %v", err)
		}
	}
	if users.count() != 1 {
		t.Fatalf("unknown subject should be read once, got %d", users.count())
	}
}

func TestModerationCache_BoundsEntries(t *testing.T) {
	g, _, _ := cachedGateFixture(time.Hour)
	g.CacheMax = 8
	ctx := context.Background()
	for i := range 100 {
		g.Inner = fakeAuthenticator{core.Principal{
			Subject: fmt.Sprintf("u%d@acme.test", i), Tenant: "org_acme",
		}}
		if _, err := g.Authenticate(ctx, "tok"); err != nil {
			t.Fatalf("Authenticate: %v", err)
		}
	}
	g.users.mu.Lock()
	n := len(g.users.items)
	g.users.mu.Unlock()
	if n > 8 {
		t.Fatalf("cache grew past CacheMax: %d entries", n)
	}
}

func TestModerationCache_ConcurrentUse(t *testing.T) {
	g, _, _ := cachedGateFixture(time.Minute)
	ctx := context.Background()
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 200 {
				if _, err := g.Authenticate(ctx, "tok"); err != nil {
					t.Errorf("Authenticate: %v", err)
					return
				}
				g.Invalidate("alice@acme.test", "org_acme")
			}
		}()
	}
	wg.Wait()
}
