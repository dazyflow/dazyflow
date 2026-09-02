// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"database/sql"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dazyflow/dazyflow/auth"
	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/engine"
	"github.com/dazyflow/dazyflow/engine/jobstore"
	_ "modernc.org/sqlite"
)

// seedBoardStore creates a `.dazyflow-store/data.db` under the sandbox
// root for (tenant, workspace) and writes a `leads` table with two rows,
// mirroring what the Collections · Save rows drop would have produced.
func seedBoardStore(t *testing.T, sb *FSSandbox, tenant, workspace string) {
	t.Helper()
	root, err := sb.Root(tenant, workspace)
	if err != nil {
		t.Fatalf("sandbox root: %v", err)
	}
	dir := filepath.Join(root, ".dazyflow-store")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir store: %v", err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "data.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE leads (email TEXT, name TEXT)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO leads (email, name) VALUES (?,?),(?,?)`,
		"a@example.com", "Ann", "b@example.com", "Bo"); err != nil {
		t.Fatalf("insert: %v", err)
	}
}

func newBoardService(t *testing.T) (*Service, *FSSandbox) {
	t.Helper()
	sb, err := NewFSSandbox(t.TempDir())
	if err != nil {
		t.Fatalf("NewFSSandbox: %v", err)
	}
	svc := &Service{
		Jobs:   jobstore.NewMemory(),
		Engine: &engine.Engine{Sandbox: sb, Resolver: &engine.NodeResolver{Native: engine.Default}},
	}
	return svc, sb
}

var boardPrincipal = core.Principal{Subject: "u", Tenant: "acme", Workspace: "main"}

func TestListBoards(t *testing.T) {
	t.Parallel()
	svc, sb := newBoardService(t)
	seedBoardStore(t, sb, "acme", "main")

	boards, err := svc.ListBoards(t.Context(), boardPrincipal, "acme", "main")
	if err != nil {
		t.Fatalf("ListBoards: %v", err)
	}
	if len(boards) != 1 || boards[0].Name != "leads" {
		t.Fatalf("expected one board 'leads', got %+v", boards)
	}
	if boards[0].Rows != 2 {
		t.Errorf("expected 2 rows, got %d", boards[0].Rows)
	}
}

func TestListBoards_EmptyStoreIsNotAnError(t *testing.T) {
	t.Parallel()
	svc, _ := newBoardService(t)
	// No store file was ever written.
	boards, err := svc.ListBoards(t.Context(), boardPrincipal, "acme", "main")
	if err != nil {
		t.Fatalf("empty store should not error: %v", err)
	}
	if len(boards) != 0 {
		t.Fatalf("expected no boards, got %+v", boards)
	}
}

func TestBoardRows(t *testing.T) {
	t.Parallel()
	svc, sb := newBoardService(t)
	seedBoardStore(t, sb, "acme", "main")

	page, err := svc.BoardRows(t.Context(), boardPrincipal, "acme", "main", "leads", 0, 0)
	if err != nil {
		t.Fatalf("BoardRows: %v", err)
	}
	if page.Total != 2 {
		t.Errorf("expected total 2, got %d", page.Total)
	}
	if len(page.Rows) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(page.Rows))
	}
	if page.Truncated {
		t.Errorf("page should not be truncated")
	}
	if got := page.Rows[0]["email"]; got != "a@example.com" {
		t.Errorf("unexpected first email: %v", got)
	}
}

func TestBoardRows_Paging(t *testing.T) {
	t.Parallel()
	svc, sb := newBoardService(t)
	seedBoardStore(t, sb, "acme", "main")

	page, err := svc.BoardRows(t.Context(), boardPrincipal, "acme", "main", "leads", 1, 0)
	if err != nil {
		t.Fatalf("BoardRows: %v", err)
	}
	if len(page.Rows) != 1 {
		t.Fatalf("expected 1 row with limit=1, got %d", len(page.Rows))
	}
	if !page.Truncated {
		t.Errorf("expected truncated=true (2 total, showing 1)")
	}
}

func TestBoardRows_UnknownBoardIs404(t *testing.T) {
	t.Parallel()
	svc, sb := newBoardService(t)
	seedBoardStore(t, sb, "acme", "main")

	_, err := svc.BoardRows(t.Context(), boardPrincipal, "acme", "main", "nope", 0, 0)
	if !errors.Is(err, errBoardNotFound) {
		t.Fatalf("expected errBoardNotFound, got %v", err)
	}
}

// TestBoardRows_RejectsCraftedName is the SQL-injection guard: a table
// name carrying SQL must never be executed. It's rejected either as an
// invalid name or — since it isn't in sqlite_master — as an unknown board;
// what must NOT happen is the leads table getting dropped.
func TestBoardRows_RejectsCraftedName(t *testing.T) {
	t.Parallel()
	svc, sb := newBoardService(t)
	seedBoardStore(t, sb, "acme", "main")

	crafted := `leads"; DROP TABLE leads; --`
	_, err := svc.BoardRows(t.Context(), boardPrincipal, "acme", "main", crafted, 0, 0)
	if err == nil {
		t.Fatalf("crafted table name should be rejected")
	}
	if !errors.Is(err, errBoardNotFound) && !errors.Is(err, errBoardInvalidName) {
		t.Fatalf("unexpected error kind: %v", err)
	}
	// The real table must survive.
	boards, err := svc.ListBoards(t.Context(), boardPrincipal, "acme", "main")
	if err != nil {
		t.Fatalf("ListBoards after attack: %v", err)
	}
	if len(boards) != 1 || boards[0].Name != "leads" || boards[0].Rows != 2 {
		t.Fatalf("leads table should be untouched, got %+v", boards)
	}
}

