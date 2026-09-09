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

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/daemon/internal/pgstore"
)

// bundles.go turns stored run records into the raw core.RunSnapshot that
// core.BuildSupportBundle then redacts. Redaction lives entirely in core, so this
// adapter only reshapes JobRecords and MUST NOT pre-filter values —
// BuildSupportBundle owns the boundary. It deliberately does NOT touch
// runRec.GraphPayload: the bundle's structure is rebuilt from the redacted
// core.Graph, never from the stored raw JSON.

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

var ErrBundleExists = fmt.Errorf("support bundle already exists")

type MemBundleStore struct {
	mu   sync.Mutex
	byID map[string]core.SupportBundleRecord
}

func NewMemBundleStore() *MemBundleStore {
	return &MemBundleStore{byID: map[string]core.SupportBundleRecord{}}
}

var _ core.BundleStore = (*MemBundleStore)(nil)

// Create requires a unique ID.
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

func (s *MemBundleStore) Get(_ context.Context, id string) (core.SupportBundleRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.byID[id]
	if !ok {
		return core.SupportBundleRecord{}, fmt.Errorf("%w: support bundle %s", core.ErrNotFound, id)
	}
	return rec, nil
}

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

func EnsurePgBundleSchema(ctx context.Context, pool *pgxpool.Pool) error {
	return pgstore.ApplySchema(ctx, pool, pgBundleSchema)
}

// PgBundleStore stores Payload as BYTEA, preserving the redacted bundle JSON
// byte-for-byte rather than re-serializing it.
type PgBundleStore struct {
	pool *pgxpool.Pool
}

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

// Prune deletes bundles past the retention window, oldest first. A bundle is a
// snapshot taken to answer one ticket, so past retention with nothing pointing
// at it, it is pure storage cost.
//
// A bundle referenced by ANY ticket is kept, whatever that ticket's status.
// Sparing only bundles whose ticket was still open broke the pairing, because
// the two prunes key on different timestamps — a bundle on created_at, a ticket
// on updated_at — so a ticket filed 13 months ago and resolved last week stayed
// while its bundle was swept, and "View diagnostic" 404'd.
//
// The invariant instead: a bundle outlives every ticket referencing it. The
// ticket's own retention decides when the pair goes, and since the sweep prunes
// tickets first, the freed bundle is collected in the same pass.
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
