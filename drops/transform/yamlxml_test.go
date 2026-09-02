// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package transform

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"strings"
	"testing"

	"github.com/dazyflow/dazyflow/core"
)

func runT(t *testing.T, exec func(context.Context, core.Job, chan<- core.Progress) (core.Result, error), job core.Job) core.Result {
	t.Helper()
	res, err := exec(t.Context(), job, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("status %s: %+v", res.Status, res.Error)
	}
	return res
}

func outText(t *testing.T, res core.Result) string {
	t.Helper()
	s, ok := res.Output["out"].Inline.(string)
	if !ok {
		t.Fatalf("out is %T, want a string", res.Output["out"].Inline)
	}
	return s
}

func rowsOut(t *testing.T, res core.Result) []map[string]any {
	t.Helper()
	r, ok := res.Output["rows"].Inline.([]map[string]any)
	if !ok {
		t.Fatalf("rows is %T", res.Output["rows"].Inline)
	}
	return r
}

func yamlJob(text string, p map[string]any) core.Job {
	if p == nil {
		p = map[string]any{}
	}
	return core.Job{ID: "j", Params: p, Input: map[string]core.Ref{"in": {Inline: text}}}
}

func rowsJob(rows []map[string]any, headers []string, p map[string]any) core.Job {
	if p == nil {
		p = map[string]any{}
	}
	return core.Job{ID: "j", Params: p, Input: map[string]core.Ref{
		"rows": {MIME: "application/json", Inline: rows, Headers: headers},
	}}
}

// A mapping is one row and a list of mappings is one row each — the same
// contract Read JSON has, which is the point: a flow shouldn't care which
// format the config arrived in.
func TestParseYAML_MatchesReadJSONsShape(t *testing.T) {
	one := runT(t, executeParseYAML, yamlJob("name: web\nport: 8080\n", nil))
	rows := rowsOut(t, one)
	if len(rows) != 1 || rows[0]["name"] != "web" {
		t.Fatalf("a mapping should be one row, got %+v", rows)
	}
	if rows[0]["port"] != 8080 {
		t.Errorf("port = %#v, want the number 8080 (YAML types are preserved)", rows[0]["port"])
	}

	list := runT(t, executeParseYAML, yamlJob("- name: a\n- name: b\n", nil))
	if got := rowsOut(t, list); len(got) != 2 {
		t.Fatalf("a list of mappings should be one row each, got %+v", got)
	}
}

// The path semantics are Read JSON's, including the rule that a scalar is an
// answer when a path asked for one.
func TestParseYAML_Path(t *testing.T) {
	doc := "spec:\n  containers:\n    - name: app\n      image: app:1\n    - name: sidecar\n      image: proxy:2\n"
	res := runT(t, executeParseYAML, yamlJob(doc, map[string]any{"path": "spec.containers"}))
	rows := rowsOut(t, res)
	if len(rows) != 2 || rows[0]["name"] != "app" {
		t.Fatalf("path should dig to the list, got %+v", rows)
	}

	// A scalar at the path: rows empty, value carries it.
	scalar := runT(t, executeParseYAML, yamlJob("version: 1.4.0\n", map[string]any{"path": "version"}))
	if got := rowsOut(t, scalar); len(got) != 0 {
		t.Errorf("a scalar isn't a table, rows should be empty: %+v", got)
	}
	if v := scalar.Output["value"].Inline; v != "1.4.0" {
		t.Errorf("value = %#v, want the string the path asked for", v)
	}
}

