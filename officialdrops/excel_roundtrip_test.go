package officialdrops

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"git.sr.ht/~klahr/hazy-flow/core"
	"git.sr.ht/~klahr/hazy-flow/engine/containerdrop"
	"git.sr.ht/~klahr/hazy-flow/engine/jsdrop"
)

// TestExcel_RoundTrip exercises the scripted (SheetJS) Excel drops end-to-end on
// the Node runtime: excel_write serializes a row stream to an .xlsx in the
// workspace sandbox, then excel_read parses it back. This proves the bundled
// SheetJS works through the real Node host + broker + ctx.files byte path
// (jsdrop.NewJobFileStore), not just that it compiles. Skipped without `node`.
func TestExcel_RoundTrip(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	drophost, err := filepath.Abs("../engine/containerdrop/nodehost/drophost.mjs")
	if err != nil {
		t.Fatal(err)
	}
	host := containerdrop.Host{
		Files: func(job core.Job) jsdrop.FileStore { return jsdrop.NewJobFileStore(job, nil) },
	}
	cat := jsdrop.NewCatalog()
	cat.Run = func(m core.Manifest, jsESM string, _ bool) core.Transport {
		return containerdrop.NewTransport(
			m,
			containerdrop.DropRef{ID: m.ID, Argv: []string{node, drophost}, Source: []byte(jsESM)},
			containerdrop.ProcessRunner{},
			host,
		)
	}
	if err := Register(cat); err != nil {
		t.Fatalf("register: %v", err)
	}
	write, ok := cat.Get("excel_write")
	if !ok {
		t.Fatal("excel_write not registered")
	}
	read, ok := cat.Get("excel_read")
	if !ok {
		t.Fatal("excel_read not registered")
	}

	ws := t.TempDir()
	ctx := context.Background()
	prog := make(chan core.Progress, 8)

	// Write two rows to report.xlsx in the workspace sandbox.
	wres, err := write.Execute(ctx, core.Job{
		ID:            "w",
		WorkspaceRoot: ws,
		Params:        map[string]any{"path": "report.xlsx", "sheet": "Sales"},
		Input: map[string]core.Ref{
			"rows": {MIME: "application/json", Inline: []any{
				map[string]any{"name": "widget", "qty": 7},
				map[string]any{"name": "gadget", "qty": 3},
			}},
		},
	}, prog)
	if err != nil || wres.Status != core.StatusOK {
		t.Fatalf("excel_write: status=%v err=%v (%+v)", wres.Status, err, wres.Error)
	}
	// Sanity: a real .xlsx (zip) landed in the sandbox, not garbage bytes.
	raw, rerr := os.ReadFile(filepath.Join(ws, "report.xlsx"))
	if rerr != nil || len(raw) < 100 || string(raw[:2]) != "PK" {
		t.Fatalf("on-disk workbook looks wrong: %d bytes err=%v", len(raw), rerr)
	}

	// Read it back; expect the same two rows keyed by header.
	rres, err := read.Execute(ctx, core.Job{
		ID:            "r",
		WorkspaceRoot: ws,
		Params:        map[string]any{"path": "report.xlsx", "sheet": "Sales", "typed": true},
	}, prog)
	if err != nil || rres.Status != core.StatusOK {
		t.Fatalf("excel_read: status=%v err=%v (%+v)", rres.Status, err, rres.Error)
	}
	rows, ok := rres.Output["rows"].Inline.([]any)
	if !ok {
		t.Fatalf("rows output not an array: %#v", rres.Output["rows"].Inline)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2: %#v", len(rows), rows)
	}
	first, _ := rows[0].(map[string]any)
	if first["name"] != "widget" {
		t.Errorf("row0.name = %#v, want widget", first["name"])
	}
}
