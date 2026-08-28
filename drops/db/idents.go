// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package db

import (
	"fmt"
	"regexp"
	"strings"
)

// SQL identifier handling for the db drops. The user's incoming
// headers are external data (Excel files, CSVs, API responses), so
// they routinely contain non-ASCII letters ("FÖRETAG", "Antal à"),
// punctuation ("MOMS%"), and case the user expects preserved. SQLite
// / Postgres / MySQL all accept arbitrary identifiers when they're
// properly quoted; the drops just need to do the quoting right and
// stop pre-rejecting on a hard-coded [A-Za-z0-9_] regex.
//
// Two functions per dialect:
//
//   - validateIdent  — rejects only genuinely unsafe input (empty,
//     NUL byte, absurdly long). Everything else is allowed and
//     the database itself enforces its own length / charset limits.
//   - quoteIdent{,Backtick} — produces a quoted identifier safe to
//     splice into generated SQL. Doubles the quote char to escape
//     embedded quotes — same convention SQLite, Postgres, and
//     MySQL share for their respective quote styles.

// maxIdentLen is a defensive cap. Real DBs cut off well before this
// (Postgres 63, MySQL 64, SQLite has no documented limit), but the
// drops sit between the user's spreadsheet and the DB, and we don't
// want a 100KB pasted "header" to keep the parser busy. The DB will
// give a clearer per-dialect error if a legitimate-but-too-long name
// makes it through.
const maxIdentLen = 1024

// validateIdent enforces the bare minimum every dialect needs: the
// name must be non-empty, not contain a NUL byte (which would
// terminate the C-string most drivers feed to the database), and
// fit inside maxIdentLen bytes. Charset is intentionally
// unrestricted — Unicode letters, punctuation, mixed case, leading
// digits all pass through unchanged and are handled by quoting.
func validateIdent(name string) error {
	if name == "" {
		return fmt.Errorf("identifier must not be empty")
	}
	if strings.ContainsRune(name, 0) {
		return fmt.Errorf("identifier must not contain NUL bytes")
	}
	if len(name) > maxIdentLen {
		return fmt.Errorf("identifier exceeds %d bytes", maxIdentLen)
	}
	return nil
}

// quoteIdent quotes name for SQLite / Postgres using the SQL
// standard double-quote convention. Embedded `"` are escaped by
// doubling. fmt's %q is NOT a valid substitute here — it uses Go
// escape sequences (`\n`, `\"`) which SQL parsers don't understand.
//
// Examples:
//
//	quoteIdent("FÖRETAG")       → `"FÖRETAG"`
//	quoteIdent(`weird"col`)     → `"weird""col"`
//	quoteIdent("normal_col")    → `"normal_col"`
func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// quoteIdentBacktick is the MySQL-flavored sibling: backticks with
// embedded backticks doubled. Used by the MySQL drops.
func quoteIdentBacktick(name string) string {
	return "`" + strings.ReplaceAll(name, "`", "``") + "`"
}

// maxColumnTypeLen caps a single column_types value. Real type
// expressions are short ("DECIMAL(10,2)", "timestamp with time zone");
// the cap stops a pasted blob from reaching the DB parser.
const maxColumnTypeLen = 64

// knownColumnTypes is the allowlist of base SQL types the db drops will
// splice into DDL. Keys are normalized (lower-cased, interior whitespace
// collapsed to a single space) so lookups are case- and spacing-
// insensitive. The set spans the three dialects these drops target:
// Postgres (timestamptz, double precision, jsonb, …), MySQL (the
// UNSIGNED integer family, datetime, …) and SQLite (datetime — the rest
// are storage-class aliases SQLite already understands). The literal
// strings used in the manifest examples and tests (INTEGER, BIGINT,
// timestamptz, DECIMAL, NUMERIC, DOUBLE PRECISION, INT UNSIGNED,
// TIMESTAMP WITH TIME ZONE, DATETIME, …) all appear here.
var knownColumnTypes = map[string]bool{
	// Integers (incl. the MySQL UNSIGNED variants used in tests).
	"smallint": true, "int": true, "integer": true, "bigint": true,
	"int unsigned": true, "integer unsigned": true,
	"smallint unsigned": true, "bigint unsigned": true,
	"tinyint": true, "mediumint": true, "serial": true, "bigserial": true,
	// Floating / fixed point.
	"real": true, "double": true, "double precision": true,
	"float": true, "numeric": true, "decimal": true,
	// Text.
	"text": true, "varchar": true, "char": true, "character": true,
	"character varying": true, "nvarchar": true, "nchar": true,
	"longtext": true, "mediumtext": true, "tinytext": true,
	"clob": true,
	// Boolean.
	"boolean": true, "bool": true,
	// Date / time.
	"timestamptz": true, "timestamp": true, "timestamp with time zone": true,
	"timestamp without time zone": true, "datetime": true,
	"date": true, "time": true, "time with time zone": true,
	"time without time zone": true,
	// Binary / structured / misc.
	"bytea": true, "blob": true, "longblob": true, "mediumblob": true,
	"tinyblob": true, "uuid": true, "json": true, "jsonb": true,
}

// columnTypeRE splits a column type into its base name (group 1) and an
// optional trailing length/precision spec (group 2, e.g. "(255)" or
// "(10,2)"). The base name is one or more words; the optional spec is one
// or two unsigned integers in parentheses. Anchored end-to-end so nothing
// may trail the spec — a value like "TEXT); DROP TABLE" can't match.
var columnTypeRE = regexp.MustCompile(`^([A-Za-z][A-Za-z ]*[A-Za-z]|[A-Za-z])\s*(\(\s*\d+\s*(?:,\s*\d+\s*)?\))?$`)

// validateColumnType guards the column_types parameter. Unlike
// identifiers (which we quote) and row values (which we bind), a
// column's SQL type cannot be quoted or parameterized — it is spliced
// verbatim into CREATE TABLE / ALTER TABLE DDL. Without this check a
// value like `TEXT); DROP TABLE users; --` would be executed.
//
// We don't rely on a character-class allowlist: alnum + space/comma/parens
// still lets an attacker smuggle extra column or constraint definitions
// (e.g. "int, evil text") through the comma+parens. Instead we match the
// value against a TYPE-TOKEN allowlist — a known base type (one of
// knownColumnTypes, case- and spacing-insensitive: "DOUBLE PRECISION",
// "INT UNSIGNED", "TIMESTAMP WITH TIME ZONE", …) optionally followed by a
// single (n) or (n,m) length/precision spec. Anything else — default
// clauses, constraints, extra columns, quotes, semicolons, comment
// markers — is rejected so a value can never break out of the type
// position.
func validateColumnType(t string) error {
	if t == "" {
		return nil
	}
	if len(t) > maxColumnTypeLen {
		return fmt.Errorf("column type %q exceeds %d bytes", t, maxColumnTypeLen)
	}
	m := columnTypeRE.FindStringSubmatch(t)
	if m == nil {
		return fmt.Errorf("column type %q is not a recognized SQL type", t)
	}
	// Normalize the base name: lower-case and collapse interior runs of
	// whitespace to a single space ("DOUBLE   PRECISION" → "double precision").
	base := strings.ToLower(strings.Join(strings.Fields(m[1]), " "))
	if !knownColumnTypes[base] {
		return fmt.Errorf("column type %q has unsupported base type %q", t, base)
	}
	return nil
}
