// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package schemaports_test

import (
	"encoding/json"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/internal/schemaports"
)

func names(ports []core.Port) []string {
	out := make([]string, 0, len(ports))
	for _, p := range ports {
		out = append(out, p.Port)
	}
	return out
}

func eq(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("ports = %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("ports = %v, want %v", got, want)
		}
	}
}

// Required first, then optional, alphabetical within each — the order the
// editor's layout depends on.
func TestBuild_RequiredFirstThenAlphabetical(t *testing.T) {
	ports := schemaports.Build([]schemaports.Candidate{
		{Name: "zeta", Type: "string"},
		{Name: "alpha", Type: "string"},
		{Name: "title", Type: "string", Required: true},
		{Name: "body", Type: "string", Required: true},
	}, schemaports.Options{})
	eq(t, names(ports), []string{"body", "title", "alpha", "zeta"})
}

// The cap must never hide a required argument: it takes them first.
func TestBuild_CapKeepsEveryRequired(t *testing.T) {
	var cands []schemaports.Candidate
	for i := 0; i < 20; i++ {
		cands = append(cands, schemaports.Candidate{Name: string(rune('a'+i)) + "opt", Type: "string"})
	}
	cands = append(cands,
		schemaports.Candidate{Name: "zz_required", Type: "string", Required: true},
		schemaports.Candidate{Name: "yy_required", Type: "string", Required: true},
	)
	ports := schemaports.Build(cands, schemaports.Options{})
	if len(ports) != schemaports.DefaultMax {
		t.Fatalf("len(ports) = %d, want %d", len(ports), schemaports.DefaultMax)
	}
	if ports[0].Port != "yy_required" || ports[1].Port != "zz_required" {
		t.Fatalf("required args must come first, got %v", names(ports))
	}
	for _, p := range ports {
		if !p.Required && p.Port == "zz_required" {
			t.Error("required flag lost")
		}
	}
}

func TestBuild_Max(t *testing.T) {
	cands := []schemaports.Candidate{
		{Name: "a", Type: "string"}, {Name: "b", Type: "string"}, {Name: "c", Type: "string"},
	}
	if got := len(schemaports.Build(cands, schemaports.Options{Max: 2})); got != 2 {
		t.Fatalf("len = %d, want 2", got)
	}
}

// Only scalars earn a pin; objects, arrays and untyped arguments stay params.
func TestBuild_ScalarsOnly(t *testing.T) {
	ports := schemaports.Build([]schemaports.Candidate{
		{Name: "text", Type: "string"},
		{Name: "count", Type: "integer"},
		{Name: "ratio", Type: "number"},
		{Name: "flag", Type: "boolean"},
		{Name: "nested", Type: "object"},
		{Name: "list", Type: "array"},
		{Name: "untyped"},
		{Name: "nullable", Type: []any{"string", "null"}},
	}, schemaports.Options{})
	want := map[string]string{
		"count":    "text/plain",
		"ratio":    "text/plain",
		"text":     "text/plain",
		"nullable": "text/plain",
		"flag":     core.MIMEBool,
	}
	if len(ports) != len(want) {
		t.Fatalf("ports = %v, want %d of them", names(ports), len(want))
	}
	for _, p := range ports {
		mime, ok := want[p.Port]
		if !ok {
			t.Errorf("unexpected port %q", p.Port)
			continue
		}
		if len(p.MIME) != 1 || p.MIME[0] != mime {
			t.Errorf("%s MIME = %v, want %s", p.Port, p.MIME, mime)
		}
	}
}

func TestBuild_ReservedAndUnspellableNamesSkipped(t *testing.T) {
	ports := schemaports.Build([]schemaports.Candidate{
		{Name: "ok", Type: "string"},
		{Name: "input", Type: "string"},
		{Name: core.PassPort, Type: "string"},
		{Name: "user name", Type: "string"},
		{Name: "a/b", Type: "string"},
		{Name: "", Type: "string"},
	}, schemaports.Options{Reserved: []string{"input"}})
	eq(t, names(ports), []string{"ok"})
}

// core.PassPort is reserved whether or not the caller lists it: a caller that
// forgot would shadow the universal passthrough pin silently.
func TestBuild_PassPortAlwaysReserved(t *testing.T) {
	ports := schemaports.Build([]schemaports.Candidate{
		{Name: core.PassPort, Type: "string"},
	}, schemaports.Options{})
	if len(ports) != 0 {
		t.Fatalf("ports = %v, want none", names(ports))
	}
}

