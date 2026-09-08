// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package db

import (
	"fmt"
	"strings"
)

type sqliteDialect struct{}

func (sqliteDialect) quote(ident string) string { return quoteIdent(ident) }
func (sqliteDialect) placeholder(int) string    { return "?" }

func (d sqliteDialect) upsertClause(conflictCols, updateCols []string) string {
	return onConflictClause(d, conflictCols, updateCols, "excluded")
}

type postgresDialect struct{}

func (postgresDialect) quote(ident string) string { return quoteIdent(ident) }
func (postgresDialect) placeholder(i int) string  { return fmt.Sprintf("$%d", i) }

func (d postgresDialect) upsertClause(conflictCols, updateCols []string) string {
	return onConflictClause(d, conflictCols, updateCols, "EXCLUDED")
}

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
