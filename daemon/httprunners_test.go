// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/engine"
)

// Who may register a runner, and what comes back out.
//
// The authz boundary is the load-bearing part. A runner receives Job.Params
// with secrets already resolved, so registering one grants the ability to
// receive every credential any flow in the org passes to that step. That is a
// larger power than editing a flow, and these tests exist to keep it from
// quietly becoming a graph:edit-level capability.

func runnerGateway(t *testing.T) (*HTTPGateway, *Runners) {
	t.Helper()
	rs := testRunners(t)
	cat := engine.NewRemoteCatalog()
	cat.DialTimeout = 250_000_000 // 250ms
	t.Cleanup(func() { _ = cat.Close() })
	gw := &HTTPGateway{
		logger:           log.New(&bytes.Buffer{}, "", 0),
		Runners:          rs,
		RunnerSupervisor: NewRunnerSupervisor(rs, cat),
	}
	return gw, rs
}

func principalWith(perms ...core.Permission) core.Principal {
	return core.Principal{
		Subject: "admin@acme", Tenant: "acme", Workspace: "main",
		Roles: []core.Role{{Name: "r", Permissions: perms}},
	}
}

func runnerBody(t *testing.T, endpoint string) []byte {
	t.Helper()
	r := sampleRunner(t, "acme", "invoices")
	body, err := json.Marshal(runnerRequest{
		Endpoint:      endpoint,
		ServerCAPEM:   string(r.ServerCAPEM),
		ClientCertPEM: string(r.ClientCertPEM),
		ClientKeyPEM:  string(r.ClientKeyPEM),
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return body
}

func put(t *testing.T, gw *HTTPGateway, p core.Principal, name string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/runners/"+name, bytes.NewReader(body))
	req.SetPathValue("name", name)
	rw := httptest.NewRecorder()
	gw.putRunner(rw, req, p)
	return rw
}

// graph:edit is explicitly not enough. Someone who can build flows must not be
// able to point the org's secrets at a host of their choosing.
func TestRunnerAPI_GraphEditIsNotEnough(t *testing.T) {
	gw, _ := runnerGateway(t)
	rw := put(t, gw, principalWith(core.PermGraphEdit, core.PermGraphRun),
		"invoices", runnerBody(t, "127.0.0.1:1"))
	if rw.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 — graph:edit must not register a runner", rw.Code)
	}
}

// Both of the intended capabilities work, and for different callers: a human
// administering the org, and an API key issued for automation.
func TestRunnerAPI_AcceptsOrgAdminAndModuleRegister(t *testing.T) {
	for _, perm := range []core.Permission{core.PermOrganizationAdmin, core.PermModuleRegister} {
		t.Run(string(perm), func(t *testing.T) {
			gw, _ := runnerGateway(t)
			rw := put(t, gw, principalWith(perm), "invoices", runnerBody(t, "127.0.0.1:1"))
			if rw.Code != http.StatusOK {
				t.Fatalf("status = %d (%s), want 200 for %q", rw.Code, rw.Body.String(), perm)
			}
		})
	}
}

// The private key goes in and must never come back.
//
// What actually holds this is structural, and worth stating so nobody thinks
// the handler is doing careful work it is not: runnerRow has no field for a
// key, and the store nils it on write. Both were checked by mutation — echoing
// the submitted runner straight back still leaks nothing, because there is
// nowhere in the wire type to put it.
//
// The test stays because the regression it DOES catch is the plausible one:
// someone adding a key field to runnerRow. Verified by doing exactly that.
func TestRunnerAPI_NeverReturnsTheClientKey(t *testing.T) {
	gw, _ := runnerGateway(t)
	body := runnerBody(t, "127.0.0.1:1")
	p := principalWith(core.PermOrganizationAdmin)

	rw := put(t, gw, p, "invoices", body)
	if rw.Code != http.StatusOK {
		t.Fatalf("PUT status = %d (%s)", rw.Code, rw.Body.String())
	}
	if strings.Contains(rw.Body.String(), "PRIVATE KEY") {
		t.Error("the write response echoed the private key back")
	}

	listRW := httptest.NewRecorder()
	gw.listRunners(listRW, httptest.NewRequest(http.MethodGet, "/api/v1/admin/runners", nil), p)
	if listRW.Code != http.StatusOK {
		t.Fatalf("GET status = %d (%s)", listRW.Code, listRW.Body.String())
	}
	if strings.Contains(listRW.Body.String(), "PRIVATE KEY") {
		t.Error("the list returned a private key")
	}
	// The certificate is public identity and SHOULD be there — otherwise this
	// test would pass just as well against a handler that returned nothing.
	if !strings.Contains(listRW.Body.String(), "BEGIN CERTIFICATE") {
		t.Error("the list omitted the certificate, which is public and useful")
	}
}

// A runner that will not connect must still be listed, with the reason.
func TestRunnerAPI_ListsAnUnreachableRunnerWithItsError(t *testing.T) {
	gw, _ := runnerGateway(t)
	p := principalWith(core.PermOrganizationAdmin)
	if rw := put(t, gw, p, "invoices", runnerBody(t, "127.0.0.1:1")); rw.Code != http.StatusOK {
		t.Fatalf("PUT status = %d (%s)", rw.Code, rw.Body.String())
	}

	rw := httptest.NewRecorder()
	gw.listRunners(rw, httptest.NewRequest(http.MethodGet, "/api/v1/admin/runners", nil), p)
	var out struct {
		Runners []runnerRow `json:"runners"`
	}
	if err := json.Unmarshal(rw.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Runners) != 1 {
		t.Fatalf("runners = %d, want the unreachable one to still be listed", len(out.Runners))
	}
	if out.Runners[0].State != RunnerUnreachable {
		t.Errorf("state = %q, want %q", out.Runners[0].State, RunnerUnreachable)
	}
	if out.Runners[0].Error == "" {
		t.Error("no error surfaced — the admin has nothing to act on")
	}
}

// One tenant's list must not contain another's runners.
func TestRunnerAPI_ListIsScopedToTheCallersTenant(t *testing.T) {
	gw, rs := runnerGateway(t)
	other := sampleRunner(t, "globex", "billing")
	other.Endpoint = "127.0.0.1:1"
	if err := rs.Put(t.Context(), other); err != nil {
		t.Fatalf("seed globex: %v", err)
	}
	p := principalWith(core.PermOrganizationAdmin) // tenant acme
	if rw := put(t, gw, p, "invoices", runnerBody(t, "127.0.0.1:1")); rw.Code != http.StatusOK {
		t.Fatalf("PUT status = %d", rw.Code)
	}

	rw := httptest.NewRecorder()
	gw.listRunners(rw, httptest.NewRequest(http.MethodGet, "/api/v1/admin/runners", nil), p)
	if strings.Contains(rw.Body.String(), "billing") {
		t.Error("acme's list contains globex's runner")
	}
}

// Deleting something that was never registered is a 404, not a silent success.
func TestRunnerAPI_DeleteUnknownIs404(t *testing.T) {
	gw, _ := runnerGateway(t)
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/admin/runners/ghost", nil)
	req.SetPathValue("name", "ghost")
	rw := httptest.NewRecorder()
	gw.deleteRunner(rw, req, principalWith(core.PermOrganizationAdmin))
	if rw.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rw.Code)
	}
}

