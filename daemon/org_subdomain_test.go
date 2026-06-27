// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"encoding/json"
	"testing"

	"git.sr.ht/~klahr/dazyflow/auth"
)

// setup wires an org-admin-capable harness with a recording profile store and
// the wildcard domain on, the precondition for the subdomain endpoints.
func newSubdomainHarness(t *testing.T) (*gatewayHarness, *recordingOrgProfiles) {
	t.Helper()
	h := newGatewayHarness(t)
	prof := newRecordingOrgProfiles()
	h.gw.Profiles = prof
	h.gw.WildcardDomain = "dazyflow.app"
	return h, prof
}

func TestOrgSubdomain_SetAndResolve(t *testing.T) {
	h, _ := newSubdomainHarness(t)

	// Claim a subdomain as the org admin (tenant "t").
	rw := h.adminDo(t, "PUT", "/api/v1/admin/org/subdomain", map[string]any{"subdomain": "Klahr"})
	if rw.Code != 200 {
		t.Fatalf("PUT subdomain = %d: %s", rw.Code, rw.Body.String())
	}
	var put struct {
		Subdomain string `json:"subdomain"`
	}
	_ = json.Unmarshal(rw.Body.Bytes(), &put)
	if put.Subdomain != "klahr" { // normalized lowercase
		t.Fatalf("stored subdomain = %q, want klahr", put.Subdomain)
	}

	// Public resolver maps the label back to the tenant.
	rw = h.do(t, "GET", "/api/v1/auth/resolve-subdomain?label=klahr", nil)
	if rw.Code != 200 {
		t.Fatalf("resolve = %d: %s", rw.Code, rw.Body.String())
	}
	var res struct {
		Tenant string `json:"tenant"`
	}
	_ = json.Unmarshal(rw.Body.Bytes(), &res)
	if res.Tenant != "t" {
		t.Fatalf("resolved tenant = %q, want t", res.Tenant)
	}

	// An unclaimed label is a 404.
	if rw := h.do(t, "GET", "/api/v1/auth/resolve-subdomain?label=nobody", nil); rw.Code != 404 {
		t.Errorf("resolve unclaimed = %d, want 404", rw.Code)
	}
}

func TestOrgSubdomain_PreservesNameAndIcon(t *testing.T) {
	h, prof := newSubdomainHarness(t)
	_ = prof.PutOrgProfile(t.Context(), auth.OrgProfile{Tenant: "t", DisplayName: "Acme", Icon: "rocket"})

	if rw := h.adminDo(t, "PUT", "/api/v1/admin/org/subdomain", map[string]any{"subdomain": "acme"}); rw.Code != 200 {
		t.Fatalf("PUT subdomain = %d: %s", rw.Code, rw.Body.String())
	}
	got, _ := prof.GetOrgProfile(t.Context(), "t")
	if got.DisplayName != "Acme" || got.Icon != "rocket" {
		t.Fatalf("name/icon clobbered: %+v", got)
	}
	if got.Subdomain != "acme" {
		t.Fatalf("subdomain = %q, want acme", got.Subdomain)
	}
}

func TestOrgSubdomain_RejectsInvalidAndReserved(t *testing.T) {
	h, _ := newSubdomainHarness(t)
	for _, bad := range []string{"WWW", "api", "has space", "under_score", "-x"} {
		rw := h.adminDo(t, "PUT", "/api/v1/admin/org/subdomain", map[string]any{"subdomain": bad})
		if rw.Code != 400 {
			t.Errorf("PUT %q = %d, want 400", bad, rw.Code)
		}
	}
}

func TestOrgSubdomain_TakenIsConflict(t *testing.T) {
	h, prof := newSubdomainHarness(t)
	// Another org already holds "taken".
	_ = prof.PutOrgProfile(t.Context(), auth.OrgProfile{Tenant: "other", Subdomain: "taken"})

	rw := h.adminDo(t, "PUT", "/api/v1/admin/org/subdomain", map[string]any{"subdomain": "taken"})
	if rw.Code != 409 {
		t.Fatalf("PUT taken subdomain = %d, want 409: %s", rw.Code, rw.Body.String())
	}
}

func TestOrgSubdomain_Availability(t *testing.T) {
	h, prof := newSubdomainHarness(t)
	_ = prof.PutOrgProfile(t.Context(), auth.OrgProfile{Tenant: "t", Subdomain: "mine"})
	_ = prof.PutOrgProfile(t.Context(), auth.OrgProfile{Tenant: "other", Subdomain: "theirs"})

	check := func(label string) (bool, string) {
		rw := h.adminDo(t, "GET", "/api/v1/admin/org/subdomain/available?label="+label, nil)
		var r struct {
			Available bool   `json:"available"`
			Reason    string `json:"reason"`
		}
		_ = json.Unmarshal(rw.Body.Bytes(), &r)
		return r.Available, r.Reason
	}

	if ok, _ := check("free"); !ok {
		t.Error("free label should be available")
	}
	if ok, reason := check("theirs"); ok || reason != "taken" {
		t.Errorf("other org's label: available=%v reason=%q, want false/taken", ok, reason)
	}
	if ok, reason := check("mine"); !ok || reason != "current" {
		t.Errorf("own label: available=%v reason=%q, want true/current", ok, reason)
	}
	if ok, reason := check("WWW"); ok || reason != "invalid" {
		t.Errorf("reserved label: available=%v reason=%q, want false/invalid", ok, reason)
	}
}

func TestOrgSubdomain_TLSAllow(t *testing.T) {
	h, prof := newSubdomainHarness(t)
	_ = prof.PutOrgProfile(t.Context(), auth.OrgProfile{Tenant: "t", Subdomain: "klahr"})

	// A claimed org host → 2xx (Caddy may issue a cert).
	if rw := h.do(t, "GET", "/api/v1/auth/tls-allow?domain=klahr.dazyflow.app", nil); rw.Code != 200 {
		t.Errorf("tls-allow claimed = %d, want 200", rw.Code)
	}
	// Unclaimed / reserved / off-domain hosts → 403 (no cert).
	for _, host := range []string{
		"nobody.dazyflow.app", // unclaimed
		"www.dazyflow.app",    // reserved label
		"klahr.evil.com",      // not our apex
		"a.b.dazyflow.app",    // multi-level (not a single label)
		"dazyflow.app",        // apex itself isn't a subdomain
	} {
		if rw := h.do(t, "GET", "/api/v1/auth/tls-allow?domain="+host, nil); rw.Code != 403 {
			t.Errorf("tls-allow %q = %d, want 403", host, rw.Code)
		}
	}
}

func TestOrgSubdomain_DisabledWithoutWildcard(t *testing.T) {
	h := newGatewayHarness(t)
	h.gw.Profiles = newRecordingOrgProfiles()
	// WildcardDomain left empty → feature off.
	if rw := h.adminDo(t, "PUT", "/api/v1/admin/org/subdomain", map[string]any{"subdomain": "x"}); rw.Code != 501 {
		t.Errorf("PUT with feature off = %d, want 501", rw.Code)
	}
}
