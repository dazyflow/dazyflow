// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/dazyflow/dazyflow/daemon/internal/pgstore"
	"github.com/jackc/pgx/v5/pgxpool"
)

// How work reaches a runner: the daemon queues a task, an agent inside the org's
// network claims it, and the waiting step polls for the outcome. The daemon never
// dials the agent, which is the whole point — the agent is behind NAT.

type RunnerTaskState string

const (
	TaskQueued  RunnerTaskState = "queued"
	TaskRunning RunnerTaskState = "running"
	TaskDone    RunnerTaskState = "done"
	TaskFailed  RunnerTaskState = "failed"
)

var ErrTaskNotClaimable = errors.New("task is not held by this runner")

type RunnerTask struct {
	ID     string
	Tenant string
	Tags   []string

	Script  string
	Shell   string
	Env     map[string]string
	Timeout time.Duration
	Stdin   string

	sealedEnv string

	State      RunnerTaskState
	ClaimedBy  string
	Progress   string
	LeaseUntil time.Time
	Result     *RunnerTaskResult
	CreatedAt  time.Time
	FinishedAt time.Time
}

type RunnerTaskResult struct {
	ExitCode int    `json:"exit_code"`
	Stdout   string `json:"stdout,omitempty"`
	Stderr   string `json:"stderr,omitempty"`
	Error    string `json:"error,omitempty"`
}

// Generous: a lapsed claim strands work an agent may still be running.
const TaskLease = 2 * time.Minute

// Unclaimed with no eligible runner online: the step fails rather than hanging.
const RunnerPickupGrace = 30 * time.Second

// Slack above the task's own timeout, so the runner's deadline expires first and
// the more useful message wins.
const RunnerDispatchGrace = 30 * time.Second

const (
	runnerPollTightFor     = 30 * time.Second
	runnerPollSlow         = 2 * time.Second
	runnerOnlineCheckEvery = 5 * time.Second
)

const pgRunnerTaskSchema = `
CREATE TABLE IF NOT EXISTS runner_tasks (
    id           TEXT PRIMARY KEY,
    tenant       TEXT NOT NULL,
    tags         TEXT[] NOT NULL DEFAULT '{}',
    script       TEXT NOT NULL,
    shell        TEXT NOT NULL DEFAULT '',
    env          JSONB,
    stdin        TEXT NOT NULL DEFAULT '',
    timeout_ms   BIGINT NOT NULL DEFAULT 0,
    state        TEXT NOT NULL,
    claimed_by   TEXT NOT NULL DEFAULT '',
    progress     TEXT NOT NULL DEFAULT '',
    lease_until  TIMESTAMPTZ,
    result       JSONB,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at  TIMESTAMPTZ
);
ALTER TABLE runner_tasks ADD COLUMN IF NOT EXISTS progress TEXT NOT NULL DEFAULT '';
ALTER TABLE runner_tasks ADD COLUMN IF NOT EXISTS shell TEXT NOT NULL DEFAULT '';
ALTER TABLE runner_tasks ADD COLUMN IF NOT EXISTS tags TEXT[] NOT NULL DEFAULT '{}';
-- The claim query's working set: a tenant's unfinished tasks, oldest first.
CREATE INDEX IF NOT EXISTS runner_tasks_claim_idx
    ON runner_tasks (tenant, created_at)
    WHERE state IN ('queued', 'running');
-- Prune's predicate. Terminal rows are the overwhelming majority of the table
-- on a busy deployment, so without this the retention sweep scans all of them
-- every hour to find the few old enough to delete.
CREATE INDEX IF NOT EXISTS runner_tasks_prune_idx
    ON runner_tasks (finished_at)
    WHERE state IN ('done', 'failed');
`

func EnsurePgRunnerTaskSchema(ctx context.Context, pool *pgxpool.Pool) error {
	return pgstore.ApplySchema(ctx, pool, pgRunnerTaskSchema)
}

