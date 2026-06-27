package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"git.sr.ht/~klahr/dazyflow/auth"
	"git.sr.ht/~klahr/dazyflow/core"
)

// ---- test stores ----------------------------------------------------

// covProfiles is an in-memory OrgProfileStore that also implements
// ListAllOrgProfiles (which platformListOrgs needs but recordingOrgProfiles
// in createorg_test.go doesn't).
type covProfiles struct {
	rows map[string]auth.OrgProfile
}

func newCovProfiles() *covProfiles { return &covProfiles{rows: map[string]auth.OrgProfile{}} }

func (c *covProfiles) GetOrgProfile(_ context.Context, tenant string) (auth.OrgProfile, error) {
	if p, ok := c.rows[tenant]; ok {
		return p, nil
	}
	return auth.OrgProfile{}, auth.ErrUnknownOrgProfile
}
func (c *covProfiles) PutOrgProfile(_ context.Context, p auth.OrgProfile) error {
	c.rows[p.Tenant] = p
	return nil
}
func (c *covProfiles) ListOrgProfiles(_ context.Context, tenants []string) (map[string]auth.OrgProfile, error) {
	out := map[string]auth.OrgProfile{}
	for _, t := range tenants {
		if p, ok := c.rows[t]; ok {
			out[t] = p
		}
	}
	return out, nil
}
func (c *covProfiles) GetOrgProfileBySubdomain(_ context.Context, sub string) (auth.OrgProfile, error) {
	for _, p := range c.rows {
		if p.Subdomain != "" && strings.EqualFold(p.Subdomain, sub) {
			return p, nil
		}
	}
	return auth.OrgProfile{}, auth.ErrUnknownOrgProfile
}
func (c *covProfiles) DeleteOrgProfile(_ context.Context, tenant string) error {
	delete(c.rows, tenant)
	return nil
}
func (c *covProfiles) ListAllOrgProfiles(_ context.Context) ([]auth.OrgProfile, error) {
	out := make([]auth.OrgProfile, 0, len(c.rows))
	for _, p := range c.rows {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Tenant < out[j].Tenant })
	return out, nil
}

// covBlocklist is an in-memory BlocklistStore.
type covBlocklist struct {
	rows map[string]auth.Blocked
}

func newCovBlocklist() *covBlocklist { return &covBlocklist{rows: map[string]auth.Blocked{}} }

func (b *covBlocklist) IsBlocked(_ context.Context, email string) (bool, auth.Blocked, error) {
	e := auth.NormalizeBlockEmail(email)
	if bl, ok := b.rows[e]; ok {
		return true, bl, nil
	}
	if at := strings.LastIndex(e, "@"); at >= 0 {
		if bl, ok := b.rows[e[at+1:]]; ok {
			return true, bl, nil
		}
	}
	return false, auth.Blocked{}, nil
}
func (b *covBlocklist) Block(_ context.Context, bl auth.Blocked) error {
	b.rows[bl.Value] = bl
	return nil
}
func (b *covBlocklist) Unblock(_ context.Context, value string) error {
	delete(b.rows, value)
	return nil
}
func (b *covBlocklist) List(_ context.Context) ([]auth.Blocked, error) {
	out := make([]auth.Blocked, 0, len(b.rows))
	for _, bl := range b.rows {
		out = append(out, bl)
	}
	return out, nil
}

// covMemDropSwitch is an in-memory DropSwitchStore.
type covMemDropSwitch struct {
	rows map[string]DropSwitch // key dropID\x00tenant
}

