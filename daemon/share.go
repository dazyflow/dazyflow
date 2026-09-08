// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/dazyflow/dazyflow/core"
)

// Public workspace-overview share links.
//
// A share is a single, regenerable cryptic token per (tenant, workspace). It
// backs a read-only public page — the TV-dashboard view of the workspace's run
// health — without a login. The token IS the credential (same model as the
// hosted-form / approval links), so the public endpoint takes no principal: it
// looks the token up, recovers the (tenant, workspace), and serves a sanitized
// status snapshot.
//
// The payload deliberately carries no IDs, error codes, payloads, owners, or
// tenant/workspace identifiers — only flow names + run status + aggregate
// counters. A leaked link reveals "is the workspace healthy", nothing it could
// be used to act on. Private flows are excluded entirely.

type Share struct {
	Tenant    string    `json:"-"`
	Workspace string    `json:"-"`
	Token     string    `json:"token"`
	CreatedAt time.Time `json:"created_at"`
	CreatedBy string    `json:"created_by,omitempty"`
}

// ShareStore persists workspace-overview share links. One row per
// (tenant, workspace); Upsert rotates the token in place so a workspace
// always has at most one live link.
type ShareStore interface {
	Get(ctx context.Context, tenant, workspace string) (Share, error)
	Upsert(ctx context.Context, tenant, workspace, token, createdBy string) (Share, error)
	Delete(ctx context.Context, tenant, workspace string) error
	Lookup(ctx context.Context, token string) (Share, error)
	DeleteByTenant(ctx context.Context, tenant string) (int, error)
	AnonymizeSubject(ctx context.Context, ident string) (int, error)
}

func newShareToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("mint share token: %w", err)
	}
	return hex.EncodeToString(raw), nil
}

func (s *Service) GetWorkspaceShare(ctx context.Context, p core.Principal, tenant, workspace string) (Share, bool, error) {
	if err := core.RequireWorkspace(p, tenant, workspace); err != nil {
		return Share{}, false, err
	}
	if s.Shares == nil {
		return Share{}, false, fmt.Errorf("share store not configured")
	}
	sh, err := s.Shares.Get(ctx, tenant, workspace)
	if err != nil {
		if errors.Is(err, core.ErrNotFound) {
			return Share{}, false, nil
		}
		return Share{}, false, err
	}
	return sh, true, nil
}

// CreateWorkspaceShare mints (or rotates) the workspace's share link and
// returns it. Rotating invalidates any link handed out earlier. Minting a
// public link is an edit-level action, so it takes graph:edit — a run-only
// viewer can see an existing link but can't publish a new public surface.
func (s *Service) CreateWorkspaceShare(ctx context.Context, p core.Principal, tenant, workspace string) (Share, error) {
	if err := core.RequireWorkspace(p, tenant, workspace); err != nil {
		return Share{}, err
	}
	if err := core.Require(p, core.PermGraphEdit); err != nil {
		return Share{}, err
	}
	if s.Shares == nil {
		return Share{}, fmt.Errorf("share store not configured")
	}
	token, err := newShareToken()
	if err != nil {
		return Share{}, err
	}
	return s.Shares.Upsert(ctx, tenant, workspace, token, p.Subject)
}

func (s *Service) DeleteWorkspaceShare(ctx context.Context, p core.Principal, tenant, workspace string) error {
	if err := core.RequireWorkspace(p, tenant, workspace); err != nil {
		return err
	}
	if err := core.Require(p, core.PermGraphEdit); err != nil {
		return err
	}
	if s.Shares == nil {
		return fmt.Errorf("share store not configured")
	}
	return s.Shares.Delete(ctx, tenant, workspace)
}

type PublicOverviewData struct {
	Label       string            `json:"label,omitempty"`
	Icon        string            `json:"icon,omitempty"`
	GeneratedAt time.Time         `json:"generated_at"`
	Stats       PublicStats       `json:"stats"`
	Flows       []PublicFlowState `json:"flows"`
}

type PublicStats struct {
	RunsToday   int  `json:"runs_today"`
	SuccessRate *int `json:"success_rate,omitempty"` // nil = no finished runs yet
	Failed      int  `json:"failed"`
	Running     int  `json:"running"`
	LiveFlows   int  `json:"live_flows"`
	TotalFlows  int  `json:"total_flows"`
}

