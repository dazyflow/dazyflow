// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"git.sr.ht/~klahr/dazyflow/auth"
	"git.sr.ht/~klahr/dazyflow/core"
)

func TestChildErrMessage_Cov(t *testing.T) {
	if got := childErrMessage(nil); got != "no error message" {
		t.Errorf("nil = %q", got)
	}
	je := &core.JobError{Code: "boom", Message: "kaboom"}
	if got := childErrMessage(je); got != je.Error() {
		t.Errorf("err = %q, want %q", got, je.Error())
	}
}

func TestExportHandler_Cov(t *testing.T) {
	user := auth.User{Subject: "ex@example.com", Email: "ex@example.com", Tenant: "home", Workspace: "main"}
	h, mem, _, tok := orgsSessionHarness(t, user)
	ctx := context.Background()
	_ = mem.PutMembership(ctx, auth.Membership{UserEmail: "ex@example.com", Tenant: "acme", Workspace: "ws"})

	// API-key credential is rejected (export requires a session).
	if rw := h.do(t, "GET", "/api/v1/me/export", nil); rw.Code != http.StatusForbidden {
		t.Fatalf("api-key export = %d, want 403", rw.Code)
	}

	// Session credential succeeds and the export is offered as a download.
	rw := sessionDo(t, h, tok, "GET", "/api/v1/me/export", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("export = %d: %s", rw.Code, rw.Body.String())
	}
	if cd := rw.Header().Get("Content-Disposition"); cd == "" {
		t.Error("missing Content-Disposition download header")
	}
	var exp DataExport
	if err := json.Unmarshal(rw.Body.Bytes(), &exp); err != nil {
		t.Fatalf("decode export: %v", err)
	}
	if exp.Profile.Email != "ex@example.com" {
		t.Fatalf("export profile email = %q", exp.Profile.Email)
	}
	if len(exp.Memberships) != 1 || exp.Memberships[0].Tenant != "acme" {
		t.Fatalf("export memberships = %+v", exp.Memberships)
	}

	// A session for an unknown email -> 404 (assembleExport's only hard error).
	ghost := auth.User{Subject: "ghost@example.com", Email: "ghost@example.com", Tenant: "x"}
	_, gtok, err := auth.IssueSession(ctx, h.gw.Sessions.(*auth.MemSessionStore), ghost, 3600*1e9)
	if err != nil {
		t.Fatalf("issue ghost session: %v", err)
	}
	if rw := sessionDo(t, h, gtok, "GET", "/api/v1/me/export", nil); rw.Code != http.StatusNotFound {
		t.Fatalf("ghost export = %d, want 404", rw.Code)
	}
}
