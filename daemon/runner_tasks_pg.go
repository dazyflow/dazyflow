// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PgRunnerTaskStore struct {
	pool *pgxpool.Pool
	// Seals script, stdin and env at rest: a task row otherwise holds cleartext.
	Cipher PayloadCipher
}

// Sealed under the tenant's key, so a row is not readable across tenants.
type PayloadCipher interface {
	SealPayload(ctx context.Context, tenant, domain, id string, plaintext []byte) ([]byte, error)
	OpenPayload(ctx context.Context, tenant, domain, id string, blob []byte) ([]byte, error)
}

// Bound into the AAD, so a sealed blob cannot be moved to another column or
// another row and still open.
const runnerTaskPayloadDomain = "runner_task"

const (
	runnerTaskFieldScript = "script"
	runnerTaskFieldStdin  = "stdin"
	runnerTaskFieldEnv    = "env"
)

// Marks a column as sealed, so old cleartext rows still read.
const sealedPrefix = "sealed:v1:"

func (s *PgRunnerTaskStore) seal(ctx context.Context, tenant, id, field, plain string) (string, error) {
	if s.Cipher == nil || plain == "" {
		return plain, nil
	}
	blob, err := s.Cipher.SealPayload(ctx, tenant, runnerTaskPayloadDomain+"/"+field, id, []byte(plain))
	if err != nil {
		return "", fmt.Errorf("seal task %s: %w", field, err)
	}
	return sealedPrefix + base64.StdEncoding.EncodeToString(blob), nil
}

// Passes through anything without the marker, so old rows still read.
func (s *PgRunnerTaskStore) unseal(ctx context.Context, tenant, id, field, stored string) (string, error) {
	if !strings.HasPrefix(stored, sealedPrefix) {
		return stored, nil
	}
	if s.Cipher == nil {
		return "", fmt.Errorf("task %s was stored encrypted but this daemon has no DAZYFLOW_MASTER_KEY to open it", field)
	}
	blob, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(stored, sealedPrefix))
	if err != nil {
		return "", fmt.Errorf("decode sealed task %s: %w", field, err)
	}
	plain, err := s.Cipher.OpenPayload(ctx, tenant, runnerTaskPayloadDomain+"/"+field, id, blob)
	if err != nil {
		return "", fmt.Errorf("open task %s: %w", field, err)
	}
	return string(plain), nil
}

func (s *PgRunnerTaskStore) open(ctx context.Context, t *RunnerTask) error {
	var err error
	if t.Script, err = s.unseal(ctx, t.Tenant, t.ID, runnerTaskFieldScript, t.Script); err != nil {
		return err
	}
	if t.Stdin, err = s.unseal(ctx, t.Tenant, t.ID, runnerTaskFieldStdin, t.Stdin); err != nil {
		return err
	}
	if t.sealedEnv != "" {
		plain, err := s.unseal(ctx, t.Tenant, t.ID, runnerTaskFieldEnv, t.sealedEnv)
		if err != nil {
			return err
		}
		t.sealedEnv = ""
		if plain != "" {
			if err := json.Unmarshal([]byte(plain), &t.Env); err != nil {
				return fmt.Errorf("decode task env: %w", err)
			}
		}
	}
	return nil
}

func NewPgRunnerTaskStore(ctx context.Context, pool *pgxpool.Pool) (*PgRunnerTaskStore, error) {
	if err := EnsurePgRunnerTaskSchema(ctx, pool); err != nil {
		return nil, err
	}
	return &PgRunnerTaskStore{pool: pool}, nil
}

// Not a secret: it names an interpreter, not a payload.
const runnerTaskColumns = `id, tenant, tags, script, shell, env, stdin, timeout_ms,
		state, claimed_by, progress, lease_until, result, created_at, finished_at`

