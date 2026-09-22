// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package db

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dazyflow/dazyflow/core"
)

// User SQL must not reach a database outside the workspace: ATTACH (and VACUUM
// INTO, which attaches its target) is capped at zero, and the step is read-only.
func TestSQLiteQuery_NoAttachOrWrites(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	seedSqliteQueryDB(t, root, "data.db", [][]any{{1, "Alice", 9.5, 1}})
	seedSqliteQueryDB(t, outside, "other.db", [][]any{{2, "Secret", 1.0, 1}})
	for _, q := range []string{
		"ATTACH DATABASE '" + filepath.Join(outside, "other.db") + "' AS o",
		"VACUUM INTO '" + filepath.Join(outside, "copy.db") + "'",
		"DELETE FROM t",
	} {
		t.Run(q, func(t *testing.T) {
			res, _ := executeSQLiteQuery(t.Context(), core.Job{
				WorkspaceRoot: root,
				Params:        map[string]any{"path": "data.db", "sql": q},
			}, nil)
			if res.Status == core.StatusOK {
				t.Fatalf("%q should have been rejected", q)
			}
		})
	}
	if _, err := os.Stat(filepath.Join(outside, "copy.db")); err == nil {
		t.Error("VACUUM INTO wrote outside the workspace")
	}
}

func TestBuiltinStoreQuery_NoAttach(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	seedSqliteQueryDB(t, outside, "other.db", nil)
	if err := os.MkdirAll(filepath.Join(root, filepath.Dir(builtinStorePath)), 0o755); err != nil {
		t.Fatal(err)
	}
	seedSqliteQueryDB(t, root, builtinStorePath, nil)
	res, _ := executeBuiltinStoreQuery(t.Context(), core.Job{
		WorkspaceRoot: root,
		Params:        map[string]any{"sql": "ATTACH DATABASE '" + filepath.Join(outside, "other.db") + "' AS o"},
	}, nil)
	if res.Status == core.StatusOK {
		t.Fatal("ATTACH should have been rejected")
	}
}

// A symlink inside the workspace pointing outside is refused rather than
// followed by sqlite.
func TestSQLiteQuery_SymlinkOutsideBlocked(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	seedSqliteQueryDB(t, outside, "other.db", nil)
	if err := os.Symlink(filepath.Join(outside, "other.db"), filepath.Join(root, "link.db")); err != nil {
		t.Skip(err)
	}
	res, _ := executeSQLiteQuery(t.Context(), core.Job{
		WorkspaceRoot: root,
		Params:        map[string]any{"path": "link.db", "sql": "SELECT * FROM t"},
	}, nil)
	if res.Status == core.StatusOK {
		t.Fatal("symlink escaping the workspace was followed")
	}
}
