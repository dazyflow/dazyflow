package db

import (
	"context"
	"encoding/json"
	"fmt"

	"git.sr.ht/~klahr/hazy-flow/core"
	"git.sr.ht/~klahr/hazy-flow/engine"
	"git.sr.ht/~klahr/hazy-flow/integrations/internal/params"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:             "postgres_query",
			Version:        "1.0",
			Label:          "Postgres query",
			Color:          "#336791",
			Icon:           "database",
			BrandLogo:      "/brands/postgres.svg",
			Category:       "io",
			Provider:       "internal",
			Integration:    "Postgres",
			Tags:           []string{"postgres", "postgresql", "sql", "database", "query", "select"},
			Description:    "Run a SELECT against your Postgres database and get rows back. Use $1, $2 placeholders in the SQL and pass values through the params array so user-supplied data is safely escaped. Set a row limit to keep large result sets bounded.",
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Outputs: []core.Port{
				{Port: "rows", Label: "Rows", MIME: []string{"application/json"}},
				{Port: "columns", Label: "Columns", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"dsn":    {"type":"string"},
					"sql":    {"type":"string"},
					"params": {"type":"array","items":{}},
					"limit":  {"type":"integer","minimum":1}
				},
				"required":["dsn","sql"]
			}`),
			Idempotent: true,
		},
		Execute: executePostgresQuery,
	})
}

// executePostgresQuery runs a single SELECT (or any read-only statement
// that produces rows) and emits the result as a list of {column: value}
// maps plus a column-name list. The two outputs together feed the same
// row shape downstream nodes already consume from excel_read — so the
// reverse loop (DB → Excel report, DB → AI prompt input) is wire-up
// compatible with the forward one.
//
// Parameterization: the graph author writes "$1, $2" placeholders in
// the SQL and passes a `params` array; pgx binds them with type
// inference. This is the only safe way to combine graph-author input
// with SQL — string concatenation belongs in a transformer node where
// the user knows they're hand-rolling escaping.
//
// Values: pgx returns Go-typed values per Postgres column type
// (int64, float64, string, bool, time.Time, []byte, []any for arrays,
// map[string]any for jsonb). The map[string]any output marshals
// straight to JSON, so consumers downstream see typed values rather
// than the all-strings shape excel_read produces. That's a deliberate
// asymmetry: SQL has types, spreadsheets don't.
//
// Connection lifecycle is the shared registry (see conns.go) — same
// pgxpool.Pool reused across all postgres_* drops for a given
// (tenant, dsn).
func executePostgresQuery(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	dsn, err := params.String(job.Params, "dsn")
	if err != nil {
		return params.Err(job, "bad_param", err.Error()), nil
	}
	sqlText, err := params.String(job.Params, "sql")
	if err != nil {
		return params.Err(job, "bad_param", err.Error()), nil
	}
	if sqlText == "" {
		return params.Err(job, "bad_param", "sql is empty"), nil
	}

	var args []any
	if v, ok := job.Params["params"]; ok && v != nil {
		raw, ok := v.([]any)
		if !ok {
			return params.Err(job, "bad_param",
				fmt.Sprintf("params: expected array, got %T", v)), nil
		}
		args = raw
	}

	limit := 0 // 0 = unlimited
	if n, ok := paramInt(job.Params, "limit"); ok {
		if n < 0 {
			return params.Err(job, "bad_param", "limit must be >= 0"), nil
		}
		limit = n
	}

	pool, err := defaultPGRegistry.pgPool(ctx, job.Tenant, dsn)
	if err != nil {
		return params.Err(job, "db", fmt.Sprintf("connect: %v", err)), nil
	}

	rows, err := pool.Query(ctx, sqlText, args...)
	if err != nil {
		return params.Err(job, "db", fmt.Sprintf("query: %v", err)), nil
	}
	defer rows.Close()

	// Column names come from FieldDescriptions, captured once before
	// we start iterating so we can map values back to names per row
	// without re-fetching metadata.
	fields := rows.FieldDescriptions()
	columns := make([]string, len(fields))
	for i, f := range fields {
		columns[i] = string(f.Name)
	}

	out := make([]map[string]any, 0, 16)
	for rows.Next() {
		vals, err := rows.Values()
		if err != nil {
			return params.Err(job, "db", fmt.Sprintf("scan row %d: %v", len(out), err)), nil
		}
		rec := make(map[string]any, len(columns))
		for i, c := range columns {
			rec[c] = vals[i]
		}
		out = append(out, rec)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	if err := rows.Err(); err != nil {
		return params.Err(job, "db", fmt.Sprintf("iterate: %v", err)), nil
	}

	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"rows":    {MIME: "application/json", Inline: out},
			"columns": {MIME: "application/json", Inline: columns},
		},
	}, nil
}
