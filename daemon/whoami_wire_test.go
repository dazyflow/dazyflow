// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/dazyflow/dazyflow/core"
)

// TestWhoamiWireShapeUnchanged pins meResponse to the map[string]any it
// replaced, byte for byte.
//
// meResponse exists because marshalling a map was 39% of the CPU of the most
// repeated authenticated request in the product. The saving is only free if
// the bytes are identical, and that rests on a fact no compiler checks:
// encoding/json emits a map's keys sorted but a struct's fields in
// DECLARATION order, so meResponse's fields must stay in alphabetical order of
// their JSON names. Adding a field anywhere else silently reorders the
// response — this test is what makes that loud instead.
func TestWhoamiWireShapeUnchanged(t *testing.T) {
	roles := []core.Role{{Name: "admin", Permissions: []core.Permission{"flow:write", "flow:read"}}}
	perms := []core.Permission{"flow:write", "flow:read"}
	memberships := []orgMembershipDTO{
		{Tenant: "acme", DisplayName: "Acme", Icon: "rocket", Workspace: "main", Roles: roles, Home: true},
		{Tenant: "other", Workspace: "main", Roles: nil, Home: false},
	}

	got := meResponse{
		Subject: "person@example.com", Tenant: "acme", Workspace: "main",
		Roles: roles, Permissions: perms, Memberships: memberships,
		EmailVerified: true, VerificationPending: true,
		PublicBaseURL: "https://dazy.example.com", SupportContact: "help@example.com",
		SupportTicketsEnabled: true,
	}

	// The literal the handler used before the struct landed.
	want := map[string]any{
		"subject": "person@example.com", "tenant": "acme", "workspace": "main",
		"roles": roles, "permissions": perms, "memberships": memberships,
		"email_verified": true, "verification_pending": true,
		"public_base_url": "https://dazy.example.com", "support_contact": "help@example.com",
		"support_tickets_enabled": true,
	}

	gotJSON, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal struct: %v", err)
	}
	wantJSON, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal map: %v", err)
	}
	if string(gotJSON) != string(wantJSON) {
		t.Errorf("wire shape changed.\n map:    %s\n struct: %s", wantJSON, gotJSON)
	}

	// Every field must be exercised above, or a field left zero on both sides
	// would agree by being absent from both and the comparison would prove
	// nothing about it.
	v := reflect.ValueOf(got)
	for i := range v.NumField() {
		if v.Field(i).IsZero() {
			t.Errorf("meResponse.%s is zero in the fixture: give it a value, "+
				"or this test cannot tell whether it is on the wire",
				v.Type().Field(i).Name)
		}
	}
}
