// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"git.sr.ht/~klahr/dazyflow/auth"
	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/daemon"
	"git.sr.ht/~klahr/dazyflow/engine"
	"git.sr.ht/~klahr/dazyflow/engine/jobstore"
	"git.sr.ht/~klahr/dazyflow/workspace"
)

// memShareStore is an in-memory daemon.ShareStore for tests — the real store
// is Postgres-only, but the service logic only needs the interface.
type memShareStore struct {
	mu sync.Mutex
	m  map[string]daemon.Share // keyed by tenant+"/"+workspace
}

func newMemShareStore() *memShareStore {
	return &memShareStore{m: map[string]daemon.Share{}}
}

func (s *memShareStore) key(tenant, ws string) string { return tenant + "/" + ws }

func (s *memShareStore) Get(_ context.Context, tenant, ws string) (daemon.Share, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sh, ok := s.m[s.key(tenant, ws)]; ok {
		return sh, nil
	}
	return daemon.Share{}, core.ErrNotFound
}

func (s *memShareStore) Upsert(_ context.Context, tenant, ws, token, by string) (daemon.Share, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sh := daemon.Share{Tenant: tenant, Workspace: ws, Token: token, CreatedAt: time.Now(), CreatedBy: by}
	s.m[s.key(tenant, ws)] = sh
	return sh, nil
}

func (s *memShareStore) Delete(_ context.Context, tenant, ws string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, s.key(tenant, ws))
	return nil
}

func (s *memShareStore) Lookup(_ context.Context, token string) (daemon.Share, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sh := range s.m {
		if sh.Token == token {
			return sh, nil
		}
	}
	return daemon.Share{}, core.ErrNotFound
}

func (s *memShareStore) DeleteByTenant(_ context.Context, tenant string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for k, sh := range s.m {
		if sh.Tenant == tenant {
			delete(s.m, k)
			n++
		}
	}
	return n, nil
}

type shareHarness struct {
	svc    *daemon.Service
	shares *memShareStore
	editor core.Principal
	viewer core.Principal
	admin  core.Principal
}

func newShareHarness(t *testing.T) *shareHarness {
	t.Helper()
	wsStore, _ := workspace.OpenFS("")
	shares := newMemShareStore()
	svc := &daemon.Service{
		Auth:       auth.Chain{},
		Workspaces: daemon.MapWorkspaces{"t/ws": wsStore},
		Jobs:       jobstore.NewMemory(),
		Engine:     &engine.Engine{Resolver: &engine.NodeResolver{Native: engine.Default}},
		Bus:        daemon.NewMemoryBus(),
		Shares:     shares,
	}
	editor := core.Role{Name: "editor", Permissions: []core.Permission{core.PermGraphRun, core.PermGraphEdit}}
	viewer := core.Role{Name: "viewer", Permissions: []core.Permission{core.PermGraphRun}}
	admin := core.Role{Name: "admin", Permissions: []core.Permission{core.PermGraphRun, core.PermGraphEdit, core.PermGraphAdmin}}
	return &shareHarness{
		svc:    svc,
		shares: shares,
		editor: core.Principal{Subject: "ed", Tenant: "t", Workspace: "ws", Roles: []core.Role{editor}},
		viewer: core.Principal{Subject: "vi", Tenant: "t", Workspace: "ws", Roles: []core.Role{viewer}},
		admin:  core.Principal{Subject: "ad", Tenant: "t", Workspace: "ws", Roles: []core.Role{admin}},
	}
}

// publish promotes a flow's HEAD to the published environment, so it counts
// toward the overview/TV stats (only published flows do). Saves must precede.
func (h *shareHarness) publish(t *testing.T, ctx context.Context, id string) {
	t.Helper()
	if _, err := h.svc.PublishFlow(ctx, h.admin, "t", "ws", id, "", ""); err != nil {
		t.Fatalf("publish %s: %v", id, err)
	}
}

