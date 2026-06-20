package daemon

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/robfig/cron/v3"

	"git.sr.ht/~klahr/dazyflow/core"
)

// Scheduler reads graphs from the configured workspaces, finds those with
// cron triggers, and fires SubmitGraph internally when each schedule is
// due. One Scheduler runs per dzd instance. In a multi-node deployment
// every instance runs a Scheduler, but only the one holding the Postgres
// advisory lock (see PgLeader, wired via SetLeader in cmd/dzd) actually
// fires triggers; the rest stay warm via rescan and take over on failover.

type Scheduler struct {
	svc      *Service
	clock    func() time.Time
	parser   cron.Parser
	logger   *log.Logger
	interval time.Duration

	mu          sync.Mutex
	tracked     map[string]*scheduledGraph // key = tenant/workspace/graphID
	rescanEvery time.Duration

	// leader reports whether THIS instance may fire triggers. Default
	// always-true (single node). In a multi-node cluster, cmd/dzd wires
	// this to a Postgres advisory-lock leader so exactly one instance
	// fires crons — otherwise every node fires every schedule N times.
	// Rescan still runs on followers so a new leader can fire instantly.
	leader func() bool

	// principal used when the scheduler submits graphs internally; a
	// real deployment would give the system a dedicated identity with
	// tenant-scoped graph:run only.
	systemPrincipal func(tenant, workspace string) core.Principal
}

// scheduledGraph represents one tracked trigger. Discriminated by
// which scheduling field is set:
//
//	scheduleFn != nil  → cron-driven (wall-clock anchored)
//	interval   != 0    → poll-driven (interval-anchored from last fire)
//
// Both fields being set at once would be a programming error; the
// rescan path sets exactly one based on the trigger Type.
type scheduledGraph struct {
	graphID    string
	tenant     string
	workspace  string
	scheduleAt time.Time
	scheduleFn cron.Schedule // for cron triggers
	interval   time.Duration // for poll triggers (zero when not used)
}

// parseCronInTZ parses a 5-field cron expression as evaluated in the
// given IANA timezone, using robfig/cron's CRON_TZ= prefix so the
// wall-clock fields anchor to a real zone (and track DST). An empty tz
// defaults to UTC, which keeps firing deterministic regardless of the
// daemon host's local time. A malformed tz surfaces as a parse error so
// the caller can skip it (scheduler) or report it (validate endpoint),
// rather than silently firing in the wrong zone. Used by BOTH the
// scheduler and the validate endpoint so the preview a user sees and the
// time the flow actually fires are computed identically.
func parseCronInTZ(p cron.Parser, expr, tz string) (cron.Schedule, error) {
	if tz == "" {
		tz = "UTC"
	}
	if _, err := time.LoadLocation(tz); err != nil {
		return nil, fmt.Errorf("bad timezone %q: %w", tz, err)
	}
	return p.Parse("CRON_TZ=" + tz + " " + expr)
}

// paramSeconds reads an integer-valued node param (e.g. a poll interval),
// tolerating the float64 that JSON unmarshalling produces as well as a plain
// int/int64. Returns 0 when the key is absent or not a number — which callers
// treat as "unset" (manual-only).
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

// triggerNodeDisabled reports whether a trigger node has been
// individually paused via its `disabled` param. This is finer-grained
// than the whole-flow graph.Disabled switch: a flow with both a cron
// and a poll trigger can pause just one. Stored in node Params (a plain
// JSON bool) so no Node struct / schema change is needed, and the value
// round-trips through the normal graph save path. Absent/false = active.
func triggerNodeDisabled(node core.Node) bool {
	v, _ := node.Params["disabled"].(bool)
	return v
}

// nextFireFrom returns the next time this entry should fire, given
// the current time. Cron entries delegate to the cron parser; poll
// entries add their interval to now (interval-anchored — see the
// GraphTrigger doc comment).
func (e *scheduledGraph) nextFireFrom(now time.Time) time.Time {
	if e.scheduleFn != nil {
		return e.scheduleFn.Next(now)
	}
	return now.Add(e.interval)
}

