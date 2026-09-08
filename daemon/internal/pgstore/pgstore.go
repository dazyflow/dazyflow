// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package pgstore

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ApplySchema runs a store's CREATE-TABLE-IF-NOT-EXISTS DDL at construction
// time. Every Pg*Store constructor opens the same way, so this keeps the
// "ensure schema, bail on error" step in one place.
//
// Each store owning its own DDL, applied when it is constructed, is the
// deliberate shape here: a store that is never wired never creates its table.
func ApplySchema(ctx context.Context, pool *pgxpool.Pool, schema string) error {
	if pool == nil {
		return fmt.Errorf("nil pool")
	}
	_, err := pool.Exec(ctx, schema)
	return err
}

// RefreshInterval bounds how stale a cross-node change may be in a store that
// caches its rows in memory.
//
// These caches back abuse responses and access checks — a killswitch flip, a
// platform-admin grant, a support agent's membership — where a few seconds of
// propagation lag is acceptable and a short poll is far cheaper than notifying
// every node.
const RefreshInterval = 10 * time.Second

// PollReload runs reload on the RefreshInterval ticker until ctx is done,
// logging each failure via logf with errFormat. The shared body of the
// drop-switch, entitlement, platform-admin and support-agent refresh loops.
//
// It deliberately does NOT reload before the first tick: every caller has just
// loaded its rows in the constructor that starts this goroutine, and a pass
// here would be a second query for the same data at every process start.
func PollReload(ctx context.Context, reload func(context.Context) error, logf func(string, ...any), errFormat string) {
	t := time.NewTicker(RefreshInterval)
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