func newCovMemDropSwitch() *covMemDropSwitch {
	return &covMemDropSwitch{rows: map[string]DropSwitch{}}
}
func (d *covMemDropSwitch) Disabled(dropID, tenant string) bool {
	if _, ok := d.rows[dropSwitchKey(dropID, "")]; ok {
		return true
	}
	_, ok := d.rows[dropSwitchKey(dropID, tenant)]
	return tenant != "" && ok
}
func (d *covMemDropSwitch) Disable(_ context.Context, sw DropSwitch) error {
	d.rows[dropSwitchKey(sw.DropID, sw.Tenant)] = sw
	return nil
}
func (d *covMemDropSwitch) Enable(_ context.Context, dropID, tenant string) error {
	delete(d.rows, dropSwitchKey(dropID, tenant))
	return nil
}
func (d *covMemDropSwitch) List(_ context.Context) ([]DropSwitch, error) {
	out := make([]DropSwitch, 0, len(d.rows))
	for _, sw := range d.rows {
		out = append(out, sw)
	}
	return out, nil
}

// covMemEntitlements is an in-memory EntitlementStore.
type covMemEntitlements struct {
	tiers map[string]Tier
	ents  map[string]TenantEntitlement
}

func newCovMemEntitlements() *covMemEntitlements {
	return &covMemEntitlements{
		tiers: map[string]Tier{
			"free": {ID: "free", Name: "Free", Plan: PlanFree, BuiltIn: true},
			"pro":  {ID: "pro", Name: "Pro", Plan: PlanPro, BuiltIn: true},
		},
		ents: map[string]TenantEntitlement{},
	}
}
func (e *covMemEntitlements) ListTiers(context.Context) ([]Tier, error) {
	out := make([]Tier, 0, len(e.tiers))
	for _, t := range e.tiers {
		out = append(out, t)
	}
	return out, nil
}
func (e *covMemEntitlements) GetTier(_ context.Context, id string) (Tier, bool) {
	t, ok := e.tiers[id]
	return t, ok
}
func (e *covMemEntitlements) PutTier(_ context.Context, t Tier) error {
	e.tiers[t.ID] = t
	return nil
}
func (e *covMemEntitlements) DeleteTier(_ context.Context, id string) error {
	if t, ok := e.tiers[id]; ok && t.BuiltIn {
		return errBuiltinTier
	}
	delete(e.tiers, id)
	return nil
}
func (e *covMemEntitlements) GetEntitlement(_ context.Context, tenant string) (TenantEntitlement, bool) {
	ent, ok := e.ents[tenant]
	return ent, ok
}
func (e *covMemEntitlements) PutEntitlement(_ context.Context, ent TenantEntitlement) error {
	e.ents[ent.Tenant] = ent
	return nil
}
func (e *covMemEntitlements) ListEntitlements(context.Context) ([]TenantEntitlement, error) {
	out := make([]TenantEntitlement, 0, len(e.ents))
	for _, ent := range e.ents {
		out = append(out, ent)
	}
	return out, nil
}

var errBuiltinTier = errBuiltin{}

type errBuiltin struct{}

func (errBuiltin) Error() string { return "cannot delete built-in tier" }

// platformRaw sends a raw (possibly malformed) JSON body with the
// platform-admin token so the handler is reached past auth.
func platformRaw(t *testing.T, h *gatewayHarness, method, path string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	if h.platformToken == "" {
		role := core.Role{Name: "platform", Permissions: []core.Permission{core.PermPlatformAdmin}}
		_, tok, err := auth.IssueAPIKey(h.ks, t.Context(), "k-platform", "", "", "op", []core.Role{role}, nil)
		if err != nil {
			t.Fatalf("issue platform key: %v", err)
		}
		h.platformToken = tok
	}
	req := httptest.NewRequest(method, path, bytes.NewBuffer(body))
	req.Header.Set("Authorization", "Bearer "+h.platformToken)
	req.Header.Set("Content-Type", "application/json")
	rw := httptest.NewRecorder()
	ServeForTest(h.gw, rw, req)
	return rw
}

