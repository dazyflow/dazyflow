// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package db

import (
	"fmt"
	"regexp"
	"strings"
)

// The user's column and table names reach SQL as IDENTIFIERS, which no driver
// can parameterise — so they are validated and quoted here rather than
// interpolated anywhere else.

const maxIdentLen = 1024

// The bare minimum every dialect needs, applied before any quoting.
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

// Doubling an embedded quote is what makes the quoting safe.
func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

func quoteIdentBacktick(name string) string {
	return "`" + strings.ReplaceAll(name, "`", "``") + "`"
}

const maxColumnTypeLen = 64

// An ALLOWLIST: a type is user-supplied and cannot be parameterised.
var knownColumnTypes = map[string]bool{
	"smallint": true, "int": true, "integer": true, "bigint": true,
	"int unsigned": true, "integer unsigned": true,
	"smallint unsigned": true, "bigint unsigned": true,
	"tinyint": true, "mediumint": true, "serial": true, "bigserial": true,
	"real": true, "double": true, "double precision": true,
	"float": true, "numeric": true, "decimal": true,
	"text": true, "varchar": true, "char": true, "character": true,
	"character varying": true, "nvarchar": true, "nchar": true,
	"longtext": true, "mediumtext": true, "tinytext": true,
	"clob":    true,
	"boolean": true, "bool": true,
	"timestamptz": true, "timestamp": true, "timestamp with time zone": true,
	"timestamp without time zone": true, "datetime": true,
	"date": true, "time": true, "time with time zone": true,
	"time without time zone": true,
	"bytea":                  true, "blob": true, "longblob": true, "mediumblob": true,
	"tinyblob": true, "uuid": true, "json": true, "jsonb": true,
}

// Splits base name from length, so each half can be checked separately.
var columnTypeRE = regexp.MustCompile(`^([A-Za-z][A-Za-z ]*[A-Za-z]|[A-Za-z])\s*(\(\s*\d+\s*(?:,\s*\d+\s*)?\))?$`)

// The type reaches DDL as text, so it is checked against the allowlist.
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
	base := strings.ToLower(strings.Join(strings.Fields(m[1]), " "))
	if !knownColumnTypes[base] {
		return fmt.Errorf("column type %q has unsupported base type %q", t, base)
	}
	return nil
}
