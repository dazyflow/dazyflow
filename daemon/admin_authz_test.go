package daemon

import (
	"net/http"
	"testing"

	"git.sr.ht/~klahr/dazyflow/auth"
	"git.sr.ht/~klahr/dazyflow/core"
)

// A tenant admin must not be able to reuse a key id that already belongs
// to another tenant: PutKey is an ON CONFLICT (id) upsert, so honoring
// the collision would silently hijack/overwrite the foreign tenant's key.
func TestIssueAPIKey_RejectsForeignTenantKeyID(t *testing.T) {
	ks := auth.NewMemKeyStore()
	svc := &Service{AdminKeys: ks}

	editor := core.Role{Name: "editor", Permissions: []core.Permission{core.PermGraphRun}}
	if _, _, err := auth.IssueAPIKey(ks, t.Context(), "shared-id", "other", "ws", "victim@other", []core.Role{editor}, nil); err != nil {
		t.Fatalf("seed victim key: %v", err)
	}

	admin := core.Principal{Subject: "root", Tenant: "t", Workspace: "ws",
		Roles: []core.Role{{Name: "admin", Permissions: []core.Permission{core.PermOrganizationAdmin}}}}
	if _, err := svc.IssueAPIKey(t.Context(), admin, IssueAPIKeyParams{
		ID: "shared-id", Subject: "attacker@t", Roles: []core.Role{editor},
	}); err == nil {
		t.Fatal("tenant admin was allowed to reuse a foreign tenant's key id")
	}

	// The victim's key must be untouched.
	got, err := ks.GetKey(t.Context(), "shared-id")
	if err != nil {
		t.Fatalf("victim key vanished: %v", err)
	}
	if got.Tenant != "other" || got.Subject != "victim@other" {
		t.Fatalf("victim key was clobbered: tenant=%q subject=%q", got.Tenant, got.Subject)
	}

	// Reusing an id within the caller's own tenant is still allowed (it's a
	// legitimate re-issue/rotation within their administrative scope).
	if _, _, err := auth.IssueAPIKey(ks, t.Context(), "own-id", "t", "ws", "u@t", []core.Role{editor}, nil); err != nil {
		t.Fatalf("seed own key: %v", err)
	}
	if _, err := svc.IssueAPIKey(t.Context(), admin, IssueAPIKeyParams{
		ID: "own-id", Subject: "u@t", Roles: []core.Role{editor},
	}); err != nil {
		t.Fatalf("re-issuing within own tenant should be allowed: %v", err)
	}
}

// RevokeAPIKey keys only on id (the store's WHERE has no tenant), so the
// service layer must scope the revoke to the caller's tenant — otherwise a
// tenant admin could revoke another tenant's key (cross-tenant DoS).
func TestRevokeAPIKey_TenantScoped(t *testing.T) {
	ks := auth.NewMemKeyStore()
	svc := &Service{AdminKeys: ks}

	editor := core.Role{Name: "editor", Permissions: []core.Permission{core.PermGraphRun}}
	if _, _, err := auth.IssueAPIKey(ks, t.Context(), "victim", "other", "ws", "v@other", []core.Role{editor}, nil); err != nil {
		t.Fatalf("seed victim key: %v", err)
	}

	admin := core.Principal{Subject: "root", Tenant: "t",
		Roles: []core.Role{{Name: "admin", Permissions: []core.Permission{core.PermOrganizationAdmin}}}}
	if err := svc.RevokeAPIKey(t.Context(), admin, "victim"); err == nil {
		t.Fatal("tenant admin was allowed to revoke a foreign tenant's key")
	}
	if k, _ := ks.GetKey(t.Context(), "victim"); k.RevokedAt != nil {
		t.Fatal("foreign key was revoked despite cross-tenant denial")
	}

	// A platform admin legitimately crosses tenant boundaries.
	pa := core.Principal{Subject: "op",
		Roles: []core.Role{{Name: "pa", Permissions: []core.Permission{core.PermPlatformAdmin}}}}
	if err := svc.RevokeAPIKey(t.Context(), pa, "victim"); err != nil {
		t.Fatalf("platform admin couldn't revoke: %v", err)
	}
	if k, _ := ks.GetKey(t.Context(), "victim"); k.RevokedAt == nil {
		t.Fatal("platform admin revoke didn't take effect")
	}
}

// createInvitation must cap the roles an inviter may grant: never the
// cross-tenant platform:admin role, and never a permission the inviter
// doesn't hold (otherwise an org admin self-invites platform:admin and
// breaks out of their tenant once switchOrg copies the roles into the
// session). The default-role path (no roles supplied) stays a trusted
// server-side grant.
func TestCreateInvitation_RejectsOverScopedRoles(t *testing.T) {
	h := newGatewayHarness(t)
	inv, err := auth.OpenJSONInvitationStore("") // empty path = in-memory
	if err != nil {
		t.Fatalf("open invitation store: %v", err)
	}
	h.gw.Invitations = inv

	// adminDo authenticates as an organization:admin token bound to "t".

	// 1) platform:admin is refused outright.
	rw := h.adminDo(t, "POST", "/api/v1/admin/invitations", map[string]any{
		"email": "newcomer@example.com",
		"roles": []map[string]any{{"name": "x", "permissions": []string{"platform:admin"}}},
	})
	if rw.Code != http.StatusForbidden {
		t.Fatalf("platform:admin invite: code=%d body=%s", rw.Code, rw.Body.String())
	}

	// 2) a permission the inviter doesn't hold (it only has organization:admin).
	rw = h.adminDo(t, "POST", "/api/v1/admin/invitations", map[string]any{
		"email": "newcomer@example.com",
		"roles": []map[string]any{{"name": "x", "permissions": []string{"secret:write"}}},
	})
	if rw.Code != http.StatusForbidden {
		t.Fatalf("over-scoped invite: code=%d body=%s", rw.Code, rw.Body.String())
	}

	// 3) the default-role invite (no roles) still succeeds.
	rw = h.adminDo(t, "POST", "/api/v1/admin/invitations", map[string]any{"email": "newcomer@example.com"})
	if rw.Code != http.StatusCreated {
		t.Fatalf("default invite should succeed: code=%d body=%s", rw.Code, rw.Body.String())
	}
}
