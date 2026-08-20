// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

// TestVerifyPassword_UpgradesLegacyCost covers the opportunistic re-hash: a
// user whose hash was minted at the old DefaultCost still logs in, and comes
// out of it stored at the current cost.
func TestVerifyPassword_UpgradesLegacyCost(t *testing.T) {
	ctx := context.Background()
	store, err := OpenJSONUserStore("")
	if err != nil {
		t.Fatalf("OpenJSONUserStore: %v", err)
	}
	const pw = "correct-horse-battery-staple"
	legacy, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("legacy hash: %v", err)
	}
	if got, _ := bcrypt.Cost(legacy); got >= PasswordHashCost {
		t.Skipf("bcrypt.DefaultCost (%d) is no longer below PasswordHashCost (%d)", got, PasswordHashCost)
	}
	if err := store.PutUser(ctx, User{
		Email: "legacy@acme.test", Subject: "legacy@acme.test",
		Tenant: "acme", Workspace: "main", PasswordHash: legacy,
	}); err != nil {
		t.Fatalf("PutUser: %v", err)
	}

	// The old hash must still verify — raising the cost cannot lock anyone out.
	if _, err := VerifyPassword(ctx, store, "legacy@acme.test", pw); err != nil {
		t.Fatalf("VerifyPassword with a legacy-cost hash: %v", err)
	}

	after, err := store.GetByEmail(ctx, "legacy@acme.test")
	if err != nil {
		t.Fatalf("GetByEmail: %v", err)
	}
	cost, err := bcrypt.Cost(after.PasswordHash)
	if err != nil {
		t.Fatalf("bcrypt.Cost after login: %v", err)
	}
	if cost != PasswordHashCost {
		t.Errorf("cost after login = %d, want %d", cost, PasswordHashCost)
	}
	// And the re-hashed credential still works on the next login.
	if _, err := VerifyPassword(ctx, store, "legacy@acme.test", pw); err != nil {
		t.Fatalf("VerifyPassword after upgrade: %v", err)
	}
	// A wrong password is still rejected after the upgrade.
	if _, err := VerifyPassword(ctx, store, "legacy@acme.test", "wrong"); err == nil {
		t.Error("wrong password accepted after upgrade")
	}
}

// TestNeedsPasswordRehash covers the predicate's edges.
func TestNeedsPasswordRehash(t *testing.T) {
	current, err := HashPassword("whatever-it-is")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if NeedsPasswordRehash(current) {
		t.Error("a freshly minted hash should not need re-hashing")
	}
	// Garbage isn't improvable by re-hashing, so it must report false rather
	// than sending the login path into a pointless write.
	if NeedsPasswordRehash([]byte("not-a-bcrypt-hash")) {
		t.Error("unparseable hash should report false")
	}
	if NeedsPasswordRehash(nil) {
		t.Error("nil hash should report false")
	}
}
