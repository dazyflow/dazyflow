// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dazyflow/dazyflow/core"
)

// The flow editor's view of the fleet. Its whole reason for existing is the
// permission it takes, so that is what these tests are about: an editor may
// fill in a step that targets a machine, and must therefore be able to see
// which machines there are — without being handed the admin endpoint that also
// mints credentials and deletes runners.

func targetsGateway(t *testing.T) *HTTPGateway {
	t.Helper()
	store := NewMemRunnerStore()
	rs := &Runners{Store: store}
	tok, err := rs.MintToken(context.Background(), "acme", "admin@acme", "")
	if err != nil {
		t.Fatalf("MintToken: %v", err)
	}
	if _, _, err := rs.Register(context.Background(), tok.Token, "invoices-box",
		[]string{"Linux", "build"}, "0.2.0"); err != nil {
		t.Fatalf("Register: %v", err)
	}
	return &HTTPGateway{Runners: rs}
}

func editorPrincipal(tenant string) core.Principal {
	return core.Principal{
		Subject: "editor-1",
		Tenant:  tenant,
		Roles:   []core.Role{{Name: "editor", Permissions: []core.Permission{core.PermGraphEdit}}},
	}
}

func listTargets(t *testing.T, h *HTTPGateway, p core.Principal) *httptest.ResponseRecorder {
	t.Helper()
	rw := httptest.NewRecorder()
	h.runnerAPI().listRunnerTargets(rw, httptest.NewRequest("GET", "/api/v1/runners", nil), p)
	return rw
}

func TestListRunnerTargets_AnEditorCanSeeTheMachinesItMayTarget(t *testing.T) {
	t.Parallel()
	h := targetsGateway(t)
	rw := listTargets(t, h, editorPrincipal("acme"))
	if rw.Code != 200 {
		t.Fatalf("code %d body %s", rw.Code, rw.Body)
	}
	var got struct{ Runners []runnerTargetRow }
	if err := json.Unmarshal(rw.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Runners) != 1 || got.Runners[0].Name != "invoices-box" {
		t.Fatalf("runners = %+v", got.Runners)
	}
	// The machine's own name is one of its tags, which is what lets a step pin
	// itself to one machine without a separate "which machine" field. Normalized
	// with the labels, because that is how a step has to spell a tag to match it
	// — a picker offering "Linux" would offer a value that never matches.
	if strings.Join(got.Runners[0].Tags, ",") != "build,invoices-box,linux" {
		t.Errorf("tags = %v, want the labels and the name, normalized", got.Runners[0].Tags)
	}
	if !got.Runners[0].Online {
		t.Error("a runner that just registered reads as offline")
	}
}

func TestListRunnerTargets_SaysNothingAboutAdministeringTheFleet(t *testing.T) {
	t.Parallel()
	h := targetsGateway(t)
	rw := listTargets(t, h, editorPrincipal("acme"))
	for _, leaked := range []string{"created_by", "created_at", "version", "last_seen"} {
		if strings.Contains(rw.Body.String(), leaked) {
			t.Errorf("the picker's answer carries %q: %s", leaked, rw.Body)
		}
	}
}

func TestListRunnerTargets_RefusesSomeoneWhoCannotEditFlows(t *testing.T) {
	t.Parallel()
	h := targetsGateway(t)
	viewer := core.Principal{
		Subject: "viewer-1", Tenant: "acme",
		Roles: []core.Role{{Name: "viewer", Permissions: []core.Permission{core.PermGraphRun}}},
	}
	if rw := listTargets(t, h, viewer); rw.Code != 403 {
		t.Errorf("code %d, want 403 for someone who cannot put a runner in a flow", rw.Code)
	}
}

// One org's machines must never appear in another's picker: names are unique
// only per organisation, so the tenant is the whole boundary.
func TestListRunnerTargets_IsScopedToTheCallersOrg(t *testing.T) {
	t.Parallel()
	h := targetsGateway(t)
	rw := listTargets(t, h, editorPrincipal("other-co"))
	if rw.Code != 200 {
		t.Fatalf("code %d body %s", rw.Code, rw.Body)
	}
	if strings.Contains(rw.Body.String(), "invoices-box") {
		t.Errorf("another org's machine leaked: %s", rw.Body)
	}
}

func TestListRunnerTargets_SaysRunnersAreNotConfigured(t *testing.T) {
	t.Parallel()
	h := &HTTPGateway{}
	rw := listTargets(t, h, editorPrincipal("acme"))
	if rw.Code != 501 {
		t.Errorf("code %d, want 501 when the deployment has no runners", rw.Code)
	}
}

func TestListRunnerTargets_AStaleMachineReadsAsOffline(t *testing.T) {
	t.Parallel()
	store := NewMemRunnerStore()
	long := time.Now().Add(-2 * RunnerOnlineWindow)
	rs := &Runners{Store: store, Now: func() time.Time { return long }}
	tok, err := rs.MintToken(context.Background(), "acme", "admin@acme", "")
	if err != nil {
		t.Fatalf("MintToken: %v", err)
	}
	if _, _, err := rs.Register(context.Background(), tok.Token, "old-laptop", nil, "0.1.0"); err != nil {
		t.Fatalf("Register: %v", err)
	}
	h := &HTTPGateway{Runners: &Runners{Store: store}}

	var got struct{ Runners []runnerTargetRow }
	rw := listTargets(t, h, editorPrincipal("acme"))
	if err := json.Unmarshal(rw.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Runners) != 1 || got.Runners[0].Online {
		t.Errorf("runners = %+v, want the stale one listed and offline", got.Runners)
	}
}

