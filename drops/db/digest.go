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
	"github.com/dazyflow/dazyflow/drops/internal/limits"
	"github.com/dazyflow/dazyflow/drops/internal/params"
	"github.com/dazyflow/dazyflow/engine"
)

// A digest is the shape no single flow can hold: entries arrive one at a time,
// over hours, and leave together. Two steps in two flows, sharing a collection
// — one adds, the other takes everything and empties it in the same
// transaction, so an entry can be sent twice or lost only if the whole take
// fails, in which case nothing moved at all.
//
// It is stored as an ordinary collection under a "digest_" prefix, so the
// entries are browsable in-app like any other rows, and naming a digest after
// a collection you already keep cannot empty that collection by accident.
const digestPrefix = "digest_"

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "digest_add",
			Version:     "1.0",
			Label:       "Collections",
			Subtitle:    "Add to a digest",
			Color:       "#0a6abf",
			Icon:        "layers",
			Category:    "io",
			Provider:    "internal",
			Integration: "Collections",
			Tags: []string{"digest", "roundup", "summary", "accumulate", "collect",
				"batch", "daily", "weekly", "queue", "pile up", "later"},
			Description: "Put something aside to be sent later, together with everything else that arrives before then. " +
				"The classic use is the daily roundup: every new lead, error or order adds a line here as it happens, " +
				"and one scheduled flow sends the lot at nine in the morning.\n\n" +
				"Name the digest and wire in what to remember — a row list, a single record, or just a piece of text, " +
				"which is kept as a 'value' column. Each entry is stamped with the time it was added. Nothing goes out " +
				"of this step but the count, because the point is that the flow does NOT act now.\n\n" +
				"The other half is 'Take the digest', in a flow with a Schedule trigger: it hands over everything piled " +
				"up and empties the digest in one go. The entries live in an ordinary collection called " +
				"\"digest_<name>\", so you can look through them under Collections while they wait.",
			Summary: "Add an entry to a named digest, to be taken and sent later by a scheduled flow.",
			Examples: []core.ParamsExample{
				{
					Title:  "Pile up new leads",
					Params: json.RawMessage(`{"digest":"leads"}`),
					Notes:  "Wire a form or webhook body into Entries; one submission becomes one entry.",
				},
				{
					Title:  "Collect a line of text",
					Params: json.RawMessage(`{"digest":"errors"}`),
					Notes:  "Text arrives as an entry with a single 'value' column, ready to render as a list later.",
				},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				{Port: "rows", Label: "Entries", Required: true},
			},
			Outputs: []core.Port{
				{Port: "added", Label: "Entries added", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"digest":{"type":"string","title":"Digest name","description":"What this pile is called. The flow that sends it later takes the same name. Stored as a collection called \"digest_<name>\"."}
				},
				"required":["digest"]
			}`),
			Idempotent: false,
		},
		Execute: executeDigestAdd,
	})

	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "digest_take",
			Version:     "1.0",
			Label:       "Collections",
			Subtitle:    "Take the digest",
			Color:       "#0a6abf",
			Icon:        "layers",
			Category:    "io",
			Provider:    "internal",
			Integration: "Collections",
			Tags: []string{"digest", "roundup", "summary", "release", "send", "daily",
				"weekly", "drain", "empty", "batch"},
			Description: "Hand over everything a digest has piled up, and empty it. Put this after a Schedule trigger — " +
				"nine in the morning, Monday, the first of the month — and wire Entries into whatever sends the roundup: " +
				"Make a table, Fill a template, an email, a Slack message.\n\n" +
				"Taking is all-or-nothing: the entries are read and the digest is emptied in the same breath, so the same " +
				"entry cannot go out twice, and a failure leaves the pile untouched to be sent next time.\n\n" +
				"An empty digest does NOT fire the Entries output — it fires 'Nothing there' instead, so a quiet night " +
				"sends no email at all unless you deliberately wire that side up. 'How many' comes out either way.",
			Summary: "Read every entry from a digest and empty it, in one transaction; fires a separate output when it was empty.",
			Examples: []core.ParamsExample{
				{
					Title:  "Send the daily roundup",
					Params: json.RawMessage(`{"digest":"leads"}`),
					Notes:  "Schedule 09:00 → Take the digest → Make a table → Email. Nothing arrived overnight, nothing is sent.",
				},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Outputs: []core.Port{
				{Port: "rows", Label: "Entries", MIME: []string{"application/json"}},
				{Port: "count", Label: "How many", MIME: []string{"application/json"}},
				{Port: "empty", Label: "Nothing there", MIME: []string{"application/x-control"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"digest":{"type":"string","title":"Digest name","description":"The pile to take, named the same as in the flow that adds to it."}
				},
				"required":["digest"]
			}`),
			Idempotent: false,
		},
		Execute: executeDigestTake,
	})
}

