// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"errors"
	"testing"
	"time"

	"github.com/dazyflow/dazyflow/core"
)

func testMCPGrantLifecycle(t *testing.T, store MCPGrantStore) {
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Microsecond)
	g, access, refresh, err := IssueMCPGrant(ctx, store, MCPGrant{
		Subject: "alice@example.com", Tenant: "t", Workspace: "ws",
		Roles:      []core.Role{{Name: "editor", Permissions: []core.Permission{core.PermGraphEdit}}},
		ClientName: "Claude", RedirectHost: "claude.ai",
	}, now)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	authn := &MCPGrantAuthenticator{Store: store, Clock: func() time.Time { return now }}
	p, err := authn.Authenticate(ctx, access)
	if err != nil || p.Subject != "alice@example.com" || p.Tenant != "t" || !p.Has(core.PermGraphEdit) {
		t.Fatalf("authenticate: %+v, %v", p, err)
	}
	if _, err := authn.Authenticate(ctx, refresh); err == nil {
		t.Error("a refresh token must not authenticate as an access token")
	}
	if _, err := authn.Authenticate(ctx, access[:len(access)-1]+"0"); err == nil {
		t.Error("a tampered access token authenticated")
	}
	late := &MCPGrantAuthenticator{Store: store, Clock: func() time.Time { return now.Add(MCPAccessTTL + time.Second) }}
	if _, err := late.Authenticate(ctx, access); err == nil {
		t.Error("an expired access token authenticated")
	}

	_, access2, refresh2, err := RefreshMCPGrant(ctx, store, refresh, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if _, err := authn.Authenticate(ctx, access); err == nil {
		t.Error("the rotated-out access token still authenticates")
	}
	if _, err := authn.Authenticate(ctx, access2); err != nil {
		t.Errorf("new access token: %v", err)
	}

	// Replaying the rotated-out refresh token revokes the whole grant.
	if _, _, _, err := RefreshMCPGrant(ctx, store, refresh, now.Add(2*time.Minute)); !errors.Is(err, ErrMCPRefreshReused) {
		t.Fatalf("replay: err = %v, want ErrMCPRefreshReused", err)
	}
	if _, err := store.GetGrant(ctx, g.ID); err == nil {
		t.Error("grant survived refresh-token reuse")
	}
	if _, _, _, err := RefreshMCPGrant(ctx, store, refresh2, now.Add(3*time.Minute)); err == nil {
		t.Error("the newest refresh token still works after the grant was revoked")
	}

	if _, _, _, err := IssueMCPGrant(ctx, store, MCPGrant{Subject: "alice@example.com", Tenant: "t"}, now); err != nil {
		t.Fatal(err)
	}
	if gs, err := store.ListGrantsBySubject(ctx, "alice@example.com"); err != nil || len(gs) != 1 {
		t.Fatalf("list: %d, %v", len(gs), err)
	}
	if n, err := store.RevokeSubjectGrants(ctx, "alice@example.com"); err != nil || n != 1 {
		t.Fatalf("revoke subject: %d, %v", n, err)
	}
}

func TestMemMCPGrantStore_Lifecycle(t *testing.T) {
	testMCPGrantLifecycle(t, NewMemMCPGrantStore())
}

func TestPgMCPGrantStore_Lifecycle(t *testing.T) {
	pool, ctx := testPool(t)
	store, err := NewPgMCPGrantStore(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM mcp_oauth_grants WHERE subject='alice@example.com'`); err != nil {
		t.Fatal(err)
	}
	testMCPGrantLifecycle(t, store)
}

func TestRefreshMCPGrant_ExpiredRefreshIsRefused(t *testing.T) {
	store := NewMemMCPGrantStore()
	now := time.Now()
	_, _, refresh, err := IssueMCPGrant(t.Context(), store, MCPGrant{Subject: "a", Tenant: "t"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := RefreshMCPGrant(t.Context(), store, refresh, now.Add(MCPRefreshTTL+time.Second)); err == nil {
		t.Fatal("expired refresh token was honoured")
	}
}
