// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package transform

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/PuerkitoBio/goquery"
	"github.com/andybalholm/cascadia"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/internal/params"
	"github.com/dazyflow/dazyflow/engine"
)

// A field selector is "css@attribute", either half optional:
//
//	.price          the trimmed text of the first .price
//	a@href          the href of the first link
//	@data-id        an attribute of the element itself (no css part)
//	@html           the element's inner HTML
//
// Both halves optional is what lets one syntax serve a page of repeated cards
// (where the css part digs inside each card) and a single document (where it
// digs from the root) without the author learning two.
const (
	attrText = "text"
	attrHTML = "html"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:       "parse_html",
			Version:  "1.0",
			Label:    "Read a web page",
			Subtitle: "Pick values out of HTML",
			Icon:     "file-code",
			Category: "transformation",
			Provider: "internal",
			Tags: []string{
				"transform", "html", "parse", "scrape", "scraping", "selector", "css",
				"web", "extract", "rows", "table",
				// web_watch owns "page" through its label; without these,
				// "scrape a product page" found the page WATCHER first.
				"page", "web page", "webpage", "product page", "price",
			},
			Description: "Pull the parts you want out of a web page. Fetch the page with the Web request step, " +
				"wire the response in here, and name each value you want with a CSS selector — the same thing " +
				"you'd type in a browser's inspector.\n\n" +
				"Each field is written `selector@attribute`, and both halves are optional: \".price\" takes the " +
				"text of the first .price, \"a@href\" takes a link's address, \"img@src\" an image's, \"@data-id\" " +
				"an attribute of the element itself, and \"@html\" its inner HTML. A field that matches nothing " +
				"comes out empty rather than failing, so one missing price does not stop the flow.\n\n" +
				"Leave 'Where each row is' empty and you get one record: the fields you named, read from the whole " +
				"page. Fill it in with the selector for the repeated thing — a product card, a table row, an " +
				"article — and you get one row per match instead, with each field read inside that row. That is " +
				"the shape the row steps want, so the result goes straight into Choose & rename columns, Write " +
				"CSV, Sheets or a database.\n\n" +
				"Set 'Page address' and any link or image you pull comes out as a full address rather than the " +
				"half of one the page carries. Every value is text, like CSV.",
			Summary: "Scrape a web page: extract fields from HTML with CSS selectors, as one record or one row per match.",
			Examples: []core.ParamsExample{
				{
					Title:  "Read one value off a page",
					Params: json.RawMessage(`{"fields":{"price":".product-price","title":"h1"}}`),
					Notes:  "No row selector: one record, on both 'value' and as a single row.",
				},
				{
					Title:  "One row per product card",
					Params: json.RawMessage(`{"row_selector":".product","fields":{"name":"h3","price":".price","link":"a@href"},"base_url":"https://shop.example.com/"}`),
					Notes:  "Each .product becomes a row; 'link' comes out as a full address thanks to the page address.",
				},
				{
					Title:  "Read a table off a page",
					Params: json.RawMessage(`{"row_selector":"table tbody tr","fields":{"date":"td:nth-child(1)","amount":"td:nth-child(2)","status":"td:nth-child(3)"}}`),
					Notes:  "One row per <tr>, with the cells picked out by position.",
				},
				{
					Title:  "Every link on the page",
					Params: json.RawMessage(`{"row_selector":"a","fields":{"text":"","href":"@href"}}`),
					Notes:  "An empty selector means the row element itself, so 'text' is the link's own text.",
				},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				{Port: "in", Label: "HTML", Required: true, MIME: []string{"text/html", "text/plain"}},
			},
			Outputs: []core.Port{
				{Port: "rows", Label: "Rows", MIME: []string{"application/json"}},
				{Port: "value", Label: "Value", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"fields":{"type":"object","title":"Values to pick out","additionalProperties":{"type":"string"},"description":"One entry per value you want: the name it comes out under, and the CSS selector that finds it. Write \"selector@attribute\" to take an attribute instead of the text — \"a@href\", \"img@src\", \"@data-id\" for the row element's own attribute, \"@html\" for its inner HTML. Leave the selector empty to mean the row element itself. Nothing matched comes out as empty text."},
					"row_selector":{"type":"string","title":"Where each row is","description":"The selector for the thing that repeats — \".product\", \"table tbody tr\", \"article\". You get one row per match, with every field read inside it. Leave it empty to read the fields once from the whole page."},
					"base_url":{"type":"string","title":"Page address","description":"The address the HTML came from. With it set, a link or image you pull comes out as a full address instead of the relative half the page carries."}
				}
			}`),
			Idempotent: true,
		},
		Execute: executeParseHTML,
	})
}

func executeParseHTML(_ context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	ref, ok := job.Input["in"]
	if !ok {
		return params.Err(job, "missing_input", "input port 'in' is required"), nil
	}
	var text string
	switch v := ref.Inline.(type) {
	case string:
		text = v
	case []byte:
		text = string(v)
	default:
		return params.Err(job, "bad_input", fmt.Sprintf("expected HTML text, got %T", ref.Inline)), nil
	}
	if strings.TrimSpace(text) == "" {
		return params.Err(job, "bad_input", "input 'in' is empty"), nil
	}

	fields, err := paramFields(job.Params)
	if err != nil {
		return params.Err(job, "bad_param", err.Error()), nil
	}
	rowSelector := strings.TrimSpace(params.StringDefault(job.Params, "row_selector", ""))
	if rowSelector != "" {
		if _, err := cascadia.Compile(rowSelector); err != nil {
			return params.Err(job, "bad_param", fmt.Sprintf(
				"'Where each row is' is not a selector I can read (%s): %v", rowSelector, err)), nil
		}
	}
	var base *url.URL
	if raw := strings.TrimSpace(params.StringDefault(job.Params, "base_url", "")); raw != "" {
		base, err = url.Parse(raw)
		if err != nil {
			return params.Err(job, "bad_param", fmt.Sprintf("'Page address' is not an address: %v", err)), nil
		}
	}

	// goquery never fails on bad markup — the HTML parser is defined to
	// recover from anything a browser would — so an error here is an I/O one.
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(text))
	if err != nil {
		return params.Err(job, "bad_input", "could not read the HTML: "+err.Error()), nil
	}

	var rowsOut []map[string]any
	if rowSelector == "" {
		rowsOut = []map[string]any{extract(doc.Selection, fields, base)}
	} else {
		matches := doc.Find(rowSelector)
		if err := capRows(matches.Length()); err != nil {
			return params.Err(job, "too_large", err.Error()), nil
		}
		rowsOut = make([]map[string]any, 0, matches.Length())
		matches.Each(func(_ int, sel *goquery.Selection) {
			rowsOut = append(rowsOut, extract(sel, fields, base))
		})
	}

	// value mirrors the family: the record itself when there is one, the whole
	// list when the page held many.
	var value any = rowsOut
	if rowSelector == "" {
		value = rowsOut[0]
	}

	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"rows":  {MIME: "application/json", Inline: rowsOut, Headers: deriveHeaders(rowsOut)},
			"value": {MIME: "application/json", Inline: value},
		},
	}, nil
}

type htmlField struct {
	name string
	css  string
	attr string
}

// paramFields reads and validates the field map up front, so a mistyped
// selector is a message naming the field rather than a column of silent
// blanks. goquery's Find swallows a selector it cannot compile and matches
// nothing, which would otherwise look exactly like a page that changed shape.
func paramFields(p map[string]any) ([]htmlField, error) {
	raw, ok := p["fields"]
	if !ok || raw == nil {
		return nil, nil
	}
	obj, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("param 'fields': expected one entry per value to pick out, got %T", raw)
	}
	names := make([]string, 0, len(obj))
	for name := range obj {
		names = append(names, name)
	}
	sort.Strings(names) // a stable column order, whatever order the map arrived in

	out := make([]htmlField, 0, len(names))
	for _, name := range names {
		spec, ok := obj[name].(string)
		if !ok {
			return nil, fmt.Errorf("field %q: expected a selector, got %T", name, obj[name])
		}
		css, attr := splitFieldSpec(spec)
		if css != "" {
			if _, err := cascadia.Compile(css); err != nil {
				return nil, fmt.Errorf("field %q is not a selector I can read (%s): %v", name, css, err)
			}
		}
		out = append(out, htmlField{name: name, css: css, attr: attr})
	}
	return out, nil
}

// splitFieldSpec cuts "css@attr" at the LAST @, so a selector that carries one
// of its own — [href@rel] is legal CSS — keeps it.
func splitFieldSpec(spec string) (css, attr string) {
	spec = strings.TrimSpace(spec)
	at := strings.LastIndexByte(spec, '@')
	if at < 0 {
		return spec, attrText
	}
	css = strings.TrimSpace(spec[:at])
	attr = strings.TrimSpace(spec[at+1:])
	if attr == "" {
		attr = attrText
	}
	return css, attr
}

// extract reads every field out of one scope — a matched row, or the whole
// document when there is no row selector.
func extract(scope *goquery.Selection, fields []htmlField, base *url.URL) map[string]any {
	if len(fields) == 0 {
		// Nothing named: the row's own text is the only thing worth guessing at.
		return map[string]any{"text": squish(scope.Text())}
	}
	row := make(map[string]any, len(fields))
	for _, f := range fields {
		target := scope
		if f.css != "" {
			target = scope.Find(f.css).First()
		}
		row[f.name] = readValue(target, f.attr, base)
	}
	return row
}

func readValue(sel *goquery.Selection, attr string, base *url.URL) string {
	if sel.Length() == 0 {
		return ""
	}
	switch attr {
	case attrText:
		return squish(sel.Text())
	case attrHTML:
		html, err := sel.Html()
		if err != nil {
			return ""
		}
		return strings.TrimSpace(html)
	}
	value, ok := sel.Attr(attr)
	if !ok {
		return ""
	}
	value = strings.TrimSpace(value)
	if base == nil || value == "" {
		return value
	}
	// Only the attributes that actually hold addresses: resolving, say, a
	// class name against a base URL would be nonsense.
	switch attr {
	case "href", "src", "action", "poster", "data-src":
		if ref, err := url.Parse(value); err == nil {
			return base.ResolveReference(ref).String()
		}
	}
	return value
}

// squish collapses the whitespace HTML is written with — newlines and the
// indentation around a tag — into single spaces, so a value reads as the page
// shows it rather than as the source file is laid out.
func squish(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
