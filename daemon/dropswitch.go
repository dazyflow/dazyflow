// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// DropSwitch is one platform-admin killswitch entry: a drop turned off
// either globally (Tenant == "") or for a single misbehaving org
// (Tenant == "org_..."). Drops are on by default — a row exists only for
// a drop that's been switched off, so the absence of a row means enabled.
type DropSwitch struct {
	DropID     string    `json:"drop_id"`
	Tenant     string    `json:"tenant"` // "" = global
	DisabledBy string    `json:"disabled_by,omitempty"`
	Reason     string    `json:"reason,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}

// DropSwitchStore is the killswitch boundary. Disabled() is on the node
// resolution hot path, so implementations must answer it from memory
// without a per-call round-trip; the mutating methods (Disable/Enable)
// and List() may hit the backing store.
type DropSwitchStore interface {
	// Disabled reports whether dropID may not run for tenant. True when a
	// global switch is set for the drop, or a per-tenant switch matching
	// tenant. Must be cheap and lock-free-ish — called per node execute.
	Disabled(dropID, tenant string) bool
	// Disable turns a drop off, globally (tenant="") or for one tenant.
	Disable(ctx context.Context, sw DropSwitch) error
	// Enable clears a switch. Idempotent.
	Enable(ctx context.Context, dropID, tenant string) error
	// List returns every active switch, for the platform-admin UI.
	List(ctx context.Context) ([]DropSwitch, error)
}

const pgDropSwitchSchema = `
CREATE TABLE IF NOT EXISTS drop_switches (
    drop_id     TEXT NOT NULL,
    tenant      TEXT NOT NULL DEFAULT '',
    disabled_by TEXT NOT NULL DEFAULT '',
    reason      TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (drop_id, tenant)
);
`

// EnsurePgDropSwitchSchema creates the drop_switches table.
func EnsurePgDropSwitchSchema(ctx context.Context, pool *pgxpool.Pool) error {
	if pool == nil {
		return fmt.Errorf("nil pool")
	}
	_, err := pool.Exec(ctx, pgDropSwitchSchema)
	return err
}

// dropSwitchKey is the composite cache key. The NUL separator can't
// appear in a drop id or tenant id, so it's an unambiguous join.
func dropSwitchKey(dropID, tenant string) string { return dropID + "\x00" + tenant }

// PgDropSwitchStore is the Postgres DropSwitchStore. It keeps an
// in-memory snapshot of every switch so Disabled() — called for every
// node execution — never touches the database. The snapshot is reloaded
// on every mutation by this node, and on a background ticker so switches
// flipped by another dzd node propagate within one refresh interval.
type PgDropSwitchStore struct {
	pool *pgxpool.Pool

	mu    sync.RWMutex
	cache map[string]bool // dropSwitchKey -> present(disabled)
}

// NewPgDropSwitchStore provisions the table, loads the initial snapshot,
// and starts a background refresh bound to ctx (the daemon lifetime).
func NewPgDropSwitchStore(ctx context.Context, pool *pgxpool.Pool) (*PgDropSwitchStore, error) {
	if err := EnsurePgDropSwitchSchema(ctx, pool); err != nil {
		return nil, err
	}
	s := &PgDropSwitchStore{pool: pool, cache: map[string]bool{}}
	if err := s.reload(ctx); err != nil {
		return nil, err
	}
	go s.refreshLoop(ctx)
	return s, nil
}

// refreshInterval bounds how stale a cross-node switch flip can be. A
// killswitch is an abuse response — a few seconds of propagation lag is
// acceptable, and a short poll is far cheaper than notifying every node.
const refreshInterval = 10 * time.Second

func (s *PgDropSwitchStore) refreshLoop(ctx context.Context) {
	t := time.NewTicker(refreshInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := s.reload(ctx); err != nil {
				log.Printf("drop-switch refresh: %v", err)
			}
		}
	}
}

func (s *PgDropSwitchStore) reload(ctx context.Context) error {
	switches, err := s.List(ctx)
	if err != nil {
		return err
	}
	next := make(map[string]bool, len(switches))
	for _, sw := range switches {
		next[dropSwitchKey(sw.DropID, sw.Tenant)] = true
	}
	s.mu.Lock()
	s.cache = next
	s.mu.Unlock()
	return nil
}

func (s *PgDropSwitchStore) Disabled(dropID, tenant string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.cache[dropSwitchKey(dropID, "")] {
		return true // global switch
	}
	return tenant != "" && s.cache[dropSwitchKey(dropID, tenant)]
}

func (s *PgDropSwitchStore) Disable(ctx context.Context, sw DropSwitch) error {
	if sw.DropID == "" {
		return fmt.Errorf("drop_id required")
	}
	created := sw.CreatedAt
	if created.IsZero() {
		created = time.Now().UTC()
	}
	const q = `
		INSERT INTO drop_switches (drop_id, tenant, disabled_by, reason, created_at)
		VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT (drop_id, tenant) DO UPDATE SET
			disabled_by=EXCLUDED.disabled_by, reason=EXCLUDED.reason,
			created_at=EXCLUDED.created_at`
	if _, err := s.pool.Exec(ctx, q, sw.DropID, sw.Tenant, sw.DisabledBy, sw.Reason, created); err != nil {
		return err
	}
	return s.reload(ctx)
}

func (s *PgDropSwitchStore) Enable(ctx context.Context, dropID, tenant string) error {
	if _, err := s.pool.Exec(ctx,
		`DELETE FROM drop_switches WHERE drop_id=$1 AND tenant=$2`, dropID, tenant); err != nil {
		return err
	}
	return s.reload(ctx)
}

func (s *PgDropSwitchStore) List(ctx context.Context) ([]DropSwitch, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT drop_id, tenant, disabled_by, reason, created_at
		 FROM drop_switches ORDER BY drop_id, tenant`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]DropSwitch, 0)
	for rows.Next() {
		var sw DropSwitch
		if err := rows.Scan(&sw.DropID, &sw.Tenant, &sw.DisabledBy, &sw.Reason, &sw.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, sw)
	}
	return out, rows.Err()
}
