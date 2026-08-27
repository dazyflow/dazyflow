// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"git.sr.ht/~klahr/dazyflow/core"
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
	// DeleteByTenant clears every per-tenant switch set against a tenant,
	// returning the count. The erasure-cascade entry point (GDPR Art. 17): a
	// row names the admin who set it and the reason they gave.
	//
	// GLOBAL switches (tenant "") are never touched, and an empty tenant is
	// refused outright rather than treated as "all". A global switch is the
	// platform's own kill-switch on a broken drop; taking those out while
	// erasing one org would silently re-enable it for everybody.
	// AnonymizeSubject replaces an erased person's identifier wherever it
	// appears in this store's rows, returning the rows changed.
	//
	// The rows belong to an ORG and outlive the person, so their identifier is
	// pseudonymised rather than deleted — the same treatment the audit trail
	// gets. Deleting an org takes these rows anyway; this is the OTHER path,
	// where a member of a shared org erases their account and the org carries
	// on with their address still in it.
	AnonymizeSubject(ctx context.Context, ident string) (int, error)
	DeleteByTenant(ctx context.Context, tenant string) (int, error)
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
	return applyPgSchema(ctx, pool, pgDropSwitchSchema)
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

// pollReload runs reload on the refreshInterval ticker until ctx is done,
// logging each failure via logf with errFormat. The shared body of the
// entitlement, platform-admin, and drop-switch refresh loops.
func pollReload(ctx context.Context, reload func(context.Context) error, logf func(string, ...any), errFormat string) {
	t := time.NewTicker(refreshInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := reload(ctx); err != nil {
				logf(errFormat, err)
			}
		}
	}
}

func (s *PgDropSwitchStore) refreshLoop(ctx context.Context) {
	pollReload(ctx, s.reload, log.Printf, "drop-switch refresh: %v")
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

func (s *PgDropSwitchStore) AnonymizeSubject(ctx context.Context, ident string) (int, error) {
	if ident == "" {
		return 0, nil
	}
	tag, err := s.pool.Exec(ctx,
		`UPDATE drop_switches SET disabled_by = $2 WHERE disabled_by = $1`, ident, core.ErasedIdentity)
	if err != nil {
		return 0, err
	}
	// disabled_by does not feed the Disabled() snapshot, so no reload here.
	return int(tag.RowsAffected()), nil
}

func (s *PgDropSwitchStore) DeleteByTenant(ctx context.Context, tenant string) (int, error) {
	if tenant == "" {
		return 0, fmt.Errorf("drop switches: tenant required (empty would match the global switches)")
	}
	tag, err := s.pool.Exec(ctx, `DELETE FROM drop_switches WHERE tenant=$1`, tenant)
	if err != nil {
		return 0, err
	}
	// Refresh the snapshot Disabled() reads, exactly as Disable/Enable do —
	// otherwise this process keeps enforcing an erased org's switches.
	if err := s.reload(ctx); err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
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
