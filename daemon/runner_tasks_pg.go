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

// PgRunnerTaskStore is the durable task queue.
//
// It exists for the same reason the job queue is in Postgres: a deployment may
// run several daemons, and the agent's result can land on a different one from
// the step that is waiting. Only the database is shared, so only the database
// can carry the handoff. With the in-memory queue, that step waits for a result
// that was delivered to a machine it cannot see.
type PgRunnerTaskStore struct {
	pool *pgxpool.Pool
	// Cipher seals the script, stdin and env at rest. See the note on
	// runnerTaskPayloadDomain for why the queue needs one at all.
	//
	// Nil means the rows are written in cleartext, which is the same posture as
	// the rest of the deployment: without DAZYFLOW_MASTER_KEY there is no
	// stored-secret encryption anywhere, and validateProductionConfig already
	// refuses to call that configuration production.
	Cipher PayloadCipher
}

// PayloadCipher seals a blob at rest under a tenant's key. Satisfied by
// *EncryptedSecrets; an interface so this store does not depend on the whole
// secret provider, and so a test can substitute one.
type PayloadCipher interface {
	SealPayload(ctx context.Context, tenant, domain, id string, plaintext []byte) ([]byte, error)
	OpenPayload(ctx context.Context, tenant, domain, id string, blob []byte) ([]byte, error)
}

// runnerTaskPayloadDomain names what these sealed blobs are, and the fields
// below name which one, so a ciphertext cannot be moved between rows or between
// columns of the same row.
//
// The queue needs sealing because it is a transport that happens to be durable.
// engine/secrets.go states the contract — a resolved secret "exists only in the
// transport.Execute call" — and engine/redact.go exists to keep resolved values
// out of persisted Results. A script written as `./sync.sh --key
// ${secret.STRIPE_KEY}` reaches Enqueue already expanded, so an unsealed row
// would hold the tenant's live credential in cleartext until
// DAZYFLOW_RUNNER_TASK_RETENTION elapsed — recoverable from a dump, a read
// replica or a backup by someone with no Dazyflow permission at all.
const runnerTaskPayloadDomain = "runner_task"

const (
	runnerTaskFieldScript = "script"
	runnerTaskFieldStdin  = "stdin"
	runnerTaskFieldEnv    = "env"
)

// sealedPrefix marks a column as holding a sealed blob rather than cleartext.
//
// It exists so rows written before sealing — and rows written by a deployment
// with no master key — still read back. Without the marker there is no way to
// tell a base64 script from a script that happens to look like base64.
const sealedPrefix = "sealed:v1:"

// seal returns the value to store: the sealed, marked, base64 form when a
// cipher is configured, and the plaintext when it is not.
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

// unseal reverses seal, passing through anything not carrying the marker.
//
// A marked value with no cipher to open it is an ERROR rather than a
// pass-through: handing the agent a base64 blob to execute would be worse than
// failing the step, and it is the shape a deployment gets by losing its master
// key.
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

// open decrypts a scanned row in place. Every read path goes through it, so a
// caller can never accidentally hand a sealed blob onwards.
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

// shell sits with script rather than among the sealed columns because it is not
// secret-bearing: it is one of a fixed handful of words, and sealing it would
// cost a decrypt on the hot claim path to learn "bash".
const runnerTaskColumns = `id, tenant, runner, label, tags, script, shell, env, stdin, timeout_ms,
		state, claimed_by, progress, lease_until, result, created_at, finished_at`

