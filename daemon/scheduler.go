package daemon

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/robfig/cron/v3"

	"git.sr.ht/~klahr/hazy-flow/core"
)

// Scheduler reads graphs from the configured workspaces, finds those with
// cron triggers, and fires SubmitGraph internally when each schedule is
// due. One Scheduler runs per hzd instance. In a multi-node deployment
// every instance runs a Scheduler, but only the one holding the Postgres
// advisory lock (see PgLeader, wired via SetLeader in cmd/hzd) actually
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
	// always-true (single node). In a multi-node cluster, cmd/hzd wires
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
// take effect without restarting hzd).
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
			return core.Principal{
				Subject:   "hazyflow-scheduler",
				Tenant:    tenant,
				Workspace: workspace,
				Roles: []core.Role{{
					Name:        "scheduler",
					Permissions: []core.Permission{core.PermGraphRun, core.PermGraphAdmin},
				}},
			}
		},
	}
}

// SetLeader installs the leadership predicate. When fn returns false
// this instance rescans but doesn't fire — used by cmd/hzd to gate the
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
			for triggerIdx, t := range g.Triggers {
				var entry *scheduledGraph
				switch t.Type {
				case "cron":
					if t.Cron == "" {
						continue
					}
					sched, err := s.parser.Parse(t.Cron)
					if err != nil {
						s.logger.Printf("bad cron %q on %s/%s/%s: %v",
							t.Cron, tenant, workspace, gid, err)
						continue
					}
					entry = &scheduledGraph{
						graphID:    gid,
						tenant:     tenant,
						workspace:  workspace,
						scheduleFn: sched,
					}
				case "poll":
					if t.IntervalSeconds <= 0 {
						s.logger.Printf("bad poll interval %d on %s/%s/%s",
							t.IntervalSeconds, tenant, workspace, gid)
						continue
					}
					entry = &scheduledGraph{
						graphID:   gid,
						tenant:    tenant,
						workspace: workspace,
						interval:  time.Duration(t.IntervalSeconds) * time.Second,
					}
				default:
					// "webhook" and any other type aren't scheduler-driven.
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
		if !e.scheduleAt.After(now) {
			s.fireGraph(ctx, e)
			s.mu.Lock()
			e.scheduleAt = e.nextFireFrom(now)
			s.mu.Unlock()
		}
	}
}

func (s *Scheduler) fireGraph(ctx context.Context, e *scheduledGraph) {
	store, err := s.svc.Workspaces.Open(e.tenant, e.workspace)
	if err != nil {
		s.logger.Printf("open ws %s/%s: %v", e.tenant, e.workspace, err)
		return
	}
	g, err := store.Load(e.graphID)
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
