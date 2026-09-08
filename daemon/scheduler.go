// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"log"
	"strconv"
	"sync"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/pollstate"
)

// Fires only the PUBLISHED revision, and only when this instance is leader.

type Scheduler struct {
	svc      *Service
	clock    func() time.Time
	parser   cron.Parser
	logger   *log.Logger
	interval time.Duration

	mu          sync.Mutex
	tracked     map[string]*scheduledGraph // key = tenant/workspace/graphID
	rescanEvery time.Duration

	// Coalesced, so a broken schedule writes one marker rather than one per tick.
	skipMarked map[string]time.Time

	// Defaults to true, so a single-node deployment fires without configuring it.
	leader func() bool

	systemPrincipal func(tenant, workspace string) core.Principal

	pollState func(ctx context.Context, tenant, graphID string) *pollstate.Marker
}

// Discriminated by interval: >0 is a poll entry, 0 a cron entry.
type scheduledGraph struct {
	graphID    string
	tenant     string
	workspace  string
	scheduleAt time.Time
	scheduleFn cron.Schedule // for cron triggers
	interval   time.Duration // BASE poll interval (zero when not poll-driven)

	// Identifies the CADENCE, so an entry survives an unrelated edit to its flow.
	specKey string

	// Poll entries only; a consistently-empty poller widens its own interval.
	emptyStreak  int
	lastMarkerAt time.Time
}

// Evaluated IN the named zone, so a wall-clock field survives DST. An unknown
// zone falls back to UTC rather than the host's local time.
func parseCronInTZ(p cron.Parser, expr, tz string) (cron.Schedule, error) {
	if tz == "" {
		tz = "UTC"
	}
	if _, err := time.LoadLocation(tz); err != nil {
		return nil, fmt.Errorf("bad timezone %q: %w", tz, err)
	}
	return p.Parse("CRON_TZ=" + tz + " " + expr)
}

func paramSeconds(params map[string]any, key string) int {
	switch v := params[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	}
	return 0
}

// A per-node switch, distinct from the whole-flow one: the scheduler must not
// enroll a step its author turned off.
func triggerNodeDisabled(node core.Node) bool {
	if node.Disabled {
		return true
	}
	v, _ := node.Params["disabled"].(bool)
	return v
}

func (e *scheduledGraph) nextFireFrom(now time.Time) time.Time {
	if e.scheduleFn != nil {
		return e.scheduleFn.Next(now)
	}
	return now.Add(e.effectiveInterval())
}

// Deterministic per-entry offset, so a fleet of pollers on the same interval
// does not fire in one thundering herd.
func (e *scheduledGraph) staggeredNextFire(key string, now time.Time) time.Time {
	return e.nextFireFrom(now).Add(-pollJitter(key, e.interval))
}

func (s *Scheduler) isLeader() bool {
	return s.leader == nil || s.leader()
}

const (
	maxPollJitter = 60 * time.Second

	// Caps how far an empty poller's interval may widen.
	maxPollBackoffMultiplier = 8

	// Consecutive empty fires tolerated before backoff starts.
	pollBackoffGrace = 3
)

func pollJitter(key string, interval time.Duration) time.Duration {
	if interval <= 0 {
		return 0
	}
	span := min(interval/4, maxPollJitter)
	if span <= 0 {
		return 0
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(key))
	return time.Duration(h.Sum64() % uint64(span))
}

// The base interval widened by the empty streak, capped.
func (e *scheduledGraph) effectiveInterval() time.Duration {
	if e.interval <= 0 {
		return e.interval
	}
	mult := 1
	if e.emptyStreak >= pollBackoffGrace {
		shift := min(e.emptyStreak-pollBackoffGrace+1, 16)
		mult = min(1<<uint(shift), maxPollBackoffMultiplier) // 2, 4, 8, …
	}
	eff := e.interval * time.Duration(mult)
	if ceiling := time.Duration(core.MaxPollIntervalSeconds) * time.Second; eff > ceiling {
		eff = ceiling
	}
	return eff
}

func NewScheduler(svc *Service) *Scheduler {
	return &Scheduler{
		svc:         svc,
		clock:       time.Now,
		parser:      scheduleCronParser,
		logger:      log.New(log.Writer(), "scheduler: ", log.LstdFlags),
		interval:    1 * time.Second,
		rescanEvery: 30 * time.Second,
		tracked:     make(map[string]*scheduledGraph),
		leader:      func() bool { return true }, // single-node default
		systemPrincipal: func(tenant, workspace string) core.Principal {
			return SystemPrincipal("dazyflow-scheduler", tenant, workspace)
		},
	}
}

