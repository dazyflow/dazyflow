// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package db

import (
	"fmt"
	"strings"
)

// dialects.go holds the three concrete SQL flavors. Each is a tiny value
// (the per-backend differences are quoting, placeholders, and the upsert
// tail) consumed by the generic execute* skeleton in dialect.go.

// --- SQLite -----------------------------------------------------------

type sqliteDialect struct{}

func (sqliteDialect) quote(ident string) string { return quoteIdent(ident) }
func (sqliteDialect) placeholder(int) string    { return "?" }

// upsertClause for SQLite: INSERT ... ON CONFLICT (k) DO UPDATE SET
// col = excluded.col, or DO NOTHING when no update columns. Lowercase
// `excluded` matches SQLite's convention (Postgres uses upper).
func (d sqliteDialect) upsertClause(conflictCols, updateCols []string) string {
	return onConflictClause(d, conflictCols, updateCols, "excluded")
}

// --- Postgres ---------------------------------------------------------

type postgresDialect struct{}

func (postgresDialect) quote(ident string) string { return quoteIdent(ident) }
func (postgresDialect) placeholder(i int) string  { return fmt.Sprintf("$%d", i) }

// upsertClause for Postgres: identical structure to SQLite but with the
// uppercase EXCLUDED pseudo-table.
func (d postgresDialect) upsertClause(conflictCols, updateCols []string) string {
	return onConflictClause(d, conflictCols, updateCols, "EXCLUDED")
}

// onConflictClause renders the ON CONFLICT (...) tail shared by SQLite
// and Postgres, differing only in the excluded-table spelling.
func onConflictClause(d dialect, conflictCols, updateCols []string, excluded string) string {
	conflictList := strings.Join(quoteAll(d, conflictCols), ", ")
	if len(updateCols) == 0 {
		return fmt.Sprintf("ON CONFLICT (%s) DO NOTHING", conflictList)
	}
	assignments := make([]string, len(updateCols))
	for i, c := range updateCols {
		q := d.quote(c)
		assignments[i] = fmt.Sprintf("%s = %s.%s", q, excluded, q)
	}
	return fmt.Sprintf("ON CONFLICT (%s) DO UPDATE SET %s", conflictList, strings.Join(assignments, ", "))
}

// --- MySQL ------------------------------------------------------------

type mysqlDialect struct{}

func (mysqlDialect) quote(ident string) string { return quoteIdentBacktick(ident) }
func (mysqlDialect) placeholder(int) string    { return "?" }

// upsertClause for MySQL: ON DUPLICATE KEY UPDATE col = VALUES(col).
// VALUES(col) is MySQL's equivalent of Postgres's EXCLUDED.col; the
// older form works on every supported server (the AS-alias syntax needs
// 8.0.20+). When updateCols is empty there is no direct DO NOTHING, so
// we set the first conflict column to itself — a semantic no-op that
// still uses the standard path rather than INSERT IGNORE (which would
// swallow unrelated errors like type mismatches).
func (d mysqlDialect) upsertClause(conflictCols, updateCols []string) string {
	effective := updateCols
	if len(effective) == 0 {
		effective = []string{conflictCols[0]}
	}
	assignments := make([]string, len(effective))
	for i, c := range effective {
		q := d.quote(c)
		assignments[i] = fmt.Sprintf("%s = VALUES(%s)", q, q)
	}
	return "ON DUPLICATE KEY UPDATE " + strings.Join(assignments, ", ")
}
