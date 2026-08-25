// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// How work reaches a runner.
//
// The runner has no address, so nothing can be pushed to it. Instead a step
// enqueues a task, the agent's next poll claims it, and the step waits for the
// result. That inverts the usual direction and buys the whole "one line to
// install" property: a machine behind NAT can do this, and a machine that
// needs an inbound port cannot.
//
// The lease is the same idea the job queue already uses, with one deliberate
// difference. A claim holds a task for a bounded time; if the agent dies
// mid-task the lease lapses. The daemon's own workers then RETRY the job. A
// runner task is instead FAILED, and never handed out twice.
//
// The reason is that the daemon cannot know what the script did before the
// machine went down. `run_on_runner` declares itself non-idempotent because a
// script is arbitrary: it may have sent the invoices, charged the card, or
// appended to a ledger. Re-running it is not a retry, it is a second
// side effect — and a script that crashes its own machine would loop forever.
// Failing once, loudly, is the only answer the daemon can give honestly; the
// author is the only one who knows whether re-running is safe.

// RunnerTaskState is where a task is in its life.
type RunnerTaskState string

const (
	TaskQueued  RunnerTaskState = "queued"
	TaskRunning RunnerTaskState = "running"
	TaskDone    RunnerTaskState = "done"
	TaskFailed  RunnerTaskState = "failed"
)

// ErrTaskNotClaimable is returned when a result or progress arrives for a task
// the caller does not hold — a lapsed lease, or another agent's task.
var ErrTaskNotClaimable = errors.New("task is not held by this runner")

// RunnerTask is one unit of work for a runner.
type RunnerTask struct {
	ID     string
	Tenant string
	// Target names a runner, or a label shared by several. Exactly one is set.
	Runner string
	Label  string

	Script  string
	Env     map[string]string
	Timeout time.Duration
	// Stdin is the value wired into the step, handed to the script on standard
	// input. A value, never a path: the runner is on another machine and a path
	// on the daemon's disk means nothing there.
	Stdin string

	State      RunnerTaskState
	ClaimedBy  string
	LeaseUntil time.Time
	Result     *RunnerTaskResult
	CreatedAt  time.Time
	FinishedAt time.Time
}

// RunnerTaskResult is what the agent reports back.
type RunnerTaskResult struct {
	ExitCode int    `json:"exit_code"`
	Stdout   string `json:"stdout,omitempty"`
	Stderr   string `json:"stderr,omitempty"`
	// Error is set when the agent could not run the script at all — a command
	// not on its allow-list, a binary that does not exist, a timeout. Distinct
	// from a non-zero exit, which means the script ran and failed.
	Error string `json:"error,omitempty"`
}

// TaskLease is how long a claim holds a task before it lapses. Generous,
// because a script may legitimately run for minutes without saying anything;
// the agent extends it by reporting progress.
const TaskLease = 2 * time.Minute

// RunnerPickupGrace is how long a task may sit unclaimed while no eligible
// runner is online before the step gives up.
//
// There is a grace period at all because a runner restarting is normal and
// brief. It is short because the online window is already generous: a runner
// that has not been seen for RunnerOnlineWindow is genuinely not there, and a
// step that hangs instead of saying so is the worst outcome — the run looks
// alive and the author has no idea their machine is down.
const RunnerPickupGrace = 30 * time.Second

// RunnerDispatchGrace is the slack added to a task's own timeout to get the
// ceiling on the whole dispatch.
//
// The agent enforces the timeout on the script and reports back, which produces
// a far better message than a deadline here ever could. So this ceiling is
// deliberately the LOSER of that race: it exists only for the case where the
// agent never answers at all, and wants to fire after the agent would have.
const RunnerDispatchGrace = 30 * time.Second

const pgRunnerTaskSchema = `
CREATE TABLE IF NOT EXISTS runner_tasks (
    id           TEXT PRIMARY KEY,
    tenant       TEXT NOT NULL,
    runner       TEXT NOT NULL DEFAULT '',
    label        TEXT NOT NULL DEFAULT '',
    script       TEXT NOT NULL,
    env          JSONB,
    stdin        TEXT NOT NULL DEFAULT '',
    timeout_ms   BIGINT NOT NULL DEFAULT 0,
    state        TEXT NOT NULL,
    claimed_by   TEXT NOT NULL DEFAULT '',
    lease_until  TIMESTAMPTZ,
    result       JSONB,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at  TIMESTAMPTZ
);
-- The claim query's working set: a tenant's unfinished tasks, oldest first.
CREATE INDEX IF NOT EXISTS runner_tasks_claim_idx
    ON runner_tasks (tenant, created_at)
    WHERE state IN ('queued', 'running');
`