// A false predicate suppresses firing entirely, so a follower is inert.
func (s *Scheduler) SetLeader(fn func() bool) {
	if fn != nil {
		s.leader = fn
	}
}

// Without it every poller runs at its base interval.
func (s *Scheduler) SetPollStateReader(fn func(ctx context.Context, tenant, graphID string) *pollstate.Marker) {
	s.pollState = fn
}

func (s *Scheduler) Run(ctx context.Context) error {
	s.logger.Printf("started (tick=%s, rescan=%s)", s.interval, s.rescanEvery)
	if err := s.rescan(ctx); err != nil {
		s.logger.Printf("initial rescan: %v", err)
	}
	tickT := time.NewTicker(s.interval)
	rescanT := time.NewTicker(s.rescanEvery)
	defer tickT.Stop()
	defer rescanT.Stop()
	// Seeded from the predicate, so a leader at startup does not read as a takeover.
	wasLeader := s.isLeader()
	for {
		select {
		case <-ctx.Done():
			s.logger.Printf("stopped: %v", ctx.Err())
			return ctx.Err()
		case <-tickT.C:
			isLeader := s.isLeader()
			if isLeader && !wasLeader {
				// A follower's scheduleAt is frozen at whatever it was, so a takeover must
				// re-anchor or the new leader fires on the dead one's stale clock.
				s.reanchor(ctx, s.clock())
			}
			wasLeader = isLeader
			if isLeader {
				s.fireDue(ctx)
			}
		case <-rescanT.C:
			if err := s.rescan(ctx); err != nil {
				s.logger.Printf("rescan: %v", err)
			}
		}
	}
}

func (s *Scheduler) rescan(ctx context.Context) error {
	specs, err := s.collectSpecs(ctx)
	if err != nil {
		return err
	}
	s.applySpecs(specs, s.clock())
	return nil
}

// Published and non-disabled only.
func (s *Scheduler) collectSpecs(ctx context.Context) ([]ScheduleSpec, error) {
	if store := s.svc.Schedules; store != nil {
		return store.ListSchedules(ctx)
	}
	return s.collectSpecsFromWorkspaces()
}

func (s *Scheduler) collectSpecsFromWorkspaces() ([]ScheduleSpec, error) {
	enum, ok := s.svc.Workspaces.(WorkspaceEnumerator)
	if !ok {
		return nil, fmt.Errorf("scheduler: workspace lookup does not support enumeration")
	}
	var out []ScheduleSpec
	for key, store := range enum.All() {
		tenant, workspace, ok := splitKey(key)
		if !ok {
			continue
		}
		graphIDs, err := store.ListGraphs()
		if err != nil {
			s.logger.Printf("list graphs in %s/%s: %v", tenant, workspace, err)
			continue
		}
		for _, gid := range graphIDs {
			g, err := store.Load(gid)
			if err != nil {
				continue
			}
			// Enrollment requires a published flow; the cadence is read from that revision.
			if pub, err := store.PublishedCommit(gid); err != nil || pub == "" {
				continue
			}
			out = append(out, DeriveScheduleSpecs(s.parser, tenant, workspace, g, s.logger.Printf)...)
		}
	}
	return out, nil
}

// Carries a surviving entry's next-fire forward, so an unrelated edit does not
// reset a schedule.
func (s *Scheduler) applySpecs(specs []ScheduleSpec, now time.Time) {
	next := make(map[string]*scheduledGraph, len(specs))
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, spec := range specs {
		// Unchanged cadence: carry the parsed schedule and backoff state forward.
		if prev, ok := s.tracked[spec.EntryKey]; ok && prev.specKey == spec.SpecKey && !prev.scheduleAt.IsZero() {
			carried := *prev
			next[spec.EntryKey] = &carried
			continue
		}
		entry, err := s.entryFromSpec(spec)
		if err != nil {
			s.logger.Printf("schedule %s: %v", spec.EntryKey, err)
			continue
		}
		entry.scheduleAt = entry.staggeredNextFire(spec.EntryKey, now)
		next[spec.EntryKey] = entry
	}
	s.tracked = next
}

func (s *Scheduler) entryFromSpec(spec ScheduleSpec) (*scheduledGraph, error) {
	e := &scheduledGraph{
		graphID:   spec.GraphID,
		tenant:    spec.Tenant,
		workspace: spec.Workspace,
		specKey:   spec.SpecKey,
	}
	if spec.IsPoll() {
		e.interval = time.Duration(spec.IntervalSeconds) * time.Second
		return e, nil
	}
	sched, err := parseCronInTZ(s.parser, spec.Cron, spec.TZ)
	if err != nil {
		return nil, err
	}
	e.scheduleFn = sched
	return e, nil
}

