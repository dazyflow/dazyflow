// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package db

import (
	"strings"
	"testing"
)

// TestDialectQuotePlaceholder covers the per-backend quoting and bind-marker
// rules for all three dialects, including the MySQL flavor that the
// integration-gated path never reaches under a Postgres-only run.
func TestDialectQuotePlaceholder(t *testing.T) {
	cases := []struct {
		name  string
		d     dialect
		ident string
		wantQ string
		ph1   string
		ph2   string
	}{
		{"sqlite", sqliteDialect{}, "col", `"col"`, "?", "?"},
		{"postgres", postgresDialect{}, "col", `"col"`, "$1", "$2"},
		{"mysql", mysqlDialect{}, "col", "`col`", "?", "?"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.d.quote(c.ident); got != c.wantQ {
				t.Errorf("quote = %q, want %q", got, c.wantQ)
			}
			if got := c.d.placeholder(1); got != c.ph1 {
				t.Errorf("placeholder(1) = %q, want %q", got, c.ph1)
			}
			if got := c.d.placeholder(2); got != c.ph2 {
				t.Errorf("placeholder(2) = %q, want %q", got, c.ph2)
			}
		})
	}
}

// TestUpsertClause_AllDialects covers the ON CONFLICT / ON DUPLICATE KEY tail
// for every dialect across the DO-UPDATE and DO-NOTHING / no-update-cols modes.
func TestUpsertClause_AllDialects(t *testing.T) {
	t.Run("sqlite do update", func(t *testing.T) {
		got := sqliteDialect{}.upsertClause([]string{"id"}, []string{"name", "email"})
		want := `ON CONFLICT ("id") DO UPDATE SET "name" = excluded."name", "email" = excluded."email"`
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("sqlite do nothing", func(t *testing.T) {
		got := sqliteDialect{}.upsertClause([]string{"id"}, nil)
		want := `ON CONFLICT ("id") DO NOTHING`
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("postgres do update uses uppercase EXCLUDED", func(t *testing.T) {
		got := postgresDialect{}.upsertClause([]string{"id"}, []string{"name"})
		want := `ON CONFLICT ("id") DO UPDATE SET "name" = EXCLUDED."name"`
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("postgres do nothing", func(t *testing.T) {
		got := postgresDialect{}.upsertClause([]string{"a", "b"}, nil)
		want := `ON CONFLICT ("a", "b") DO NOTHING`
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("mysql with update cols", func(t *testing.T) {
		got := mysqlDialect{}.upsertClause([]string{"id"}, []string{"name", "email"})
		want := "ON DUPLICATE KEY UPDATE `name` = VALUES(`name`), `email` = VALUES(`email`)"
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("mysql empty update cols falls back to first conflict col", func(t *testing.T) {
		// MySQL has no DO NOTHING; the no-op sets the first conflict column to
		// itself rather than INSERT IGNORE.
		got := mysqlDialect{}.upsertClause([]string{"id", "other"}, nil)
		want := "ON DUPLICATE KEY UPDATE `id` = VALUES(`id`)"
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
}

// TestInsertSQL covers the shared INSERT renderer for plain and upsert-tail
// forms across the placeholder dialects.
func TestInsertSQL(t *testing.T) {
	t.Run("postgres plain insert numbers placeholders", func(t *testing.T) {
		got := insertSQL(postgresDialect{}, `"public"."t"`, []string{"a", "b"}, "")
		want := `INSERT INTO "public"."t" ("a", "b") VALUES ($1, $2)`
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("sqlite with trailing tail", func(t *testing.T) {
		got := insertSQL(sqliteDialect{}, `"t"`, []string{"a"}, "ON CONFLICT DO NOTHING")
		want := `INSERT INTO "t" ("a") VALUES (?) ON CONFLICT DO NOTHING`
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
}

// TestCreateTableSQL covers default TEXT typing, the column_types override,
// and the optional trailing UNIQUE constraint.
func TestCreateTableSQL(t *testing.T) {
	t.Run("defaults to TEXT, override applies, no unique", func(t *testing.T) {
		got := createTableSQL(sqliteDialect{}, `"t"`, []string{"id", "name"}, map[string]string{"id": "INTEGER"}, nil)
		want := `CREATE TABLE IF NOT EXISTS "t" ("id" INTEGER, "name" TEXT)`
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("empty override string falls back to TEXT", func(t *testing.T) {
		got := createTableSQL(sqliteDialect{}, `"t"`, []string{"id"}, map[string]string{"id": ""}, nil)
		if !strings.Contains(got, `"id" TEXT`) {
			t.Errorf("blank type should fall back to TEXT, got %q", got)
		}
	})
	t.Run("trailing unique constraint", func(t *testing.T) {
		got := createTableSQL(postgresDialect{}, `"public"."t"`, []string{"email", "name"}, nil, []string{"email"})
		if !strings.HasSuffix(got, `, UNIQUE ("email"))`) {
			t.Errorf("missing UNIQUE tail: %q", got)
		}
	})
}

// TestPlaceholdersAndQuoteAll covers the small slice builders.
func TestPlaceholdersAndQuoteAll(t *testing.T) {
	if got := placeholders(postgresDialect{}, 3); got != "$1, $2, $3" {
		t.Errorf("placeholders pg = %q", got)
	}
	if got := placeholders(sqliteDialect{}, 2); got != "?, ?" {
		t.Errorf("placeholders sqlite = %q", got)
	}
	if got := quoteAll(mysqlDialect{}, []string{"a", "b"}); strings.Join(got, ",") != "`a`,`b`" {
		t.Errorf("quoteAll mysql = %v", got)
	}
}

// TestBindArgs covers value extraction in header order, including a missing
// key binding nil (SQL NULL).
func TestBindArgs(t *testing.T) {
	row := map[string]any{"a": 1, "c": 3}
	got := bindArgs([]string{"a", "b", "c"}, row)
	if len(got) != 3 || got[0] != 1 || got[1] != nil || got[2] != 3 {
		t.Errorf("bindArgs = %v, want [1 <nil> 3]", got)
	}
}
