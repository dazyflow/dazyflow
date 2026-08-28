// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestMemKeyStore_AdminMethods(t *testing.T) {
	ctx := context.Background()
	store := NewMemKeyStore()

	put := func(id, tenant, subject string) {
		t.Helper()
		if _, _, err := IssueAPIKey(store, ctx, id, tenant, "ws", subject, nil, nil); err != nil {
			t.Fatalf("IssueAPIKey(%s): %v", id, err)
		}
	}
	put("a1", "acme", "alice")
	put("a2", "acme", "bob")
	put("g1", "globex", "alice")

	// ListAll, sorted by ID.
	all, err := store.ListAll(ctx)
	if err != nil || len(all) != 3 {
		t.Fatalf("ListAll = %v, %v", all, err)
	}
	if all[0].ID != "a1" || all[1].ID != "a2" || all[2].ID != "g1" {
		t.Errorf("ListAll not sorted: %v", all)
	}

	// ListByTenant.
	if list, _ := store.ListByTenant(ctx, "acme"); len(list) != 2 {
		t.Errorf("ListByTenant(acme) = %d, want 2", len(list))
	}
	if list, _ := store.ListByTenant(ctx, "nope"); len(list) != 0 {
		t.Errorf("ListByTenant(nope) = %d, want 0", len(list))
	}

	// ListBySubject (alice has keys in two tenants).
	if list, _ := store.ListBySubject(ctx, "alice"); len(list) != 2 {
		t.Errorf("ListBySubject(alice) = %d, want 2", len(list))
	}

	// DeleteBySubject removes alice everywhere.
	if n, _ := store.DeleteBySubject(ctx, "alice"); n != 2 {
		t.Errorf("DeleteBySubject(alice) = %d, want 2", n)
	}
	if list, _ := store.ListBySubject(ctx, "alice"); len(list) != 0 {
		t.Errorf("alice keys remain after delete: %v", list)
	}

	// DeleteByTenant removes the remaining acme key.
	if n, _ := store.DeleteByTenant(ctx, "acme"); n != 1 {
		t.Errorf("DeleteByTenant(acme) = %d, want 1", n)
	}
	if all, _ := store.ListAll(ctx); len(all) != 0 {
		t.Errorf("store should be empty, got %v", all)
	}
}

func TestAPIKeyAuthenticate_MalformedCredentials(t *testing.T) {
	ctx := context.Background()
	auth := &APIKeyAuthenticator{Store: NewMemKeyStore()}

	for _, cred := range []string{
		"nopfx_k1_secret",  // wrong prefix
		"dzk_onlyid",       // no second segment
		"dzk__secret",      // empty id
		"dzk_k1_",          // empty secret
		"dzk_unknown_abcd", // unknown key id
		"dzk_k1_zzzz",      // non-hex secret (after a real key exists below)
	} {
		if _, err := auth.Authenticate(ctx, cred); !errors.Is(err, ErrInvalidCredential) {
			t.Errorf("Authenticate(%q) = %v, want ErrInvalidCredential", cred, err)
		}
	}
}

func TestAPIKeyAuthenticate_NonHexSecret(t *testing.T) {
	ctx := context.Background()
	store := NewMemKeyStore()
	if _, _, err := IssueAPIKey(store, ctx, "k1", "t", "ws", "u", nil, nil); err != nil {
		t.Fatalf("IssueAPIKey: %v", err)
	}
	auth := &APIKeyAuthenticator{Store: store}
	// Real key id but a non-hex secret → hex.DecodeString fails.
	if _, err := auth.Authenticate(ctx, "dzk_k1_zz"); !errors.Is(err, ErrInvalidCredential) {
		t.Errorf("non-hex secret err = %v", err)
	}
}

func TestAPIKeyAuthenticate_ClockExpiry(t *testing.T) {
	ctx := context.Background()
	store := NewMemKeyStore()
	exp := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	_, cleartext, err := IssueAPIKey(store, ctx, "k1", "t", "ws", "u", nil, &exp)
	if err != nil {
		t.Fatalf("IssueAPIKey: %v", err)
	}
	// Custom clock set past expiry → rejected.
	expired := &APIKeyAuthenticator{Store: store, Clock: func() time.Time { return exp.Add(time.Hour) }}
	if _, err := expired.Authenticate(ctx, cleartext); !errors.Is(err, ErrInvalidCredential) {
		t.Errorf("expired-by-clock err = %v", err)
	}
	// Clock before expiry → accepted.
	live := &APIKeyAuthenticator{Store: store, Clock: func() time.Time { return exp.Add(-time.Hour) }}
	if _, err := live.Authenticate(ctx, cleartext); err != nil {
		t.Errorf("live key rejected: %v", err)
	}
}

func TestValidateKeyID_Cov(t *testing.T) {
	if err := validateKeyID("ok-id-123"); err != nil {
		t.Errorf("valid id rejected: %v", err)
	}
	long := make([]byte, 65)
	for i := range long {
		long[i] = 'a'
	}
	for name, id := range map[string]string{
		"empty":      "",
		"too long":   string(long),
		"underscore": "a_b",
		"space":      "a b",
	} {
		if err := validateKeyID(id); err == nil {
			t.Errorf("validateKeyID(%s) accepted invalid id", name)
		}
	}
}
