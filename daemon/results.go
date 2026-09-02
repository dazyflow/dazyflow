// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Results boards — an in-app, read-only view of the Collections.
//
// The Collections drops (drops/db/builtin_store.go) let a non-technical
// user collect rows with zero setup; these endpoints let them *see* those
// rows. A "board" is just a user table inside the workspace's built-in
// store SQLite file. There is no new storage system here: the read path is
// a thin, read-only open of the same `.dazyflow-store/data.db` the drops
// write to, scoped to the caller's (tenant, workspace).
//
// Because the store lives under the sandbox subtree it already rides the
// GDPR erasure cascade (FSSandbox.RemoveTenant) for free — boards need no
// new deletion/export wiring.

package daemon

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/dazyflow/dazyflow/core"
	_ "modernc.org/sqlite"
)

// builtinStoreRelPath mirrors drops/db.builtinStorePath — the fixed,
// workspace-local SQLite file the Collections drops read and write. It
// is re-declared here rather than imported because importing drops/db into
// the daemon would run that package's init()s and re-register its drops.
// Keep in sync with drops/db/builtin_store.go.
const builtinStoreRelPath = ".dazyflow-store/data.db"

// boardRowLimit caps how many rows BoardRows returns in a single page,
// even if a larger ?limit is requested. The page is meant to be browsed,
// not bulk-exported (the UI offers CSV for the latter, built client-side
// from what it fetched).
const boardRowLimit = 1000

// boardRowIDKey is the reserved key under which each row's SQLite rowid is
// returned — the stable handle the delete-a-row endpoint keys off. It's
// deliberately NOT one of BoardPage.Columns, so the table view never shows it;
// only the row-delete action reads it. Prefixed to avoid colliding with a real
// column of the same name.
const boardRowIDKey = "_dz_rowid"

// Sentinel errors the HTTP layer maps to status codes. Mirrors the
// not-configured / not-found / bad-input conventions in httpme.go.
var (
	errBoardsUnavailable = errors.New("Collections requires a workspace sandbox")
	errBoardNotFound     = errors.New("no such board")
	errBoardInvalidName  = errors.New("invalid board name")
)

// BoardSummary is one row of the board list: the table name and how many
// rows it currently holds.
type BoardSummary struct {
	Name string `json:"name"`
	Rows int64  `json:"rows"`
}

// BoardPage is a window onto one board's contents — its columns and a
// page of rows, plus the full row count and whether the page was capped.
type BoardPage struct {
	Name      string           `json:"name"`
	Columns   []string         `json:"columns"`
	Rows      []map[string]any `json:"rows"`
	Total     int64            `json:"total"`
	Truncated bool             `json:"truncated"`
}

// quoteBoardIdent quotes a table name for SQLite using the SQL-standard
// double-quote convention (embedded quotes doubled), mirroring
// drops/db/idents.go quoteIdent. The board name is the only
// user-controlled SQL identifier on this surface, so every reference to it
// goes through here.
func quoteBoardIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// validateBoardName rejects only genuinely unsafe names (empty, NUL byte,
// absurdly long) — the same minimum drops/db.validateIdent enforces before
// a name is quoted. Charset is intentionally unrestricted; quoting handles
// the rest, and reads additionally confirm the name is a real table in
// sqlite_master before querying it.
func validateBoardName(name string) error {
	if name == "" {
		return fmt.Errorf("%w: must not be empty", errBoardInvalidName)
	}
	if strings.ContainsRune(name, 0) {
		return fmt.Errorf("%w: must not contain NUL bytes", errBoardInvalidName)
	}
	if len(name) > 1024 {
		return fmt.Errorf("%w: too long", errBoardInvalidName)
	}
	return nil
}