// platformHarness wires every store the platform-admin handlers touch.
func platformHarness(t *testing.T) (*gatewayHarness, *covProfiles, *covBlocklist, *fakeMembershipStore, *covMemEntitlements, *covMemDropSwitch) {
	t.Helper()
	h := newGatewayHarness(t)
	users, _ := auth.OpenJSONUserStore("")
	prof := newCovProfiles()
	bl := newCovBlocklist()
	mem := newFakeMembershipStore()
	ent := newCovMemEntitlements()
	ds := newCovMemDropSwitch()
	sessions := auth.NewMemSessionStore()
	invites, _ := auth.OpenJSONInvitationStore("")

	h.gw.Users = users
	h.gw.Profiles = prof
	h.gw.Blocklist = bl
	h.gw.Memberships = mem
	h.gw.Sessions = sessions
	h.gw.Invitations = invites
	h.svc.Entitlements = ent
	h.gw.DropSwitches = ds
	return h, prof, bl, mem, ent, ds
}

// ---- user moderation ------------------------------------------------

func TestPlatformUsers_ListGetModerate(t *testing.T) {
	h, prof, bl, mem, _, _ := platformHarness(t)
	ctx := context.Background()
	_ = h.gw.Users.PutUser(ctx, auth.User{Email: "alice@example.com", Subject: "alice@example.com", Tenant: "acme"})
	_ = h.gw.Users.PutUser(ctx, auth.User{Email: "bob@example.com", Subject: "bob@example.com", Tenant: "acme"})
	_ = prof.PutOrgProfile(ctx, auth.OrgProfile{Tenant: "acme", DisplayName: "Acme Inc"})
	_ = mem.PutMembership(ctx, auth.Membership{UserEmail: "alice@example.com", Tenant: "acme"})

	// Non-platform-admin is forbidden.
	if rw := h.do(t, "GET", "/api/v1/admin/platform/users", nil); rw.Code != http.StatusForbidden {
		t.Fatalf("editor list users = %d, want 403", rw.Code)
	}

	// List as platform admin, sorted with tenant names resolved.
	rw := h.platformDo(t, "GET", "/api/v1/admin/platform/users", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("list users = %d: %s", rw.Code, rw.Body.String())
	}
	var lr struct {
		Users []platformUserDTO `json:"users"`
	}
	if err := json.Unmarshal(rw.Body.Bytes(), &lr); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(lr.Users) != 2 || lr.Users[0].Email != "alice@example.com" {
		t.Fatalf("users = %+v", lr.Users)
	}
	if lr.Users[0].TenantName != "Acme Inc" {
		t.Fatalf("tenant name = %q, want Acme Inc", lr.Users[0].TenantName)
	}

	// Get one user with memberships.
	rw = h.platformDo(t, "GET", "/api/v1/admin/platform/users/alice@example.com", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("get user = %d: %s", rw.Code, rw.Body.String())
	}
	// Unknown user -> 404.
	if rw := h.platformDo(t, "GET", "/api/v1/admin/platform/users/ghost@example.com", nil); rw.Code != http.StatusNotFound {
		t.Fatalf("get ghost = %d, want 404", rw.Code)
	}

	// Suspend bob, then unsuspend.
	rw = h.platformDo(t, "POST", "/api/v1/admin/platform/users/bob@example.com/suspend", map[string]any{"reason": "spam"})
	if rw.Code != http.StatusOK {
		t.Fatalf("suspend = %d: %s", rw.Code, rw.Body.String())
	}
	if u, _ := h.gw.Users.GetByEmail(ctx, "bob@example.com"); u.Status != auth.StatusSuspended {
		t.Fatalf("bob status = %q, want suspended", u.Status)
	}
	rw = h.platformDo(t, "POST", "/api/v1/admin/platform/users/bob@example.com/unsuspend", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("unsuspend = %d: %s", rw.Code, rw.Body.String())
	}
	if u, _ := h.gw.Users.GetByEmail(ctx, "bob@example.com"); u.Status != auth.StatusActive {
		t.Fatalf("bob status = %q, want active", u.Status)
	}

	// Ban bob by domain.
	rw = h.platformDo(t, "POST", "/api/v1/admin/platform/users/bob@example.com/ban", map[string]any{"reason": "abuse", "domain": true})
	if rw.Code != http.StatusOK {
		t.Fatalf("ban = %d: %s", rw.Code, rw.Body.String())
	}
	if blocked, _, _ := bl.IsBlocked(ctx, "anyone@example.com"); !blocked {
		t.Fatal("domain ban did not block example.com")
	}
}