// EnsurePgRunnerTaskSchema creates the task table.
func EnsurePgRunnerTaskSchema(ctx context.Context, pool *pgxpool.Pool) error {
	return applyPgSchema(ctx, pool, pgRunnerTaskSchema)
}

// RunnerTaskStore is the task queue.
type RunnerTaskStore interface {
	Enqueue(ctx context.Context, t RunnerTask) error
	// Claim hands the oldest task this runner is eligible for to the agent,
	// or returns ErrNoTask when there is nothing to do.
	Claim(ctx context.Context, r Runner, now time.Time, lease time.Duration) (RunnerTask, error)
	// Extend pushes a held task's lease out, so a long script does not lapse.
	Extend(ctx context.Context, id, runnerName string, until time.Time) error
	// Complete records a result. Refuses a task the runner does not hold.
	Complete(ctx context.Context, id, runnerName string, res RunnerTaskResult, now time.Time) error
	// FailAbandoned marks a task whose lease has lapsed as failed, reporting
	// whether this call is the one that did it.
	//
	// The bool matters: a result can land between the caller noticing the lapse
	// and this call, and the agent's real answer must win over our guess that
	// it was gone. False means exactly that happened, and the caller should
	// read the task again rather than reporting a failure that did not occur.
	FailAbandoned(ctx context.Context, tenant, id string, now time.Time) (bool, error)
	// CancelQueued closes a task nobody has claimed yet, reporting whether it
	// did.
	//
	// This is what stops a script from running after the step that asked for it
	// has given up. A task left queued stays claimable forever, so a machine
	// switched on an hour later would happily run it — which for a script that
	// sends invoices is the same harm as running it twice.
	//
	// Only 'queued' is touched. False means it was claimed in the meantime, and
	// a claimed task belongs to the agent holding it: killing that would create
	// exactly the ambiguity the lease rules exist to avoid.
	CancelQueued(ctx context.Context, tenant, id string, res RunnerTaskResult, now time.Time) (bool, error)
	Get(ctx context.Context, tenant, id string) (RunnerTask, error)
}

// ErrNoTask means the queue had nothing for this runner. Not a failure — it is
// the normal answer to most polls.
var ErrNoTask = errors.New("no task available")

// eligible reports whether a runner may claim a task.
//
// A task targets a runner by name OR by label. Name is exact. Label matches any
// runner carrying it, which is how a pool of interchangeable machines works.
func eligible(t RunnerTask, r Runner) bool {
	if t.Tenant != r.Tenant {
		return false
	}
	if t.Runner != "" {
		return t.Runner == r.Name
	}
	if t.Label != "" {
		for _, l := range r.Labels {
			if l == t.Label {
				return true
			}
		}
		return false
	}
	// Neither set: nothing may claim it. A task with no target is a bug
	// upstream, and letting any runner take it would run someone's script on
	// an arbitrary machine.
	return false
}

// abandoned reports that a claim has lapsed: the agent took the task and then
// stopped saying anything, so the machine is presumed gone.
//
// A zero LeaseUntil is not abandonment. It means the row was written without a
// claim ever being recorded, and treating "no lease" as "expired lease" would
// condemn a task the moment it appeared.
func abandoned(t RunnerTask, now time.Time) bool {
	return t.State == TaskRunning && !t.LeaseUntil.IsZero() && now.After(t.LeaseUntil)
}

// abandonedResult is the result recorded for a task whose runner vanished. It
// goes in the Error field rather than the exit code, because the script did not
// exit — nobody knows what it did.
func abandonedResult(runner string) RunnerTaskResult {
	return RunnerTaskResult{Error: "the runner " + runner +
		" stopped responding while this step was running, so it was not finished"}
}

// cancelledResult is recorded for a task the waiting step gave up on, so the
// row says why it was closed rather than looking like a task that vanished.
func cancelledResult(reason string) RunnerTaskResult {
	return RunnerTaskResult{Error: "this step gave up before any runner ran it: " + reason}
}

// ---- in-memory store --------------------------------------------------

// MemRunnerTaskStore implements the queue in process.
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

