// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"

	"git.sr.ht/~klahr/dazyflow/auth"
	"git.sr.ht/~klahr/dazyflow/core"
)

// TestWorkspaceBrand_Cov covers workspaceBrand's branches: named org profile,
// personal-tenant fallback, and a named (non-personal) tenant fallback.
func TestWorkspaceBrand_Cov(t *testing.T) {
	svc := &Service{}

	// No OrgProfiles store: personal tenant drops its label; named tenant kept.
	if label, icon := svc.workspaceBrand(context.Background(), "usr_abc123"); label != "" || icon != "" {
		t.Fatalf("personal tenant brand = %q/%q, want empty", label, icon)
	}
	if label, _ := svc.workspaceBrand(context.Background(), "acme"); label != "acme" {
		t.Fatalf("named tenant brand = %q, want acme", label)
	}

	// With a profile carrying a display name + icon.
	profiles := newCovProfiles()
	svc.OrgProfiles = profiles
	_ = profiles.PutOrgProfile(context.Background(), auth.OrgProfile{
		Tenant: "acme", DisplayName: "Acme Inc", Icon: "rocket",
	})
	label, icon := svc.workspaceBrand(context.Background(), "acme")
	if label != "Acme Inc" || icon != "rocket" {
		t.Fatalf("profile brand = %q/%q, want Acme Inc/rocket", label, icon)
	}

	// A profile with only an icon (no display name) for a personal tenant:
	// icon survives, label is dropped.
	_ = profiles.PutOrgProfile(context.Background(), auth.OrgProfile{
		Tenant: "usr_x", Icon: "star",
	})
	label, icon = svc.workspaceBrand(context.Background(), "usr_x")
	if label != "" || icon != "star" {
		t.Fatalf("icon-only personal brand = %q/%q, want \"\"/star", label, icon)
	}
}

// TestShareError_Cov covers shareError's three status mappings.
func TestShareError_Cov(t *testing.T) {
	h := newGatewayHarness(t)

	rw := httptest.NewRecorder()
	h.gw.shareError(rw, core.ErrUnauthorized)
	if rw.Code != 403 {
		t.Fatalf("unauthorized = %d, want 403", rw.Code)
	}

	rw = httptest.NewRecorder()
	h.gw.shareError(rw, errors.New("share store not configured"))
	if rw.Code != 501 {
		t.Fatalf("not-configured = %d, want 501", rw.Code)
	}

	rw = httptest.NewRecorder()
	h.gw.shareError(rw, errors.New("disk on fire"))
	if rw.Code != 500 {
		t.Fatalf("generic = %d, want 500", rw.Code)
	}
}