// Discards a frozen next-fire, which is what a takeover needs.
func (s *Scheduler) reanchor(ctx context.Context, now time.Time) {
	s.mu.Lock()
	stale := make([]*scheduledGraph, 0, len(s.tracked))
	for k, e := range s.tracked {
		// A past scheduleAt is a fire the dead leader owed and never made.
		if !e.scheduleAt.IsZero() && !e.scheduleAt.After(now) {
			carried := *e
			stale = append(stale, &carried)
		}
		e.scheduleAt = e.staggeredNextFire(k, now)
	}
	s.mu.Unlock()
	for _, e := range stale {
		s.recordMissedFires(ctx, e, now)
	}
}

const maxCountedMissedFires = 500

// Notes fires that were due and never happened, so a leadership gap is visible
// in the Runs list rather than silently absent.
func (s *Scheduler) recordMissedFires(ctx context.Context, e *scheduledGraph, now time.Time) {
	if e.scheduleAt.IsZero() {
		return
	}
	missed := 0
	for t := e.nextFireFrom(e.scheduleAt); !t.After(now) && missed < maxCountedMissedFires; t = e.nextFireFrom(t) {
		if t.IsZero() {
			return // an impossible schedule; nextFireFrom gave up
		}
		missed++
	}
	if missed == 0 {
		return
	}
	count := strconv.Itoa(missed)
	if missed >= maxCountedMissedFires {
		count = "at least " + count
	}
	due, reached := e.scheduleAt.UTC().Format(time.RFC3339), now.UTC().Format(time.RFC3339)
	s.logger.Printf("missed %s fire(s) of %s/%s/%s (due %s, reached %s)",
		count, e.tenant, e.workspace, e.graphID, due, reached)
	s.markBroken(ctx, e, "schedule_fires_missed",
		fmt.Sprintf("This flow's schedule missed %s run(s): one was due at %s and the scheduler "+
			"only reached it at %s. Missed runs are not made up — the schedule continues from now.",
			count, due, reached))
}

func (s *Scheduler) fireDue(ctx context.Context) {
	now := s.clock()
	s.mu.Lock()
	entries := make([]*scheduledGraph, 0, len(s.tracked))
	for _, e := range s.tracked {
		entries = append(entries, e)
	}
	s.mu.Unlock()

	for _, e := range entries {
		// A zero scheduleAt means "never fires".
		if e.scheduleAt.IsZero() {
			continue
		}
		if !e.scheduleAt.After(now) {
			// Whole fires fell in the gap, so say so rather than firing them all now.
			s.recordMissedFires(ctx, e, now)
			s.fireGraph(ctx, e)
			// Fold the latest outcome into the empty streak before computing the next fire.
			marker := s.readPollMarker(ctx, e)
			s.mu.Lock()
			s.foldPollOutcomeLocked(e, marker)
			e.scheduleAt = e.nextFireFrom(now)
			s.mu.Unlock()
		}
	}
}

// The one I/O in the tick, so it is best-effort and never blocks the loop.
func (s *Scheduler) readPollMarker(ctx context.Context, e *scheduledGraph) *pollstate.Marker {
	if e.interval <= 0 || s.pollState == nil {
		return nil
	}
	return s.pollState(ctx, e.tenant, e.graphID)
}

func (s *Scheduler) foldPollOutcomeLocked(e *scheduledGraph, m *pollstate.Marker) {
	if m == nil {
		return
	}
	at := m.ParseAt()
	if !at.After(e.lastMarkerAt) {
		return // already counted this run's outcome (or unparseable timestamp)
	}
	e.lastMarkerAt = at
	if m.Empty {
		e.emptyStreak++
	} else {
		e.emptyStreak = 0
	}
}