func TestClearBoard(t *testing.T) {
	t.Parallel()
	svc, sb := newBoardService(t)
	seedBoardStore(t, sb, "acme", "main")

	if err := svc.ClearBoard(t.Context(), boardPrincipal, "acme", "main", "leads"); err != nil {
		t.Fatalf("ClearBoard: %v", err)
	}
	boards, err := svc.ListBoards(t.Context(), boardPrincipal, "acme", "main")
	if err != nil {
		t.Fatalf("ListBoards: %v", err)
	}
	if len(boards) != 0 {
		t.Fatalf("expected board cleared, got %+v", boards)
	}
}

func TestClearBoard_EmptyStoreIsNoOp(t *testing.T) {
	t.Parallel()
	svc, _ := newBoardService(t)
	if err := svc.ClearBoard(t.Context(), boardPrincipal, "acme", "main", "leads"); err != nil {
		t.Fatalf("clearing a board in an empty store should be a no-op, got %v", err)
	}
}

func TestBoards_NoSandboxIsUnavailable(t *testing.T) {
	t.Parallel()
	svc := &Service{Jobs: jobstore.NewMemory(), Engine: &engine.Engine{}}
	_, err := svc.ListBoards(t.Context(), boardPrincipal, "acme", "main")
	if !errors.Is(err, errBoardsUnavailable) {
		t.Fatalf("expected errBoardsUnavailable, got %v", err)
	}
}

// HTTP-handler authz / scope / not-configured branches of the /me/boards
// endpoints. The default harness has no Engine.Sandbox, so the board service
// reports errBoardsUnavailable -> 501.

func TestListBoardsMe_NotConfigured(t *testing.T) {
	t.Parallel()
	h := newGatewayHarness(t)
	rw := h.do(t, "GET", "/api/v1/me/boards", nil)
	if rw.Code != http.StatusNotImplemented {
		t.Fatalf("list boards no sandbox = %d (%s), want 501", rw.Code, rw.Body.String())
	}
	if got := rw.Body.String(); !strings.Contains(got, "not_configured") {
		t.Errorf("body %s", got)
	}
}

func TestGetBoardMe_NotConfigured(t *testing.T) {
	t.Parallel()
	h := newGatewayHarness(t)
	rw := h.do(t, "GET", "/api/v1/me/boards/leads", nil)
	if rw.Code != http.StatusNotImplemented {
		t.Fatalf("get board no sandbox = %d (%s), want 501", rw.Code, rw.Body.String())
	}
}

func TestListBoardsMe_ForbiddenScope(t *testing.T) {
	t.Parallel()
	h := newGatewayHarness(t)
	// Cross-tenant ?tenant= -> 403 forbidden_scope.
	rw := h.do(t, "GET", "/api/v1/me/boards?tenant=other", nil)
	if rw.Code != http.StatusForbidden {
		t.Fatalf("cross-tenant boards = %d (%s), want 403", rw.Code, rw.Body.String())
	}
	// Cross-workspace.
	rw = h.do(t, "GET", "/api/v1/me/boards?workspace=other", nil)
	if rw.Code != http.StatusForbidden {
		t.Fatalf("cross-workspace boards = %d, want 403", rw.Code)
	}
}

