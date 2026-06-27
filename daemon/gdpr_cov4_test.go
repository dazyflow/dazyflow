// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"testing"

	"git.sr.ht/~klahr/dazyflow/auth"
	"git.sr.ht/~klahr/dazyflow/core"
)

// TestMergeErase_Cov covers mergeErase: counts sum, booleans OR, warnings
// concatenate.
func TestMergeErase_Cov(t *testing.T) {
	a := EraseReport{
		Sessions: 1, APIKeys: 2, Memberships: 3, Invitations: 4,
		AuditEvents: 5, Jobs: 6, RunLogs: 7, BusEvents: 8,
		WorkspaceWiped: true, Warnings: []string{"a"},
	}
	b := EraseReport{
		Sessions: 10, APIKeys: 20, Memberships: 30, Invitations: 40,
		AuditEvents: 50, Jobs: 60, RunLogs: 70, BusEvents: 80,
		SandboxWiped: true, OrgAuthDeleted: true, OrgProfileGone: true,
		Warnings: []string{"b"},
	}
	got := mergeErase(a, b)
	if got.Sessions != 11 || got.APIKeys != 22 || got.Memberships != 33 ||
		got.Invitations != 44 || got.AuditEvents != 55 || got.Jobs != 66 ||
		got.RunLogs != 77 || got.BusEvents != 88 {
		t.Fatalf("counts merged wrong: %+v", got)
	}
	if !got.WorkspaceWiped || !got.SandboxWiped || !got.OrgAuthDeleted || !got.OrgProfileGone {
		t.Fatalf("booleans not OR'd: %+v", got)
	}
	if len(got.Warnings) != 2 {
		t.Fatalf("warnings = %v, want 2", got.Warnings)
	}
}

// TestEraseReport_Warnf covers EraseReport.warnf.
func TestEraseReport_Warnf(t *testing.T) {
	var r EraseReport
	r.warnf("failed %s: %d", "thing", 42)
	if len(r.Warnings) != 1 || r.Warnings[0] != "failed thing: 42" {
		t.Fatalf("warnings = %v", r.Warnings)
	}
}

// TestTenantHasOtherMembers_Cov covers the helper's three legs: nil store,
// sole occupant, and a shared org.
func TestTenantHasOtherMembers_Cov(t *testing.T) {
	h := newGatewayHarness(t)

	// Nil Memberships store -> false (no others known).
	if h.gw.tenantHasOtherMembers(context.Background(), "acme", "a@acme.test") {
		t.Fatal("nil store should report no other members")
	}

	mem := newFakeMembershipStore()
	h.gw.Memberships = mem
	_ = mem.PutMembership(context.Background(), auth.Membership{
		UserEmail: "a@acme.test", Tenant: "acme", Roles: []core.Role{core.TeamRoleEditor()},
	})
	// Sole occupant -> false.
	if h.gw.tenantHasOtherMembers(context.Background(), "acme", "A@Acme.test") {
		t.Fatal("sole occupant should report no other members")
	}
	// Add a second member -> true.
	_ = mem.PutMembership(context.Background(), auth.Membership{
		UserEmail: "b@acme.test", Tenant: "acme", Roles: []core.Role{core.TeamRoleEditor()},
	})
	if !h.gw.tenantHasOtherMembers(context.Background(), "acme", "a@acme.test") {
		t.Fatal("shared org should report other members")
	}
}
