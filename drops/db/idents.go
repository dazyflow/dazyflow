package db

import (
	"fmt"
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

// validateColumnType guards the column_types parameter. Unlike
// identifiers (which we quote) and row values (which we bind), a
// column's SQL type cannot be quoted or parameterized — it is spliced
// verbatim into CREATE TABLE / ALTER TABLE DDL. Without this check a
// value like `TEXT); DROP TABLE users; --` would be executed.
//
// A legitimate type is a name optionally carrying a size/precision in
// parentheses, optionally with modifier words ("DOUBLE PRECISION",
// "INT UNSIGNED", "TIMESTAMP WITH TIME ZONE"). We allow only letters,
// digits, spaces, commas, parentheses and underscores; everything else
// — quotes, semicolons, comment markers — is rejected so a value can
// never break out of the type position.
func validateColumnType(t string) error {
	if t == "" {
		return nil
	}
	if len(t) > maxColumnTypeLen {
		return fmt.Errorf("column type %q exceeds %d bytes", t, maxColumnTypeLen)
	}
	for _, r := range t {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == ' ', r == ',', r == '(', r == ')', r == '_':
		default:
			return fmt.Errorf("column type %q contains unsupported character %q", t, string(r))
		}
	}
	return nil
}