// openBoardStore opens the workspace's Collections store. Returns (nil, nil)
// when the store file doesn't exist yet — a workspace whose flows have
// never saved a row simply has an empty store, which is a valid state, not
// an error (mirrors openBuiltinStore's create=false path in drops/db).
// The caller closes the returned db.
func (s *Service) openBoardStore(tenant, workspace string) (*sql.DB, error) {
	if s.Engine == nil || s.Engine.Sandbox == nil {
		return nil, errBoardsUnavailable
	}
	root, err := s.Engine.Sandbox.Root(tenant, workspace)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(root, filepath.FromSlash(builtinStoreRelPath))
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open store: %w", err)
	}
	return db, nil
}

// ListBoards returns the user tables in the workspace's Collections store,
// each with its current row count, ordered by name. SQLite internal tables
// (sqlite_*) are excluded. An empty / never-written store returns an empty
// list, not an error. Scoping to (tenant, workspace) is the caller's
// responsibility (the HTTP layer enforces it before calling).
func (s *Service) ListBoards(ctx context.Context, p core.Principal, tenant, workspace string) ([]BoardSummary, error) {
	db, err := s.openBoardStore(tenant, workspace)
	if err != nil {
		return nil, err
	}
	if db == nil {
		return []BoardSummary{}, nil
	}
	defer db.Close()

	rows, err := db.QueryContext(ctx,
		`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list tables: %w", err)
	}
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan table name: %w", err)
		}
		names = append(names, n)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("iterate tables: %w", err)
	}
	rows.Close()

	out := make([]BoardSummary, 0, len(names))
	for _, n := range names {
		// n comes straight from sqlite_master so it's a real table, but
		// quote it anyway — the identifier is still being spliced into SQL.
		var count int64
		if err := db.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM "+quoteBoardIdent(n)).Scan(&count); err != nil {
			return nil, fmt.Errorf("count rows in %q: %w", n, err)
		}
		out = append(out, BoardSummary{Name: n, Rows: count})
	}
	return out, nil
}

// BoardRows returns a page of one board's rows plus its column names and
// total row count. The table name is validated and confirmed present in
// sqlite_master before it is ever interpolated into a query, so an
// attacker-controlled {name} can't reach the SQL parser as anything but a
// quoted identifier matching a real table. limit is capped at
// boardRowLimit; offset is floored at 0.
func (s *Service) BoardRows(ctx context.Context, p core.Principal, tenant, workspace, name string, limit, offset int) (BoardPage, error) {
	if err := validateBoardName(name); err != nil {
		return BoardPage{}, err
	}
	if limit <= 0 || limit > boardRowLimit {
		limit = boardRowLimit
	}
	if offset < 0 {
		offset = 0
	}

	db, err := s.openBoardStore(tenant, workspace)
	if err != nil {
		return BoardPage{}, err
	}
	if db == nil {
		return BoardPage{}, errBoardNotFound
	}
	defer db.Close()

	if !s.boardExists(ctx, db, name) {
		return BoardPage{}, errBoardNotFound
	}

	quoted := quoteBoardIdent(name)
	var total int64
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+quoted).Scan(&total); err != nil {
		return BoardPage{}, fmt.Errorf("count rows: %w", err)
	}

	// limit/offset are bound as parameters (only the validated, confirmed
	// table name is spliced). The leading `rowid AS _dz_rowid` gives each row a
	// stable handle for the delete endpoint; `*` still expands to just the
	// user columns, so the displayed table is unchanged.
	rows, err := db.QueryContext(ctx,
		"SELECT rowid AS "+boardRowIDKey+", * FROM "+quoted+" LIMIT ? OFFSET ?", limit, offset)
	if err != nil {
		return BoardPage{}, fmt.Errorf("query rows: %w", err)
	}
	defer rows.Close()

	scanned, err := rows.Columns()
	if err != nil {
		return BoardPage{}, fmt.Errorf("columns: %w", err)
	}
	// scanned[0] is the rowid alias; the rest are the real, displayed columns.
	columns := append([]string(nil), scanned[1:]...)
	out := make([]map[string]any, 0, limit)
	for rows.Next() {
		vals := make([]any, len(scanned))
		ptrs := make([]any, len(scanned))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return BoardPage{}, fmt.Errorf("scan row %d: %w", len(out), err)
		}
		rec := make(map[string]any, len(scanned))
		// The rowid — the delete handle — under its reserved key, kept out of
		// the displayed columns above.
		rec[boardRowIDKey] = vals[0]
		for i := 1; i < len(scanned); i++ {
			c := scanned[i]
			// The Collections store stores TEXT, so values arrive as strings;
			// a stray BLOB column would otherwise marshal to base64 noise.
			// Render bytes as a string for the friendly table view.
			if b, ok := vals[i].([]byte); ok {
				rec[c] = string(b)
			} else {
				rec[c] = vals[i]
			}
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return BoardPage{}, fmt.Errorf("iterate rows: %w", err)
	}

	return BoardPage{
		Name:      name,
		Columns:   columns,
		Rows:      out,
		Total:     total,
		Truncated: total > int64(offset+len(out)),
	}, nil
}

// ClearBoard drops a board (its table) from the workspace's built-in
// store. Idempotent: an absent store or absent table returns cleanly (an
// empty store is not an error — same stance as the read path). The name is
// validated and quoted before it touches SQL.
func (s *Service) ClearBoard(ctx context.Context, p core.Principal, tenant, workspace, name string) error {
	if err := validateBoardName(name); err != nil {
		return err
	}
	db, err := s.openBoardStore(tenant, workspace)
	if err != nil {
		return err
	}
	if db == nil {
		return nil
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, "DROP TABLE IF EXISTS "+quoteBoardIdent(name)); err != nil {
		return fmt.Errorf("drop board: %w", err)
	}
	return nil
}

// DeleteBoardRow removes a single row from a board by its SQLite rowid (the
// handle BoardRows returns under boardRowIDKey). Idempotent: deleting a row
// that's already gone succeeds with no effect — a double-click or a stale
// table view shouldn't error. An absent store or table is a not-found, mirroring
// the read path. The name is validated and quoted before it touches SQL; the
// rowid is bound as a parameter.
func (s *Service) DeleteBoardRow(ctx context.Context, p core.Principal, tenant, workspace, name string, rowID int64) error {
	if err := validateBoardName(name); err != nil {
		return err
	}
	db, err := s.openBoardStore(tenant, workspace)
	if err != nil {
		return err
	}
	if db == nil {
		return errBoardNotFound
	}
	defer db.Close()
	if !s.boardExists(ctx, db, name) {
		return errBoardNotFound
	}
	if _, err := db.ExecContext(ctx,
		"DELETE FROM "+quoteBoardIdent(name)+" WHERE rowid = ?", rowID); err != nil {
		return fmt.Errorf("delete row: %w", err)
	}
	return nil
}

// boardExists reports whether name is a real user table in the store. The
// read path uses it so an unknown board is a clean 404 rather than a raw
// "no such table" SQL error, and so we only ever SELECT from a name the
// store actually holds.
func (s *Service) boardExists(ctx context.Context, db *sql.DB, name string) bool {
	var got string
	err := db.QueryRowContext(ctx,
		`SELECT name FROM sqlite_master WHERE type='table' AND name=? AND name NOT LIKE 'sqlite_%'`,
		name).Scan(&got)
	return err == nil
}

// --- HTTP handlers ----------------------------------------------------

// boardScope is resolveScope for /me/boards. The cross-scope guard matters
// most on this surface: unlike the flows surface (where the service re-checks
// permissions per flow), the board service just opens whatever sandbox dir
// it's handed, so this is the only barrier.
func (h *HTTPGateway) boardScope(rw http.ResponseWriter, r *http.Request, p core.Principal) (string, string, bool) {
	return h.resolveScope(rw, r, p, "read boards in")
}

// writeBoardError maps the board service sentinels onto the structured
// error envelope: 501 when there's no sandbox, 404 for an unknown board,
// 400 for an invalid name, 500 otherwise.
func writeBoardError(rw http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errBoardsUnavailable):
		writeAPIError(rw, http.StatusNotImplemented, "not_configured", err.Error())
	case errors.Is(err, errBoardNotFound):
		writeAPIError(rw, http.StatusNotFound, "board_not_found", err.Error())
	case errors.Is(err, errBoardInvalidName):
		writeAPIError(rw, http.StatusBadRequest, "invalid_board", err.Error())
	default:
		writeAPIError(rw, http.StatusInternalServerError, "internal_error", err.Error())
	}
}

func (h *HTTPGateway) listBoardsMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	tenant, workspace, ok := h.boardScope(rw, r, p)
	if !ok {
		return
	}
	// Boards hold run output (often user-collected data); reading them needs
	// the same authority as viewing results. The board service does no authz
	// of its own, so it must be gated here.
	if err := core.Require(p, core.PermGraphRun); err != nil {
		writeAPIError(rw, http.StatusForbidden, "forbidden", err.Error())
		return
	}
	boards, err := h.svc.ListBoards(r.Context(), p, tenant, workspace)
	if err != nil {
		writeBoardError(rw, err)
		return
	}
	if boards == nil {
		boards = []BoardSummary{}
	}
	writeJSON(rw, http.StatusOK, map[string]any{"boards": boards})
}

func (h *HTTPGateway) getBoardMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	tenant, workspace, ok := h.boardScope(rw, r, p)
	if !ok {
		return
	}
	if err := core.Require(p, core.PermGraphRun); err != nil {
		writeAPIError(rw, http.StatusForbidden, "forbidden", err.Error())
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	page, err := h.svc.BoardRows(r.Context(), p, tenant, workspace, r.PathValue("name"), limit, offset)
	if err != nil {
		writeBoardError(rw, err)
		return
	}
	writeJSON(rw, http.StatusOK, page)
}

func (h *HTTPGateway) clearBoardMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	tenant, workspace, ok := h.boardScope(rw, r, p)
	if !ok {
		return
	}
	// Clearing a board is destructive (drops the table). Require edit
	// authority, not the read-level graph:run — a viewer must not be able to
	// wipe collected data.
	if err := core.Require(p, core.PermGraphEdit); err != nil {
		writeAPIError(rw, http.StatusForbidden, "forbidden", err.Error())
		return
	}
	name := r.PathValue("name")
	if err := h.svc.ClearBoard(r.Context(), p, tenant, workspace, name); err != nil {
		writeBoardError(rw, err)
		return
	}
	h.audit(r.Context(), p, "board.clear", name, "tenant="+tenant+" workspace="+workspace)
	rw.WriteHeader(http.StatusNoContent)
}

func (h *HTTPGateway) deleteBoardRowMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	tenant, workspace, ok := h.boardScope(rw, r, p)
	if !ok {
		return
	}
	// Deleting a row mutates collected data — same edit-level bar as clearing,
	// so a read-only viewer can't remove rows.
	if err := core.Require(p, core.PermGraphEdit); err != nil {
		writeAPIError(rw, http.StatusForbidden, "forbidden", err.Error())
		return
	}
	name := r.PathValue("name")
	rowID, err := strconv.ParseInt(r.PathValue("rowid"), 10, 64)
	if err != nil {
		writeAPIError(rw, http.StatusBadRequest, "invalid_row", "row id must be an integer")
		return
	}
	if err := h.svc.DeleteBoardRow(r.Context(), p, tenant, workspace, name, rowID); err != nil {
		writeBoardError(rw, err)
		return
	}
	h.audit(r.Context(), p, "board.row.delete", name, fmt.Sprintf("tenant=%s workspace=%s rowid=%d", tenant, workspace, rowID))
	rw.WriteHeader(http.StatusNoContent)
}
