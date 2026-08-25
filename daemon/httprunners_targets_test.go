// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"git.sr.ht/~klahr/dazyflow/core"
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
	tok, err := rs.MintToken(context.Background(), "acme", "admin@acme")
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
	h.listRunnerTargets(rw, httptest.NewRequest("GET", "/api/v1/runners", nil), p)
	return rw
}

func TestListRunnerTargets_AnEditorCanSeeTheMachinesItMayTarget(t *testing.T) {
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
	// Normalized, because that is how a step has to spell a label to match one
	// — the picker offering "Linux" would offer a value that never matches.
	if strings.Join(got.Runners[0].Labels, ",") != "build,linux" {
		t.Errorf("labels = %v, want them as registration stores them", got.Runners[0].Labels)
	}
	// Just registered, so it has checked in: the picker says so while choosing,
	// which is the difference between a step that runs and one that waits and
	// then fails.
	if !got.Runners[0].Online {
		t.Error("a runner that just registered reads as offline")
	}
}

// The narrower shape is the point of the separate endpoint: an editor is told
// where work can go, not who administers the fleet.
func TestListRunnerTargets_SaysNothingAboutAdministeringTheFleet(t *testing.T) {
	h := targetsGateway(t)
	rw := listTargets(t, h, editorPrincipal("acme"))
	for _, leaked := range []string{"created_by", "created_at", "version", "last_seen"} {
		if strings.Contains(rw.Body.String(), leaked) {
			t.Errorf("the picker's answer carries %q: %s", leaked, rw.Body)
		}
	}
}

func TestListRunnerTargets_RefusesSomeoneWhoCannotEditFlows(t *testing.T) {
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
	h := targetsGateway(t)
	rw := listTargets(t, h, editorPrincipal("other-co"))
	if rw.Code != 200 {
		t.Fatalf("code %d body %s", rw.Code, rw.Body)
	}
	if strings.Contains(rw.Body.String(), "invoices-box") {
		t.Errorf("another org's machine leaked: %s", rw.Body)
	}
}

// A deployment without Postgres has no runners at all, and the picker degrades
// to a text box on the strength of this answer.
func TestListRunnerTargets_SaysRunnersAreNotConfigured(t *testing.T) {
	h := &HTTPGateway{}
	rw := listTargets(t, h, editorPrincipal("acme"))
	if rw.Code != 501 {
		t.Errorf("code %d, want 501 when the deployment has no runners", rw.Code)
	}
}

// The list is derived from check-ins rather than reported, so a machine that
// stopped polling has to fall out of "online" on its own.
func TestListRunnerTargets_AStaleMachineReadsAsOffline(t *testing.T) {
	store := NewMemRunnerStore()
	long := time.Now().Add(-2 * RunnerOnlineWindow)
	rs := &Runners{Store: store, Now: func() time.Time { return long }}
	tok, err := rs.MintToken(context.Background(), "acme", "admin@acme")
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
