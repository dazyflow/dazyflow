// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"path/filepath"
	"testing"
)

// Tenant scoping is the whole contract of these two methods: a listing
// must return that tenant's invitations and an org deletion must remove
// only that tenant's rows. Asserting counts alone is not enough — an
// inverted predicate returns the complement, which can have a plausible
// size — so the tenant of every returned row is checked too.
func TestJSONInvitationStore_TenantScoping(t *testing.T) {
	path := filepath.Join(t.TempDir(), "invitations.json")
	s, err := OpenJSONInvitationStore(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	ctx := context.Background()
	for _, inv := range []Invitation{
		{Token: "t-acme-1", Tenant: "acme", Email: "a@acme.test"},
		{Token: "t-acme-2", Tenant: "acme", Email: "b@acme.test"},
		{Token: "t-other", Tenant: "other", Email: "c@other.test"},
	} {
		if err := s.PutInvitation(ctx, inv); err != nil {
			t.Fatalf("put %s: %v", inv.Token, err)
		}
	}

	got, err := s.ListByTenant(ctx, "acme")
	if err != nil {
		t.Fatalf("list acme: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("ListByTenant(acme) returned %d invitations, want 2", len(got))
	}
	for _, inv := range got {
		if inv.Tenant != "acme" {
			t.Errorf("ListByTenant(acme) returned a %q invitation (%s)", inv.Tenant, inv.Token)
		}
	}

	n, err := s.DeleteByTenant(ctx, "acme")
	if err != nil {
		t.Fatalf("delete acme: %v", err)
	}
	if n != 2 {
		t.Errorf("DeleteByTenant(acme) removed %d, want 2", n)
	}

	// The other tenant's invitation must survive the org deletion.
	rest, err := s.ListByTenant(ctx, "other")
	if err != nil {
		t.Fatalf("list other: %v", err)
	}
	if len(rest) != 1 || rest[0].Token != "t-other" {
		t.Errorf("other tenant holds %v, want just t-other", rest)
	}
}
