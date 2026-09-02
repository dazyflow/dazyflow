// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dazyflow/dazyflow/auth"
	"github.com/dazyflow/dazyflow/core"
)

// recordingOrgProfiles captures the last PutOrgProfile so the create-org
// test can assert the new org's name was seeded.
type recordingOrgProfiles struct {
	saved map[string]auth.OrgProfile
}

func newRecordingOrgProfiles() *recordingOrgProfiles {
	return &recordingOrgProfiles{saved: map[string]auth.OrgProfile{}}
}

func (r *recordingOrgProfiles) GetOrgProfile(_ context.Context, tenant string) (auth.OrgProfile, error) {
	if p, ok := r.saved[tenant]; ok {
		return p, nil
	}
	return auth.OrgProfile{}, auth.ErrUnknownOrgProfile
}
func (r *recordingOrgProfiles) PutOrgProfile(_ context.Context, p auth.OrgProfile) error {
	// Mirror the Postgres unique index on subdomain: a non-empty label held by
	// a DIFFERENT tenant is a conflict.
	if p.Subdomain != "" {
		for tid, ex := range r.saved {
			if tid != p.Tenant && strings.EqualFold(ex.Subdomain, p.Subdomain) {
				return auth.ErrSubdomainTaken
			}
		}
	}
	r.saved[p.Tenant] = p
	return nil
}
func (r *recordingOrgProfiles) ListOrgProfiles(_ context.Context, tenants []string) (map[string]auth.OrgProfile, error) {
	out := map[string]auth.OrgProfile{}
	for _, tid := range tenants {
		if p, ok := r.saved[tid]; ok {
			out[tid] = p
		}
	}
	return out, nil
}
func (r *recordingOrgProfiles) GetOrgProfileBySubdomain(_ context.Context, subdomain string) (auth.OrgProfile, error) {
	for _, p := range r.saved {
		if p.Subdomain != "" && strings.EqualFold(p.Subdomain, subdomain) {
			return p, nil
		}
	}
	return auth.OrgProfile{}, auth.ErrUnknownOrgProfile
}
func (r *recordingOrgProfiles) DeleteOrgProfile(_ context.Context, tenant string) error {
	delete(r.saved, tenant)
	return nil
}

