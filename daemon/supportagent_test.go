// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"testing"
)

func TestMemSupportAgentStore_GrantRevoke(t *testing.T) {
	ctx := context.Background()
	s := NewMemSupportAgentStore()

	if s.Granted("agent@vendor.com") {
		t.Fatal("no grants yet")
	}
	if err := s.Grant(ctx, "Agent@Vendor.com", "operator-1"); err != nil {
		t.Fatalf("grant: %v", err)
	}
	// Lookup is case/space-insensitive (normalized like the platform-admin store).
	if !s.Granted("agent@vendor.com") || !s.Granted("  AGENT@vendor.com ") {
		t.Error("granted lookup should normalize email")
	}

	list, _ := s.List(ctx)
	if len(list) != 1 || list[0].GrantedBy != "operator-1" {
		t.Errorf("list wrong: %+v", list)
	}

	if err := s.Revoke(ctx, "agent@vendor.com"); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if s.Granted("agent@vendor.com") {
		t.Error("revoked agent must not be granted")
	}
}

func TestMemSupportAgentStore_EmptyEmail(t *testing.T) {
	ctx := context.Background()
	s := NewMemSupportAgentStore()
	if s.Granted("") {
		t.Error("empty email is never granted")
	}
	if err := s.Grant(ctx, "  ", "op"); err == nil {
		t.Error("granting an empty email should fail")
	}
}
