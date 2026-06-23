package db

import (
	"context"
	"encoding/json"
	"fmt"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/drops/internal/params"
	"git.sr.ht/~klahr/dazyflow/engine"
	_ "github.com/go-sql-driver/mysql"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "mysql_insert_rows",
			Version:     "1.0",
			Label:       "MySQL",
			Subtitle:    "Insert rows",
			Color:       "#00758f",
			Icon:        "database",
			BrandLogo:   "/brands/mysql.svg",
			Category:    "io",
			Provider:    "internal",
			Integration: "MySQL",
			Tags:        []string{"mysql", "mariadb", "sql", "database", "insert", "etl"},
			Description: "Insert rows into a MySQL or MariaDB table. Drops in rows from Sheets, Excel, or any transform node — the shape is interchangeable across the family.",
			Summary:     "Batch-insert rows into a MySQL/MariaDB table inside one transaction; auto-creates the table from headers when missing.",
			Examples: []core.ParamsExample{
				{
					Title:  "Load Excel rows into a new table",
					Params: json.RawMessage(`{"table":"signups"}`),
					Notes:  "create_table defaults to true, so the table is built from the upstream headers on first run. The connection comes from your MySQL connection, set once under Apps.",
				},
				{
					Title:  "Append into a pre-existing schema",
					Params: json.RawMessage(`{"table":"orders","create_table":false,"column_types":{"id":"BIGINT","total":"DECIMAL(10,2)"}}`),
				},
			},
			// The connection string is a per-tenant connection, set once under
			// Apps (the same Connect flow as Postgres/Claude/ntfy) — so the editor
			// shows a "Connect MySQL" affordance instead of a raw DSN field, and the
			// secret never lands in the graph. injectConnectionDefaults fills the
			// unset 'dsn' param from conn.mysql.dsn at run time. No MySQL server?
			// Use the SQLite step instead (zero setup).
			ConnectionFields: []core.ConnectionField{
				{Key: "dsn", Label: "Connection string", Secret: true, Required: true, Placeholder: "user:pass@tcp(host:3306)/db"},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				{Port: "rows", Label: "Rows", Required: true, MIME: []string{"application/json"}},
			},
			Outputs: []core.Port{
				{Port: "inserted", Label: "Rows inserted", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"table":        {"type":"string"},
					"create_table": {"type":"boolean","default":true,"description":"Auto-create the table from headers when missing. Defaults true."},
					"column_types": {"type":"object","additionalProperties":{"type":"string"}},
					"field_mapping":{"type":"object","additionalProperties":{"type":"string"},"title":"Column mapping","description":"Optional. Choose which incoming fields to write and name their columns — {incoming field: column name}. Only listed fields are written (others dropped); blank a column name to skip a field. Leave empty to write every field. For row filtering or defaults, use a Map rows step first."}
				},
				"required":["table"]
			}`),
		},
		Execute: executeMySQLInsertRows,
	})
}

// executeMySQLInsertRows mirrors postgres_insert_rows for the MySQL
// world. Three notable syntactic differences:
//
//   - identifier quoting uses backticks: `col` not "col"
//   - placeholders are ?, not $1/$2/...
//   - no schema concept; the database lives in the DSN, so there's no
//     `schema` param to qualify the table name
//
// Connection pooling: routed through defaultMySQLRegistry which caches
// *sql.DB handles per (tenant, dsn). *sql.DB is already a connection
// pool internally; caching the handle avoids per-job Ping + auth and
// keeps connection re-use across drops in the same workspace.
func executeMySQLInsertRows(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	dsn, err := params.String(job.Params, "dsn")
	if err != nil {
		return params.Err(job, "bad_param", err.Error()), nil
	}
	table, err := params.String(job.Params, "table")
	if err != nil {
		return params.Err(job, "bad_param", err.Error()), nil
	}
	if err := validateIdent(table); err != nil {
		return params.Err(job, "bad_param", fmt.Sprintf("table name %q: %v", table, err)), nil
	}

	ri, errRes := parseRowsInput(job)
	if errRes != nil {
		return *errRes, nil
	}

	db, err := defaultMySQLRegistry.sqlDB(ctx, job.Tenant, dsn)
	if err != nil {
		return params.Err(job, "db", fmt.Sprintf("connect: %v", err)), nil
	}

	return runInsert(ctx, job, mysqlDialect{}, sqlConn{db: db}, quoteIdentBacktick(table), ri)
}
