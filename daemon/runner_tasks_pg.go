// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PgRunnerTaskStore is the durable task queue.
//
// It exists for the same reason the job queue is in Postgres: a deployment may
// run several daemons, and the agent's result can land on a different one from
// the step that is waiting. Only the database is shared, so only the database
// can carry the handoff. With the in-memory queue, that step waits for a result
// that was delivered to a machine it cannot see.
type PgRunnerTaskStore struct {
	pool *pgxpool.Pool
}

func NewPgRunnerTaskStore(ctx context.Context, pool *pgxpool.Pool) (*PgRunnerTaskStore, error) {
	if err := EnsurePgRunnerTaskSchema(ctx, pool); err != nil {
		return nil, err
	}
	return &PgRunnerTaskStore{pool: pool}, nil
}

const runnerTaskColumns = `id, tenant, runner, label, script, env, stdin, timeout_ms,
		state, claimed_by, lease_until, result, created_at, finished_at`

func scanRunnerTask(row pgx.Row) (RunnerTask, error) {
	var t RunnerTask
	var env, result []byte
	var timeoutMS int64
	var leaseUntil, finishedAt *time.Time
	if err := row.Scan(&t.ID, &t.Tenant, &t.Runner, &t.Label, &t.Script, &env,
		&t.Stdin, &timeoutMS, &t.State, &t.ClaimedBy, &leaseUntil, &result,
		&t.CreatedAt, &finishedAt); err != nil {
		return RunnerTask{}, err
	}
	t.Timeout = time.Duration(timeoutMS) * time.Millisecond
	if leaseUntil != nil {
		t.LeaseUntil = *leaseUntil
	}
	if finishedAt != nil {
		t.FinishedAt = *finishedAt
	}
	if len(env) > 0 {
		if err := json.Unmarshal(env, &t.Env); err != nil {
			return RunnerTask{}, fmt.Errorf("decode task env: %w", err)
		}
	}
	if len(result) > 0 {
		var res RunnerTaskResult
		if err := json.Unmarshal(result, &res); err != nil {
			return RunnerTask{}, fmt.Errorf("decode task result: %w", err)
		}
		t.Result = &res
	}
	return t, nil
}

func (s *PgRunnerTaskStore) Enqueue(ctx context.Context, t RunnerTask) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO runner_tasks
		    (id, tenant, runner, label, script, env, stdin, timeout_ms, state, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		t.ID, t.Tenant, t.Runner, t.Label, t.Script, jsonOrNil(t.Env), t.Stdin,
		t.Timeout.Milliseconds(), string(t.State), t.CreatedAt)
	return err
}

// claimRunnerTaskQuery takes the oldest task this runner may run, in the same
// shape the job queue uses: the inner SELECT picks one row with FOR UPDATE SKIP
// LOCKED so several agents polling at once each get a different task instead of
// contending for the same one.
//
// Only 'queued' is claimable. A lapsed claim is never handed out again — see
// the note at the top of runner_tasks.go.
//
// The eligibility clause is the tenant boundary, and it is written to fail
// closed: a task carrying neither a runner name nor a label matches nothing,
// because `label <> ”` excludes it. A task with no target is a bug upstream,
// and the wrong answer to it is to run someone's script on an arbitrary
// machine.
const claimRunnerTaskQuery = `
		UPDATE runner_tasks
		   SET state = 'running', claimed_by = $2, lease_until = $4
		 WHERE id = (
		     SELECT id FROM runner_tasks
		      WHERE tenant = $1
		        AND state = 'queued'
		        AND ( (runner <> '' AND runner = $2)
		           OR (runner = '' AND label <> '' AND label = ANY($3::text[])) )
		      ORDER BY created_at
		      FOR UPDATE SKIP LOCKED
		      LIMIT 1
		 )
		 RETURNING ` + runnerTaskColumns

func (s *PgRunnerTaskStore) Claim(ctx context.Context, r Runner, now time.Time, lease time.Duration) (RunnerTask, error) {
	labels := r.Labels
	if labels == nil {
		labels = []string{}
	}
	// The lease is computed from the caller's clock rather than the database's
	// now(), so the store honours an injected time the way the memory one does.
	t, err := scanRunnerTask(s.pool.QueryRow(ctx, claimRunnerTaskQuery,
		r.Tenant, r.Name, labels, now.Add(lease)))
	if err != nil {
		if isPgNoRows(err) {
			return RunnerTask{}, ErrNoTask
		}
		return RunnerTask{}, err
	}
	return t, nil
}