// memPlatformAdmins is an in-memory PlatformAdminStore for handler tests.
type memPlatformAdmins struct{ set map[string]string }

func newMemPlatformAdmins() *memPlatformAdmins { return &memPlatformAdmins{set: map[string]string{}} }
func (m *memPlatformAdmins) Granted(email string) bool {
	_, ok := m.set[normalizeEmail(email)]
	return ok
}
func (m *memPlatformAdmins) Grant(_ context.Context, email, by string) error {
	m.set[normalizeEmail(email)] = by
	return nil
}
func (m *memPlatformAdmins) Revoke(_ context.Context, email string) error {
	delete(m.set, normalizeEmail(email))
	return nil
}
func (m *memPlatformAdmins) List(_ context.Context) ([]PlatformAdminGrant, error) {
	out := make([]PlatformAdminGrant, 0, len(m.set))
	for e, by := range m.set {
		out = append(out, PlatformAdminGrant{Email: e, GrantedBy: by})
	}
	return out, nil
}

func TestPlatformAdminGrantRevoke(t *testing.T) {
	h, _, _, _, _, _ := platformHarness(t)
	ctx := context.Background()
	grants := newMemPlatformAdmins()
	h.gw.PlatformAdminGrants = grants
	_ = h.gw.Users.PutUser(ctx, auth.User{Email: "carol@example.com", Subject: "carol@example.com", Tenant: "acme"})

	// Grant the runtime role.
	rw := h.platformDo(t, "POST", "/api/v1/admin/platform/users/carol@example.com/platform-admin", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("grant = %d: %s", rw.Code, rw.Body.String())
	}
	if !grants.Granted("carol@example.com") {
		t.Fatal("grant did not record carol")
	}
	// DTO now reflects effective-but-not-env admin.
	var gr struct {
		User platformUserDTO `json:"user"`
	}
	_ = json.Unmarshal(rw.Body.Bytes(), &gr)
	if !gr.User.PlatformAdmin || gr.User.PlatformAdminEnv {
		t.Fatalf("carol DTO = %+v, want platform_admin=true env=false", gr.User)
	}

	// Revoke it.
	rw = h.platformDo(t, "DELETE", "/api/v1/admin/platform/users/carol@example.com/platform-admin", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("revoke = %d: %s", rw.Code, rw.Body.String())
	}
	if grants.Granted("carol@example.com") {
		t.Fatal("revoke did not remove carol")
	}

	// Can't revoke an env-allowlist admin — must edit the env var instead.
	h.gw.PlatformAdmins = []string{"envadmin@example.com"}
	_ = h.gw.Users.PutUser(ctx, auth.User{Email: "envadmin@example.com", Subject: "envadmin@example.com"})
	if rw := h.platformDo(t, "DELETE", "/api/v1/admin/platform/users/envadmin@example.com/platform-admin", nil); rw.Code != http.StatusConflict {
		t.Fatalf("revoke env admin = %d, want 409", rw.Code)
	}

	// Can't revoke yourself (platform token subject is "op").
	if rw := h.platformDo(t, "DELETE", "/api/v1/admin/platform/users/op/platform-admin", nil); rw.Code != http.StatusBadRequest {
		t.Fatalf("self revoke = %d, want 400", rw.Code)
	}

	// Granting an unknown account is a 404.
	if rw := h.platformDo(t, "POST", "/api/v1/admin/platform/users/ghost@example.com/platform-admin", nil); rw.Code != http.StatusNotFound {
		t.Fatalf("grant ghost = %d, want 404", rw.Code)
	}
}

