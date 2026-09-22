// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package db

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// sandboxedRealPath resolves rel (already probed through os.Root) to the real
// path sqlite should open, and re-verifies that path is inside the workspace.
// sqlite only takes a filename, so the name handed to it must be the one that
// was checked: resolving the symlinks here and opening the result means a link
// swapped in after the os.Root probe cannot redirect the open.
func sandboxedRealPath(workspaceRoot, rel string) (string, error) {
	rootReal, err := filepath.EvalSymlinks(workspaceRoot)
	if err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(filepath.Join(rootReal, rel))
	if err != nil {
		return "", err
	}
	r, err := filepath.Rel(rootReal, real)
	if err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) || filepath.IsAbs(r) {
		return "", fmt.Errorf("path escapes workspace")
	}
	return real, nil
}

// lockedQueryConn pins one connection of db for running user-written SQL.
// ATTACH is capped at zero databases, which also blocks VACUUM INTO (it attaches
// its target), so a query cannot reach a database file outside the workspace.
// The connection is also put in query_only mode: these are SELECT steps.
// Limits are per connection, so the caller must run its SQL on the returned
// conn, not on the pool.
func lockedQueryConn(ctx context.Context, db *sql.DB) (*sql.Conn, error) {
	c, err := db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := sqlite.Limit(c, sqlite3.SQLITE_LIMIT_ATTACHED, 0); err != nil {
		c.Close()
		return nil, fmt.Errorf("limit attached: %w", err)
	}
	if _, err := c.ExecContext(ctx, "PRAGMA query_only = 1"); err != nil {
		c.Close()
		return nil, fmt.Errorf("query_only: %w", err)
	}
	return c, nil
}