// NewScheduler wires a scheduler around the daemon Service. interval is
// how often the scheduler checks for due triggers; rescanEvery is how
// often it refreshes the list of tracked graphs (so workspace edits
// take effect without restarting dzd).
func NewScheduler(svc *Service) *Scheduler {
	return &Scheduler{
		svc:         svc,
		clock:       time.Now,
		parser:      cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow),
		logger:      log.New(log.Writer(), "scheduler: ", log.LstdFlags),
		interval:    1 * time.Second,
		rescanEvery: 30 * time.Second,
		tracked:     make(map[string]*scheduledGraph),
		leader:      func() bool { return true }, // single-node default
		systemPrincipal: func(tenant, workspace string) core.Principal {
			// graph:admin lets cron-fired runs bypass per-flow
			// visibility: an admin who set up a schedule on a private
			// flow shouldn't have that schedule break because they're
			// not the active subject at fire time.
			return SystemPrincipal("dazyflow-scheduler", tenant, workspace)
		},
	}
}

// SetLeader installs the leadership predicate. When fn returns false
// this instance rescans but doesn't fire — used by cmd/dzd to gate the
// scheduler on a Postgres advisory-lock leader in multi-node clusters.
func (s *Scheduler) SetLeader(fn func() bool) {
	if fn != nil {
		s.leader = fn
	}
}

// Run blocks until ctx is cancelled. It alternates between scheduling
// ticks (every interval) and full workspace rescans (every rescanEvery).
func (s *Scheduler) Run(ctx context.Context) error {
	s.logger.Printf("started (tick=%s, rescan=%s)", s.interval, s.rescanEvery)
	if err := s.rescan(ctx); err != nil {
		s.logger.Printf("initial rescan: %v", err)
	}
	tickT := time.NewTicker(s.interval)
	rescanT := time.NewTicker(s.rescanEvery)
	defer tickT.Stop()
	defer rescanT.Stop()
	for {
		select {
		case <-ctx.Done():
			s.logger.Printf("stopped: %v", ctx.Err())
			return ctx.Err()
		case <-tickT.C:
			// Only the leader fires; followers stay warm via rescan and
			// take over instantly if the leader dies.
			if s.leader == nil || s.leader() {
				s.fireDue(ctx)
			}
		case <-rescanT.C:
			if err := s.rescan(ctx); err != nil {
				s.logger.Printf("rescan: %v", err)
			}
		}
	}
}

