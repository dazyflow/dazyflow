// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/internal/params"
	"github.com/dazyflow/dazyflow/engine"
	"github.com/dazyflow/dazyflow/internal/rowcel"
)

// rowidAlias names the hidden key each row is deleted by. SQLite gives every
// row a rowid; selecting it under a name nothing would choose lets a filter
// run in Go — the same CEL the Find step uses — and still delete exactly the
// rows it matched.
const rowidAlias = "__dz_rowid"

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "builtin_store_delete",
			Version:     "1.0",
			Label:       "Collections",
			Subtitle:    "Delete rows",
			Color:       "#0a6abf",
			Icon:        "trash-2",
			Category:    "io",
			Provider:    "internal",
			Integration: "Collections",
			Tags: []string{"collection", "collections", "store", "delete", "remove",
				"clear", "empty", "clean up", "tidy", "housekeeping", "purge", "expire"},
			Description: "Remove rows from a collection. Until now a collection could be written to and read from but never " +
				"tidied, so anything a flow remembered — the orders it has already handled, the addresses it has already " +
				"mailed — piled up for good.\n\n" +
				"Set 'Where' with the same visual conditions the Find step uses and only the matching rows go. A common " +
				"one is age: keep the last month by deleting where saved_at is before the date you want.\n\n" +
				"Deleting everything is deliberately harder: leave 'Where' empty and the step refuses unless you also " +
				"turn on 'Delete every row'. The collection itself stays, so the flow that fills it keeps working.",
			Summary: "Delete matching rows from a collection; emptying it whole needs an explicit switch.",
			Examples: []core.ParamsExample{
				{
					Title:  "Forget what has already been handled",
					Params: json.RawMessage(`{"table":"seen_orders","filter":"row.status == \"done\""}`),
					Notes:  "Build the condition with the visual editor — you don't type the expression by hand.",
				},
				{
					Title:  "Keep only the last month",
					Params: json.RawMessage(`{"table":"events","filter":"row.saved_at < \"2026-08-10\""}`),
					Notes:  "saved_at is the stamp Save rows writes, so an ISO date compares as text.",
				},
				{
					Title:  "Start the collection over",
					Params: json.RawMessage(`{"table":"scratch","all":true}`),
					Notes:  "With no condition, the switch is what says you meant it.",
				},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				{Port: "table", Label: "Collection", MIME: []string{"text/plain"}},
			},
			Outputs: []core.Port{
				{Port: "deleted", Label: "Rows deleted", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"table":  {"type":"string","format":"collection","title":"Collection","description":"Name of the collection to delete from. Overridden by a value connected into the Collection input."},
					"filter": {"type":"string","format":"row-condition","x_columns_source":"collection","title":"Where","description":"Delete only the rows that match these conditions. Leave empty to mean every row — which the step will not do unless 'Delete every row' is also on."},
					"all":    {"type":"boolean","default":false,"title":"Delete every row","description":"Confirms that a step with no condition really is meant to empty the whole collection. Ignored when a condition is set."}
				},
				"required":["table"]
			}`),
			// Deleting the same rows twice is harmless — the second pass matches
			// nothing — so a retry edge is safe here.
			Idempotent: true,
		},
		Execute: executeBuiltinStoreDelete,
	})
}

func executeBuiltinStoreDelete(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	table, err := resolveTable(job)
	if err != nil {
		return params.Err(job, "bad_param", err.Error()), nil
	}
	filter := strings.TrimSpace(params.StringDefault(job.Params, "filter", ""))
	all := params.BoolDefault(job.Params, "all", false)
	if filter == "" && !all {
		return params.Err(job, "bad_param",
			"this would delete every row in "+table+" — set a condition under 'Where', or turn on 'Delete every row' to say you meant it"), nil
	}

	db, errResult := openBuiltinStore(job, false)
	if errResult != nil {
		return *errResult, nil
	}
	// Nothing was ever saved, so nothing is there to delete.
	if db == nil {
		return deletedResult(job, 0), nil
	}
	defer db.Close()

	if filter == "" {
		affected, err := db.ExecContext(ctx, "DELETE FROM "+quoteIdent(table))
		if err != nil {
			if isMissingTable(err) {
				return params.Err(job, "no_such_collection", missingCollectionMsg(ctx, db, table)), nil
			}
			return params.Err(job, "db", fmt.Sprintf("delete: %v", err)), nil
		}
		n, _ := affected.RowsAffected()
		return deletedResult(job, int(n)), nil
	}

	env, err := rowcel.Env()
	if err != nil {
		return params.Err(job, "internal", fmt.Sprintf("cel env: %v", err)), nil
	}
	prog, err := rowcel.Compile(env, filter, "filter")
	if err != nil {
		return params.Err(job, "bad_param", err.Error()), nil
	}

	deleted, err := deleteMatching(ctx, db, table, func(row map[string]any) (bool, error) {
		return rowcel.EvalBool(prog, row)
	})
	if err != nil {
		switch {
		case isMissingTable(err):
			return params.Err(job, "no_such_collection", missingCollectionMsg(ctx, db, table)), nil
		case strings.Contains(err.Error(), "row limit"):
			return params.Err(job, "too_many_rows", err.Error()), nil
		case strings.HasPrefix(err.Error(), "filter:"):
			return params.Err(job, "eval", err.Error()), nil
		}
		return params.Err(job, "db", fmt.Sprintf("delete: %v", err)), nil
	}
	return deletedResult(job, deleted), nil
}

func deletedResult(job core.Job, n int) core.Result {
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{"deleted": {MIME: "application/json", Inline: n}},
	}
}

// deleteMatching reads the collection, asks keep about each row, and removes
// the matches in one transaction — so a filter that errors halfway leaves the
// collection exactly as it was.
func deleteMatching(ctx context.Context, db *sql.DB, table string, match func(map[string]any) (bool, error)) (int, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	cursor, err := tx.QueryContext(ctx, "SELECT rowid AS "+rowidAlias+", * FROM "+quoteIdent(table))
	if err != nil {
		return 0, err
	}
	rows, _, err := scanAll(cursor)
	cursor.Close()
	if err != nil {
		return 0, err
	}

	var ids []any
	for _, row := range rows {
		id, ok := row[rowidAlias]
		if !ok {
			return 0, fmt.Errorf("collection has no row key to delete by")
		}
		// The key is an artefact of this query, not one of the row's own
		// columns, so a filter must not see it.
		delete(row, rowidAlias)
		hit, err := match(row)
		if err != nil {
			return 0, fmt.Errorf("filter: %w", err)
		}
		if hit {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return 0, tx.Commit()
	}

	// Chunked: SQLite caps how many parameters one statement may carry, and a
	// collection at the row ceiling would blow past it.
	const chunk = 500
	deleted := 0
	for start := 0; start < len(ids); start += chunk {
		end := min(start+chunk, len(ids))
		batch := ids[start:end]
		stmt := "DELETE FROM " + quoteIdent(table) + " WHERE rowid IN (" +
			strings.TrimSuffix(strings.Repeat("?,", len(batch)), ",") + ")"
		res, err := tx.ExecContext(ctx, stmt, batch...)
		if err != nil {
			return 0, err
		}
		n, _ := res.RowsAffected()
		deleted += int(n)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit: %w", err)
	}
	return deleted, nil
}