func TestListBoardsMe_MissingScope(t *testing.T) {
	t.Parallel()
	h := newGatewayHarness(t)
	// A token with no tenant/workspace binding and no query params -> 400.
	role := core.Role{Name: "free", Permissions: []core.Permission{core.PermGraphRun}}
	_, tok, err := auth.IssueAPIKey(h.ks, t.Context(), "k-unbound", "", "", "nobody", []core.Role{role}, nil)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	saved := h.token
	h.token = tok
	defer func() { h.token = saved }()
	rw := h.do(t, "GET", "/api/v1/me/boards", nil)
	if rw.Code != http.StatusBadRequest {
		t.Fatalf("unbound boards = %d (%s), want 400", rw.Code, rw.Body.String())
	}
}

func TestClearBoardMe_ForbiddenWithoutEditPerm(t *testing.T) {
	t.Parallel()
	h := newRunOnlyHarness(t)
	// Run-only token lacks graph:edit; clear is 403.
	rw := runOnlyDo(t, h, "DELETE", "/api/v1/me/boards/leads", nil)
	if rw.Code != http.StatusForbidden {
		t.Fatalf("run-only clear = %d (%s), want 403", rw.Code, rw.Body.String())
	}
}

func TestDeleteBoardRow(t *testing.T) {
	t.Parallel()
	svc, sb := newBoardService(t)
	seedBoardStore(t, sb, "acme", "main")

	page, err := svc.BoardRows(t.Context(), boardPrincipal, "acme", "main", "leads", 0, 0)
	if err != nil {
		t.Fatalf("BoardRows: %v", err)
	}
	// The rowid handle rides each row under the reserved key, and is NOT one of
	// the displayed columns.
	if len(page.Columns) != 2 {
		t.Errorf("columns = %v, want just the two data columns", page.Columns)
	}
	for _, c := range page.Columns {
		if c == boardRowIDKey {
			t.Errorf("%s leaked into displayed columns", boardRowIDKey)
		}
	}
	rowID, ok := page.Rows[0][boardRowIDKey].(int64)
	if !ok {
		t.Fatalf("row missing int64 %s, got %T", boardRowIDKey, page.Rows[0][boardRowIDKey])
	}
	target := page.Rows[0]["email"]

	if err := svc.DeleteBoardRow(t.Context(), boardPrincipal, "acme", "main", "leads", rowID); err != nil {
		t.Fatalf("DeleteBoardRow: %v", err)
	}
	after, err := svc.BoardRows(t.Context(), boardPrincipal, "acme", "main", "leads", 0, 0)
	if err != nil {
		t.Fatalf("BoardRows after: %v", err)
	}
	if after.Total != 1 {
		t.Errorf("expected 1 row after delete, got %d", after.Total)
	}
	for _, r := range after.Rows {
		if r["email"] == target {
			t.Errorf("deleted row (email %v) is still present", target)
		}
	}
	// Idempotent: deleting the same rowid again succeeds with no effect.
	if err := svc.DeleteBoardRow(t.Context(), boardPrincipal, "acme", "main", "leads", rowID); err != nil {
		t.Errorf("re-deleting a gone row should be a no-op, got %v", err)
	}
}

func TestDeleteBoardRow_UnknownBoardIs404(t *testing.T) {
	t.Parallel()
	svc, sb := newBoardService(t)
	seedBoardStore(t, sb, "acme", "main")
	if err := svc.DeleteBoardRow(t.Context(), boardPrincipal, "acme", "main", "nope", 1); !errors.Is(err, errBoardNotFound) {
		t.Fatalf("expected errBoardNotFound, got %v", err)
	}
}

func TestDeleteBoardRowMe_ForbiddenWithoutEditPerm(t *testing.T) {
	t.Parallel()
	h := newRunOnlyHarness(t)
	// Run-only token lacks graph:edit; deleting a row is 403 (same bar as clear).
	rw := runOnlyDo(t, h, "DELETE", "/api/v1/me/boards/leads/rows/1", nil)
	if rw.Code != http.StatusForbidden {
		t.Fatalf("run-only row delete = %d (%s), want 403", rw.Code, rw.Body.String())
	}
}