// With no grant store wired, the runtime grant/revoke endpoints return 501
// (the env allowlist still works).
func TestPlatformAdminGrant_NoStore(t *testing.T) {
	h, _, _, _, _, _ := platformHarness(t)
	_ = h.gw.Users.PutUser(context.Background(), auth.User{Email: "dave@example.com", Subject: "dave@example.com"})
	if rw := h.platformDo(t, "POST", "/api/v1/admin/platform/users/dave@example.com/platform-admin", nil); rw.Code != http.StatusNotImplemented {
		t.Fatalf("grant without store = %d, want 501", rw.Code)
	}
}

func TestPlatformUsers_ModerationGuards(t *testing.T) {
	h, _, _, _, _, _ := platformHarness(t)
	ctx := context.Background()
	h.gw.PlatformAdmins = []string{"op@example.com"}
	_ = h.gw.Users.PutUser(ctx, auth.User{Email: "op@example.com", Subject: "op@example.com"})

	// Can't moderate another platform admin.
	if rw := h.platformDo(t, "POST", "/api/v1/admin/platform/users/op@example.com/suspend", nil); rw.Code != http.StatusForbidden {
		t.Fatalf("moderate admin = %d, want 403", rw.Code)
	}
	// Can't moderate self (platform token subject is "op").
	if rw := h.platformDo(t, "POST", "/api/v1/admin/platform/users/op/suspend", nil); rw.Code != http.StatusBadRequest {
		t.Fatalf("moderate self = %d, want 400", rw.Code)
	}
	// Unknown account -> 404.
	if rw := h.platformDo(t, "POST", "/api/v1/admin/platform/users/ghost@example.com/suspend", nil); rw.Code != http.StatusNotFound {
		t.Fatalf("moderate ghost = %d, want 404", rw.Code)
	}
}

// ---- org moderation -------------------------------------------------

func TestPlatformOrgs_ListGetSuspendBan(t *testing.T) {
	h, prof, bl, mem, _, _ := platformHarness(t)
	ctx := context.Background()
	_ = prof.PutOrgProfile(ctx, auth.OrgProfile{Tenant: "acme", DisplayName: "Acme"})
	_ = mem.PutMembership(ctx, auth.Membership{UserEmail: "carol@acme.test", Tenant: "acme"})
	_ = h.gw.Users.PutUser(ctx, auth.User{Email: "carol@acme.test", Subject: "carol@acme.test", Tenant: "acme"})

	rw := h.platformDo(t, "GET", "/api/v1/admin/platform/orgs", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("list orgs = %d: %s", rw.Code, rw.Body.String())
	}
	var lr struct {
		Orgs []platformOrgDTO `json:"orgs"`
	}
	_ = json.Unmarshal(rw.Body.Bytes(), &lr)
	if len(lr.Orgs) != 1 || lr.Orgs[0].MemberCount != 1 {
		t.Fatalf("orgs = %+v", lr.Orgs)
	}

	// Get org detail (members + effective limits).
	rw = h.platformDo(t, "GET", "/api/v1/admin/platform/orgs/acme", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("get org = %d: %s", rw.Code, rw.Body.String())
	}
	// Detail for an org with no profile row synthesizes a minimal one.
	if rw := h.platformDo(t, "GET", "/api/v1/admin/platform/orgs/never-seen", nil); rw.Code != http.StatusOK {
		t.Fatalf("get unknown org = %d, want 200", rw.Code)
	}

	// Suspend then unsuspend.
	if rw := h.platformDo(t, "POST", "/api/v1/admin/platform/orgs/acme/suspend", map[string]any{"reason": "tos"}); rw.Code != http.StatusOK {
		t.Fatalf("suspend org = %d: %s", rw.Code, rw.Body.String())
	}
	if p, _ := prof.GetOrgProfile(ctx, "acme"); p.Status != auth.StatusSuspended {
		t.Fatalf("acme status = %q, want suspended", p.Status)
	}
	if rw := h.platformDo(t, "POST", "/api/v1/admin/platform/orgs/acme/unsuspend", nil); rw.Code != http.StatusOK {
		t.Fatalf("unsuspend org = %d", rw.Code)
	}

	// Ban org -> blocklists members.
	if rw := h.platformDo(t, "POST", "/api/v1/admin/platform/orgs/acme/ban", map[string]any{"reason": "fraud"}); rw.Code != http.StatusOK {
		t.Fatalf("ban org = %d: %s", rw.Code, rw.Body.String())
	}
	if blocked, _, _ := bl.IsBlocked(ctx, "carol@acme.test"); !blocked {
		t.Fatal("org ban did not blocklist member")
	}

	// Empty tenant in path is rejected by the suspend handler.
	if rw := h.platformDo(t, "POST", "/api/v1/admin/platform/orgs/%20/suspend", nil); rw.Code != http.StatusBadRequest {
		t.Fatalf("blank tenant = %d, want 400", rw.Code)
	}
}

