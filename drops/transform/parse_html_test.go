// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package transform

import (
	"strings"
	"testing"

	"github.com/dazyflow/dazyflow/core"
)

const shopPage = `
<html><body>
  <h1>  Coffee
     beans </h1>
  <ul>
    <li class="product" data-id="7">
      <h3>Ethiopia</h3>
      <span class="price">129 kr</span>
      <a href="/p/ethiopia">buy <b>now</b></a>
    </li>
    <li class="product" data-id="8">
      <h3>Colombia</h3>
      <a href="https://other.example.com/p/colombia">buy</a>
    </li>
  </ul>
</body></html>`

func runHTML(t *testing.T, p map[string]any, in any) core.Result {
	t.Helper()
	job := core.Job{ID: "t", Params: p}
	if in != nil {
		job.Input = map[string]core.Ref{"in": {Inline: in}}
	}
	res, err := executeParseHTML(t.Context(), job, nil)
	if err != nil {
		t.Fatalf("executeParseHTML: %v", err)
	}
	return res
}

func htmlRows(t *testing.T, res core.Result) []map[string]any {
	t.Helper()
	if res.Status != core.StatusOK {
		t.Fatalf("status=%v err=%+v", res.Status, res.Error)
	}
	rows, ok := res.Output["rows"].Inline.([]map[string]any)
	if !ok {
		t.Fatalf("rows is %T, want a row list", res.Output["rows"].Inline)
	}
	return rows
}

func htmlFails(t *testing.T, res core.Result, code, contains string) {
	t.Helper()
	if res.Status != core.StatusError {
		t.Fatalf("status=%v, want an error", res.Status)
	}
	if res.Error.Code != code {
		t.Fatalf("code=%q (%s), want %q", res.Error.Code, res.Error.Message, code)
	}
	if contains != "" && !strings.Contains(res.Error.Message, contains) {
		t.Errorf("message = %q, want it to mention %q", res.Error.Message, contains)
	}
}

// No row selector: the fields are read once, from the whole page.
func TestParseHTML_OneRecordFromThePage(t *testing.T) {
	res := runHTML(t, map[string]any{
		"fields": map[string]any{"title": "h1", "first_price": ".price"},
	}, shopPage)

	rows := htmlRows(t, res)
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	// The heading is written across three source lines; a value should read
	// as the page shows it.
	if rows[0]["title"] != "Coffee beans" {
		t.Errorf("title = %q, want %q", rows[0]["title"], "Coffee beans")
	}
	if rows[0]["first_price"] != "129 kr" {
		t.Errorf("first_price = %q", rows[0]["first_price"])
	}
	// value is the record itself, not a list of one.
	if _, isObject := res.Output["value"].Inline.(map[string]any); !isObject {
		t.Errorf("value is %T, want the record", res.Output["value"].Inline)
	}
}

func TestParseHTML_OneRowPerMatch(t *testing.T) {
	res := runHTML(t, map[string]any{
		"row_selector": ".product",
		"fields": map[string]any{
			"name":  "h3",
			"price": ".price",
			"link":  "a@href",
			"id":    "@data-id",
		},
		"base_url": "https://shop.example.com/catalogue/",
	}, shopPage)

	rows := htmlRows(t, res)
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}
	if rows[0]["name"] != "Ethiopia" || rows[0]["id"] != "7" {
		t.Errorf("first row = %v", rows[0])
	}
	// A relative href resolves against the page address...
	if rows[0]["link"] != "https://shop.example.com/p/ethiopia" {
		t.Errorf("link = %q, want it resolved against the base", rows[0]["link"])
	}
	// ...and one that is already absolute is left alone.
	if rows[1]["link"] != "https://other.example.com/p/colombia" {
		t.Errorf("absolute link = %q, want it untouched", rows[1]["link"])
	}
	// A field that matched nothing is empty, not missing: the column has to
	// survive into a CSV or a spreadsheet.
	if got, present := rows[1]["price"]; !present || got != "" {
		t.Errorf("missing price = %#v (present=%v), want an empty string", got, present)
	}
	if headers := res.Output["rows"].Headers; len(headers) != 4 {
		t.Errorf("headers = %v, want one per field", headers)
	}
}

// An empty selector means the matched element itself, which is how you read a
// row's own text or its own attribute.
func TestParseHTML_EmptySelectorMeansTheRowItself(t *testing.T) {
	rows := htmlRows(t, runHTML(t, map[string]any{
		"row_selector": ".product a",
		"fields":       map[string]any{"text": "", "href": "@href"},
	}, shopPage))

	if len(rows) != 2 || rows[0]["text"] != "buy now" {
		t.Fatalf("rows = %v", rows)
	}
	if rows[0]["href"] != "/p/ethiopia" {
		t.Errorf("href = %q — with no page address it stays as written", rows[0]["href"])
	}
}

