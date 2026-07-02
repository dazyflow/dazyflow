// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package transform

import (
	"strings"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
)

func runParseXML(t *testing.T, in any, params map[string]any) core.Result {
	t.Helper()
	if params == nil {
		params = map[string]any{}
	}
	res, err := executeParseXML(t.Context(), core.Job{
		ID:     "test",
		Params: params,
		Input:  map[string]core.Ref{"in": {Inline: in}},
	}, nil)
	if err != nil {
		t.Fatalf("executeParseXML returned error: %v", err)
	}
	return res
}

func xmlValue(t *testing.T, res core.Result) any {
	t.Helper()
	if res.Status != core.StatusOK {
		t.Fatalf("status = %v, error = %+v", res.Status, res.Error)
	}
	return res.Output["value"].Inline
}

func TestParseXML_TextAndAttributes(t *testing.T) {
	res := runParseXML(t, `<book id="7" lang="en"><title>Go</title></book>`, nil)
	m, ok := xmlValue(t, res).(map[string]any)
	if !ok {
		t.Fatalf("value is %T, want map", xmlValue(t, res))
	}
	if m["@id"] != "7" || m["@lang"] != "en" {
		t.Errorf("attributes = %v/%v, want 7/en", m["@id"], m["@lang"])
	}
	if m["title"] != "Go" {
		t.Errorf("title = %v, want Go", m["title"])
	}
}

func TestParseXML_TextOnlyElementIsString(t *testing.T) {
	res := runParseXML(t, `<name>Ada</name>`, nil)
	if v := xmlValue(t, res); v != "Ada" {
		t.Errorf("value = %v (%T), want \"Ada\"", v, v)
	}
}

func TestParseXML_MixedTextUsesHashText(t *testing.T) {
	res := runParseXML(t, `<p class="lead">hello</p>`, nil)
	m := xmlValue(t, res).(map[string]any)
	if m["@class"] != "lead" || m["#text"] != "hello" {
		t.Errorf("value = %+v, want @class=lead #text=hello", m)
	}
}

func TestParseXML_RepeatedChildrenBecomeList(t *testing.T) {
	res := runParseXML(t, `<tags><tag>a</tag><tag>b</tag><tag>c</tag></tags>`, nil)
	m := xmlValue(t, res).(map[string]any)
	list, ok := m["tag"].([]any)
	if !ok || len(list) != 3 {
		t.Fatalf("tag = %#v, want 3-element list", m["tag"])
	}
	if list[0] != "a" || list[2] != "c" {
		t.Errorf("tags = %v", list)
	}
}

func TestParseXML_PathToRowsRSSStyle(t *testing.T) {
	in := `<rss><channel>
		<item><id>1</id><title>First</title></item>
		<item><id>2</id><title>Second</title></item>
	</channel></rss>`
	res := runParseXML(t, in, map[string]any{"path": "channel.item"})
	if res.Status != core.StatusOK {
		t.Fatalf("status = %v, err = %+v", res.Status, res.Error)
	}
	rows, ok := res.Output["rows"].Inline.([]map[string]any)
	if !ok || len(rows) != 2 {
		t.Fatalf("rows = %#v, want 2", res.Output["rows"].Inline)
	}
	if rows[0]["id"] != "1" || rows[1]["title"] != "Second" {
		t.Errorf("rows = %+v", rows)
	}
}

func TestParseXML_SingleObjectBecomesOneRow(t *testing.T) {
	res := runParseXML(t, `<item><sku>abc</sku><qty>3</qty></item>`, nil)
	rows := res.Output["rows"].Inline.([]map[string]any)
	if len(rows) != 1 || rows[0]["sku"] != "abc" {
		t.Fatalf("rows = %+v, want one row sku=abc", rows)
	}
}

func TestParseXML_NamespaceStrippedToLocal(t *testing.T) {
	in := `<root xmlns:d="http://example.com/d"><d:price>9.99</d:price></root>`
	res := runParseXML(t, in, nil)
	m := xmlValue(t, res).(map[string]any)
	if m["price"] != "9.99" {
		t.Errorf("value = %+v, want price=9.99 (namespace stripped)", m)
	}
	if _, hasXmlns := m["@d"]; hasXmlns {
		t.Errorf("namespace declaration should be dropped, got %+v", m)
	}
}

func TestParseXML_ScalarPathYieldsNoRowsButValue(t *testing.T) {
	res := runParseXML(t, `<doc><title>Hi</title></doc>`, map[string]any{"path": "title"})
	if res.Status != core.StatusOK {
		t.Fatalf("status = %v", res.Status)
	}
	if rows := res.Output["rows"].Inline.([]map[string]any); len(rows) != 0 {
		t.Errorf("scalar path should give 0 rows, got %d", len(rows))
	}
	if v := res.Output["value"].Inline; v != "Hi" {
		t.Errorf("value = %v, want Hi", v)
	}
}

func TestParseXML_Malformed(t *testing.T) {
	res := runParseXML(t, `<a><b></a>`, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_input" {
		t.Errorf("status/code = %v/%v, want error/bad_input", res.Status, res.Error)
	}
}

func TestParseXML_Empty(t *testing.T) {
	res := runParseXML(t, "   ", nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_input" {
		t.Errorf("status/code = %v/%v, want error/bad_input", res.Status, res.Error)
	}
}

func TestParseXML_NonString(t *testing.T) {
	res := runParseXML(t, 42, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_input" {
		t.Errorf("status/code = %v/%v, want error/bad_input", res.Status, res.Error)
	}
}

func TestParseXML_BadPath(t *testing.T) {
	res := runParseXML(t, `<a><b>x</b></a>`, map[string]any{"path": "nope"})
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("status/code = %v/%v, want error/bad_param", res.Status, res.Error)
	}
}

func TestParseXML_DepthGuard(t *testing.T) {
	// Build a document nested past the limit; it must fail cleanly, not crash.
	deep := strings.Repeat("<a>", maxXMLDepth+5) + "x" + strings.Repeat("</a>", maxXMLDepth+5)
	res := runParseXML(t, deep, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_input" {
		t.Errorf("status/code = %v/%v, want error/bad_input", res.Status, res.Error)
	}
}