// rescan inspects the workspace lookup for graphs with cron triggers and
// rebuilds the tracked map. Existing entries' next-fire time is preserved
// when the spec didn't change so we don't double-fire.
func (s *Scheduler) rescan(ctx context.Context) error {
	enum, ok := s.svc.Workspaces.(WorkspaceEnumerator)
	if !ok {
		return fmt.Errorf("scheduler: workspace lookup does not support enumeration")
	}
	now := s.clock()
	next := make(map[string]*scheduledGraph)
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
			// A disabled flow is paused — skip every trigger here.
			// Cron + poll triggers will resume automatically once the
			// flow is re-enabled and the scheduler re-scans.
			if g.Disabled {
				continue
			}
			// Require published: a scheduled flow fires only once it's been
			// published. A never-published draft with a configured schedule
			// is not enrolled, so it doesn't tick (and the editor shows a
			// "needs publish" chip). The schedule timing itself is still read
			// from HEAD below, so editing the interval after publishing takes
			// effect immediately — only enrollment gates on publish state.
			if pub, err := store.PublishedCommit(gid); err != nil || pub == "" {
				continue
			}
			for triggerIdx, t := range g.Triggers {
				var entry *scheduledGraph
				switch t.Type {
				case "cron":
					if t.Cron == "" {
						continue
					}
					sched, err := parseCronInTZ(s.parser, t.Cron, t.TZ)
					if err != nil {
						s.logger.Printf("bad cron %q (tz %q) on %s/%s/%s: %v",
							t.Cron, t.TZ, tenant, workspace, gid, err)
						continue
					}
					entry = &scheduledGraph{
						graphID:    gid,
						tenant:     tenant,
						workspace:  workspace,
						scheduleFn: sched,
					}
				default:
					// "webhook" and any other type aren't scheduler-driven.
					// "poll" is no longer a graph-level trigger — the interval
					// lives on the poll_trigger NODE now (scanned below), so a
					// legacy graph-level poll falls through here and is ignored
					// (the trigger lint flags it for migration to a node).
					continue
				}

				// Key includes the trigger index so a graph with both a
				// cron AND a poll trigger gets two scheduler entries (one
				// per trigger) instead of clobbering one with the other.
				k := fmt.Sprintf("%s/%s/%s#%d", tenant, workspace, gid, triggerIdx)
				if existing, ok := s.tracked[k]; ok && !existing.scheduleAt.IsZero() {
					entry.scheduleAt = existing.scheduleAt
				} else {
					entry.scheduleAt = entry.nextFireFrom(now)
				}
				next[k] = entry
			}

			// cron_trigger NODES carry their own schedule (Phase 2: the
			// cron expression lives on the node, not only on a graph-level
			// trigger). Scan them in addition to g.Triggers so a
			// node-authored schedule fires too. Keyed by node ID — stable
			// across edits, unlike the trigger-array index used above, so
			// rescans don't reshuffle keys and double-fire.
			for _, node := range g.Nodes {
				if node.Module != "cron_trigger" {
					continue
				}
				if triggerNodeDisabled(node) {
					continue // this trigger is individually paused
				}
				expr, _ := node.Params["cron"].(string)
				expr = strings.TrimSpace(expr)
				if expr == "" {
					continue // unscheduled node — runs only on manual Run
				}
				tz, _ := node.Params["tz"].(string)
				sched, err := parseCronInTZ(s.parser, expr, tz)
				if err != nil {
					s.logger.Printf("bad cron %q (tz %q) on node %s of %s/%s/%s: %v",
						expr, tz, node.ID, tenant, workspace, gid, err)
					continue
				}
				entry := &scheduledGraph{
					graphID:    gid,
					tenant:     tenant,
					workspace:  workspace,
					scheduleFn: sched,
				}
				k := fmt.Sprintf("%s/%s/%s@%s", tenant, workspace, gid, node.ID)
				if existing, ok := s.tracked[k]; ok && !existing.scheduleAt.IsZero() {
					entry.scheduleAt = existing.scheduleAt
				} else {
					entry.scheduleAt = entry.nextFireFrom(now)
				}
				next[k] = entry
			}

			// poll_trigger NODES carry their interval on the node (same
			// model as cron_trigger above; poll is no longer a graph-level
			// trigger). Same overflow/ceiling guard the old graph-level poll
			// used: reject <= 0 and anything past the 1-year max, since
			// IntervalSeconds * time.Second overflows int64 ns past ~292y and
			// would otherwise fire every tick.
			//
			// google_form_trigger uses the identical interval mechanism: the
			// scheduler just fires the graph on the node's interval_seconds,
			// and the node itself fetches new Form responses since its stored
			// cursor at execute time (see drops/trigger/gform).
			for _, node := range g.Nodes {
				if node.Module != "poll_trigger" && node.Module != "google_form_trigger" {
					continue
				}
				if triggerNodeDisabled(node) {
					continue // this trigger is individually paused
				}
				secs := paramSeconds(node.Params, "interval_seconds")
				if secs == 0 {
					continue // unset — runs only on manual Run
				}
				if secs < 0 || secs > core.MaxPollIntervalSeconds {
					s.logger.Printf("bad poll interval %d on node %s of %s/%s/%s",
						secs, node.ID, tenant, workspace, gid)
					continue
				}
				entry := &scheduledGraph{
					graphID:   gid,
					tenant:    tenant,
					workspace: workspace,
					interval:  time.Duration(secs) * time.Second,
				}
				k := fmt.Sprintf("%s/%s/%s@%s", tenant, workspace, gid, node.ID)
				if existing, ok := s.tracked[k]; ok && !existing.scheduleAt.IsZero() {
					entry.scheduleAt = existing.scheduleAt
				} else {
					entry.scheduleAt = entry.nextFireFrom(now)
				}
				next[k] = entry
			}
		}
	}
	s.mu.Lock()
	s.tracked = next
	s.mu.Unlock()
	_ = ctx // reserved for future cancellation hooks
	return nil
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
		// A zero scheduleAt means "never fires" — cron.Schedule.Next gives
		// up on an impossible date (e.g. Feb 30) and returns the zero time.
		// Without this guard the zero time reads as "due now" and the graph
		// fires every tick forever. Treat it as dormant.
		if e.scheduleAt.IsZero() {
			continue
		}
		if !e.scheduleAt.After(now) {
			s.fireGraph(ctx, e)
			s.mu.Lock()
			e.scheduleAt = e.nextFireFrom(now)
			s.mu.Unlock()
		}
	}
}