type PublicFlowState struct {
	Name      string             `json:"name"`
	Icon      string             `json:"icon,omitempty"`
	RunStatus core.FlowRunStatus `json:"run_status,omitempty"`
	// LastStatus is the most recent run's status (succeeded / failed /
	// running / queued / …), empty when the flow has never run.
	LastStatus core.JobStatus `json:"last_status,omitempty"`
	LastRunAt  *time.Time     `json:"last_run_at,omitempty"`
	// NextRunAt is the next automatic fire time (RFC3339 UTC), set only for
	// flows that are actually live on a scheduler trigger (cron / poll / form
	// interval) — so the board can show "next run" alongside the last one.
	// nil for manual, paused, needs-publish, or webhook-only flows (nothing
	// the scheduler will fire on a clock). Computed with the same cron parser
	// the scheduler fires on, so it matches what will really run.
	NextRunAt *time.Time       `json:"next_run_at,omitempty"`
	History   []core.JobStatus `json:"history,omitempty"`
}

const shareRunWindow = 200

const shareFlowHistory = 10

func (s *Service) PublicWorkspaceOverview(ctx context.Context, token string, now time.Time) (PublicOverviewData, error) {
	if s.Shares == nil {
		return PublicOverviewData{}, core.ErrNotFound
	}
	share, err := s.Shares.Lookup(ctx, token)
	if err != nil {
		return PublicOverviewData{}, err // core.ErrNotFound bubbles to a 404
	}

	runs, err := core.ListRunSummaries(ctx, s.Jobs, core.ListGraphRunsOpts{
		Tenant:    share.Tenant,
		Workspace: share.Workspace,
		Limit:     shareRunWindow,
	})
	if err != nil {
		return PublicOverviewData{}, err
	}
	type latest struct {
		status core.JobStatus
		at     time.Time
	}
	latestByGraph := map[string]latest{}
	historyByGraph := map[string][]core.JobStatus{}
	for _, r := range runs {
		if _, seen := latestByGraph[r.GraphID]; !seen {
			latestByGraph[r.GraphID] = latest{status: r.Status, at: runStartedOrEnqueued(r)}
		}
		if len(historyByGraph[r.GraphID]) < shareFlowHistory {
			historyByGraph[r.GraphID] = append(historyByGraph[r.GraphID], r.Status)
		}
	}

	label, icon := s.workspaceBrand(ctx, share.Tenant)
	data := PublicOverviewData{Label: label, Icon: icon, GeneratedAt: now}

	// Flow tiles + the "counted" set the stats are scoped to — one and the
	// same, so the board and the authenticated Dashboard show identical
	// numbers. Excluded entirely (no tile, no count): private flows
	// (owner-scoped, names could be sensitive) and needs_publish flows
	// (configured-but-unpublished drafts the scheduler won't run — effectively
	// test mode, and their runs would skew the numbers). Best-effort: an
	// unavailable store yields an empty board rather than unscoped counters.
	counted := map[string]bool{}
	var needsAttention int
	if store, werr := s.Workspaces.Open(share.Tenant, share.Workspace); werr == nil {
		ids, _ := store.ListGraphs()
		for _, id := range ids {
			g, lerr := store.Load(id)
			if lerr != nil {
				continue
			}
			if g.EffectiveVisibility() == core.VisibilityPrivate {
				continue
			}
			pub, _ := store.PublishedCommit(id)
			if pub == "" {
				continue
			}
			runStatus := core.FlowRunStatusPublished(g, true)
			// A disabled flow is intentionally off — same treatment: not shown,
			// not counted, so a pre-pause failure can't read as "needs attention"
			// nor drag down the success rate.
			if runStatus == core.FlowPaused {
				continue
			}
			counted[id] = true
			st := PublicFlowState{
				Name:      flowDisplayName(g, id),
				Icon:      g.Icon,
				RunStatus: runStatus,
			}
			if runStatus == core.FlowLive {
				data.Stats.LiveFlows++
				st.NextRunAt = nextScheduledFire(g, now)
			}
			lr, hasLatest := latestByGraph[id]
			if hasLatest {
				st.LastStatus = lr.status
				if !lr.at.IsZero() {
					at := lr.at
					st.LastRunAt = &at
				}
				if lr.status == core.JobStatusFailed {
					needsAttention++
				}
			}
			st.History = historyByGraph[id]
			data.Flows = append(data.Flows, st)
		}
		data.Stats.TotalFlows = len(data.Flows)
		sort.SliceStable(data.Flows, func(i, j int) bool {
			pi, pj := flowSortRank(data.Flows[i]), flowSortRank(data.Flows[j])
			if pi != pj {
				return pi < pj
			}
			return data.Flows[i].Name < data.Flows[j].Name
		})
	}

	dayStart := startOfDay(now)
	var runsToday, running, finished, succeeded int
	for _, r := range runs {
		if !counted[r.GraphID] {
			continue
		}
		ref := runStartedOrEnqueued(r)
		if !ref.IsZero() && !ref.Before(dayStart) {
			runsToday++
		}
		switch r.Status {
		case core.JobStatusSucceeded:
			succeeded++
			finished++
		case core.JobStatusFailed:
			finished++
		case core.JobStatusRunning, core.JobStatusQueued:
			running++
		}
	}
	data.Stats.RunsToday = runsToday
	data.Stats.Failed = needsAttention
	data.Stats.Running = running
	if finished > 0 {
		rate := int(math.Round(float64(succeeded) / float64(finished) * 100))
		data.Stats.SuccessRate = &rate
	}
	if data.Flows == nil {
		data.Flows = []PublicFlowState{}
	}
	return data, nil
}