type RunnerTaskStore interface {
	Enqueue(ctx context.Context, t RunnerTask) error
	Claim(ctx context.Context, r Runner, now time.Time, lease time.Duration) (RunnerTask, error)
	// So a long script does not lapse mid-run.
	Extend(ctx context.Context, r Runner, id string, until time.Time, message string) error
	Complete(ctx context.Context, r Runner, id string, res RunnerTaskResult, now time.Time) error
	// A lapsed claim: the agent took the task and stopped answering.
	FailAbandoned(ctx context.Context, tenant, id string, now time.Time) (bool, error)
	// Only a task nobody has claimed; a held one must be left to its agent.
	CancelQueued(ctx context.Context, tenant, id string, res RunnerTaskResult, now time.Time) (bool, error)
	Get(ctx context.Context, tenant, id string) (RunnerTask, error)
	// Nobody is waiting any more, so the agent would run work with no reader.
	OrphanedTasks(ctx context.Context, now time.Time, grace, queuedCeiling time.Duration, limit int) ([]RunnerTask, error)
	DeleteByTenant(ctx context.Context, tenant string) (int, error)
}

var ErrNoTask = errors.New("no task available")

// A runner must carry ALL of the task's tags.
func eligible(t RunnerTask, r Runner) bool {
	if t.Tenant != r.Tenant {
		return false
	}
	return r.HasTags(t.Tags)
}

func heldBy(t RunnerTask, r Runner) bool {
	return t.Tenant == r.Tenant && t.ClaimedBy == r.Name && t.State == TaskRunning
}

// The agent took the task and then stopped answering.
func abandoned(t RunnerTask, now time.Time) bool {
	return t.State == TaskRunning && !t.LeaseUntil.IsZero() && now.After(t.LeaseUntil)
}

func orphaned(t RunnerTask, now time.Time, grace, queuedCeiling time.Duration) bool {
	switch t.State {
	case TaskRunning:
		return abandoned(t, now)
	case TaskQueued:
		ceiling := queuedCeiling
		if t.Timeout > 0 {
			ceiling = t.Timeout + grace
		}
		return !t.CreatedAt.IsZero() && now.Sub(t.CreatedAt) > ceiling
	default:
		return false
	}
}

func abandonedResult(runner string) RunnerTaskResult {
	return RunnerTaskResult{Error: "the runner " + runner +
		" stopped responding while this step was running, so it was not finished"}
}

func cancelledResult(reason string) RunnerTaskResult {
	return RunnerTaskResult{Error: "this step gave up before any runner ran it: " + reason}
}

type MemRunnerTaskStore struct {
	mu    sync.Mutex
	tasks map[string]*RunnerTask
}

func NewMemRunnerTaskStore() *MemRunnerTaskStore {
	return &MemRunnerTaskStore{tasks: map[string]*RunnerTask{}}
}

func (m *MemRunnerTaskStore) Enqueue(_ context.Context, t RunnerTask) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	stored := t
	m.tasks[t.ID] = &stored
	return nil
}

func (m *MemRunnerTaskStore) DeleteByTenant(_ context.Context, tenant string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for id, t := range m.tasks {
		if t.Tenant == tenant {
			delete(m.tasks, id)
			n++
		}
	}
	return n, nil
}

func (m *MemRunnerTaskStore) Claim(_ context.Context, r Runner, now time.Time, lease time.Duration) (RunnerTask, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var ids []string
	for id := range m.tasks {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		return m.tasks[ids[i]].CreatedAt.Before(m.tasks[ids[j]].CreatedAt)
	})
	for _, id := range ids {
		t := m.tasks[id]
		if !eligible(*t, r) {
			continue
		}
		// Queued only. A lapsed claim is NOT re-offered: the first agent may still be
		// running the script, and a second run would repeat its side effects.
		if t.State != TaskQueued {
			continue
		}
		t.State = TaskRunning
		t.ClaimedBy = r.Name
		t.LeaseUntil = now.Add(lease)
		return *t, nil
	}
	return RunnerTask{}, ErrNoTask
}