func (h *shareHarness) enqueueRun(t *testing.T, ctx context.Context, id, graphID string, status core.JobStatus, at time.Time) {
	t.Helper()
	started := at
	rec := core.JobRecord{
		ID:         id,
		Kind:       core.JobKindGraph,
		GraphID:    graphID,
		Tenant:     "t",
		Workspace:  "ws",
		Status:     status,
		EnqueuedAt: at,
		StartedAt:  &started,
	}
	if core.IsTerminalStatus(status) {
		rec.FinishedAt = &started
	}
	if err := h.svc.Jobs.Enqueue(ctx, rec); err != nil {
		t.Fatalf("enqueue %s: %v", id, err)
	}
}

func TestCreateWorkspaceShare_RequiresEdit(t *testing.T) {
	h := newShareHarness(t)
	ctx := context.Background()
	// A run-only viewer can't mint a public link.
	if _, err := h.svc.CreateWorkspaceShare(ctx, h.viewer, "t", "ws"); err == nil {
		t.Fatal("viewer was allowed to create a share link")
	}
	// An editor can.
	sh, err := h.svc.CreateWorkspaceShare(ctx, h.editor, "t", "ws")
	if err != nil {
		t.Fatalf("editor create: %v", err)
	}
	if sh.Token == "" {
		t.Fatal("expected a non-empty token")
	}
}

func TestCreateWorkspaceShare_RotateInvalidatesOld(t *testing.T) {
	h := newShareHarness(t)
	ctx := context.Background()
	first, err := h.svc.CreateWorkspaceShare(ctx, h.editor, "t", "ws")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	second, err := h.svc.CreateWorkspaceShare(ctx, h.editor, "t", "ws")
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if first.Token == second.Token {
		t.Fatal("rotate should mint a fresh token")
	}
	// The old token no longer resolves.
	if _, err := h.svc.PublicWorkspaceOverview(ctx, first.Token, time.Now()); err == nil {
		t.Fatal("rotated-away token still resolved")
	}
	if _, err := h.svc.PublicWorkspaceOverview(ctx, second.Token, time.Now()); err != nil {
		t.Fatalf("current token should resolve: %v", err)
	}
}

func TestPublicWorkspaceOverview_SanitizedAndScoped(t *testing.T) {
	h := newShareHarness(t)
	ctx := context.Background()

	// Two visible flows + one private one.
	for _, g := range []core.Graph{
		{ID: "alpha", Tenant: "t", Workspace: "ws", Name: "Alpha"},
		{ID: "beta", Tenant: "t", Workspace: "ws", Name: "Beta"},
		{ID: "hidden", Tenant: "t", Workspace: "ws", Name: "Secret", Visibility: core.VisibilityPrivate},
	} {
		if _, err := h.svc.SaveGraph(ctx, h.editor, g); err != nil {
			t.Fatalf("save %s: %v", g.ID, err)
		}
	}
	// Only published flows count; publish the two visible ones (the private
	// "hidden" is excluded by visibility regardless).
	h.publish(t, ctx, "alpha")
	h.publish(t, ctx, "beta")

	// Fixed midday timestamp so the -2h/-30m/-5m runs below all fall on the
	// same UTC calendar day as `now`. Using time.Now() here made the test
	// flaky near UTC midnight: a run "2h ago" crosses into the previous day,
	// so RunsToday (counted from startOfDay) drops to 2. PublicWorkspaceOverview
	// takes `now` explicitly, so pinning it keeps the day boundary deterministic.
	now := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	// alpha: latest is a failure (older success first, newer failure second).
	h.enqueueRun(t, ctx, "a1", "alpha", core.JobStatusSucceeded, now.Add(-2*time.Hour))
	h.enqueueRun(t, ctx, "a2", "alpha", core.JobStatusFailed, now.Add(-30*time.Minute))
	// beta: currently running.
	h.enqueueRun(t, ctx, "b1", "beta", core.JobStatusRunning, now.Add(-5*time.Minute))

	sh, err := h.svc.CreateWorkspaceShare(ctx, h.editor, "t", "ws")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	data, err := h.svc.PublicWorkspaceOverview(ctx, sh.Token, now)
	if err != nil {
		t.Fatalf("overview: %v", err)
	}

	// Private flow excluded; only the two visible ones surface.
	if data.Stats.TotalFlows != 2 {
		t.Fatalf("total_flows = %d, want 2", data.Stats.TotalFlows)
	}
	for _, f := range data.Flows {
		if f.Name == "Secret" {
			t.Fatal("private flow leaked into the public board")
		}
	}

	// Counters: 3 runs today, 2 finished (1 ok / 1 failed) → 50%, 1 failed, 1 running.
	if data.Stats.RunsToday != 3 {
		t.Errorf("runs_today = %d, want 3", data.Stats.RunsToday)
	}
	if data.Stats.Failed != 1 {
		t.Errorf("failed = %d, want 1", data.Stats.Failed)
	}
	if data.Stats.Running != 1 {
		t.Errorf("running = %d, want 1", data.Stats.Running)
	}
	if data.Stats.SuccessRate == nil || *data.Stats.SuccessRate != 50 {
		t.Errorf("success_rate = %v, want 50", data.Stats.SuccessRate)
	}

	// Failing flow sorts first; its latest status is the failure, not the
	// earlier success.
	if len(data.Flows) == 0 || data.Flows[0].Name != "Alpha" {
		t.Fatalf("expected Alpha first (failing), got %+v", data.Flows)
	}
	if data.Flows[0].LastStatus != core.JobStatusFailed {
		t.Errorf("alpha last_status = %q, want failed", data.Flows[0].LastStatus)
	}
	// History strip is newest-first: alpha ran succeeded then failed, so the
	// strip is [failed, succeeded].
	wantHist := []core.JobStatus{core.JobStatusFailed, core.JobStatusSucceeded}
	if got := data.Flows[0].History; len(got) != 2 || got[0] != wantHist[0] || got[1] != wantHist[1] {
		t.Errorf("alpha history = %v, want %v", got, wantHist)
	}
}

