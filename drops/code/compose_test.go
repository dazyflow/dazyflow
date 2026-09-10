// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// The unit tests call executeCode directly. This one goes through the
// registry, which is the only way to prove the two things a flow author
// actually depends on: that the step is registered and reachable by id, and
// that what it emits is a shape the row family will take — a list of objects,
// not goja values dressed up as one.
package code_test

import (
	"context"
	"strings"
	"testing"

	"github.com/dazyflow/dazyflow/core"
	_ "github.com/dazyflow/dazyflow/drops"
	"github.com/dazyflow/dazyflow/engine"
)

func step(t *testing.T, module string, params map[string]any, in map[string]core.Ref) map[string]core.Ref {
	t.Helper()
	tr, ok := engine.Default.Get(module)
	if !ok {
		t.Fatalf("no such module %q", module)
	}
	res, err := tr.Execute(context.Background(), core.Job{ID: "t", NodeID: module, Params: params, Input: in}, nil)
	if err != nil {
		t.Fatalf("%s: %v", module, err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("%s: %+v", module, res.Error)
	}
	return res.Output
}

func TestCode_SitsInARowPipeline(t *testing.T) {
	parsed := step(t, "parse_json", map[string]any{}, map[string]core.Ref{
		"in": {MIME: "text/plain", Inline: `[
			{"name":"ada","total":50},
			{"name":"grace","total":200},
			{"name":"katherine","total":400}
		]`},
	})

	computed := step(t, "code", map[string]any{
		"mode": "each",
		"code": "if (row.total <= 100) return;\nreturn { name: row.name.toUpperCase(), vat: row.total * 0.25 };",
	}, map[string]core.Ref{"in": parsed["rows"]})

	csv := step(t, "build_csv", map[string]any{}, map[string]core.Ref{"rows": computed["out"]})

	got, isText := csv["out"].Inline.(string)
	if !isText {
		t.Fatalf("build_csv emitted %T, want CSV text", csv["out"].Inline)
	}
	want := "name,vat\nGRACE,50\nKATHERINE,100\n"
	if got != want {
		t.Errorf("CSV =\n%q\nwant\n%q", got, want)
	}
}

func TestCode_IsReachableByID(t *testing.T) {
	tr, ok := engine.Default.Get("code")
	if !ok {
		t.Fatal("the code drop is not registered")
	}
	m := tr.Manifest()
	if m.Label != "Code" || m.Category != "transformation" {
		t.Errorf("manifest = %q / %q", m.Label, m.Category)
	}
	if !strings.Contains(m.Description, "no network") {
		t.Error("the description no longer states the sandbox's limits")
	}
}