func scanRunnerTask(row pgx.Row) (RunnerTask, error) {
	var t RunnerTask
	var env, result []byte
	var timeoutMS int64
	var leaseUntil, finishedAt *time.Time
	// runner and label are the pre-tags columns; nothing writes them any more.
	var legacyRunner, legacyLabel string
	if err := row.Scan(&t.ID, &t.Tenant, &legacyRunner, &legacyLabel, &t.Tags, &t.Script,
		&t.Shell, &env, &t.Stdin, &timeoutMS, &t.State, &t.ClaimedBy, &t.Progress,
		&leaseUntil, &result, &t.CreatedAt, &finishedAt); err != nil {
		return RunnerTask{}, err
	}
	// A task queued by the previous version, claimed by this one moments later
	// across a rolling deploy. Both old shapes are one tag now — a machine's
	// name is a tag, so the name column needs no special case.
	if len(t.Tags) == 0 {
		for _, legacy := range []string{legacyRunner, legacyLabel} {
			if legacy != "" {
				t.Tags = append(t.Tags, legacy)
			}
		}
	}
	t.Timeout = time.Duration(timeoutMS) * time.Millisecond
	if leaseUntil != nil {
		t.LeaseUntil = *leaseUntil
	}
	if finishedAt != nil {
		t.FinishedAt = *finishedAt
	}
	if len(env) > 0 {
		// The column holds either the map itself or, when sealed, a JSON
		// string carrying the marked blob. Try the string first: a sealed
		// value is not a valid map and a map is not a valid string, so the
		// two shapes cannot be confused.
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

// sealEnv renders the env map for storage. Env goes through the same secret
// substitution as params (see resolveTemplatesCollecting), so it is a secret
// carrier too and gets the same treatment; sealed, it is stored as a JSON
// string rather than an object.
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

// claimRunnerTaskQuery takes the oldest task this runner may run, in the same
// shape the job queue uses: the inner SELECT picks one row with FOR UPDATE SKIP
// LOCKED so several agents polling at once each get a different task instead of
// contending for the same one.
//
// Only 'queued' is claimable. A lapsed claim is never handed out again — see
// the note at the top of runner_tasks.go.
//
// The eligibility clause is the tenant boundary, and it must agree exactly with
// eligible() in runner_tasks.go — the memory store applies that function and
// this store applies this SQL, and the two are meant to be indistinguishable.
//
// `tags <@ $3` is the AND rule: every tag the task asks for is in the set this
// machine carries ($3 is Runner.Tags(), so the machine's own name is in there).
// The cardinality guard is what makes it fail closed, and it is load-bearing
// rather than tidy: in Postgres `'{}' <@ anything` is TRUE, so without it a task
// with no tags would match EVERY machine — and a task with no target is a bug
// upstream whose wrong answer is to run someone's script on an arbitrary
// machine.
//
// The ELSE branch is the pre-tags shape, for a task queued by the previous
// version and claimed by this one across a rolling deploy. Both old columns
// resolve against the same tag array, because a machine's name is now one of its
// tags.
const claimRunnerTaskQuery = `
		UPDATE runner_tasks
		   SET state = 'running', claimed_by = $2, lease_until = $4
		 WHERE id = (
		     SELECT id FROM runner_tasks
		      WHERE tenant = $1
		        AND state = 'queued'
		        AND CASE WHEN cardinality(tags) > 0
		                 THEN tags <@ $3::text[]
		                 ELSE (runner <> '' AND runner = ANY($3::text[]))
		                   OR (runner = '' AND label <> '' AND label = ANY($3::text[]))
		            END
		      ORDER BY created_at
		      FOR UPDATE SKIP LOCKED
		      LIMIT 1
		 )
		 RETURNING ` + runnerTaskColumns

func (s *PgRunnerTaskStore) Claim(ctx context.Context, r Runner, now time.Time, lease time.Duration) (RunnerTask, error) {
	// Everything this machine can be targeted by, its name included — the same
	// set Runner.HasTags checks against.
	tags := r.Tags()
	if tags == nil {
		tags = []string{}
	}
	// The lease is computed from the caller's clock rather than the database's
	// now(), so the store honours an injected time the way the memory one does.
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

// Extend and Complete both match on tenant as well as claimant. The name alone
// is not an identity — tenant_runners is keyed on (tenant, name) — so without
// the tenant predicate an agent could report on a same-named runner's task in
// another organisation, and only the task id's randomness would be in its way.
func (s *PgRunnerTaskStore) Extend(ctx context.Context, r Runner, id string, until time.Time, message string) error {
	// COALESCE-shaped: an empty message leaves the previous line standing, so a
	// bare heartbeat does not blank out what the script last said.
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
	if err := s.open(ctx, &t); err != nil {
		return RunnerTask{}, err
	}
	return t, nil
}

// OrphanedTasks lists non-terminal rows nobody is waiting for any more. See
// the contract on RunnerTaskStore.
//
// A listing rather than a bulk UPDATE so the caller closes each row through
// FailAbandoned / CancelQueued — those already carry the atomic re-check and
// the wording, and duplicating either in SQL would let the two drift.
//
// The payload is deliberately NOT decrypted here: the sweeper only needs the
// id, the tenant, the state and the claimant, and opening a sealed script to
// throw it away would put plaintext secrets in the sweeper's memory for no
// reason.
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

// DeleteByTenant removes every task row an org ever queued, whatever its state,
// returning the count. The erasure cascade's hook (GDPR Art. 17).
//
// Unlike Prune this takes running and queued rows too. An org being erased has
// nothing left that could legitimately claim them, and a queued row left behind
// stays claimable — the machine that picks it up would run a deleted org's
// script.
func (s *PgRunnerTaskStore) DeleteByTenant(ctx context.Context, tenant string) (int, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM runner_tasks WHERE tenant = $1`, tenant)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

var _ RunnerTaskStore = (*PgRunnerTaskStore)(nil)