// digestTable is the collection a digest lives in. The name is validated
// before the prefix so a message names what the author typed.
func digestTable(job core.Job) (string, *core.Result) {
	name, err := params.String(job.Params, "digest")
	if err != nil {
		return "", errResult(job, "bad_param", "'Digest name' is required")
	}
	name = strings.TrimSpace(name)
	if err := validateIdent(name); err != nil {
		return "", errResult(job, "bad_param", fmt.Sprintf("digest name %q: %v", name, err))
	}
	return digestPrefix + name, nil
}

// executeDigestAdd is the append step wearing a different name: same store,
// same column evolution, same save stamp. A bare value is wrapped first, since
// "remember this line" is half of what a digest is for and normalizeRows has
// no use for a lone string.
func executeDigestAdd(ctx context.Context, job core.Job, progress chan<- core.Progress) (core.Result, error) {
	table, errRes := digestTable(job)
	if errRes != nil {
		return *errRes, nil
	}
	ref, ok := job.Input["rows"]
	if !ok {
		return params.Err(job, "missing_input", "input port 'rows' is required — wire in what to remember"), nil
	}
	switch v := ref.Inline.(type) {
	case string, float64, int, int64, bool, json.Number:
		ref = core.Ref{MIME: "application/json", Inline: []any{map[string]any{"value": v}}}
	}

	inner := job
	inner.Params = map[string]any{"table": table}
	inner.Input = map[string]core.Ref{"rows": ref}

	res, err := executeBuiltinStoreAppend(ctx, inner, progress)
	if err != nil || res.Status != core.StatusOK {
		return res, err
	}
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{"added": res.Output["inserted"]},
	}, nil
}

func executeDigestTake(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	table, errRes := digestTable(job)
	if errRes != nil {
		return *errRes, nil
	}

	db, errResult := openBuiltinStore(job, false)
	if errResult != nil {
		return *errResult, nil
	}
	// Never written to, so there is nothing to take — which is a quiet night,
	// not a broken flow.
	if db == nil {
		return emptyDigest(job), nil
	}
	defer db.Close()

	rows, columns, err := takeAll(ctx, db, table)
	if err != nil {
		if isMissingTable(err) {
			return emptyDigest(job), nil
		}
		if errors := err.Error(); strings.Contains(errors, "row limit") {
			return params.Err(job, "too_many_rows", errors), nil
		}
		return params.Err(job, "db", err.Error()), nil
	}
	if len(rows) == 0 {
		return emptyDigest(job), nil
	}

	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"rows":  {MIME: "application/json", Inline: rows, Headers: columns},
			"count": {MIME: "application/json", Inline: len(rows)},
		},
	}, nil
}

func emptyDigest(job core.Job) core.Result {
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"count": {MIME: "application/json", Inline: 0},
			"empty": {MIME: "application/x-control"},
		},
	}
}

// takeAll reads every row and empties the table in one transaction, so the
// entries are either handed over and gone, or still there for next time.
func takeAll(ctx context.Context, db *sql.DB, table string) ([]map[string]any, []string, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	cursor, err := tx.QueryContext(ctx, "SELECT * FROM "+quoteIdent(table))
	if err != nil {
		return nil, nil, err
	}
	rows, columns, err := scanAll(cursor)
	cursor.Close()
	if err != nil {
		return nil, nil, err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM "+quoteIdent(table)); err != nil {
		return nil, nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, fmt.Errorf("commit: %w", err)
	}
	return rows, columns, nil
}

// scanAll materializes a result set as rows plus the column order, bounding
// the scan against the row ceiling.
func scanAll(cursor *sql.Rows) ([]map[string]any, []string, error) {
	columns, err := cursor.Columns()
	if err != nil {
		return nil, nil, err
	}
	out := make([]map[string]any, 0, 16)
	for cursor.Next() {
		vals := make([]any, len(columns))
		ptrs := make([]any, len(columns))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := cursor.Scan(ptrs...); err != nil {
			return nil, nil, err
		}
		rec := make(map[string]any, len(columns))
		for i, c := range columns {
			rec[c] = vals[i]
		}
		out = append(out, rec)
		if len(out) > limits.MaxRows() {
			return nil, nil, fmt.Errorf("more than the %d-row limit; raise DAZYFLOW_MAX_ROWS", limits.MaxRows())
		}
	}
	return out, columns, cursor.Err()
}

func isMissingTable(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "no such table")
}