func (s *Service) workspaceBrand(ctx context.Context, tenant string) (label, icon string) {
	if s.OrgProfiles != nil {
		if prof, err := s.OrgProfiles.GetOrgProfile(ctx, tenant); err == nil {
			icon = prof.Icon
			if prof.DisplayName != "" {
				return prof.DisplayName, icon
			}
		}
	}
	if looksPersonalTenant(tenant) {
		return "", icon
	}
	return tenant, icon
}

// flowDisplayName falls back to the flow id when a graph has no name set, so a
// tile is never blank.
func flowDisplayName(g core.Graph, id string) string {
	if g.Name != "" {
		return g.Name
	}
	return id
}

func flowSortRank(f PublicFlowState) int {
	switch f.LastStatus {
	case core.JobStatusFailed:
		return 0
	case core.JobStatusRunning, core.JobStatusQueued:
		return 1
	default:
		return 2
	}
}

func runStartedOrEnqueued(r core.RunSummary) time.Time {
	if r.StartedAt != nil {
		return *r.StartedAt
	}
	return r.EnqueuedAt
}

// startOfDay truncates to local midnight — the boundary "runs today" counts from.
func startOfDay(now time.Time) time.Time {
	y, m, d := now.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, now.Location())
}

// nextScheduledFire returns the earliest upcoming automatic fire after `now`
// across a flow's enabled scheduler triggers — graph-level cron, cron_trigger
// nodes, and poll_trigger/google_form_trigger nodes — or nil when the flow has
// no active schedule. It mirrors the scheduler's firing rules (and reuses the
// same cron parser as the Schedules page) so the time shown matches reality:
//   - a whole-flow pause (g.Disabled) suppresses everything;
//   - a per-trigger pause (node `disabled`) skips that node;
//   - poll intervals are projected from now (interval-anchored, like the
//     scheduler's own preview).
//
// Webhook triggers are excluded — they fire from an HTTP request, not a clock,
// so they have no "next run". The caller gates on FlowLive, so this is only
// invoked for published, enabled flows.
func nextScheduledFire(g core.Graph, now time.Time) *time.Time {
	if g.Disabled {
		return nil
	}
	var best time.Time
	consider := func(t time.Time) {
		if t.IsZero() {
			return
		}
		if best.IsZero() || t.Before(best) {
			best = t
		}
	}

	for _, tr := range g.Triggers {
		if tr.Type != "cron" {
			continue
		}
		expr := strings.TrimSpace(tr.Cron)
		if expr == "" {
			continue
		}
		if sched, err := parseCronInTZ(cronValidator, expr, tr.TZ); err == nil {
			consider(sched.Next(now))
		}
	}

	for _, node := range g.Nodes {
		if triggerNodeDisabled(node) {
			continue
		}
		switch node.Module {
		case "cron_trigger":
			expr, _ := node.Params["cron"].(string)
			expr = strings.TrimSpace(expr)
			if expr == "" {
				continue
			}
			tz, _ := node.Params["tz"].(string)
			if sched, err := parseCronInTZ(cronValidator, expr, tz); err == nil {
				consider(sched.Next(now))
			}
		case "poll_trigger", "google_form_trigger", "ticketmaster_on_new_event":
			secs := paramSeconds(node.Params, "interval_seconds")
			if secs <= 0 || secs > core.MaxPollIntervalSeconds {
				continue
			}
			consider(now.Add(time.Duration(secs) * time.Second))
		}
	}

	if best.IsZero() {
		return nil
	}
	utc := best.UTC()
	return &utc
}
