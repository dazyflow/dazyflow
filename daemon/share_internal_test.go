// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/dazyflow/dazyflow/auth"
	"github.com/dazyflow/dazyflow/core"
)

func TestWorkspaceBrand_Cov(t *testing.T) {
	t.Parallel()
	svc := &Service{}

	if label, icon := svc.workspaceBrand(context.Background(), "usr_abc123"); label != "" || icon != "" {
		t.Fatalf("personal tenant brand = %q/%q, want empty", label, icon)
	}
	if label, _ := svc.workspaceBrand(context.Background(), "acme"); label != "acme" {
		t.Fatalf("named tenant brand = %q, want acme", label)
	}

	profiles := newCovProfiles()
	svc.OrgProfiles = profiles
	_ = profiles.PutOrgProfile(context.Background(), auth.OrgProfile{
		Tenant: "acme", DisplayName: "Acme Inc", Icon: "rocket",
	})
	label, icon := svc.workspaceBrand(context.Background(), "acme")
	if label != "Acme Inc" || icon != "rocket" {
		t.Fatalf("profile brand = %q/%q, want Acme Inc/rocket", label, icon)
	}

	_ = profiles.PutOrgProfile(context.Background(), auth.OrgProfile{
		Tenant: "usr_x", Icon: "star",
	})
	label, icon = svc.workspaceBrand(context.Background(), "usr_x")
	if label != "" || icon != "star" {
		t.Fatalf("icon-only personal brand = %q/%q, want \"\"/star", label, icon)
	}
}

func TestShareError_Cov(t *testing.T) {
	t.Parallel()
	h := newGatewayHarness(t)

	rw := httptest.NewRecorder()
	h.gw.shareAPI().shareError(rw, core.ErrUnauthorized)
	if rw.Code != 403 {
		t.Fatalf("unauthorized = %d, want 403", rw.Code)
	}

	rw = httptest.NewRecorder()
	h.gw.shareAPI().shareError(rw, errors.New("share store not configured"))
	if rw.Code != 501 {
		t.Fatalf("not-configured = %d, want 501", rw.Code)
	}

	rw = httptest.NewRecorder()
	h.gw.shareAPI().shareError(rw, errors.New("disk on fire"))
	if rw.Code != 500 {
		t.Fatalf("generic = %d, want 500", rw.Code)
	}
}