// ---- drops (killswitch) --------------------------------------------

func TestPlatformDrops_ListDisableEnable(t *testing.T) {
	h, _, _, _, _, ds := platformHarness(t)

	// Pick a real drop id from the catalog.
	manifests := h.svc.Engine.Resolver.(interface {
		Manifests() map[string]core.Manifest
	}).Manifests()
	var dropID string
	for id := range manifests {
		dropID = id
		break
	}
	if dropID == "" {
		t.Skip("no manifests in catalog")
	}

	rw := h.platformDo(t, "GET", "/api/v1/admin/platform/drops", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("list drops = %d: %s", rw.Code, rw.Body.String())
	}

	// Disable globally.
	if rw := h.platformDo(t, "POST", "/api/v1/admin/platform/drops/"+dropID+"/disable", map[string]any{"reason": "exploit"}); rw.Code != http.StatusNoContent {
		t.Fatalf("disable = %d: %s", rw.Code, rw.Body.String())
	}
	if !ds.Disabled(dropID, "anyone") {
		t.Fatal("drop not disabled after switch")
	}

	// Disable for a single tenant too (exercises the @tenant target path).
	if rw := h.platformDo(t, "POST", "/api/v1/admin/platform/drops/"+dropID+"/disable", map[string]any{"tenant": "acme"}); rw.Code != http.StatusNoContent {
		t.Fatalf("disable tenant = %d", rw.Code)
	}

	// Enable global again.
	if rw := h.platformDo(t, "POST", "/api/v1/admin/platform/drops/"+dropID+"/enable", nil); rw.Code != http.StatusNoContent {
		t.Fatalf("enable = %d", rw.Code)
	}

	// Missing id -> 400 (use a path that maps to an empty id is hard; instead
	// test the nil-store branch via a fresh harness).
	noStore := newGatewayHarness(t)
	if rw := noStore.platformDo(t, "GET", "/api/v1/admin/platform/drops", nil); rw.Code != http.StatusNotImplemented {
		t.Fatalf("nil dropswitch list = %d, want 501", rw.Code)
	}
}

// ---- tiers & entitlements ------------------------------------------

