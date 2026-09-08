// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dazyflow/dazyflow/core"
)

type flushRecorder struct{ *httptest.ResponseRecorder }

func (flushRecorder) Flush() {}

func TestSystemLogTail_Cov(t *testing.T) {
	t.Parallel()
	h := newGatewayHarness(t)

	rw := httptest.NewRecorder()
	h.gw.orgAPI().systemLogTail(rw, httptest.NewRequest("GET", "/api/v1/admin/system/log", nil),
		core.Principal{Subject: "u", Tenant: "t"})
	if rw.Code != 403 {
		t.Fatalf("non-admin = %d, want 403", rw.Code)
	}

	admin := core.Principal{Subject: "root", Roles: []core.Role{{
		Name: "platform", Permissions: []core.Permission{core.PermPlatformAdmin},
	}}}

	rw = httptest.NewRecorder()
	h.gw.orgAPI().systemLogTail(rw, httptest.NewRequest("GET", "/api/v1/admin/system/log", nil), admin)
	if rw.Code != 501 {
		t.Fatalf("no-logtail = %d, want 501; body=%s", rw.Code, rw.Body.String())
	}

	lt := NewLogTail(100)
	_, _ = lt.Write([]byte("first line\n"))
	_, _ = lt.Write([]byte("second line\n"))
	h.gw.LogTail = lt

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // ensure the live loop exits on the first select
	req := httptest.NewRequest("GET", "/api/v1/admin/system/log?tail=10", nil).WithContext(ctx)
	fr := flushRecorder{httptest.NewRecorder()}
	h.gw.orgAPI().systemLogTail(fr, req, admin)

	if fr.Code != 200 {
		t.Fatalf("backfill = %d, want 200", fr.Code)
	}
	body := fr.Body.String()
	if !strings.Contains(body, "first line") || !strings.Contains(body, "second line") {
		t.Fatalf("backfill body missing lines: %q", body)
	}

	ctx2, cancel2 := context.WithCancel(context.Background())
	cancel2()
	req2 := httptest.NewRequest("GET", "/api/v1/admin/system/log?tail=0", nil).WithContext(ctx2)
	fr2 := flushRecorder{httptest.NewRecorder()}
	h.gw.orgAPI().systemLogTail(fr2, req2, admin)
	if fr2.Code != 200 {
		t.Fatalf("no-backfill = %d, want 200", fr2.Code)
	}
}
