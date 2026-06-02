package containerdrop

import (
	"context"
	"testing"

	"git.sr.ht/~klahr/hazyflow/core"
	"git.sr.ht/~klahr/hazyflow/engine/jsdrop"
	"git.sr.ht/~klahr/hazyflow/officialdrops"
)

// TestOfficialDrops_RunViaNode is the Phase D gate: official drops, registered
// into the catalog, resolve through the catalog's Run hook to the Node host and
// execute there — the uniform runtime. It exercises the excel drops (SheetJS),
// proving the heaviest official drop runs in Node end-to-end (write → read an
// .xlsx through the broker). Under Node, SheetJS needs none of the goja shims.
func TestOfficialDrops_RunViaNode(t *testing.T) {
	node := nodeHostArgv(t) // skips if node absent

	fs := &memFS{m: map[string][]byte{}}
	host := testHost(&stubDoer{}, fs)

	cat := jsdrop.NewCatalog()
	cat.Run = func(m core.Manifest, jsESM string, _ bool) core.Transport {
		return NewTransport(
			m,
			DropRef{ID: m.ID, Argv: node, Source: []byte(jsESM)},
			ProcessRunner{},
			host,
		)
	}
	if err := officialdrops.Register(cat); err != nil {
		t.Fatalf("register official drops: %v", err)
	}

	write, ok := cat.GetForTenant("", "excel_write", "")
	if !ok {
		t.Fatal("excel_write not resolved")
	}
	read, ok := cat.GetForTenant("", "excel_read", "")
	if !ok {
		t.Fatal("excel_read not resolved")
	}

	ctx := context.Background()
	prog := make(chan core.Progress, 8)

	wres, err := write.Execute(ctx, core.Job{
		ID:     "w",
		Params: map[string]any{"path": "report.xlsx", "sheet": "Sales"},
		Input: map[string]core.Ref{
			"rows": {MIME: "application/json", Inline: []any{
				map[string]any{"name": "widget", "qty": 7},
			}},
		},
	}, prog)
	if err != nil || wres.Status != core.StatusOK {
		t.Fatalf("excel_write via Node: status=%v err=%v (%+v)", wres.Status, err, wres.Error)
	}

	rres, err := read.Execute(ctx, core.Job{
		ID:     "r",
		Params: map[string]any{"path": "report.xlsx", "sheet": "Sales", "typed": true},
	}, prog)
	if err != nil || rres.Status != core.StatusOK {
		t.Fatalf("excel_read via Node: status=%v err=%v (%+v)", rres.Status, err, rres.Error)
	}
	rows, ok := rres.Output["rows"].Inline.([]any)
	if !ok || len(rows) != 1 {
		t.Fatalf("rows = %#v, want one row", rres.Output["rows"].Inline)
	}
	if first, _ := rows[0].(map[string]any); first["name"] != "widget" {
		t.Errorf("row name = %#v, want widget", rows[0])
	}
}
