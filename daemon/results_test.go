package daemon

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"git.sr.ht/~klahr/hazyflow/core"
	"git.sr.ht/~klahr/hazyflow/engine"
	"git.sr.ht/~klahr/hazyflow/engine/jobstore"
	_ "modernc.org/sqlite"
)

// seedBoardStore creates a `.hazyflow-store/data.db` under the sandbox
// root for (tenant, workspace) and writes a `leads` table with two rows,
// mirroring what the Built-in store · Save drop would have produced.
func seedBoardStore(t *testing.T, sb *FSSandbox, tenant, workspace string) {
	t.Helper()
	root, err := sb.Root(tenant, workspace)
	if err != nil {
		t.Fatalf("sandbox root: %v", err)
	}
	dir := filepath.Join(root, ".hazyflow-store")
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
	svc, _ := newBoardService(t)
	if err := svc.ClearBoard(t.Context(), boardPrincipal, "acme", "main", "leads"); err != nil {
		t.Fatalf("clearing a board in an empty store should be a no-op, got %v", err)
	}
}

func TestBoards_NoSandboxIsUnavailable(t *testing.T) {
	svc := &Service{Jobs: jobstore.NewMemory(), Engine: &engine.Engine{}}
	_, err := svc.ListBoards(t.Context(), boardPrincipal, "acme", "main")
	if !errors.Is(err, errBoardsUnavailable) {
		t.Fatalf("expected errBoardsUnavailable, got %v", err)
	}
}