// TestPublicWorkspaceOverview_NeedsAttentionByFlow pins the "needs attention"
// semantics so the board matches the authenticated Dashboard: it counts FLOWS
// whose latest run failed (one per flow, not per failed run), a flow that has
// since recovered drops off, and an unpublished (needs_publish) draft is kept
// off the board entirely — no tile, and out of every counter.
func TestPublicWorkspaceOverview_NeedsAttentionByFlow(t *testing.T) {
	h := newShareHarness(t)
	ctx := context.Background()

	cron := []core.Node{{ID: "c", Module: "cron_trigger", Params: map[string]any{"cron": "*/5 * * * *"}}}
	for _, g := range []core.Graph{
		{ID: "flaky", Tenant: "t", Workspace: "ws", Name: "Flaky"},
		{ID: "recovered", Tenant: "t", Workspace: "ws", Name: "Recovered"},
		{ID: "draft", Tenant: "t", Workspace: "ws", Name: "Draft", Nodes: cron}, // left unpublished → excluded
	} {
		if _, err := h.svc.SaveGraph(ctx, h.editor, g); err != nil {
			t.Fatalf("save %s: %v", g.ID, err)
		}
	}
	// flaky + recovered are real (published) flows; "draft" stays unpublished
	// and must drop out of every counter.
	h.publish(t, ctx, "flaky")
	h.publish(t, ctx, "recovered")

	now := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	// flaky: two failures, latest also a failure → counts ONCE.
	h.enqueueRun(t, ctx, "f1", "flaky", core.JobStatusFailed, now.Add(-2*time.Hour))
	h.enqueueRun(t, ctx, "f2", "flaky", core.JobStatusFailed, now.Add(-30*time.Minute))
	// recovered: failed then succeeded (succeeded is latest) → not counted.
	h.enqueueRun(t, ctx, "r1", "recovered", core.JobStatusFailed, now.Add(-2*time.Hour))
	h.enqueueRun(t, ctx, "r2", "recovered", core.JobStatusSucceeded, now.Add(-20*time.Minute))
	// draft is needs_publish: its failed run must not reach the stats at all.
	h.enqueueRun(t, ctx, "d1", "draft", core.JobStatusFailed, now.Add(-10*time.Minute))

	sh, err := h.svc.CreateWorkspaceShare(ctx, h.editor, "t", "ws")
	if err != nil {
		t.Fatal(err)
	}
	data, err := h.svc.PublicWorkspaceOverview(ctx, sh.Token, now)
	if err != nil {
		t.Fatalf("overview: %v", err)
	}

	// Only "flaky" needs attention: one entry per flow, "recovered" cleared
	// itself, and the unpublished "draft" is excluded.
	if data.Stats.Failed != 1 {
		t.Errorf("failed = %d, want 1 (flaky only)", data.Stats.Failed)
	}
	// Counted runs are flaky+recovered's only (draft excluded): 4 runs today,
	// 4 finished (3 failed / 1 ok) → 25%.
	if data.Stats.RunsToday != 4 {
		t.Errorf("runs_today = %d, want 4 (draft's run excluded)", data.Stats.RunsToday)
	}
	if data.Stats.SuccessRate == nil || *data.Stats.SuccessRate != 25 {
		t.Errorf("success_rate = %v, want 25", data.Stats.SuccessRate)
	}
	// The unpublished draft is kept off the board entirely, so total_flows
	// counts only the two real flows.
	for i := range data.Flows {
		if data.Flows[i].Name == "Draft" {
			t.Errorf("unpublished draft should not appear on the board, got %+v", data.Flows[i])
		}
	}
	if data.Stats.TotalFlows != 2 {
		t.Errorf("total_flows = %d, want 2 (draft excluded)", data.Stats.TotalFlows)
	}
}