func (m *MemRunnerTaskStore) Claim(_ context.Context, r Runner, now time.Time, lease time.Duration) (RunnerTask, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	// Oldest first, so a queue drains in the order it was filled.
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
		// Queued only. A lapsed claim is NOT up for grabs again — see the
		// note at the top of this file; handing it out twice would run
		// someone's script twice.
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

func (m *MemRunnerTaskStore) Extend(_ context.Context, id, runnerName string, until time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tasks[id]
	if !ok || t.ClaimedBy != runnerName || t.State != TaskRunning {
		return ErrTaskNotClaimable
	}
	t.LeaseUntil = until
	return nil
}

func (m *MemRunnerTaskStore) Complete(_ context.Context, id, runnerName string, res RunnerTaskResult, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tasks[id]
	if !ok || t.ClaimedBy != runnerName || t.State != TaskRunning {
		return ErrTaskNotClaimable
	}
	stored := res
	t.Result = &stored
	t.FinishedAt = now
	// A non-zero exit is a FAILED task, not a done one: the step should fail
	// the same way it would if a built-in step had errored.
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
	// Re-check under the lock rather than trusting the caller's earlier read:
	// that read was outside it, and the agent may have reported in between.
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

func (m *MemRunnerTaskStore) Get(_ context.Context, tenant, id string) (RunnerTask, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tasks[id]
	if !ok || t.Tenant != tenant {
		return RunnerTask{}, fmt.Errorf("task %q not found", id)
	}
	return *t, nil
}

// ---- the dispatcher the step calls -------------------------------------

// RunnerDispatcher enqueues a task and waits for a runner to finish it.
type RunnerDispatcher struct {
	Tasks   RunnerTaskStore
	Runners *Runners
	// PollInterval is how often the waiting step checks for a result. Polling
	// rather than an in-process signal because a deployment may run several
	// daemons: the agent's result can land on a different one from the step
	// that is waiting, and only the database is shared.
	PollInterval time.Duration
	// PickupGrace and DispatchGrace override RunnerPickupGrace and
	// RunnerDispatchGrace; zero means the constant. Overridable so a test can
	// exercise the give-up paths without waiting out a real timeout.
	PickupGrace   time.Duration
	DispatchGrace time.Duration
	// NewID generates task ids; overridable for tests.
	NewID func() string
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

// DispatchRequest is what the step asks for.
type DispatchRequest struct {
	Tenant  string
	Runner  string
	Label   string
	Script  string
	Env     map[string]string
	Stdin   string
	Timeout time.Duration
}

// Dispatch enqueues a task and blocks until it finishes, the context is
// cancelled, or nothing picks it up in time.
//
// The "nothing picked it up" case gets its own error deliberately. A step that
// simply hangs when a runner is offline is the worst outcome: the run looks
// alive, the lease machinery has nothing to reclaim, and the author has no idea
// their machine is down. Failing with a message that names the runner turns it
// into something actionable.
func (d *RunnerDispatcher) Dispatch(ctx context.Context, req DispatchRequest, onProgress func(string)) (RunnerTaskResult, error) {
	if d == nil || d.Tasks == nil {
		return RunnerTaskResult{}, fmt.Errorf("runners are not configured on this deployment")
	}
	if req.Runner == "" && req.Label == "" {
		return RunnerTaskResult{}, fmt.Errorf("this step needs a runner or a label to send the work to")
	}
	// Refuse up front when the target cannot possibly answer, rather than
	// enqueueing into a queue nothing reads.
	if err := d.checkTargetExists(ctx, req); err != nil {
		return RunnerTaskResult{}, err
	}

	task := RunnerTask{
		ID:        d.newID(),
		Tenant:    req.Tenant,
		Runner:    req.Runner,
		Label:     req.Label,
		Script:    req.Script,
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
		onProgress(d.waitingMessage(req))
	}

	poll := d.PollInterval
	if poll <= 0 {
		poll = 500 * time.Millisecond
	}
	// The ceiling on the whole wait. Without one, a step whose runner goes
	// away between claiming and answering waits on the ambient run context —
	// which may have no deadline at all, leaving the run alive forever.
	var deadline time.Time
	if req.Timeout > 0 {
		deadline = time.Now().Add(req.Timeout + d.dispatchGrace())
	}
	queuedSince := time.Now()

	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			// The run was cancelled or ran out of time. Close the task so a
			// runner cannot pick up a script for a run that is already over.
			// On a detached context, because ctx is what just died.
			_, _ = d.Tasks.CancelQueued(context.WithoutCancel(ctx), req.Tenant, task.ID,
				cancelledResult("the run was cancelled"), time.Now())
			return RunnerTaskResult{}, ctx.Err()
		case <-ticker.C:
		}
		now := time.Now()
		cur, err := d.Tasks.Get(ctx, req.Tenant, task.ID)
		if err != nil {
			return RunnerTaskResult{}, fmt.Errorf("read the task back: %w", err)
		}
		switch cur.State {
		case TaskDone, TaskFailed:
			if cur.Result == nil {
				return RunnerTaskResult{}, fmt.Errorf("the runner finished the task without reporting anything")
			}
			return *cur.Result, nil

		case TaskRunning:
			// Claimed, then silence. The task is failed rather than left for
			// another runner to pick up, and the step is told which machine
			// went quiet.
			if abandoned(cur, now) {
				failed, err := d.Tasks.FailAbandoned(ctx, req.Tenant, task.ID, now)
				if err != nil {
					return RunnerTaskResult{}, fmt.Errorf("fail the abandoned task: %w", err)
				}
				if !failed {
					// It answered after all. Next tick reads the real result.
					continue
				}
				return RunnerTaskResult{}, fmt.Errorf(
					"runner %q stopped responding while running this step; "+
						"the script was not re-run, because nobody knows how far it got",
					cur.ClaimedBy)
			}

		case TaskQueued:
			// Nothing has picked it up. That is normal for a moment, and
			// normal for a while if the runner is busy with another task —
			// its heartbeat keeps it online meanwhile. It is only a problem
			// once no runner that COULD take this work is there at all.
			if now.Sub(queuedSince) > d.pickupGrace() {
				if err := d.checkTargetOnline(ctx, req, now); err != nil {
					cancelled, cerr := d.Tasks.CancelQueued(ctx, req.Tenant, task.ID,
						cancelledResult(err.Error()), now)
					if cerr != nil {
						// Say both: the diagnosis is still right, and a task
						// left claimable is the dangerous half.
						return RunnerTaskResult{}, fmt.Errorf(
							"%w (and the queued task could not be closed: %v)", err, cerr)
					}
					if !cancelled {
						// A runner claimed it as we were giving up, so one is
						// there after all. Keep waiting for its answer.
						continue
					}
					return RunnerTaskResult{}, err
				}
			}
		}

		if !deadline.IsZero() && now.After(deadline) {
			if cur.State == TaskQueued {
				reason := fmt.Sprintf("no runner picked this step up within %s", req.Timeout)
				// Unlike the path above, the outcome does not change with the
				// answer: the ceiling stops the step regardless. If it was
				// claimed in the last instant there is nothing to close and
				// nothing to be done about it.
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

// checkTargetOnline reports that no runner able to take this task is currently
// present. Distinct from checkTargetExists, which asks whether one is
// registered at all: a registered machine that is switched off is the common
// case, and the message has to name it to be worth anything.
func (d *RunnerDispatcher) checkTargetOnline(ctx context.Context, req DispatchRequest, now time.Time) error {
	if d.Runners == nil {
		return nil
	}
	matches, err := d.eligibleRunners(ctx, req)
	if err != nil {
		// A database blip is not evidence the runner is down; keep waiting.
		return nil
	}
	for _, r := range matches {
		if r.Online(now) {
			return nil
		}
	}
	if req.Runner != "" {
		return fmt.Errorf("runner %q is registered but has not checked in recently — "+
			"the agent is not running on that machine", req.Runner)
	}
	return fmt.Errorf("no runner labelled %q has checked in recently — "+
		"none of those machines is running the agent", req.Label)
}

// checkTargetExists rejects a target no registered runner could serve.
func (d *RunnerDispatcher) checkTargetExists(ctx context.Context, req DispatchRequest) error {
	if d.Runners == nil {
		return nil
	}
	matches, err := d.eligibleRunners(ctx, req)
	if err != nil {
		return fmt.Errorf("look up your runners: %w", err)
	}
	if len(matches) > 0 {
		return nil
	}
	if req.Runner != "" {
		return fmt.Errorf("no runner named %q is registered for this organisation", req.Runner)
	}
	return fmt.Errorf("no runner is labelled %q", req.Label)
}

// eligibleRunners lists this organisation's runners that could take the task —
// the name match, or every runner carrying the label.
func (d *RunnerDispatcher) eligibleRunners(ctx context.Context, req DispatchRequest) ([]Runner, error) {
	rs, err := d.Runners.List(ctx, req.Tenant)
	if err != nil {
		return nil, err
	}
	var out []Runner
	for _, r := range rs {
		if req.Runner != "" {
			if r.Name == req.Runner {
				out = append(out, r)
			}
			continue
		}
		for _, l := range r.Labels {
			if l == req.Label {
				out = append(out, r)
				break
			}
		}
	}
	return out, nil
}

func (d *RunnerDispatcher) waitingMessage(req DispatchRequest) string {
	if req.Runner != "" {
		return "waiting for runner " + req.Runner
	}
	return "waiting for a runner labelled " + req.Label
}

func (d *RunnerDispatcher) newID() string {
	if d.NewID != nil {
		return d.NewID()
	}
	// Reuse the same random-id shape the rest of the daemon uses for
	// externally visible ids.
	plain, _, err := newRunnerSecret("task_")
	if err != nil {
		// rand failing is not recoverable and not worth a second error path
		// through every caller; a time-based id is still unique enough to
		// correlate one task.
		return fmt.Sprintf("task_%d", time.Now().UnixNano())
	}
	return plain
}

// jsonOrNil marshals a map for storage, returning nil for an empty one so the
// column stays NULL rather than holding "{}".
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