func (s *PgRunnerTaskStore) Extend(ctx context.Context, id, runnerName string, until time.Time) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE runner_tasks SET lease_until = $3
		 WHERE id = $1 AND claimed_by = $2 AND state = 'running'`,
		id, runnerName, until)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrTaskNotClaimable
	}
	return nil
}

func (s *PgRunnerTaskStore) Complete(ctx context.Context, id, runnerName string, res RunnerTaskResult, now time.Time) error {
	body, err := json.Marshal(res)
	if err != nil {
		return fmt.Errorf("encode task result: %w", err)
	}
	// A non-zero exit is a FAILED task, not a done one: the step fails the way
	// any other step fails rather than succeeding with an error in its output.
	state := TaskDone
	if res.Error != "" || res.ExitCode != 0 {
		state = TaskFailed
	}
	// `state = 'running'` in the WHERE clause is what refuses a result for a
	// task we already gave up on. Accepting it would resurrect a step that has
	// already failed.
	tag, err := s.pool.Exec(ctx, `
		UPDATE runner_tasks
		   SET state = $4, result = $5, finished_at = $3
		 WHERE id = $1 AND claimed_by = $2 AND state = 'running'`,
		id, runnerName, now, string(state), body)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrTaskNotClaimable
	}
	return nil
}

// FailAbandoned condemns a task whose runner went quiet.
//
// In a transaction, because the check and the write must not be separable: the
// row is locked, re-tested against the same abandonment rule the caller used,
// and only then failed. The alternative — trusting the caller's earlier read —
// would let a result that arrived in between be overwritten by our guess that
// the machine was gone.
func (s *PgRunnerTaskStore) FailAbandoned(ctx context.Context, tenant, id string, now time.Time) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	cur, err := scanRunnerTask(tx.QueryRow(ctx, `
		SELECT `+runnerTaskColumns+`
		  FROM runner_tasks WHERE id = $1 AND tenant = $2 FOR UPDATE`, id, tenant))
	if err != nil {
		if isPgNoRows(err) {
			return false, fmt.Errorf("task %q not found", id)
		}
		return false, err
	}
	if !abandoned(cur, now) {
		return false, nil
	}
	body, err := json.Marshal(abandonedResult(cur.ClaimedBy))
	if err != nil {
		return false, fmt.Errorf("encode task result: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE runner_tasks
		   SET state = 'failed', result = $2, finished_at = $3
		 WHERE id = $1`, id, body, now); err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}

// CancelQueued closes an unclaimed task so nothing can run it later.
//
// `state = 'queued'` in the WHERE clause does the whole job: it is the atomic
// test for "nobody has taken this yet", so a runner claiming it in the same
// instant either wins the claim or loses to this cancel, never both.
func (s *PgRunnerTaskStore) CancelQueued(ctx context.Context, tenant, id string, res RunnerTaskResult, now time.Time) (bool, error) {
	body, err := json.Marshal(res)
	if err != nil {
		return false, fmt.Errorf("encode task result: %w", err)
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE runner_tasks
		   SET state = 'failed', result = $3, finished_at = $4
		 WHERE id = $1 AND tenant = $2 AND state = 'queued'`,
		id, tenant, body, now)
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() > 0 {
		return true, nil
	}
	// Nothing updated: either it was claimed, or there is no such task. Tell
	// those apart, because "not found" is a caller bug and "claimed" is not.
	var exists bool
	if err := s.pool.QueryRow(ctx,
		`SELECT true FROM runner_tasks WHERE id = $1 AND tenant = $2`, id, tenant).Scan(&exists); err != nil {
		if isPgNoRows(err) {
			return false, fmt.Errorf("task %q not found", id)
		}
		return false, err
	}
	return false, nil
}

func (s *PgRunnerTaskStore) Get(ctx context.Context, tenant, id string) (RunnerTask, error) {
	t, err := scanRunnerTask(s.pool.QueryRow(ctx, `
		SELECT `+runnerTaskColumns+`
		  FROM runner_tasks WHERE id = $1 AND tenant = $2`, id, tenant))
	if err != nil {
		if isPgNoRows(err) {
			return RunnerTask{}, fmt.Errorf("task %q not found", id)
		}
		return RunnerTask{}, err
	}
	return t, nil
}

// Prune deletes finished task rows older than the cutoff, in bounded batches so
// a large backlog does not lock the table in one statement.
//
// Only terminal rows go. A queued or running row is never touched however old
// it looks: "old and still running" is a long script or a step whose lease has
// not lapsed yet, and deleting it would make the waiting step fail to read its
// own task back.
func (s *PgRunnerTaskStore) Prune(ctx context.Context, olderThan time.Duration, batch int) (int, error) {
	if olderThan <= 0 {
		return 0, nil
	}
	if batch <= 0 {
		batch = 5000
	}
	cutoff := time.Now().Add(-olderThan)
	total := 0
	for {
		tag, err := s.pool.Exec(ctx, `
			DELETE FROM runner_tasks WHERE id IN (
			    SELECT id FROM runner_tasks
			     WHERE state IN ('done', 'failed')
			       AND finished_at IS NOT NULL
			       AND finished_at < $1
			     LIMIT $2
			)`, cutoff, batch)
		if err != nil {
			return total, err
		}
		n := int(tag.RowsAffected())
		total += n
		if n < batch {
			return total, nil
		}
	}
}

var _ RunnerTaskStore = (*PgRunnerTaskStore)(nil)