func (m *MemRunnerTaskStore) Extend(_ context.Context, r Runner, id string, until time.Time, message string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tasks[id]
	if !ok || !heldBy(*t, r) {
		return ErrTaskNotClaimable
	}
	t.LeaseUntil = until
	if message != "" {
		t.Progress = message
	}
	return nil
}

func (m *MemRunnerTaskStore) Complete(_ context.Context, r Runner, id string, res RunnerTaskResult, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tasks[id]
	if !ok || !heldBy(*t, r) {
		return ErrTaskNotClaimable
	}
	stored := res
	t.Result = &stored
	t.FinishedAt = now
	if res.Error != "" || res.ExitCode != 0 {
		t.State = TaskFailed
	} else {
		t.State = TaskDone
	}
	return nil
}

func (m *MemRunnerTaskStore) FailAbandoned(_ context.Context, tenant, id string, now time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tasks[id]
	if !ok || t.Tenant != tenant {
		return false, fmt.Errorf("task %q not found", id)
	}
	if !abandoned(*t, now) {
		return false, nil
	}
	res := abandonedResult(t.ClaimedBy)
	t.Result = &res
	t.State = TaskFailed
	t.FinishedAt = now
	return true, nil
}

func (m *MemRunnerTaskStore) CancelQueued(_ context.Context, tenant, id string, res RunnerTaskResult, now time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tasks[id]
	if !ok || t.Tenant != tenant {
		return false, fmt.Errorf("task %q not found", id)
	}
	if t.State != TaskQueued {
		return false, nil
	}
	stored := res
	t.Result = &stored
	t.State = TaskFailed
	t.FinishedAt = now
	return true, nil
}

