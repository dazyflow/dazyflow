// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package support

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/daemon/internal/pgstore"
)

// bundles.go is the daemon-side glue for the Support feature: it turns
// the stored run records into the raw core.RunSnapshot that core.BuildSupportBundle
// then redacts. The redaction itself lives entirely in core — this adapter only
// projects JobRecords into the snapshot's shape and MUST NOT pre-filter values
// (BuildSupportBundle owns the boundary). Deliberately it does NOT touch
// runRec.GraphPayload: the bundle's structure is rebuilt from the redacted
// core.Graph, never from the stored raw graph JSON.

// RunSnapshotFromRecords projects a run's graph-record + its node-records into a
// core.RunSnapshot. runRec is the graph-kind record (the run itself); nodeRecs
// are its node-kind records (typically from ListNodeRecords with GraphRunID set).
// The snapshot carries RAW refs and errors — core.BuildSupportBundle drops the
// payloads and JobError.Details.
func RunSnapshotFromRecords(runRec core.JobRecord, nodeRecs []core.JobRecord) core.RunSnapshot {
	enqueued := runRec.EnqueuedAt
	rs := core.RunSnapshot{
		RunID:      runRec.ID,
		Status:     runRec.Status,
		EnqueuedAt: &enqueued,
		StartedAt:  runRec.StartedAt,
		FinishedAt: runRec.FinishedAt,
	}
	if runRec.Result != nil {
		rs.Error = runRec.Result.Error
	}
	rs.Nodes = make([]core.NodeRunSnapshot, 0, len(nodeRecs))
	for _, nr := range nodeRecs {
		n := core.NodeRunSnapshot{
			NodeID:     nr.NodeID,
			Status:     nr.Status,
			Attempt:    nr.Attempt,
			StartedAt:  nr.StartedAt,
			FinishedAt: nr.FinishedAt,
		}
		if nr.Result != nil {
			n.Error = nr.Result.Error
			n.Output = nr.Result.Output
		}
		rs.Nodes = append(rs.Nodes, n)
	}
	return rs
}

// ErrBundleExists is returned when creating a record with a duplicate ID; a
// missing record reports core.ErrNotFound.
var ErrBundleExists = fmt.Errorf("support bundle already exists")

// MemBundleStore is a mutex-guarded in-memory core.BundleStore.
type MemBundleStore struct {
	mu   sync.Mutex
	byID map[string]core.SupportBundleRecord
}

// NewMemBundleStore returns an empty in-memory bundle store.
func NewMemBundleStore() *MemBundleStore {
	return &MemBundleStore{byID: map[string]core.SupportBundleRecord{}}
}

var _ core.BundleStore = (*MemBundleStore)(nil)