func setLabels(t *testing.T, h *HTTPGateway, p core.Principal, name, body string) *httptest.ResponseRecorder {
	t.Helper()
	rw := httptest.NewRecorder()
	req := httptest.NewRequest("PUT", "/api/v1/admin/runners/"+name+"/labels", strings.NewReader(body))
	req.SetPathValue("name", name)
	h.runnerAPI().setRunnerLabels(rw, req, p)
	return rw
}

func TestSetRunnerLabels_RetagsAMachineWithoutVisitingIt(t *testing.T) {
	t.Parallel()
	h := targetsGateway(t)
	audit := NewMemAuditLog()
	h.Audit = audit
	rw := setLabels(t, h, adminPrincipal("acme"), "invoices-box", `{"labels":[" Build ","linux","BUILD"]}`)
	if rw.Code != 200 {
		t.Fatalf("code %d body %s", rw.Code, rw.Body)
	}
	var got runnerRow
	if err := json.Unmarshal(rw.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if strings.Join(got.Labels, ",") != "build,linux" {
		t.Errorf("labels = %v, want them normalized and de-duplicated", got.Labels)
	}
	if got.Name != "invoices-box" || !got.Online {
		t.Errorf("row = %+v, want the whole updated runner", got)
	}

	events, err := audit.List(context.Background(), core.AuditQuery{Tenant: "acme"})
	if err != nil {
		t.Fatalf("audit list: %v", err)
	}
	var found bool
	for _, e := range events {
		if e.Action == "runner.labels" && e.Target == "invoices-box" && e.Detail == "build,linux" {
			found = true
		}
	}
	if !found {
		t.Errorf("retagging was not audited: %+v", events)
	}
}

func TestSetRunnerLabels_NeedsRunnerAdminRatherThanGraphEdit(t *testing.T) {
	t.Parallel()
	h := targetsGateway(t)
	if rw := setLabels(t, h, editorPrincipal("acme"), "invoices-box", `{"labels":["build"]}`); rw.Code != 403 {
		t.Errorf("code %d, want 403 for someone who can only edit flows", rw.Code)
	}
}

func TestSetRunnerLabels_RefusesALabelTheInstallCommandCouldNotExpress(t *testing.T) {
	t.Parallel()
	h := targetsGateway(t)
	rw := setLabels(t, h, adminPrincipal("acme"), "invoices-box", `{"labels":["a,b"]}`)
	if rw.Code != 400 {
		t.Fatalf("code %d body %s, want 400", rw.Code, rw.Body)
	}
	if !strings.Contains(rw.Body.String(), "comma") {
		t.Errorf("body = %s, want it to name the problem", rw.Body)
	}
}

func TestSetRunnerLabels_CannotReachAnotherOrgsMachine(t *testing.T) {
	t.Parallel()
	h := targetsGateway(t)
	if rw := setLabels(t, h, adminPrincipal("globex"), "invoices-box", `{"labels":["theirs"]}`); rw.Code != 404 {
		t.Errorf("code %d, want 404 across the tenant boundary", rw.Code)
	}
}

func TestSetRunnerLabels_ClearingThemIsAllowed(t *testing.T) {
	t.Parallel()
	h := targetsGateway(t)
	rw := setLabels(t, h, adminPrincipal("acme"), "invoices-box", `{"labels":[]}`)
	if rw.Code != 200 {
		t.Fatalf("code %d body %s", rw.Code, rw.Body)
	}
	var got runnerRow
	if err := json.Unmarshal(rw.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Labels) != 0 {
		t.Errorf("labels = %v, want none", got.Labels)
	}
}

// Work goes to whichever eligible machine polls first, so an offline machine is
// never sent anything — there is no claim-time preference to make. What CAN go
// wrong is a step whose machines are all switched off: it waits out the pickup
// grace and then fails, and for those thirty seconds a bare "waiting for a
// machine tagged build" reads like progress. These pin the difference.
func TestWaitingMessage_SaysHowManyOfTheMatchingMachinesAreOn(t *testing.T) {
	t.Parallel()
	now := time.Now()
	req := DispatchRequest{Tenant: "acme", Tags: []string{"build"}}
	matches := []Runner{
		{Name: "a", LastSeen: now},
		{Name: "b", LastSeen: now.Add(-2 * RunnerOnlineWindow)},
		{Name: "c", LastSeen: now},
	}
	got := waitingMessage(req, matches, now)
	if !strings.Contains(got, "2 of 3 switched on") {
		t.Errorf("message = %q, want it to count the machines that could take it", got)
	}
}

func TestWaitingMessage_SaysWhenNothingIsThereToTakeIt(t *testing.T) {
	t.Parallel()
	now := time.Now()
	req := DispatchRequest{Tenant: "acme", Tags: []string{"build", "gpu"}}
	stale := now.Add(-2 * RunnerOnlineWindow)

	got := waitingMessage(req, []Runner{{Name: "a", LastSeen: stale}, {Name: "b", LastSeen: stale}}, now)
	if !strings.Contains(got, "none of the 2 machines") || !strings.Contains(got, "will fail") {
		t.Errorf("message = %q, want it to say nothing is there", got)
	}
	if !strings.Contains(got, "build + gpu") {
		t.Errorf("message = %q, want the tags joined with +", got)
	}

	one := waitingMessage(req, []Runner{{Name: "render-01", LastSeen: stale}}, now)
	if !strings.Contains(one, "render-01") {
		t.Errorf("message = %q, want the one machine named", one)
	}
}

// A deployment with no runner registry reaches this too, and must not promise
// anything about who is there.
func TestWaitingMessage_PromisesNothingWhenTheFleetIsUnknown(t *testing.T) {
	t.Parallel()
	got := waitingMessage(DispatchRequest{Tags: []string{"build"}}, nil, time.Now())
	if got != "waiting for a machine tagged build" {
		t.Errorf("message = %q, want the plain form", got)
	}
}