// Every port is inline-only, unconditionally — both callers send values to
// something that cannot read the daemon's disk.
func TestBuild_AlwaysInlineOnly(t *testing.T) {
	ports := schemaports.Build([]schemaports.Candidate{{Name: "x", Type: "string"}}, schemaports.Options{})
	if len(ports) != 1 || !ports[0].InlineOnly {
		t.Fatalf("port = %+v, want InlineOnly", ports)
	}
}

func TestBuild_LabelFallsBackToName(t *testing.T) {
	ports := schemaports.Build([]schemaports.Candidate{
		{Name: "x", Type: "string"},
		{Name: "y", Type: "string", Label: "Why"},
	}, schemaports.Options{})
	if ports[0].Label != "x" || ports[1].Label != "Why" {
		t.Fatalf("labels = %q/%q", ports[0].Label, ports[1].Label)
	}
}

func TestFromJSONSchema(t *testing.T) {
	cands := schemaports.FromJSONSchema(json.RawMessage(`{
		"type":"object",
		"properties":{
			"title":{"type":"string","title":"Title"},
			"draft":{"type":"boolean"}
		},
		"required":["title"]
	}`))
	if len(cands) != 2 {
		t.Fatalf("candidates = %+v, want 2", cands)
	}
	for _, c := range cands {
		switch c.Name {
		case "title":
			if !c.Required || c.Label != "Title" {
				t.Errorf("title = %+v", c)
			}
		case "draft":
			if c.Required {
				t.Errorf("draft should be optional: %+v", c)
			}
		default:
			t.Errorf("unexpected candidate %q", c.Name)
		}
	}
}

// An unreadable schema is not an error — the step still works through params
// and the overlay port, and failing here would refuse a whole server over one
// tool's unusual schema.
func TestFromJSONSchema_UnreadableYieldsNothing(t *testing.T) {
	for _, raw := range []string{``, `not json`, `{"type":"object"}`, `[]`} {
		if got := schemaports.FromJSONSchema(json.RawMessage(raw)); got != nil {
			t.Errorf("%q → %+v, want nil", raw, got)
		}
	}
}

func TestAssemble_Precedence(t *testing.T) {
	ports := []core.Port{{Port: "a"}, {Port: "b"}, {Port: "c"}, {Port: "input"}, {Port: core.PassPort}}
	args, err := schemaports.Assemble(
		map[string]any{"a": "from-params", "b": "from-params", "c": "from-params"},
		map[string]core.Ref{
			"b":           {Inline: "from-port"},
			"input":       {Inline: map[string]any{"b": "from-overlay", "c": "from-overlay"}},
			core.PassPort: {Inline: "ignored"},
			"undeclared":  {Inline: "ignored"},
		},
		ports, "input",
	)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"a": "from-params", "b": "from-port", "c": "from-overlay"}
	for k, v := range want {
		if args[k] != v {
			t.Errorf("%s = %v, want %v", k, args[k], v)
		}
	}
	if _, ok := args["undeclared"]; ok {
		t.Error("an input for a port the manifest never declared must not become an argument")
	}
	if _, ok := args[core.PassPort]; ok {
		t.Error("the passthrough pin must not become an argument")
	}
}

func TestAssemble_OverlayShapes(t *testing.T) {
	ports := []core.Port{{Port: "k"}, {Port: "input"}}
	for _, tc := range []struct {
		name   string
		inline any
		want   any
		bad    bool
	}{
		{name: "object", inline: map[string]any{"k": "v"}, want: "v"},
		{name: "json string", inline: `{"k":"v"}`, want: "v"},
		{name: "json bytes", inline: []byte(`{"k":"v"}`), want: "v"},
		{name: "not json", inline: "nope", bad: true},
		{name: "json array", inline: "[1,2]", bad: true},
		{name: "bytes not json", inline: []byte("nope"), bad: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args, err := schemaports.Assemble(nil, map[string]core.Ref{"input": {Inline: tc.inline}}, ports, "input")
			if tc.bad {
				if err == nil {
					t.Fatalf("want an error, got %+v", args)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if args["k"] != tc.want {
				t.Fatalf("k = %v, want %v", args["k"], tc.want)
			}
		})
	}
}

// A port wired with an explicit nil must not blank out a typed param: absent
// and empty are different statements.
func TestAssemble_NilPortDoesNotOverride(t *testing.T) {
	args, err := schemaports.Assemble(
		map[string]any{"a": "typed"},
		map[string]core.Ref{"a": {Inline: nil}},
		[]core.Port{{Port: "a"}}, "input",
	)
	if err != nil {
		t.Fatal(err)
	}
	if args["a"] != "typed" {
		t.Fatalf("a = %v, want the typed param to survive", args["a"])
	}
}