// Create stores a record; ID is required and must be unique.
func (s *MemBundleStore) Create(_ context.Context, rec core.SupportBundleRecord) error {
	if rec.ID == "" {
		return fmt.Errorf("support bundle id is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.byID[rec.ID]; exists {
		return fmt.Errorf("%w: %s", ErrBundleExists, rec.ID)
	}
	s.byID[rec.ID] = rec
	return nil
}

// Get returns the record, or core.ErrNotFound.
func (s *MemBundleStore) Get(_ context.Context, id string) (core.SupportBundleRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.byID[id]
	if !ok {
		return core.SupportBundleRecord{}, fmt.Errorf("%w: support bundle %s", core.ErrNotFound, id)
	}
	return rec, nil
}

// ListForTenant returns every bundle record in tenant, newest first.
func (s *MemBundleStore) ListForTenant(_ context.Context, tenant string) ([]core.SupportBundleRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]core.SupportBundleRecord, 0)
	for _, rec := range s.byID {
		if rec.Tenant == tenant {
			out = append(out, rec)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

// ---- Postgres --------------------------------------------------------------

const pgBundleSchema = `
CREATE TABLE IF NOT EXISTS support_bundles (
    id         TEXT PRIMARY KEY,
    tenant     TEXT NOT NULL,
    flow_id    TEXT NOT NULL,
    run_id     TEXT NOT NULL DEFAULT '',
    mode       TEXT NOT NULL,
    payload    BYTEA NOT NULL,
    created_by TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS support_bundles_tenant_idx ON support_bundles (tenant);
`

// EnsurePgBundleSchema creates the support_bundles table. Idempotent.
func EnsurePgBundleSchema(ctx context.Context, pool *pgxpool.Pool) error {
	return pgstore.ApplySchema(ctx, pool, pgBundleSchema)
}

// PgBundleStore is the Postgres core.BundleStore. Payload is stored as BYTEA to
// preserve the redacted bundle JSON byte-for-byte (never re-serialized).
type PgBundleStore struct {
	pool *pgxpool.Pool
}

// NewPgBundleStore creates the schema and returns the store.
func NewPgBundleStore(ctx context.Context, pool *pgxpool.Pool) (*PgBundleStore, error) {
	if err := EnsurePgBundleSchema(ctx, pool); err != nil {
		return nil, err
	}
	return &PgBundleStore{pool: pool}, nil
}

var _ core.BundleStore = (*PgBundleStore)(nil)

const bundleCols = `id, tenant, flow_id, run_id, mode, payload, created_by, created_at`

func scanBundle(r pgScanner) (core.SupportBundleRecord, error) {
	var (
		rec  core.SupportBundleRecord
		mode string
	)
	if err := r.Scan(&rec.ID, &rec.Tenant, &rec.FlowID, &rec.RunID, &mode,
		&rec.Payload, &rec.CreatedBy, &rec.CreatedAt); err != nil {
		return core.SupportBundleRecord{}, err
	}
	rec.Mode = core.RedactMode(mode)
	return rec, nil
}

func (s *PgBundleStore) Create(ctx context.Context, rec core.SupportBundleRecord) error {
	if rec.ID == "" {
		return fmt.Errorf("support bundle id is required")
	}
	ct, err := s.pool.Exec(ctx,
		`INSERT INTO support_bundles (`+bundleCols+`)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT (id) DO NOTHING`,
		rec.ID, rec.Tenant, rec.FlowID, rec.RunID, string(rec.Mode), rec.Payload, rec.CreatedBy, rec.CreatedAt)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("%w: %s", ErrBundleExists, rec.ID)
	}
	return nil
}

func (s *PgBundleStore) Get(ctx context.Context, id string) (core.SupportBundleRecord, error) {
	rec, err := scanBundle(s.pool.QueryRow(ctx, `SELECT `+bundleCols+` FROM support_bundles WHERE id=$1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return core.SupportBundleRecord{}, fmt.Errorf("%w: support bundle %s", core.ErrNotFound, id)
	}
	return rec, err
}

func (s *PgBundleStore) ListForTenant(ctx context.Context, tenant string) ([]core.SupportBundleRecord, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+bundleCols+` FROM support_bundles WHERE tenant=$1 ORDER BY created_at DESC`, tenant)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]core.SupportBundleRecord, 0)
	for rows.Next() {
		rec, err := scanBundle(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

// DeleteByTenant removes every stored diagnostic bundle belonging to one org.
// Bundles are redacted by construction, but they still describe the org's flow
// structure — so they leave with the org (gdpr.go tenantEraser).
func (s *MemBundleStore) AnonymizeSubject(_ context.Context, ident string) (int, error) {
	if ident == "" {
		return 0, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for id, rec := range s.byID {
		if rec.CreatedBy == ident {
			rec.CreatedBy = core.ErasedIdentity
			s.byID[id] = rec
			n++
		}
	}
	return n, nil
}

func (s *MemBundleStore) DeleteByTenant(ctx context.Context, tenant string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for id, rec := range s.byID {
		if rec.Tenant == tenant {
			delete(s.byID, id)
			n++
		}
	}
	return n, nil
}

// DeleteByTenant removes an org's stored bundles.
// AnonymizeSubject scrubs an erased person from created_by. Not on
// core.BundleStore, matching DeleteByTenant: the cascade probes for both rather
// than making every bundle store implement erasure.
func (s *PgBundleStore) AnonymizeSubject(ctx context.Context, ident string) (int, error) {
	if ident == "" {
		return 0, nil
	}
	tag, err := s.pool.Exec(ctx,
		`UPDATE support_bundles SET created_by = $2 WHERE created_by = $1`, ident, core.ErasedIdentity)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

func (s *PgBundleStore) DeleteByTenant(ctx context.Context, tenant string) (int, error) {
	ct, err := s.pool.Exec(ctx, `DELETE FROM support_bundles WHERE tenant = $1`, tenant)
	if err != nil {
		return 0, err
	}
	return int(ct.RowsAffected()), nil
}

// Prune deletes stored diagnostic bundles older than the retention window,
// oldest first, up to batch rows. A bundle is a point-in-time snapshot taken to
// answer one ticket; once it's past retention and nothing points at it, it is
// pure storage cost.
//
// A bundle referenced by ANY ticket is kept, whatever that ticket's status. The
// obvious-looking version of this only spared bundles whose ticket was still
// open, which quietly broke the pairing: the two prunes key on different
// timestamps — a bundle on its `created_at`, a ticket on its `updated_at` — so a
// ticket filed 13 months ago, conversed on for a year and resolved last week
// stayed (its updated_at is recent) while its bundle was swept (its created_at
// is not). `bundle_id` was still set, so "View diagnostic" 404'd for both the
// customer and the agent.
//
// The invariant instead: a bundle outlives every ticket that references it. The
// ticket's own retention decides when the pair goes, and because the sweep
// prunes tickets before bundles, the freed bundle is collected in the same pass.
// A bundle no ticket references (a filing that failed halfway) still ages out on
// its own.
func (s *PgBundleStore) Prune(ctx context.Context, olderThan time.Duration, batch int) (int, error) {
	if olderThan <= 0 {
		return 0, nil
	}
	if batch <= 0 {
		batch = 1000
	}
	cutoff := time.Now().UTC().Add(-olderThan)
	ct, err := s.pool.Exec(ctx,
		`DELETE FROM support_bundles
		  WHERE id IN (
		      SELECT b.id FROM support_bundles b
		       WHERE b.created_at < $1
		         AND NOT EXISTS (
		             SELECT 1 FROM support_tickets t
		              WHERE t.bundle_id = b.id)
		       ORDER BY b.created_at ASC LIMIT $2)`, cutoff, batch)
	if err != nil {
		return 0, err
	}
	return int(ct.RowsAffected()), nil
}
