// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"bytes"
	"context"
	"errors"
	"log"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

func TestVerifyPassword_UpgradesLegacyCost(t *testing.T) {
	ctx := context.Background()
	store, err := OpenJSONUserStore("")
	if err != nil {
		t.Fatalf("OpenJSONUserStore: %v", err)
	}
	const pw = "correct-horse-battery-staple"
	legacy, err := bcrypt.GenerateFromPassword([]byte(pw), activeHashCost-1)
	if err != nil {
		t.Fatalf("legacy hash: %v", err)
	}
	if got, _ := bcrypt.Cost(legacy); got >= activeHashCost {
		t.Fatalf("legacy hash cost %d is not below activeHashCost %d", got, activeHashCost)
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
	if cost != activeHashCost {
		t.Errorf("cost after login = %d, want %d", cost, activeHashCost)
	}
	if _, err := VerifyPassword(ctx, store, "legacy@acme.test", pw); err != nil {
		t.Fatalf("VerifyPassword after upgrade: %v", err)
	}
	if _, err := VerifyPassword(ctx, store, "legacy@acme.test", "wrong"); err == nil {
		t.Error("wrong password accepted after upgrade")
	}
}

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

type putFailStore struct {
	UserStore
	err error
}

func (p putFailStore) PutUser(context.Context, User) error { return p.err }

// The cost upgrade is best-effort: a failure is logged and swallowed. Both
// directions matter and neither shows up in the return value, so the log is
// the only place the distinction is observable — operators watch for this
// warning, and a login that silently stops re-hashing is a real regression.
func TestVerifyPassword_UpgradeFailureIsLoggedAndSwallowed(t *testing.T) {
	ctx := context.Background()
	inner, err := OpenJSONUserStore("")
	if err != nil {
		t.Fatalf("OpenJSONUserStore: %v", err)
	}
	const pw = "correct-horse-battery-staple"
	legacy, err := bcrypt.GenerateFromPassword([]byte(pw), activeHashCost-1)
	if err != nil {
		t.Fatalf("legacy hash: %v", err)
	}
	if err := inner.PutUser(ctx, User{
		Email: "legacy@acme.test", Subject: "legacy@acme.test",
		Tenant: "acme", Workspace: "main", PasswordHash: legacy,
	}); err != nil {
		t.Fatalf("PutUser: %v", err)
	}

	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prev) })

	// The write is refused, so the re-hash fails: the login must still
	// succeed and the failure must reach the log.
	store := putFailStore{UserStore: inner, err: errors.New("read-only store")}
	if _, err := VerifyPassword(ctx, store, "legacy@acme.test", pw); err != nil {
		t.Fatalf("a failed re-hash must not fail the login: %v", err)
	}
	if !strings.Contains(buf.String(), "could not re-hash password") {
		t.Errorf("no warning logged for a failed re-hash; log = %q", buf.String())
	}

	// The write above never landed, so the stored hash is still legacy and
	// the next login upgrades it for real — which must be silent.
	buf.Reset()
	if _, err := VerifyPassword(ctx, inner, "legacy@acme.test", pw); err != nil {
		t.Fatalf("VerifyPassword: %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("a successful re-hash logged %q, want silence", buf.String())
	}
}