// Bad material is the admin's typo, so it comes back as a 400 with the reason
// intact rather than a generic failure they cannot act on.
func TestRunnerAPI_RejectsBadMaterialWithAReason(t *testing.T) {
	gw, _ := runnerGateway(t)
	body, err := json.Marshal(runnerRequest{Endpoint: "127.0.0.1:1"}) // no certificates
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	rw := put(t, gw, principalWith(core.PermOrganizationAdmin), "invoices", body)
	if rw.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rw.Code)
	}
	if !strings.Contains(rw.Body.String(), "certificate") {
		t.Errorf("body = %s, want it to name what is missing", rw.Body.String())
	}
}

// The test endpoint stores nothing: an admin checking material before saving
// must not leave a broken registration behind on every failed attempt.
func TestRunnerAPI_TestStoresNothing(t *testing.T) {
	gw, rs := runnerGateway(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/runners/invoices/test",
		bytes.NewReader(runnerBody(t, "127.0.0.1:1")))
	req.SetPathValue("name", "invoices")
	rw := httptest.NewRecorder()
	gw.testRunner(rw, req, principalWith(core.PermOrganizationAdmin))

	// The probe reports failure in its body, not as an HTTP error: it answered
	// the question the admin asked.
	if rw.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 even for an unreachable runner", rw.Code)
	}
	var res probeResult
	if err := json.Unmarshal(rw.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if res.OK {
		t.Error("probe reported ok against a dead endpoint")
	}
	if res.Error == "" {
		t.Error("probe reported no reason")
	}
	// Identity is reported even when the connection fails — it comes from the
	// certificate the admin pasted, which is exactly what they are checking.
	if res.Subject == "" {
		t.Error("probe did not report the certificate subject")
	}
	if list, err := rs.Store.List(t.Context(), "acme"); err != nil {
		t.Fatalf("List: %v", err)
	} else if len(list) != 0 {
		t.Errorf("the test endpoint stored %d runner(s)", len(list))
	}
}

// A deployment without runner storage should say so, not panic.
func TestRunnerAPI_UnconfiguredIs501(t *testing.T) {
	gw := &HTTPGateway{logger: log.New(&bytes.Buffer{}, "", 0)}
	rw := httptest.NewRecorder()
	gw.listRunners(rw, httptest.NewRequest(http.MethodGet, "/api/v1/admin/runners", nil),
		principalWith(core.PermOrganizationAdmin))
	if rw.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501", rw.Code)
	}
}

// Authorization is checked before configuration: an unauthorized caller must
// not be able to probe whether the feature is switched on.
func TestRunnerAPI_AuthzBeforeConfigProbe(t *testing.T) {
	gw := &HTTPGateway{logger: log.New(&bytes.Buffer{}, "", 0)}
	rw := httptest.NewRecorder()
	gw.listRunners(rw, httptest.NewRequest(http.MethodGet, "/api/v1/admin/runners", nil),
		principalWith(core.PermGraphEdit))
	if rw.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 before any 501", rw.Code)
	}
}