// TestPublicWorkspaceOverview_DisabledFlowExcluded pins that a disabled
// (paused) flow is kept off the public board and out of every counter — its
// last run failing before it was paused must not register as "needs attention"
// nor drag down the success rate.
func TestPublicWorkspaceOverview_DisabledFlowExcluded(t *testing.T) {
	h := newShareHarness(t)
	ctx := context.Background()

	for _, g := range []core.Graph{
		{ID: "active", Tenant: "t", Workspace: "ws", Name: "Active"},
		{ID: "off", Tenant: "t", Workspace: "ws", Name: "Off", Disabled: true},
	} {
		if _, err := h.svc.SaveGraph(ctx, h.editor, g); err != nil {
			t.Fatalf("save %s: %v", g.ID, err)
		}
	}
	// Publish both so "off" is excluded specifically for being DISABLED, not
	// merely for being unpublished — keeps this test pinned to the paused path.
	h.publish(t, ctx, "active")
	h.publish(t, ctx, "off")

	now := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	// Both flows' latest run failed — but the disabled one is intentionally off.
	h.enqueueRun(t, ctx, "a1", "active", core.JobStatusFailed, now.Add(-30*time.Minute))
	h.enqueueRun(t, ctx, "o1", "off", core.JobStatusFailed, now.Add(-10*time.Minute))

	sh, err := h.svc.CreateWorkspaceShare(ctx, h.editor, "t", "ws")
	if err != nil {
		t.Fatal(err)
	}
	data, err := h.svc.PublicWorkspaceOverview(ctx, sh.Token, now)
	if err != nil {
		t.Fatalf("overview: %v", err)
	}

	if data.Stats.Failed != 1 {
		t.Errorf("failed = %d, want 1 (disabled flow excluded)", data.Stats.Failed)
	}
	if data.Stats.RunsToday != 1 {
		t.Errorf("runs_today = %d, want 1 (disabled flow's run excluded)", data.Stats.RunsToday)
	}
	for i := range data.Flows {
		if data.Flows[i].Name == "Off" {
			t.Errorf("disabled flow should not appear on the board, got %+v", data.Flows[i])
		}
	}
	if data.Stats.TotalFlows != 1 {
		t.Errorf("total_flows = %d, want 1 (disabled excluded)", data.Stats.TotalFlows)
	}
}

