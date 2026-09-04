// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"fmt"
	"iter"
	"path/filepath"
	"sort"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/dazyflow/dazyflow/workspace"
)

// PgWorkspaces is the WorkspaceLookup for Postgres-backed flow storage.
//
// Unlike AutoFSWorkspaces there is nothing to cache or evict: a Store here is a
// handle over the shared pool, not an open git repository, so Open mints one
// per call and the resident cost of an install is its connection pool rather
// than one repository per tenant. Nothing is provisioned either — a workspace
// exists once something is saved into it.
type PgWorkspaces struct {
	pool *pgxpool.Pool
	// mirrorCache is where git mirroring keeps the repository it synthesizes
	// from the revision log. Empty leaves mirroring unavailable, and Push says
	// so rather than failing quietly.
	//
	// Per-replica by design: synthesis is deterministic, so any replica derives
	// the same commits and a push from a cold one fast-forwards onto a warm
	// one's. That makes the cache disposable rather than something the replicas
	// have to agree about.
	mirrorCache string
}

// NewPgWorkspaces returns a lookup over pool. The caller must have applied the
// schema (workspace.EnsurePgWorkspaceSchema).
func NewPgWorkspaces(pool *pgxpool.Pool) *PgWorkspaces {
	return &PgWorkspaces{pool: pool}
}

// SetMirrorCache enables git mirroring, using dir as the root for each
// workspace's synthesized repository.
func (p *PgWorkspaces) SetMirrorCache(dir string) { p.mirrorCache = dir }

func (p *PgWorkspaces) Open(tenant, ws string) (*workspace.Store, error) {
	st, wsClean, err := safeWorkspaceSegment(tenant, ws)
	if err != nil {
		return nil, err
	}
	var opts []workspace.PgOption
	if p.mirrorCache != "" {
		opts = append(opts, workspace.WithMirrorCache(filepath.Join(p.mirrorCache, st, wsClean)))
	}
	return workspace.OpenPostgres(p.pool, st, wsClean, opts...)
}

// List returns the workspaces a tenant has saved anything into. A tenant with
// none lists empty rather than erroring — the switcher then shows the default,
// matching the filesystem lookup.
func (p *PgWorkspaces) List(tenant string) ([]string, error) {
	st, _, err := safeWorkspaceSegment(tenant, "main")
	if err != nil {
		return nil, err
	}
	rows, err := p.pool.Query(context.Background(),
		`SELECT DISTINCT workspace FROM flow_heads WHERE tenant=$1`, st)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]string, 0)
	for rows.Next() {
		var w string
		if err := rows.Scan(&w); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Strings(out)
	return out, nil
}

// All yields every workspace that holds at least one flow.
func (p *PgWorkspaces) All() iter.Seq2[string, *workspace.Store] {
	return func(yield func(string, *workspace.Store) bool) {
		rows, err := p.pool.Query(context.Background(),
			`SELECT DISTINCT tenant, workspace FROM flow_heads ORDER BY tenant, workspace`)
		if err != nil {
			return
		}
		// Collected before yielding: the consumer is free to write during the
		// sweep, and holding the rows open across that would pin a connection
		// for the whole pass.
		type pair struct{ tenant, ws string }
		var pairs []pair
		for rows.Next() {
			var pr pair
			if err := rows.Scan(&pr.tenant, &pr.ws); err != nil {
				rows.Close()
				return
			}
			pairs = append(pairs, pr)
		}
		rows.Close()
		if rows.Err() != nil {
			return
		}
		for _, pr := range pairs {
			s, err := p.Open(pr.tenant, pr.ws)
			if err != nil {
				continue
			}
			if !yield(pr.tenant+"/"+pr.ws, s) {
				return
			}
		}
	}
}

// RemoveTenant deletes every flow, revision and environment pointer a tenant
// owns — the workspace half of the GDPR erasure cascade (Art. 17). Idempotent.
func (p *PgWorkspaces) RemoveTenant(tenant string) error {
	st, _, err := safeWorkspaceSegment(tenant, "main")
	if err != nil {
		return err
	}
	ctx := context.Background()
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for _, tbl := range []string{"flow_envs", "flow_heads", "flow_revisions"} {
		if _, err := tx.Exec(ctx, "DELETE FROM "+tbl+" WHERE tenant=$1", st); err != nil {
			return fmt.Errorf("erase %s: %w", tbl, err)
		}
	}
	return tx.Commit(ctx)
}