func scanRunnerTask(row pgx.Row) (RunnerTask, error) {
	var t RunnerTask
	var env, result []byte
	var timeoutMS int64
	var leaseUntil, finishedAt *time.Time
	if err := row.Scan(&t.ID, &t.Tenant, &t.Tags, &t.Script,
		&t.Shell, &env, &t.Stdin, &timeoutMS, &t.State, &t.ClaimedBy, &t.Progress,
		&leaseUntil, &result, &t.CreatedAt, &finishedAt); err != nil {
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
		var sealed string
		if err := json.Unmarshal(env, &sealed); err == nil {
			t.sealedEnv = sealed
		} else if err := json.Unmarshal(env, &t.Env); err != nil {
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
	script, err := s.seal(ctx, t.Tenant, t.ID, runnerTaskFieldScript, t.Script)
	if err != nil {
		return err
	}
	stdin, err := s.seal(ctx, t.Tenant, t.ID, runnerTaskFieldStdin, t.Stdin)
	if err != nil {
		return err
	}
	env, err := s.sealEnv(ctx, t)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO runner_tasks
		    (id, tenant, tags, script, shell, env, stdin, timeout_ms, state, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		t.ID, t.Tenant, tagsOrEmpty(t.Tags), script, t.Shell, env, stdin,
		t.Timeout.Milliseconds(), string(t.State), t.CreatedAt)
	return err
}

// Env carries credentials as often as stdin does.
func (s *PgRunnerTaskStore) sealEnv(ctx context.Context, t RunnerTask) ([]byte, error) {
	raw := jsonOrNil(t.Env)
	if raw == nil || s.Cipher == nil {
		return raw, nil
	}
	sealed, err := s.seal(ctx, t.Tenant, t.ID, runnerTaskFieldEnv, string(raw))
	if err != nil {
		return nil, err
	}
	return json.Marshal(sealed)
}

// A runner must carry ALL of a task's tags. A lapsed claim is not re-offered:
// the first agent may still be running the script.
const claimRunnerTaskQuery = `
		UPDATE runner_tasks
		   SET state = 'running', claimed_by = $2, lease_until = $4
		 WHERE id = (
		     SELECT id FROM runner_tasks
		      WHERE tenant = $1
		        AND state = 'queued'
		        AND cardinality(tags) > 0
		        AND tags <@ $3::text[]
		      ORDER BY created_at
		      FOR UPDATE SKIP LOCKED
		      LIMIT 1
		 )
		 RETURNING ` + runnerTaskColumns

func (s *PgRunnerTaskStore) Claim(ctx context.Context, r Runner, now time.Time, lease time.Duration) (RunnerTask, error) {
	tags := r.Tags()
	if tags == nil {
		tags = []string{}
	}
	t, err := scanRunnerTask(s.pool.QueryRow(ctx, claimRunnerTaskQuery,
		r.Tenant, r.Name, tags, now.Add(lease)))
	if err != nil {
		if isPgNoRows(err) {
			return RunnerTask{}, ErrNoTask
		}
		return RunnerTask{}, err
	}
	if err := s.open(ctx, &t); err != nil {
		return RunnerTask{}, err
	}
	return t, nil
}

func (s *PgRunnerTaskStore) Extend(ctx context.Context, r Runner, id string, until time.Time, message string) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE runner_tasks
		   SET lease_until = $4,
		       progress = CASE WHEN $5 = '' THEN progress ELSE $5 END
		 WHERE id = $1 AND tenant = $2 AND claimed_by = $3 AND state = 'running'`,
		id, r.Tenant, r.Name, until, message)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrTaskNotClaimable
	}
	return nil
}

func (s *PgRunnerTaskStore) Complete(ctx context.Context, r Runner, id string, res RunnerTaskResult, now time.Time) error {
	body, err := json.Marshal(res)
	if err != nil {
		return fmt.Errorf("encode task result: %w", err)
	}
	state := TaskDone
	if res.Error != "" || res.ExitCode != 0 {
		state = TaskFailed
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE runner_tasks
		   SET state = $5, result = $6, finished_at = $4
		 WHERE id = $1 AND tenant = $2 AND claimed_by = $3 AND state = 'running'`,
		id, r.Tenant, r.Name, now, string(state), body)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrTaskNotClaimable
	}
	return nil
}

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
	if err := s.open(ctx, &t); err != nil {
		return RunnerTask{}, err
	}
	return t, nil
}

func (s *PgRunnerTaskStore) OrphanedTasks(ctx context.Context, now time.Time, grace, queuedCeiling time.Duration, limit int) ([]RunnerTask, error) {
	if limit <= 0 {
		limit = 500
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, tenant, state, claimed_by, created_at, timeout_ms
		  FROM runner_tasks
		 WHERE (state = 'running'
		        AND lease_until IS NOT NULL
		        AND lease_until < $1)
		    OR (state = 'queued'
		        AND created_at < $1 - (CASE WHEN timeout_ms > 0
		                                    THEN make_interval(secs => timeout_ms / 1000.0) + $2
		                                    ELSE $3 END))
		 ORDER BY created_at
		 LIMIT $4`,
		now, grace, queuedCeiling, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RunnerTask
	for rows.Next() {
		var t RunnerTask
		var timeoutMS int64
		if err := rows.Scan(&t.ID, &t.Tenant, &t.State, &t.ClaimedBy, &t.CreatedAt, &timeoutMS); err != nil {
			return nil, err
		}
		t.Timeout = time.Duration(timeoutMS) * time.Millisecond
		out = append(out, t)
	}
	return out, rows.Err()
}

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

func (s *PgRunnerTaskStore) DeleteByTenant(ctx context.Context, tenant string) (int, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM runner_tasks WHERE tenant = $1`, tenant)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

var _ RunnerTaskStore = (*PgRunnerTaskStore)(nil)