// TestPublicWorkspaceOverview_UnpublishedExcluded pins that an unpublished
// flow is a draft whatever its trigger — a MANUAL unpublished flow (which is
// not "needs_publish") must still be kept off the board and out of the
// counters, so its test-run failures don't read as "needs attention".
func TestPublicWorkspaceOverview_UnpublishedExcluded(t *testing.T) {
	h := newShareHarness(t)
	ctx := context.Background()

	for _, g := range []core.Graph{
		{ID: "live", Tenant: "t", Workspace: "ws", Name: "Live"},
		{ID: "draft", Tenant: "t", Workspace: "ws", Name: "Draft"}, // manual, never published
	} {
		if _, err := h.svc.SaveGraph(ctx, h.editor, g); err != nil {
			t.Fatalf("save %s: %v", g.ID, err)
		}
	}
	h.publish(t, ctx, "live") // "draft" stays unpublished

	now := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	h.enqueueRun(t, ctx, "l1", "live", core.JobStatusFailed, now.Add(-30*time.Minute))
	h.enqueueRun(t, ctx, "d1", "draft", core.JobStatusFailed, now.Add(-10*time.Minute))

	sh, err := h.svc.CreateWorkspaceShare(ctx, h.editor, "t", "ws")
	if err != nil {
		t.Fatal(err)
	}
	data, err := h.svc.PublicWorkspaceOverview(ctx, sh.Token, now)
	if err != nil {
		t.Fatalf("overview: %v", err)
	}
	if data.Stats.Failed != 1 {
		t.Errorf("failed = %d, want 1 (unpublished draft excluded)", data.Stats.Failed)
	}
	if data.Stats.TotalFlows != 1 {
		t.Errorf("total_flows = %d, want 1 (only the published flow)", data.Stats.TotalFlows)
	}
	for i := range data.Flows {
		if data.Flows[i].Name == "Draft" {
			t.Errorf("unpublished draft should not appear on the board, got %+v", data.Flows[i])
		}
	}
}

// TestPublicWorkspaceOverview_SuccessRateRounds guards the success-rate
// rounding: 2 of 3 finished = 66.67%, which must round to 67 (matching the
// Dashboard's Math.round), not truncate to 66.
func TestPublicWorkspaceOverview_SuccessRateRounds(t *testing.T) {
	h := newShareHarness(t)
	ctx := context.Background()

	if _, err := h.svc.SaveGraph(ctx, h.editor,
		core.Graph{ID: "g", Tenant: "t", Workspace: "ws", Name: "G"}); err != nil {
		t.Fatalf("save: %v", err)
	}
	h.publish(t, ctx, "g")
	now := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	h.enqueueRun(t, ctx, "s1", "g", core.JobStatusSucceeded, now.Add(-3*time.Hour))
	h.enqueueRun(t, ctx, "s2", "g", core.JobStatusSucceeded, now.Add(-2*time.Hour))
	h.enqueueRun(t, ctx, "x1", "g", core.JobStatusFailed, now.Add(-1*time.Hour))

	sh, err := h.svc.CreateWorkspaceShare(ctx, h.editor, "t", "ws")
	if err != nil {
		t.Fatal(err)
	}
	data, err := h.svc.PublicWorkspaceOverview(ctx, sh.Token, now)
	if err != nil {
		t.Fatalf("overview: %v", err)
	}
	if data.Stats.SuccessRate == nil || *data.Stats.SuccessRate != 67 {
		t.Errorf("success_rate = %v, want 67 (2/3 rounded)", data.Stats.SuccessRate)
	}
}

func TestPublicWorkspaceOverview_UnknownToken(t *testing.T) {
	h := newShareHarness(t)
	if _, err := h.svc.PublicWorkspaceOverview(context.Background(), "nope", time.Now()); err == nil {
		t.Fatal("expected error for unknown token")
	}
}

func TestDeleteWorkspaceShare(t *testing.T) {
	h := newShareHarness(t)
	ctx := context.Background()
	sh, err := h.svc.CreateWorkspaceShare(ctx, h.editor, "t", "ws")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := h.svc.DeleteWorkspaceShare(ctx, h.editor, "t", "ws"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := h.svc.PublicWorkspaceOverview(ctx, sh.Token, time.Now()); err == nil {
		t.Fatal("token still resolved after delete")
	}
	_, exists, err := h.svc.GetWorkspaceShare(ctx, h.editor, "t", "ws")
	if err != nil {
		t.Fatalf("get after delete: %v", err)
	}
	if exists {
		t.Fatal("share still present after delete")
	}
}
