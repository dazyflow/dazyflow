package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"git.sr.ht/~klahr/hazyflow/core"
	"git.sr.ht/~klahr/hazyflow/engine"
	"git.sr.ht/~klahr/hazyflow/drops/internal/params"
	_ "github.com/go-sql-driver/mysql"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:             "mysql_insert_rows",
			Version:        "1.0",
			Label:          "MySQL insert rows",
			Color:          "#00758f",
			Icon:           "database",
			BrandLogo:      "/brands/mysql.svg",
			Category:       "io",
			Provider:       "internal",
			Integration:    "MySQL",
			Tags:           []string{"mysql", "mariadb", "sql", "database", "insert", "etl"},
			Description:    "Insert rows into a MySQL or MariaDB table. Drops in rows from Sheets, Excel, or any transform node — the shape is interchangeable across the family.",
			Summary:        "Batch-insert rows into a MySQL/MariaDB table inside one transaction; auto-creates the table from headers when missing.",
			Examples: []core.ParamsExample{
				{
					Title:  "Load Excel rows into a new table",
					Params: json.RawMessage(`{"dsn":"${secret.MYSQL_DSN}","table":"signups"}`),
					Notes:  "create_table defaults to true, so the table is built from the upstream headers on first run.",
				},
				{
					Title:  "Append into a pre-existing schema",
					Params: json.RawMessage(`{"dsn":"${secret.MYSQL_DSN}","table":"orders","create_table":false,"column_types":{"id":"BIGINT","total":"DECIMAL(10,2)"}}`),
				},
			},
			RequiresConnections: []core.ConnectionRequirement{
				{Kind: "secret", Name: "MYSQL_DSN", Note: "MySQL connection string (user:pass@host:3306/db)"},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				{Port: "rows", Label: "Rows", Required: true, MIME: []string{"application/json"}},
				{Port: "headers", Label: "Headers", Required: false, MIME: []string{"application/json"}},
			},
			Outputs: []core.Port{
				{Port: "inserted", Label: "Inserted count", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"dsn":          {"type":"string"},
					"table":        {"type":"string"},
					"create_table": {"type":"boolean","default":true,"description":"Auto-create the table from headers when missing. Defaults true."},
					"column_types": {"type":"object","additionalProperties":{"type":"string"}}
				},
				"required":["dsn","table"]
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
	rows, headers := ri.rows, ri.headers

	db, err := defaultMySQLRegistry.sqlDB(ctx, job.Tenant, dsn)
	if err != nil {
		return params.Err(job, "db", fmt.Sprintf("connect: %v", err)), nil
	}

	createTable := true
	if v, present := params.Bool(job.Params, "create_table"); present {
		createTable = v
	}
	if createTable && len(headers) > 0 {
		colTypes, _ := paramStringMap(job.Params, "column_types")
		if err := mysqlEnsureTable(ctx, db, table, headers, colTypes); err != nil {
			return params.Err(job, "db", err.Error()), nil
		}
	}

	if len(rows) == 0 {
		return core.Result{
			JobID:  job.ID,
			Status: core.StatusOK,
			Output: map[string]core.Ref{
				"inserted": {MIME: "application/json", Inline: 0},
			},
		}, nil
	}

	inserted, err := mysqlInsertBatch(ctx, db, table, headers, rows)
	if err != nil {
		return params.Err(job, "db", err.Error()), nil
	}
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"inserted": {MIME: "application/json", Inline: inserted},
		},
	}, nil
}

// mysqlEnsureTable issues CREATE TABLE IF NOT EXISTS sized to
// headers. Default column type is TEXT (which in MySQL stores up to
// 65,535 bytes — plenty for Excel string columns). VARCHAR would
// need a length and we don't know it; users who want VARCHAR(N)
// pass it explicitly via column_types.
func mysqlEnsureTable(ctx context.Context, db *sql.DB, table string, headers []string, colTypes map[string]string) error {
	cols := make([]string, len(headers))
	for i, h := range headers {
		t := "TEXT"
		if v, ok := colTypes[h]; ok && v != "" {
			t = v
		}
		cols[i] = fmt.Sprintf("%s %s", quoteIdentBacktick(h), t)
	}
	stmt := fmt.Sprintf("CREATE TABLE IF NOT EXISTS %s (%s)", quoteIdentBacktick(table), strings.Join(cols, ", "))
	if _, err := db.ExecContext(ctx, stmt); err != nil {
		return fmt.Errorf("create table: %w", err)
	}
	return nil
}

// mysqlInsertBatch runs all rows in one transaction. Per-row failure
// rolls the whole batch back — same contract as the other db drops.
func mysqlInsertBatch(ctx context.Context, db *sql.DB, table string, headers []string, rows []map[string]any) (int, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin tx: %w", err)
	}
	cols := make([]string, len(headers))
	placeholders := make([]string, len(headers))
	for i, h := range headers {
		cols[i] = quoteIdentBacktick(h)
		placeholders[i] = "?"
	}
	stmt, err := tx.PrepareContext(ctx, fmt.Sprintf(
		"INSERT INTO %s (%s) VALUES (%s)",
		quoteIdentBacktick(table), strings.Join(cols, ", "), strings.Join(placeholders, ", "),
	))
	if err != nil {
		_ = tx.Rollback()
		return 0, fmt.Errorf("prepare insert: %w", err)
	}
	defer stmt.Close()

	count := 0
	for i, row := range rows {
		args := make([]any, len(headers))
		for j, h := range headers {
			args[j] = row[h]
		}
		if _, err := stmt.ExecContext(ctx, args...); err != nil {
			_ = tx.Rollback()
			return 0, fmt.Errorf("insert row %d: %w", i, err)
		}
		count++
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit: %w", err)
	}
	return count, nil
}
