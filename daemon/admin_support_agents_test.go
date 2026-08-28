// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"strings"
	"testing"

	"git.sr.ht/~klahr/dazyflow/daemon/support"
)

func TestSupportAgentManagement(t *testing.T) {
	h := newGatewayHarness(t)
	h.gw.SupportAgents = support.NewMemAgentStore()

	// Grant (platform admin), email normalized.
	rw := h.platformDo(t, "POST", "/api/v1/admin/platform/support-agents", map[string]any{"email": "Sam@Vendor.com"})
	if rw.Code != 200 {
		t.Fatalf("grant: code %d body %s", rw.Code, rw.Body)
	}
	if !strings.Contains(rw.Body.String(), "sam@vendor.com") {
		t.Errorf("granted agent not in response (normalized?): %s", rw.Body)
	}
	if !h.gw.SupportAgents.Granted("sam@vendor.com") {
		t.Error("store should report the agent granted")
	}

	// List.
	lrw := h.platformDo(t, "GET", "/api/v1/admin/platform/support-agents", nil)
	if lrw.Code != 200 || !strings.Contains(lrw.Body.String(), "sam@vendor.com") {
		t.Fatalf("list: code %d body %s", lrw.Code, lrw.Body)
	}

	// A non-platform admin (org admin) is forbidden.
	arw := h.adminDo(t, "GET", "/api/v1/admin/platform/support-agents", nil)
	if arw.Code != 403 {
		t.Errorf("org admin should be forbidden, got %d", arw.Code)
	}

	// Bad email rejected.
	brw := h.platformDo(t, "POST", "/api/v1/admin/platform/support-agents", map[string]any{"email": "notanemail"})
	if brw.Code != 400 {
		t.Errorf("bad email should 400, got %d", brw.Code)
	}

	// Revoke.
	drw := h.platformDo(t, "DELETE", "/api/v1/admin/platform/support-agents/sam@vendor.com", nil)
	if drw.Code != 200 {
		t.Fatalf("revoke: code %d body %s", drw.Code, drw.Body)
	}
	if h.gw.SupportAgents.Granted("sam@vendor.com") {
		t.Error("revoked agent must not be granted")
	}
}

func TestSupportAgentManagement_Disabled(t *testing.T) {
	h := newGatewayHarness(t) // no SupportAgents wired
	rw := h.platformDo(t, "GET", "/api/v1/admin/platform/support-agents", nil)
	if rw.Code != 501 {
		t.Errorf("disabled should 501, got %d", rw.Code)
	}
}