func (s *Scheduler) fireGraph(ctx context.Context, e *scheduledGraph) {
	// Plan gate (T3): on deployments that keep scheduling off the free
	// plan, skip the fire (logged, not silent — and the Usage page tells
	// the tenant why). The run-limit gate inside SubmitGraph still
	// applies on top for pro-allowed fires.
	if err := s.svc.checkTriggerQuota(ctx, e.tenant); err != nil {
		s.logger.Printf("skip %s/%s/%s: %v", e.tenant, e.workspace, e.graphID, err)
		return
	}
	store, err := s.svc.Workspaces.Open(e.tenant, e.workspace)
	if err != nil {
		s.logger.Printf("open ws %s/%s: %v", e.tenant, e.workspace, err)
		return
	}
	// Require published: never auto-fire a flow that hasn't been published.
	// rescan already skips enrolling unpublished flows; this is the
	// belt-and-braces gate in case publish state changed between the last
	// rescan and this tick (e.g. the flow was unpublished/rolled back).
	if pub, err := store.PublishedCommit(e.graphID); err != nil || pub == "" {
		if err != nil {
			s.logger.Printf("skip %s/%s/%s: published lookup: %v", e.tenant, e.workspace, e.graphID, err)
		} else {
			s.logger.Printf("skip %s/%s/%s: not published (publish to enable its schedule)", e.tenant, e.workspace, e.graphID)
		}
		return
	}
	// Fire the PUBLISHED revision, not the draft at HEAD: an author can
	// keep editing a flow without a half-finished change firing on the
	// next cron tick. The schedule itself (when to fire, whether the
	// trigger is paused) is read from HEAD during rescan, so timing + pause
	// changes still take effect immediately — only the executed graph
	// content is pinned to the published version.
	g, err := store.LoadPublishedOrHead(e.graphID)
	if err != nil {
		s.logger.Printf("load %s/%s/%s: %v", e.tenant, e.workspace, e.graphID, err)
		return
	}
	p := s.systemPrincipal(e.tenant, e.workspace)
	runID, err := s.svc.SubmitGraph(ctx, p, g)
	if err != nil {
		s.logger.Printf("fire %s/%s/%s: %v", e.tenant, e.workspace, e.graphID, err)
		return
	}
	s.logger.Printf("fired %s/%s/%s → %s", e.tenant, e.workspace, e.graphID, runID)
}

func splitKey(key string) (tenant, workspace string, ok bool) {
	for i := 0; i < len(key); i++ {
		if key[i] == '/' {
			return key[:i], key[i+1:], true
		}
	}
	return "", "", false
}

// TrackedCount reports how many graphs the scheduler is currently
// watching. Exposed for tests.
func (s *Scheduler) TrackedCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.tracked)
}

// SetClock lets tests inject a deterministic clock. Production code
// uses time.Now via the field default.
func (s *Scheduler) SetClock(clock func() time.Time) {
	s.clock = clock
}

// SetInterval lets tests tighten the tick rate.
func (s *Scheduler) SetInterval(tick, rescan time.Duration) {
	s.interval = tick
	s.rescanEvery = rescan
}