func (s *Scheduler) fireGraph(ctx context.Context, e *scheduledGraph) {
	if err := s.svc.checkTriggerQuota(ctx, e.tenant); err != nil {
		s.logger.Printf("skip %s/%s/%s: %v", e.tenant, e.workspace, e.graphID, err)
		if s.markOnce("quota", e.tenant, e.workspace, e.graphID) {
			s.svc.recordSkippedFire(ctx, e.tenant, e.workspace, e.graphID, "plan_polling_off",
				"Scheduled run skipped — this plan does not include scheduled and polling triggers. "+
					"Manual runs still work.")
		}
		return
	}
	store, err := s.svc.Workspaces.Open(e.tenant, e.workspace)
	if err != nil {
		// The flow cannot be reached, so nobody would otherwise be told.
		s.logger.Printf("open ws %s/%s: %v", e.tenant, e.workspace, err)
		s.markBroken(ctx, e, "workspace_unavailable",
			"This flow's schedule could not run: its workspace could not be opened. "+
				"The flow has not run since. Error: "+err.Error())
		return
	}
	// Never auto-fire an unpublished flow.
	if pub, err := store.PublishedCommit(e.graphID); err != nil || pub == "" {
		if err != nil {
			s.logger.Printf("skip %s/%s/%s: published lookup: %v", e.tenant, e.workspace, e.graphID, err)
			s.markBroken(ctx, e, "publish_lookup_failed",
				"This flow's schedule could not run: its published revision could not be looked up. "+
					"The flow has not run since. Error: "+err.Error())
		} else {
			// Deliberate: unpublishing is how a schedule is turned off.
			s.logger.Printf("skip %s/%s/%s: not published (publish to enable its schedule)", e.tenant, e.workspace, e.graphID)
		}
		return
	}
	// The PUBLISHED revision, not HEAD, so an in-progress edit never fires.
	g, err := store.LoadPublished(e.graphID)
	if err != nil {
		s.logger.Printf("load %s/%s/%s: %v", e.tenant, e.workspace, e.graphID, err)
		s.markBroken(ctx, e, "published_flow_unreadable",
			"This flow's schedule could not run: its published version could not be read. "+
				"The flow has not run since it broke — re-publish it to fix. Error: "+err.Error())
		return
	}
	// Paused flows never reach here; specs drop them.
	if g.Disabled {
		s.logger.Printf("skip %s/%s/%s: flow is paused", e.tenant, e.workspace, e.graphID)
		return
	}
	p := s.systemPrincipal(e.tenant, e.workspace)
	runID, err := s.svc.SubmitGraph(ctx, p, g)
	if err != nil {
		// Refused over the monthly cap, and counted so the operator can see it.
		switch {
		case errors.Is(err, core.ErrPlanLimit):
			if s.svc.Usage != nil {
				_ = s.svc.Usage.AddSkippedRun(ctx, e.tenant, s.clock())
			}
			if s.markOnce("cap", e.tenant, e.workspace, e.graphID) {
				s.svc.recordSkippedFire(ctx, e.tenant, e.workspace, e.graphID, "plan_run_cap",
					"Scheduled run skipped — over the plan's monthly run limit.")
			}
			s.svc.notifyRunCapReached(g)
		default:
			if s.markOnce("submit", e.tenant, e.workspace, e.graphID) {
				s.svc.recordBrokenSchedule(ctx, g, "schedule_submit_failed",
					"This flow's schedule could not start a run: "+err.Error())
			}
		}
		s.logger.Printf("fire %s/%s/%s: %v", e.tenant, e.workspace, e.graphID, err)
		return
	}
	s.logger.Printf("fired %s/%s/%s → %s", e.tenant, e.workspace, e.graphID, runID)
}

// Records a schedule that could not fire, so it is visible rather than silent.
func (s *Scheduler) markBroken(ctx context.Context, e *scheduledGraph, code, message string) {
	if !s.markOnce(code, e.tenant, e.workspace, e.graphID) {
		return
	}
	g := core.Graph{ID: e.graphID, Tenant: e.tenant, Workspace: e.workspace}
	if store, err := s.svc.Workspaces.Open(e.tenant, e.workspace); err == nil {
		if draft, derr := store.Load(e.graphID); derr == nil {
			g.Name, g.Owner, g.Language = draft.Name, draft.Owner, draft.Language
			g.FailureNotify = draft.FailureNotify
		}
	}
	s.svc.recordBrokenSchedule(ctx, g, code, message)
}

const skipMarkerWindow = time.Hour

// Coalesces repeat markers of the same kind for the same flow.
func (s *Scheduler) markOnce(kind, tenant, workspace, graphID string) bool {
	key := kind + "|" + tenant + "/" + workspace + "/" + graphID
	now := s.clock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.skipMarked == nil {
		s.skipMarked = map[string]time.Time{}
	}
	if last, ok := s.skipMarked[key]; ok && now.Sub(last) < skipMarkerWindow {
		return false
	}
	s.skipMarked[key] = now
	return true
}

func splitKey(key string) (tenant, workspace string, ok bool) {
	for i := 0; i < len(key); i++ {
		if key[i] == '/' {
			return key[:i], key[i+1:], true
		}
	}
	return "", "", false
}

func (s *Scheduler) TrackedCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.tracked)
}

func (s *Scheduler) SetClock(clock func() time.Time) {
	s.clock = clock
}

func (s *Scheduler) SetInterval(tick, rescan time.Duration) {
	s.interval = tick
	s.rescanEvery = rescan
}