func (m *MemRunnerTaskStore) OrphanedTasks(_ context.Context, now time.Time, grace, queuedCeiling time.Duration, limit int) ([]RunnerTask, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []RunnerTask
	for _, t := range m.tasks {
		if orphaned(*t, now, grace, queuedCeiling) {
			out = append(out, *t)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *MemRunnerTaskStore) Get(_ context.Context, tenant, id string) (RunnerTask, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tasks[id]
	if !ok || t.Tenant != tenant {
		return RunnerTask{}, fmt.Errorf("task %q not found", id)
	}
	return *t, nil
}

type RunnerDispatcher struct {
	Tasks         RunnerTaskStore
	Runners       *Runners
	PollInterval  time.Duration
	PickupGrace   time.Duration
	DispatchGrace time.Duration
	NewID         func() string
}

func (d *RunnerDispatcher) pickupGrace() time.Duration {
	if d.PickupGrace > 0 {
		return d.PickupGrace
	}
	return RunnerPickupGrace
}

func (d *RunnerDispatcher) dispatchGrace() time.Duration {
	if d.DispatchGrace > 0 {
		return d.DispatchGrace
	}
	return RunnerDispatchGrace
}

type DispatchRequest struct {
	Tenant  string
	Tags    []string
	Script  string
	Shell   string
	Env     map[string]string
	Stdin   string
	Timeout time.Duration
}

func (d *RunnerDispatcher) Dispatch(ctx context.Context, req DispatchRequest, onProgress func(string)) (RunnerTaskResult, error) {
	if d == nil || d.Tasks == nil {
		return RunnerTaskResult{}, fmt.Errorf("runners are not configured on this deployment")
	}
	if len(req.Tags) == 0 {
		return RunnerTaskResult{}, fmt.Errorf("this step needs at least one tag saying where to run")
	}
	matches, err := d.checkTargetExists(ctx, req)
	if err != nil {
		return RunnerTaskResult{}, err
	}

	task := RunnerTask{
		ID:        d.newID(),
		Tenant:    req.Tenant,
		Tags:      req.Tags,
		Script:    req.Script,
		Shell:     req.Shell,
		Env:       req.Env,
		Stdin:     req.Stdin,
		Timeout:   req.Timeout,
		State:     TaskQueued,
		CreatedAt: time.Now(),
	}
	if err := d.Tasks.Enqueue(ctx, task); err != nil {
		return RunnerTaskResult{}, fmt.Errorf("queue the task: %w", err)
	}
	if onProgress != nil {
		onProgress(waitingMessage(req, matches, time.Now()))
	}

	poll := d.PollInterval
	if poll <= 0 {
		poll = 500 * time.Millisecond
	}
	// Without it a step whose runner goes away waits for ever.
	var deadline time.Time
	if req.Timeout > 0 {
		deadline = time.Now().Add(req.Timeout + d.dispatchGrace())
	}
	queuedSince := time.Now()
	lastProgress := ""
	var lastOnlineCheck time.Time

	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	slowPollAfter := time.Now().Add(runnerPollTightFor)
	slowed := false
	for {
		select {
		case <-ctx.Done():
			// Close the task, or an agent picks up work nobody will read.
			_, _ = d.Tasks.CancelQueued(context.WithoutCancel(ctx), req.Tenant, task.ID,
				cancelledResult("the run was cancelled"), time.Now())
			return RunnerTaskResult{}, ctx.Err()
		case <-ticker.C:
		}
		now := time.Now()
		if !slowed && now.After(slowPollAfter) {
			slowed = true
			ticker.Reset(max(poll, runnerPollSlow))
		}
		cur, err := d.Tasks.Get(ctx, req.Tenant, task.ID)
		if err != nil {
			return RunnerTaskResult{}, fmt.Errorf("read the task back: %w", err)
		}
		if onProgress != nil && cur.Progress != "" && cur.Progress != lastProgress {
			lastProgress = cur.Progress
			onProgress(cur.Progress)
		}
		switch cur.State {
		case TaskDone, TaskFailed:
			if cur.Result == nil {
				return RunnerTaskResult{}, fmt.Errorf("the runner finished the task without reporting anything")
			}
			return *cur.Result, nil

		case TaskRunning:
			if abandoned(cur, now) {
				failed, err := d.Tasks.FailAbandoned(ctx, req.Tenant, task.ID, now)
				if err != nil {
					return RunnerTaskResult{}, fmt.Errorf("fail the abandoned task: %w", err)
				}
				if !failed {
					continue
				}
				return RunnerTaskResult{}, fmt.Errorf(
					"runner %q stopped responding while running this step; "+
						"the script was not re-run, because nobody knows how far it got",
					cur.ClaimedBy)
			}

		case TaskQueued:
			if now.Sub(queuedSince) > d.pickupGrace() &&
				now.Sub(lastOnlineCheck) >= min(runnerOnlineCheckEvery, d.pickupGrace()) {
				lastOnlineCheck = now
				if err := d.checkTargetOnline(ctx, req, now); err != nil {
					cancelled, cerr := d.Tasks.CancelQueued(ctx, req.Tenant, task.ID,
						cancelledResult(err.Error()), now)
					if cerr != nil {
						return RunnerTaskResult{}, fmt.Errorf(
							"%w (and the queued task could not be closed: %v)", err, cerr)
					}
					if !cancelled {
						continue
					}
					return RunnerTaskResult{}, err
				}
			}
		}

		if !deadline.IsZero() && now.After(deadline) {
			if cur.State == TaskQueued {
				reason := fmt.Sprintf("no machine tagged %s picked this step up within %s",
					tagList(req.Tags), req.Timeout)
				if _, cerr := d.Tasks.CancelQueued(ctx, req.Tenant, task.ID,
					cancelledResult(reason), now); cerr != nil {
					return RunnerTaskResult{}, fmt.Errorf(
						"%s (and the queued task could not be closed: %v)", reason, cerr)
				}
				return RunnerTaskResult{}, errors.New(reason)
			}
			return RunnerTaskResult{}, fmt.Errorf(
				"runner %q did not finish this step within %s", cur.ClaimedBy, req.Timeout)
		}
	}
}

func (d *RunnerDispatcher) checkTargetOnline(ctx context.Context, req DispatchRequest, now time.Time) error {
	if d.Runners == nil {
		return nil
	}
	matches, err := d.eligibleRunners(ctx, req)
	if err != nil {
		return nil
	}
	for _, r := range matches {
		if r.Online(now) {
			return nil
		}
	}
	return fmt.Errorf("no machine tagged %s has checked in recently (%s) — "+
		"the agent is not running there", tagList(req.Tags), runnerNames(matches))
}

func (d *RunnerDispatcher) checkTargetExists(ctx context.Context, req DispatchRequest) ([]Runner, error) {
	if d.Runners == nil {
		return nil, nil
	}
	matches, err := d.eligibleRunners(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("look up your runners: %w", err)
	}
	if len(matches) > 0 {
		return matches, nil
	}
	if missing, err := d.unmatchedTags(ctx, req); err == nil && len(missing) > 0 {
		return nil, fmt.Errorf("no machine carries the tag %s", tagList(missing))
	}
	return nil, fmt.Errorf("no single machine carries all of %s — "+
		"each tag exists, but no machine has the whole set", tagList(req.Tags))
}

func (d *RunnerDispatcher) unmatchedTags(ctx context.Context, req DispatchRequest) ([]string, error) {
	rs, err := d.Runners.List(ctx, req.Tenant)
	if err != nil {
		return nil, err
	}
	known := map[string]struct{}{}
	for _, r := range rs {
		for _, t := range r.Tags() {
			known[t] = struct{}{}
		}
	}
	var missing []string
	for _, t := range req.Tags {
		if _, ok := known[t]; !ok {
			missing = append(missing, t)
		}
	}
	return missing, nil
}

func (d *RunnerDispatcher) eligibleRunners(ctx context.Context, req DispatchRequest) ([]Runner, error) {
	rs, err := d.Runners.List(ctx, req.Tenant)
	if err != nil {
		return nil, err
	}
	var out []Runner
	for _, r := range rs {
		if r.HasTags(req.Tags) {
			out = append(out, r)
		}
	}
	return out, nil
}

// Names what is being waited for, so a stuck step is diagnosable from the run view.
func waitingMessage(req DispatchRequest, matches []Runner, now time.Time) string {
	base := "waiting for a machine tagged " + tagList(req.Tags)
	if len(matches) == 0 {
		return base
	}
	online := 0
	for _, r := range matches {
		if r.Online(now) {
			online++
		}
	}
	if online == 0 {
		who := fmt.Sprintf("none of the %d machines carrying them", len(matches))
		if len(matches) == 1 {
			who = "the only machine carrying them (" + matches[0].Name + ")"
		}
		return fmt.Sprintf("%s — %s has checked in recently, "+
			"so this will fail shortly unless one starts", base, who)
	}
	return fmt.Sprintf("%s (%d of %d switched on)", base, online, len(matches))
}

func tagList(tags []string) string {
	if len(tags) == 0 {
		return "(none)"
	}
	return strings.Join(tags, " + ")
}

func runnerNames(rs []Runner) string {
	if len(rs) == 0 {
		return "none"
	}
	names := make([]string, 0, len(rs))
	for _, r := range rs {
		names = append(names, r.Name)
	}
	return strings.Join(names, ", ")
}

func (d *RunnerDispatcher) newID() string {
	if d.NewID != nil {
		return d.NewID()
	}
	plain, _, err := newRunnerSecret("task_")
	if err != nil {
		return fmt.Sprintf("task_%d", time.Now().UnixNano())
	}
	return plain
}

// A nil slice would violate the NOT NULL array column.
func tagsOrEmpty(tags []string) []string {
	if tags == nil {
		return []string{}
	}
	return tags
}

func jsonOrNil(m map[string]string) []byte {
	if len(m) == 0 {
		return nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		return nil
	}
	return b
}