// Without a path, being handed something that isn't row-shaped is a mistake,
// not an answer — same asymmetry Read JSON draws.
func TestParseYAML_ScalarWithoutAPathFails(t *testing.T) {
	res, err := executeParseYAML(t.Context(), yamlJob("just a string\n", nil), nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status == core.StatusOK {
		t.Fatal("a bare scalar with no path should fail the step")
	}
	if res.Error.Code != "not_tabular" {
		t.Errorf("code = %q", res.Error.Code)
	}
}

// The first thing that genuinely differs from JSON: a stream may hold several
// documents. Each becomes a row, which is how a manifest bundle is shaped.
func TestParseYAML_MultiDocumentStream(t *testing.T) {
	stream := "kind: Deployment\nname: web\n---\nkind: Service\nname: web-svc\n---\nkind: Ingress\nname: web-ing\n"

	all := runT(t, executeParseYAML, yamlJob(stream, nil))
	rows := rowsOut(t, all)
	if len(rows) != 3 {
		t.Fatalf("want one row per document, got %d: %+v", len(rows), rows)
	}
	if rows[0]["kind"] != "Deployment" || rows[2]["kind"] != "Ingress" {
		t.Errorf("documents came out in the wrong order: %+v", rows)
	}

	first := runT(t, executeParseYAML, yamlJob(stream, map[string]any{"documents": "first"}))
	if got := rowsOut(t, first); len(got) != 1 || got[0]["kind"] != "Deployment" {
		t.Errorf("'first' should keep only the first document, got %+v", got)
	}
}

// A trailing "---" or a comment-only document must not become an empty row.
func TestParseYAML_EmptyDocumentsAreSkipped(t *testing.T) {
	res := runT(t, executeParseYAML, yamlJob("a: 1\n---\n# just a comment\n---\n", nil))
	if got := rowsOut(t, res); len(got) != 1 {
		t.Fatalf("want 1 real document, got %d: %+v", len(got), got)
	}
}

// The second real difference: YAML permits non-string mapping keys, which
// decode to map[interface{}]interface{} — a shape no downstream step here can
// read. Keys are rendered as text rather than the row arriving empty.
func TestParseYAML_NonStringKeysBecomeText(t *testing.T) {
	res := runT(t, executeParseYAML, yamlJob("ports:\n  8080: http\n  8443: https\n", map[string]any{"path": "ports"}))
	v, ok := res.Output["value"].Inline.(map[string]any)
	if !ok {
		t.Fatalf("value is %T, want a string-keyed map a row consumer can read", res.Output["value"].Inline)
	}
	if v["8080"] != "http" || v["8443"] != "https" {
		t.Errorf("numeric keys weren't coerced to text: %+v", v)
	}
}

// yaml.v3 refuses an alias bomb itself, which matters because this text can
// arrive from an http_request. Asserted so a library change that removes the
// protection is noticed here rather than in production.
func TestParseYAML_AliasBombIsRefused(t *testing.T) {
	bomb := "a: &a [\"x\",\"x\",\"x\",\"x\",\"x\",\"x\",\"x\",\"x\",\"x\"]\n" +
		"b: &b [*a,*a,*a,*a,*a,*a,*a,*a,*a]\n" +
		"c: &c [*b,*b,*b,*b,*b,*b,*b,*b,*b]\n" +
		"d: &d [*c,*c,*c,*c,*c,*c,*c,*c,*c]\n" +
		"e: &e [*d,*d,*d,*d,*d,*d,*d,*d,*d]\n" +
		"f: &f [*e,*e,*e,*e,*e,*e,*e,*e,*e]\n" +
		"g: &g [*f,*f,*f,*f,*f,*f,*f,*f,*f]\n"

	res, err := executeParseYAML(t.Context(), yamlJob(bomb, nil), nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status == core.StatusOK {
		t.Fatal("an alias bomb must not be expanded")
	}
}

func TestParseYAML_BadYAMLIsExplained(t *testing.T) {
	res, err := executeParseYAML(t.Context(), yamlJob("a: [unclosed\n", nil), nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status == core.StatusOK {
		t.Fatal("malformed YAML should fail the step")
	}
	if !strings.Contains(res.Error.Message, "YAML") {
		t.Errorf("message = %q, want it to say what didn't parse", res.Error.Message)
	}
}

// Round trip: what Write YAML produces, Read YAML must read back.
func TestYAMLRoundTrip(t *testing.T) {
	rows := []map[string]any{
		{"name": "web", "port": 8080, "note": "has: a colon"},
		{"name": "db", "port": 5432, "note": "0123 keeps its zero"},
	}
	built := outText(t, runT(t, executeBuildYAML, rowsJob(rows, []string{"name", "port", "note"}, nil)))

	back := rowsOut(t, runT(t, executeParseYAML, yamlJob(built, nil)))
	if len(back) != 2 {
		t.Fatalf("round trip gave %d rows: %q", len(back), built)
	}
	if back[0]["name"] != "web" || back[1]["port"] != 5432 {
		t.Errorf("values didn't survive: %+v", back)
	}
	// The two values chosen to break naive serialisers: a colon needs
	// quoting, and a leading zero must not become an octal number or lose it.
	if back[0]["note"] != "has: a colon" {
		t.Errorf("a colon in a value broke the document: %#v", back[0]["note"])
	}
	if back[1]["note"] != "0123 keeps its zero" {
		t.Errorf("leading zero mangled: %#v", back[1]["note"])
	}
}

// Key order is the reason rows are encoded as yaml.Nodes: yaml.v3 sorts a Go
// map's keys, and a generated config file that reorders every line each run
// is unusable in a repo.
func TestBuildYAML_KeepsColumnOrder(t *testing.T) {
	rows := []map[string]any{{"zebra": 1, "apple": 2, "mango": 3}}
	out := outText(t, runT(t, executeBuildYAML, rowsJob(rows, []string{"zebra", "apple", "mango"}, map[string]any{"single": true})))

	zi, ai, mi := strings.Index(out, "zebra"), strings.Index(out, "apple"), strings.Index(out, "mango")
	if !(zi < ai && ai < mi) {
		t.Errorf("keys were reordered (alphabetised?):\n%s", out)
	}
}

// With no declared columns the order must still be STABLE run to run, or the
// same flow produces a different file each time.
func TestBuildYAML_StableOrderWithoutColumns(t *testing.T) {
	rows := []map[string]any{{"c": 1, "a": 2, "b": 3}}
	first := outText(t, runT(t, executeBuildYAML, rowsJob(rows, nil, map[string]any{"single": true})))
	for range 20 {
		if got := outText(t, runT(t, executeBuildYAML, rowsJob(rows, nil, map[string]any{"single": true}))); got != first {
			t.Fatalf("output varies between runs:\n%q\nvs\n%q", first, got)
		}
	}
}

func TestBuildYAML_SingleAndDocuments(t *testing.T) {
	rows := []map[string]any{{"a": 1}, {"a": 2}}

	list := outText(t, runT(t, executeBuildYAML, rowsJob(rows, []string{"a"}, nil)))
	if !strings.HasPrefix(strings.TrimSpace(list), "-") {
		t.Errorf("two rows should be a list:\n%s", list)
	}

	one := outText(t, runT(t, executeBuildYAML, rowsJob(rows[:1], []string{"a"}, map[string]any{"single": true})))
	if strings.HasPrefix(strings.TrimSpace(one), "-") {
		t.Errorf("'single' should emit the mapping itself:\n%s", one)
	}

	docs := outText(t, runT(t, executeBuildYAML, rowsJob(rows, []string{"a"}, map[string]any{"documents": true})))
	if strings.Count(docs, "---") != 2 {
		t.Errorf("want a document separator per row:\n%s", docs)
	}
	// And the result is a real stream Read YAML can read back.
	if got := rowsOut(t, runT(t, executeParseYAML, yamlJob(docs, nil))); len(got) != 2 {
		t.Errorf("the document stream didn't round trip: %+v", got)
	}
}

func TestBuildJSON_RoundTripsThroughReadJSON(t *testing.T) {
	src := []map[string]any{{"id": 1, "name": `Smith & Sons "Ltd"`}}
	out := outText(t, runT(t, executeBuildJSON, rowsJob(src, []string{"id", "name"}, nil)))

	var back []map[string]any
	if err := json.Unmarshal([]byte(out), &back); err != nil {
		t.Fatalf("output isn't valid JSON (%v): %s", err, out)
	}
	if back[0]["name"] != `Smith & Sons "Ltd"` {
		t.Errorf("quotes/ampersand didn't survive: %#v", back[0]["name"])
	}
	// HTML escaping must be OFF. Go's encoder escapes & < > as \\u0026 \\u003c
	// \\u003e by default — valid JSON, but a URL or a "Smith & Sons" arrives
	// mangled to anything reading the raw text, and an API body is not a web page.
	for _, esc := range []string{`\\u0026`, `\\u003c`, `\\u003e`} {
		if strings.Contains(out, esc) {
			t.Errorf("output carries HTML escaping (%s): %s", esc, out)
		}
	}
	if !strings.Contains(out, "&") {
		t.Errorf("the ampersand should be written literally: %s", out)
	}
}

func TestBuildJSON_SingleObject(t *testing.T) {
	rows := []map[string]any{{"a": 1}}
	arr := outText(t, runT(t, executeBuildJSON, rowsJob(rows, []string{"a"}, nil)))
	if !strings.HasPrefix(arr, "[") {
		t.Errorf("default should be an array: %s", arr)
	}
	obj := outText(t, runT(t, executeBuildJSON, rowsJob(rows, []string{"a"}, map[string]any{"single": true})))
	if !strings.HasPrefix(obj, "{") {
		t.Errorf("'single' should emit the object: %s", obj)
	}
	// Two rows can't collapse to one object, so 'single' is ignored rather
	// than silently dropping a row.
	two := outText(t, runT(t, executeBuildJSON, rowsJob([]map[string]any{{"a": 1}, {"a": 2}}, []string{"a"}, map[string]any{"single": true})))
	if !strings.HasPrefix(two, "[") {
		t.Errorf("'single' with two rows must stay an array: %s", two)
	}
}

// A column the rows lack becomes null rather than being omitted: an API
// validating a schema wants the key present.
func TestBuildJSON_MissingColumnIsNull(t *testing.T) {
	out := outText(t, runT(t, executeBuildJSON, rowsJob([]map[string]any{{"a": 1}}, nil, map[string]any{"columns": []any{"a", "b"}})))
	var back []map[string]any
	if err := json.Unmarshal([]byte(out), &back); err != nil {
		t.Fatal(err)
	}
	if v, present := back[0]["b"]; !present || v != nil {
		t.Errorf("missing column should be present and null, got %#v (present=%v)", v, present)
	}
}

func TestBuildXML_ShapeAndEscaping(t *testing.T) {
	rows := []map[string]any{{"id": 1, "customer": "Smith & Sons <Ltd>"}}
	out := outText(t, runT(t, executeBuildXML, rowsJob(rows, []string{"id", "customer"},
		map[string]any{"root": "invoices", "item": "invoice"})))

	if !strings.Contains(out, "<invoices>") || !strings.Contains(out, "<invoice>") {
		t.Errorf("root/item elements missing:\n%s", out)
	}
	// The ampersand and angle brackets must be escaped, or the file is
	// unparseable — the whole reason not to build XML in a template.
	if strings.Contains(out, "Smith & Sons") {
		t.Errorf("ampersand wasn't escaped:\n%s", out)
	}
	if !strings.Contains(out, "&amp;") {
		t.Errorf("want &amp; in the output:\n%s", out)
	}
	// And the result must actually parse.
	var probe struct {
		Invoices []struct {
			Customer string `xml:"customer"`
		} `xml:"invoice"`
	}
	if err := xml.Unmarshal([]byte(out), &probe); err != nil {
		t.Fatalf("output isn't valid XML (%v):\n%s", err, out)
	}
	if len(probe.Invoices) != 1 || probe.Invoices[0].Customer != "Smith & Sons <Ltd>" {
		t.Errorf("value didn't survive the round trip: %+v", probe.Invoices)
	}
}

// A column name that isn't a legal XML element is refused, not mangled:
// renaming it silently would put the wrong tag in someone's import file and
// the failure would surface at the far end as a schema mismatch.
func TestBuildXML_IllegalColumnNameIsRefused(t *testing.T) {
	for _, col := range []string{"total (SEK)", "2026", "xml-thing", ""} {
		rows := []map[string]any{{col: "v"}}
		res, err := executeBuildXML(t.Context(), rowsJob(rows, []string{col}, nil), nil)
		if err != nil {
			t.Fatalf("execute: %v", err)
		}
		if res.Status == core.StatusOK {
			t.Errorf("column %q is not a legal element name and should be refused", col)
			continue
		}
		if col != "" && !strings.Contains(res.Error.Message, col) {
			t.Errorf("error should name the offending column %q: %q", col, res.Error.Message)
		}
	}
}

func TestBuildXML_DeclarationToggle(t *testing.T) {
	rows := []map[string]any{{"a": "1"}}
	with := outText(t, runT(t, executeBuildXML, rowsJob(rows, []string{"a"}, nil)))
	if !strings.HasPrefix(with, "<?xml") {
		t.Errorf("declaration should be on by default: %q", with)
	}
	without := outText(t, runT(t, executeBuildXML, rowsJob(rows, []string{"a"}, map[string]any{"declaration": false})))
	if strings.HasPrefix(without, "<?xml") {
		t.Errorf("declaration should be omitted when turned off: %q", without)
	}
}
