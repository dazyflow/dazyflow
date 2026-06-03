package db

import (
	"context"
	"encoding/json"
	"fmt"

	"git.sr.ht/~klahr/hazyflow/core"
	"git.sr.ht/~klahr/hazyflow/engine"
	"git.sr.ht/~klahr/hazyflow/drops/internal/params"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:             "mysql_query",
			Version:        "1.0",
			Label:          "MySQL query",
			Color:          "#00758f",
			Icon:           "database",
			BrandLogo:      "/brands/mysql.svg",
			Category:       "io",
			Provider:       "internal",
			Integration:    "MySQL",
			Tags:           []string{"mysql", "mariadb", "sql", "database", "query", "select"},
			Description:    "Run a SELECT against your MySQL or MariaDB database and get rows back. Use ? placeholders in the SQL and pass values through the params array so user-supplied data is safely escaped. Set a row limit to keep large result sets bounded.",
			Summary:        "Run a parameterized SELECT against MySQL/MariaDB and emit rows plus column names as a result set.",
			Examples: []core.ParamsExample{
				{
					Title:  "Recent orders for one customer",
					Params: json.RawMessage(`{"dsn":"${tenant:MYSQL_DSN}","sql":"SELECT id, total FROM orders WHERE customer_id = ? ORDER BY id DESC LIMIT 50","params":[42]}`),
				},
				{
					Title:  "Aggregate with a row cap",
					Params: json.RawMessage(`{"dsn":"${tenant:MYSQL_DSN}","sql":"SELECT status, count(*) AS n FROM orders GROUP BY status","limit":100}`),
					Notes:  "Empty params is fine when the SQL has no ? placeholders.",
				},
			},
			RequiresConnections: []core.ConnectionRequirement{
				{Kind: "secret", Name: "MYSQL_DSN", Note: "MySQL connection string (user:pass@host:3306/db)"},
			},
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
		Execute: executeMySQLQuery,
	})
}

// executeMySQLQuery runs a single SELECT and emits the result as the
// shared {column: value}[] shape — same as postgres_query and
// sqlite_query, so the three are wire-compatible end-to-end.
//
// Type handling caveat: the MySQL driver returns []byte for most
// stored types unless the connection has parseTime + the right
// charset/collation set up. Most users get strings back — this is a
// MySQL-driver quirk, not something the drop tries to fight. If you
// want native int64/float64/time.Time, include parseTime=true in the
// DSN and use INTEGER/DECIMAL/DATETIME column types on the source
// table.
func executeMySQLQuery(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
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

	limit := 0
	if n, ok := paramInt(job.Params, "limit"); ok {
		if n < 0 {
			return params.Err(job, "bad_param", "limit must be >= 0"), nil
		}
		limit = n
	}

	db, err := defaultMySQLRegistry.sqlDB(ctx, job.Tenant, dsn)
	if err != nil {
		return params.Err(job, "db", fmt.Sprintf("connect: %v", err)), nil
	}

	rows, err := db.QueryContext(ctx, sqlText, args...)
	if err != nil {
		return params.Err(job, "db", fmt.Sprintf("query: %v", err)), nil
	}
	defer rows.Close()

	columns, err := rows.Columns()
	if err != nil {
		return params.Err(job, "db", fmt.Sprintf("columns: %v", err)), nil
	}

	out := make([]map[string]any, 0, 16)
	for rows.Next() {
		vals := make([]any, len(columns))
		ptrs := make([]any, len(columns))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return params.Err(job, "db", fmt.Sprintf("scan row %d: %v", len(out), err)), nil
		}
		rec := make(map[string]any, len(columns))
		for i, c := range columns {
			// MySQL driver returns []byte for text/varchar columns by
			// default. Convert to string for the common case so JSON
			// downstream consumers don't see base64-encoded blobs.
			if b, ok := vals[i].([]byte); ok {
				rec[c] = string(b)
			} else {
				rec[c] = vals[i]
			}
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