// TestCreateOrg covers self-serve org creation: the caller gets an org_<hex>
// tenant, an admin membership in it, and a seeded profile with the chosen
// name. Empty names are rejected.
func TestCreateOrg(t *testing.T) {
	t.Parallel()
	h := newGatewayHarness(t)
	mem := newFakeMembershipStore()
	prof := newRecordingOrgProfiles()
	h.gw.Memberships = mem
	h.gw.Profiles = prof

	rw := h.do(t, "POST", "/api/v1/me/orgs", map[string]any{"display_name": "Acme Inc"})
	if rw.Code != 200 {
		t.Fatalf("create org status = %d, body=%s", rw.Code, rw.Body.String())
	}
	var resp struct {
		Tenant      string `json:"tenant"`
		DisplayName string `json:"display_name"`
		Workspace   string `json:"workspace"`
	}
	if err := json.Unmarshal(rw.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !strings.HasPrefix(resp.Tenant, "org_") {
		t.Fatalf("tenant = %q, want org_<hex> prefix", resp.Tenant)
	}
	if resp.DisplayName != "Acme Inc" || resp.Workspace != "main" {
		t.Fatalf("resp = %+v, want name=Acme Inc workspace=main", resp)
	}

	// The caller (harness subject "alice") is now an admin member of the org.
	m, err := mem.GetMembership(t.Context(), "alice", resp.Tenant)
	if err != nil {
		t.Fatalf("expected membership for creator: %v", err)
	}
	if !hasRole(m.Roles, "admin") {
		t.Fatalf("creator roles = %+v, want admin", m.Roles)
	}

	// The profile carries the chosen display name.
	if p := prof.saved[resp.Tenant]; p.DisplayName != "Acme Inc" {
		t.Fatalf("saved profile name = %q, want Acme Inc", p.DisplayName)
	}

	// Empty name is a 400.
	if rw := h.do(t, "POST", "/api/v1/me/orgs", map[string]any{"display_name": "  "}); rw.Code != 400 {
		t.Fatalf("empty name status = %d, want 400", rw.Code)
	}
}

// TestExportOrg covers the export-first step: an org admin downloads the
// org's profile, members, and every flow's full graph.
func TestExportOrg(t *testing.T) {
	t.Parallel()
	h := newGatewayHarness(t)
	// A flow in the org's workspace (harness store is tenant "t" / ws "ws").
	if _, err := h.ws.Save(core.Graph{
		ID: "flow1", Nodes: []core.Node{{ID: "n1", Module: "noop"}},
	}, "root@t.example"); err != nil {
		t.Fatalf("save flow: %v", err)
	}
	mem := newFakeMembershipStore()
	seedMember(t, mem, "root@t.example", "t", core.TeamRoleAdmin())
	prof := newRecordingOrgProfiles()
	_ = prof.PutOrgProfile(t.Context(), auth.OrgProfile{Tenant: "t", DisplayName: "Acme"})
	h.gw.Memberships = mem
	h.gw.Profiles = prof

	// adminDo's token is tenant "t" with organization:admin → canManageOrg("t").
	rw := h.adminDo(t, "GET", "/api/v1/admin/orgs/t/export", nil)
	if rw.Code != 200 {
		t.Fatalf("export status = %d, body=%s", rw.Code, rw.Body.String())
	}
	var exp OrgExport
	if err := json.Unmarshal(rw.Body.Bytes(), &exp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if exp.Tenant != "t" || exp.DisplayName != "Acme" {
		t.Fatalf("export = %+v, want tenant=t name=Acme", exp)
	}
	if len(exp.Flows) != 1 || exp.Flows[0].ID != "flow1" || exp.Flows[0].Graph.ID != "flow1" {
		t.Fatalf("flows = %+v, want one flow1 with its graph", exp.Flows)
	}
	if exp.Flows[0].Workspace != "ws" {
		t.Fatalf("flow workspace = %q, want ws", exp.Flows[0].Workspace)
	}
	if len(exp.Members) != 1 || exp.Members[0].Email != "root@t.example" {
		t.Fatalf("members = %+v, want root@t.example", exp.Members)
	}
}

// TestDeleteOrg_RequiresPassword covers the step-up auth: a human (session)
// principal must re-enter the correct password to delete an org — missing or
// wrong password is rejected before any data is touched.
func TestDeleteOrg_RequiresPassword(t *testing.T) {
	t.Parallel()
	h := newGatewayHarness(t)
	users, _ := auth.OpenJSONUserStore("")
	hash, err := auth.HashPassword("correct horse battery")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	admin := auth.User{
		Subject: "boss@acme.com", Email: "boss@acme.com", PasswordHash: hash,
		Tenant: "delorg", Workspace: "main", Roles: []core.Role{core.TeamRoleAdmin()},
	}
	if err := users.PutUser(t.Context(), admin); err != nil {
		t.Fatalf("put user: %v", err)
	}
	sessions := auth.NewMemSessionStore()
	h.gw.Users = users
	h.gw.Sessions = sessions
	h.gw.Memberships = newFakeMembershipStore()
	// Extend the API-key-only chain with session auth so the token validates.
	h.svc.Auth = auth.Chain{
		&auth.APIKeyAuthenticator{Store: h.ks},
		&auth.SessionAuthenticator{Store: sessions},
	}
	_, tok, err := auth.IssueSession(t.Context(), sessions, admin, time.Hour)
	if err != nil {
		t.Fatalf("issue session: %v", err)
	}

	del := func(body any) *httptest.ResponseRecorder {
		var br *bytes.Buffer
		if body != nil {
			b, _ := json.Marshal(body)
			br = bytes.NewBuffer(b)
		} else {
			br = bytes.NewBuffer(nil)
		}
		req := httptest.NewRequest("DELETE", "/api/v1/admin/orgs/delorg?confirm=delorg", br)
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Content-Type", "application/json")
		rw := httptest.NewRecorder()
		ServeForTest(h.gw, rw, req)
		return rw
	}

	if rw := del(nil); rw.Code != http.StatusBadRequest {
		t.Fatalf("missing password: status = %d, want 400; body=%s", rw.Code, rw.Body.String())
	}
	if rw := del(map[string]any{"password": "wrong"}); rw.Code != http.StatusForbidden {
		t.Fatalf("wrong password: status = %d, want 403; body=%s", rw.Code, rw.Body.String())
	}
	if rw := del(map[string]any{"password": "correct horse battery"}); rw.Code != http.StatusOK {
		t.Fatalf("correct password: status = %d, want 200; body=%s", rw.Code, rw.Body.String())
	}
}

// TestDeleteOrg_BlocksApiKey verifies an API key — even one carrying
// organization:admin — cannot delete an org; it must go through an
// interactive session. The org-admin key passes authorization but is
// rejected with session_required before any data is touched.
func TestDeleteOrg_BlocksApiKey(t *testing.T) {
	t.Parallel()
	h := newGatewayHarness(t)
	h.gw.Users, _ = auth.OpenJSONUserStore("")
	h.gw.Memberships = newFakeMembershipStore()
	// adminDo authenticates with an organization:admin API key on tenant "t".
	rw := h.adminDo(t, "DELETE", "/api/v1/admin/orgs/t?confirm=t",
		map[string]any{"password": "whatever"})
	if rw.Code != http.StatusForbidden {
		t.Fatalf("api-key delete status = %d, want 403; body=%s", rw.Code, rw.Body.String())
	}
	if !strings.Contains(rw.Body.String(), "session_required") {
		t.Fatalf("expected session_required error, got %s", rw.Body.String())
	}
}

func hasRole(roles []core.Role, name string) bool {
	for _, r := range roles {
		if r.Name == name {
			return true
		}
	}
	return false
}

// TestCollectMemberships_HomeSurvivesSwitch is the regression for the
// "my home org disappeared after creating/switching orgs" bug: the home
// entry must come from the user record (user.Tenant), not the session's
// current tenant — otherwise switching into another org drops the home org
// from the switcher (it isn't a membership row).
func TestCollectMemberships_HomeSurvivesSwitch(t *testing.T) {
	t.Parallel()
	users, err := auth.OpenJSONUserStore("")
	if err != nil {
		t.Fatalf("open users: %v", err)
	}
	// Alice's home org is "klahr"; she also belongs to "org_new".
	if err := users.PutUser(t.Context(), auth.User{
		Email: "alice@klahr.se", Subject: "alice@klahr.se",
		Tenant: "klahr", Workspace: "main", Roles: []core.Role{core.TeamRoleAdmin()},
	}); err != nil {
		t.Fatalf("put user: %v", err)
	}
	mem := newFakeMembershipStore()
	seedMember(t, mem, "alice@klahr.se", "org_new", core.TeamRoleAdmin())

	h := &HTTPGateway{Users: users, Memberships: mem}

	// Session is currently active in the OTHER org (post-switch state).
	p := core.Principal{Subject: "alice@klahr.se", Tenant: "org_new", Workspace: "main"}
	got := h.authAPI().collectMemberships(t.Context(), p)

	seen := map[string]bool{}
	var home string
	for _, m := range got {
		seen[m.Tenant] = true
		if m.Home {
			home = m.Tenant
		}
	}
	if !seen["klahr"] {
		t.Fatalf("home org 'klahr' missing after switch; got %+v", got)
	}
	if !seen["org_new"] {
		t.Fatalf("'org_new' membership missing; got %+v", got)
	}
	if home != "klahr" {
		t.Fatalf("home entry = %q, want 'klahr' (the user's own tenant, not the session tenant)", home)
	}
}