func TestParseHTML_InnerHTML(t *testing.T) {
	rows := htmlRows(t, runHTML(t, map[string]any{
		"row_selector": ".product a",
		"fields":       map[string]any{"markup": "@html"},
	}, shopPage))
	if rows[0]["markup"] != "buy <b>now</b>" {
		t.Errorf("markup = %q", rows[0]["markup"])
	}
}

func TestParseHTML_ReadsATable(t *testing.T) {
	page := `<table><thead><tr><th>Date</th><th>Amount</th></tr></thead>
		<tbody>
			<tr><td>2026-01-02</td><td>120</td></tr>
			<tr><td>2026-01-03</td><td>240</td></tr>
		</tbody></table>`

	rows := htmlRows(t, runHTML(t, map[string]any{
		"row_selector": "table tbody tr",
		"fields":       map[string]any{"date": "td:nth-child(1)", "amount": "td:nth-child(2)"},
	}, page))

	if len(rows) != 2 || rows[1]["date"] != "2026-01-03" || rows[1]["amount"] != "240" {
		t.Errorf("rows = %v", rows)
	}
}

// Naming no fields is not an error: the row's own text is the one value worth
// guessing at.
func TestParseHTML_NoFieldsGivesTheText(t *testing.T) {
	rows := htmlRows(t, runHTML(t, map[string]any{"row_selector": "h3"}, shopPage))
	if len(rows) != 2 || rows[0]["text"] != "Ethiopia" {
		t.Errorf("rows = %v", rows)
	}
}

// goquery matches nothing for a selector it cannot compile, which looks
// exactly like a page that changed shape. Both are checked up front so the
// author is told which field is wrong.
func TestParseHTML_NamesABadSelector(t *testing.T) {
	htmlFails(t, runHTML(t, map[string]any{
		"fields": map[string]any{"price": ".product:::"},
	}, shopPage), "bad_param", `"price"`)

	htmlFails(t, runHTML(t, map[string]any{
		"row_selector": "[[[",
	}, shopPage), "bad_param", "Where each row is")

	htmlFails(t, runHTML(t, map[string]any{
		"fields": map[string]any{"price": 7},
	}, shopPage), "bad_param", `"price"`)
}

func TestParseHTML_BadBaseURL(t *testing.T) {
	htmlFails(t, runHTML(t, map[string]any{
		"fields":   map[string]any{"t": "h1"},
		"base_url": "://nope",
	}, shopPage), "bad_param", "Page address")
}

// A browser recovers from broken markup and so does the parser behind this
// step; a page missing its closing tags still yields its values.
func TestParseHTML_SurvivesBrokenMarkup(t *testing.T) {
	rows := htmlRows(t, runHTML(t, map[string]any{
		"row_selector": "li",
		"fields":       map[string]any{"name": "b"},
	}, `<ul><li><b>one<li><b>two`))
	if len(rows) != 2 || rows[1]["name"] != "two" {
		t.Errorf("rows = %v", rows)
	}
}

func TestParseHTML_RejectsBadInput(t *testing.T) {
	htmlFails(t, runHTML(t, map[string]any{}, nil), "missing_input", "")
	htmlFails(t, runHTML(t, map[string]any{}, "   "), "bad_input", "empty")
	htmlFails(t, runHTML(t, map[string]any{}, 42), "bad_input", "int")
}

func TestParseHTML_TakesBytes(t *testing.T) {
	rows := htmlRows(t, runHTML(t, map[string]any{"fields": map[string]any{"t": "h1"}}, []byte(shopPage)))
	if rows[0]["t"] != "Coffee beans" {
		t.Errorf("rows = %v", rows)
	}
}

func TestSplitFieldSpec(t *testing.T) {
	for spec, want := range map[string][2]string{
		".price":        {".price", attrText},
		"a@href":        {"a", "href"},
		"@data-id":      {"", "data-id"},
		"@html":         {"", attrHTML},
		"":              {"", attrText},
		"a@":            {"a", attrText},
		"  h1  ":        {"h1", attrText},
		"a[href]@title": {"a[href]", "title"},
	} {
		css, attr := splitFieldSpec(spec)
		if css != want[0] || attr != want[1] {
			t.Errorf("%q → (%q, %q), want (%q, %q)", spec, css, attr, want[0], want[1])
		}
	}
}
