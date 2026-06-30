// SPDX-FileCopyrightText: 2026 Joachim Klahr
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

	"git.sr.ht/~klahr/dazyflow/core"
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

// Share is one workspace-overview share link.
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
	// Get returns the workspace's current share, or core.ErrNotFound when
	// none has been created.
	Get(ctx context.Context, tenant, workspace string) (Share, error)
	// Upsert creates or rotates (tenant, workspace)'s share to the given
	// token, returning the stored row.
	Upsert(ctx context.Context, tenant, workspace, token, createdBy string) (Share, error)
	// Delete removes the workspace's share. Idempotent — a missing row is
	// not an error.
	Delete(ctx context.Context, tenant, workspace string) error
	// Lookup resolves a token back to its share (the public path). Returns
	// core.ErrNotFound for an unknown/rotated token.
	Lookup(ctx context.Context, token string) (Share, error)
	// DeleteByTenant erases every share for a tenant — the GDPR/org-erasure
	// cascade hook (see gdpr.go's tenantEraser).
	DeleteByTenant(ctx context.Context, tenant string) (int, error)
}

// newShareToken mints a 32-byte cryptic token, hex-encoded (64 chars). Same
// generator the password-reset / email-verification flows use; unguessable and
// URL-safe.
func newShareToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("mint share token: %w", err)
	}
	return hex.EncodeToString(raw), nil
}

// GetWorkspaceShare returns the workspace's current share link, or ok=false
// when none exists. Read access is gated on workspace membership.
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

// DeleteWorkspaceShare revokes the workspace's share link. Idempotent. Same
// edit-level gate as creating one.
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

// PublicOverviewData is the sanitized snapshot the public TV page renders.
// No IDs, no error detail, no tenant/workspace — only what's safe on a wall.
type PublicOverviewData struct {
	// Label is the org's human-facing display name, used to title the board.
	// Empty when the org has no display name (and isn't worth showing a raw
	// id for) — the UI then falls back to a generic title.
	Label string `json:"label,omitempty"`
	// Icon is the org's logo — a data: URL / image reference or a logical
	// icon name — shown beside the board title. Empty when the org has no
	// icon set, in which case the UI shows just the title. Already public on
	// the sign-in page, so safe on a wall.
	Icon        string            `json:"icon,omitempty"`
	GeneratedAt time.Time         `json:"generated_at"`
	Stats       PublicStats       `json:"stats"`
	Flows       []PublicFlowState `json:"flows"`
}

// PublicStats are the headline counters across the workspace's recent runs.
type PublicStats struct {
	RunsToday   int  `json:"runs_today"`
	SuccessRate *int `json:"success_rate,omitempty"` // nil = no finished runs yet
	Failed      int  `json:"failed"`
	Running     int  `json:"running"`
	LiveFlows   int  `json:"live_flows"`
	TotalFlows  int  `json:"total_flows"`
}

// PublicFlowState is one flow's tile on the TV grid.
type PublicFlowState struct {
	Name string `json:"name"`
	Icon string `json:"icon,omitempty"`
	// RunStatus is the flow's automation posture: live / manual / paused /
	// needs_publish.
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
	NextRunAt *time.Time `json:"next_run_at,omitempty"`
	// History is the flow's recent run outcomes, newest first, capped at
	// shareFlowHistory. Only the statuses — no ids or timing — so the board
	// can draw an at-a-glance health strip without leaking anything.
	History []core.JobStatus `json:"history,omitempty"`
}

// shareRunWindow is how many recent runs the public snapshot summarizes —
// matches the Dashboard's own window so the numbers line up.
const shareRunWindow = 200

// shareFlowHistory caps the per-flow recent-run strip (the health pipes on
// each card).
const shareFlowHistory = 10

// PublicWorkspaceOverview resolves a share token and builds the sanitized
// status snapshot for it. No principal: the token is the authorization, so
// this reads the workspace store and job store directly (the same pattern the
// webhook-trigger path uses). Returns core.ErrNotFound for an unknown token.
func (s *Service) PublicWorkspaceOverview(ctx context.Context, token string, now time.Time) (PublicOverviewData, error) {
	if s.Shares == nil {
		// No share store on this deployment → no link can exist. Report it
		// as an unknown link (404) rather than a server error.
		return PublicOverviewData{}, core.ErrNotFound
	}
	share, err := s.Shares.Lookup(ctx, token)
	if err != nil {
		return PublicOverviewData{}, err // core.ErrNotFound bubbles to a 404
	}

	// Recent-run window — matches the Dashboard's so the two surfaces agree.
	runs, err := s.Jobs.ListGraphRuns(ctx, core.ListGraphRunsOpts{
		Tenant:    share.Tenant,
		Workspace: share.Workspace,
		Limit:     shareRunWindow,
	})
	if err != nil {
		return PublicOverviewData{}, err
	}
	// First pass: latest run + recent-run strip per graph. Runs come
	// newest-first, so the first one we see for a graph is its latest.
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
			// An unpublished flow is a draft, whatever its trigger — its runs are
			// test runs, so keep it off the public wall and out of every counter.
			// (Subsumes the old needs_publish-only skip: those are unpublished
			// too.)
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
			// Next scheduled fire — only meaningful for a live flow (published +
			// enabled + a scheduler trigger). A paused flow won't fire on a
			// clock, so it gets no next-run.
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
				// "Needs attention" = the flow's latest run failed (one per
				// flow), the same rule the Dashboard counts by.
				if lr.status == core.JobStatusFailed {
					needsAttention++
				}
			}
			st.History = historyByGraph[id]
			data.Flows = append(data.Flows, st)
		}
		data.Stats.TotalFlows = len(data.Flows)
		// Stable, friendly ordering: failing flows first (they're what a wall
		// is for), then running, then by name.
		sort.SliceStable(data.Flows, func(i, j int) bool {
			pi, pj := flowSortRank(data.Flows[i]), flowSortRank(data.Flows[j])
			if pi != pj {
				return pi < pj
			}
			return data.Flows[i].Name < data.Flows[j].Name
		})
	}

	// Headline counters over the runs of counted flows only, so owner/test-mode
	// activity stays out of them (mirrors the Dashboard's exclusion).
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
		// Round (not truncate) so this matches the Dashboard's Math.round — a
		// truncating int() here showed 76% where the overview showed 77%.
		rate := int(math.Round(float64(succeeded) / float64(finished) * 100))
		data.Stats.SuccessRate = &rate
	}
	if data.Flows == nil {
		data.Flows = []PublicFlowState{}
	}
	return data, nil
}

// workspaceBrand is the friendly board identity for a tenant: its org display
// name and icon when set. The label falls back to a bare personal-tenant id
// (usr_<hex>) being dropped — it's meaningless chrome, so the UI shows its
// generic title; a named tenant id is kept as a last resort. The icon is
// whatever the org profile carries (empty when none). Best-effort — any store
// error yields the fallback label and an empty icon.
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

// flowSortRank orders tiles so the wall draws attention to trouble: failed
// first, then running, then everything else.
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

// runStartedOrEnqueued is the run's effective wall-clock time: when it began
// if known, else when it was enqueued. Mirrors the Dashboard's client-side
// choice so "today" counts agree.
func runStartedOrEnqueued(r core.JobRecord) time.Time {
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

	// Graph-level cron triggers (no per-trigger pause flag at this level).
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

	// Trigger nodes.
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
		case "poll_trigger", "google_form_trigger":
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