func TestPlatformTiers_CRUD(t *testing.T) {
	h, _, _, _, ent, _ := platformHarness(t)

	rw := h.platformDo(t, "GET", "/api/v1/admin/platform/tiers", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("list tiers = %d: %s", rw.Code, rw.Body.String())
	}

	// Create a custom tier (id from body).
	if rw := h.platformDo(t, "POST", "/api/v1/admin/platform/tiers", map[string]any{"id": "team", "name": "Team", "plan": "pro"}); rw.Code != http.StatusOK {
		t.Fatalf("put tier = %d: %s", rw.Code, rw.Body.String())
	}
	if _, ok := ent.GetTier(context.Background(), "team"); !ok {
		t.Fatal("team tier not stored")
	}

	// Update via path id; client can't flip built-in.
	if rw := h.platformDo(t, "PUT", "/api/v1/admin/platform/tiers/free", map[string]any{"name": "Free Plus", "built_in": false}); rw.Code != http.StatusOK {
		t.Fatalf("update tier = %d", rw.Code)
	}
	if tr, _ := ent.GetTier(context.Background(), "free"); !tr.BuiltIn {
		t.Fatal("built_in flag was flipped by client")
	}

	// Empty id rejected.
	if rw := h.platformDo(t, "POST", "/api/v1/admin/platform/tiers", map[string]any{"name": "no id"}); rw.Code != http.StatusBadRequest {
		t.Fatalf("put tier no id = %d, want 400", rw.Code)
	}
	// Malformed JSON rejected.
	if rw := platformRaw(t, h, "POST", "/api/v1/admin/platform/tiers", []byte("{bad")); rw.Code != http.StatusBadRequest {
		t.Fatalf("put tier bad json = %d, want 400", rw.Code)
	}

	// Delete custom tier; deleting built-in errors.
	if rw := h.platformDo(t, "DELETE", "/api/v1/admin/platform/tiers/team", nil); rw.Code != http.StatusNoContent {
		t.Fatalf("delete tier = %d", rw.Code)
	}
	if rw := h.platformDo(t, "DELETE", "/api/v1/admin/platform/tiers/free", nil); rw.Code != http.StatusBadRequest {
		t.Fatalf("delete built-in = %d, want 400", rw.Code)
	}
}

func TestPlatformEntitlement_GetPut(t *testing.T) {
	h, _, _, _, ent, _ := platformHarness(t)

	rw := h.platformDo(t, "GET", "/api/v1/admin/platform/orgs/acme/entitlement", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("get ent = %d: %s", rw.Code, rw.Body.String())
	}

	// Put with a valid plan override.
	if rw := h.platformDo(t, "PUT", "/api/v1/admin/platform/orgs/acme/entitlement", map[string]any{"tier_id": "pro", "plan_override": "pro"}); rw.Code != http.StatusOK {
		t.Fatalf("put ent = %d: %s", rw.Code, rw.Body.String())
	}
	if got, ok := ent.GetEntitlement(context.Background(), "acme"); !ok || got.TierID != "pro" {
		t.Fatalf("entitlement = %+v ok=%v", got, ok)
	}

	// Invalid plan override rejected.
	if rw := h.platformDo(t, "PUT", "/api/v1/admin/platform/orgs/acme/entitlement", map[string]any{"plan_override": "platinum"}); rw.Code != http.StatusBadRequest {
		t.Fatalf("bad plan = %d, want 400", rw.Code)
	}
	// Malformed JSON rejected.
	if rw := platformRaw(t, h, "PUT", "/api/v1/admin/platform/orgs/acme/entitlement", []byte("nope")); rw.Code != http.StatusBadRequest {
		t.Fatalf("bad json = %d, want 400", rw.Code)
	}
}

// ---- cross-tenant invite -------------------------------------------

func TestPlatformInviteMember(t *testing.T) {
	h, _, _, _, _, _ := platformHarness(t)

	// Default roles + workspace.
	rw := h.platformDo(t, "POST", "/api/v1/admin/platform/orgs/acme/invite", map[string]any{"email": "newbie@example.com"})
	if rw.Code != http.StatusCreated {
		t.Fatalf("invite = %d: %s", rw.Code, rw.Body.String())
	}
	list, _ := h.gw.Invitations.ListByTenant(context.Background(), "acme")
	if len(list) != 1 || list[0].Email != "newbie@example.com" {
		t.Fatalf("invitations = %+v", list)
	}

	// Bad email rejected.
	if rw := h.platformDo(t, "POST", "/api/v1/admin/platform/orgs/acme/invite", map[string]any{"email": "not-an-email"}); rw.Code != http.StatusBadRequest {
		t.Fatalf("bad email = %d, want 400", rw.Code)
	}
	// Malformed JSON.
	if rw := platformRaw(t, h, "POST", "/api/v1/admin/platform/orgs/acme/invite", []byte("{")); rw.Code != http.StatusBadRequest {
		t.Fatalf("bad json = %d, want 400", rw.Code)
	}
}
