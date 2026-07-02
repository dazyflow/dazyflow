// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package transform

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"strings"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/engine"
)

// maxXMLDepth bounds element nesting so a pathological (or malicious) document
// can't drive unbounded recursion and overflow the stack. Far deeper than any
// real config/API payload; a document past it fails cleanly.
const maxXMLDepth = 256

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "parse_xml",
			Version:     "1.0",
			Label:       "Read XML",
			Subtitle:    "XML text into rows",
			Icon:        "code-xml",
			Category:    "transformation",
			Provider:    "internal",
			Tags:        []string{"transform", "xml", "parse", "rows", "etl", "rss", "soap"},
			Description: "Turn XML text into rows or a structured value. Feed it an HTTP response, a downloaded file, or an RSS/SOAP payload and it parses into the rows + headers shape the transform family, Sheets, and the DB drops consume. Conversion: an element's attributes become @-prefixed keys (id=\"7\" → \"@id\":\"7\"), child elements become keys (repeated children become a list), and text content is the element's value (or \"#text\" alongside attributes/children). The document's root wrapper is unwrapped, so 'path' is relative to its children — point it at the repeated element to get one row each (e.g. \"channel.item\" for RSS). Namespaces are stripped to their local name. All values are text, like CSV.",
			Summary:     "Parse XML text into rows + a structured value; 'path' digs to a repeated element.",
			Examples: []core.ParamsExample{
				{
					Title:  "Parse XML into a structured value",
					Params: json.RawMessage(`{}`),
					Notes:  "Wire the XML text into 'in'. Read the tree on 'value'; a single top-level record also comes out as one row.",
				},
				{
					Title:  "Pull RSS items into rows",
					Params: json.RawMessage(`{"path":"channel.item"}`),
					Notes:  "For <rss><channel><item>…</item><item>…</item></channel></rss>, path digs to the repeated <item> and emits one row each.",
				},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				{Port: "in", Label: "XML", Required: true, MIME: []string{"application/xml", "text/xml", "text/plain"}},
			},
			Outputs: []core.Port{
				{Port: "rows", Label: "Rows", MIME: []string{"application/json"}},
				{Port: "value", Label: "Value", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"path":{"type":"string","description":"Optional dot-path into the parsed document (relative to the root's children) before rows are built, e.g. \"channel.item\". Each segment indexes a child element name."}
				}
			}`),
			Idempotent: true,
		},
		Execute: executeParseXML,
	})
}

// executeParseXML parses the 'in' text as XML into a generic value, optionally
// descends a dot-path, and emits both a `value` (the parsed subtree) and
// `rows` (when that subtree is an object or an array of objects). Mirrors
// parse_json's outputs and reuses its digPath / rowsFromValue helpers.
func executeParseXML(_ context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	ref, ok := job.Input["in"]
	if !ok {
		return errResult(job, "missing_input", "input port 'in' is required"), nil
	}
	text, ok := ref.Inline.(string)
	if !ok {
		return errResult(job, "bad_input", fmt.Sprintf("expected XML text, got %T", ref.Inline)), nil
	}
	if strings.TrimSpace(text) == "" {
		return errResult(job, "bad_input", "input 'in' is empty"), nil
	}

	value, err := xmlToValue(text)
	if err != nil {
		return errResult(job, "bad_input", "input is not valid XML: "+err.Error()), nil
	}

	if pathRaw, ok := job.Params["path"].(string); ok && pathRaw != "" {
		value, err = digPath(value, pathRaw)
		if err != nil {
			return errResult(job, "bad_param", err.Error()), nil
		}
	}

	// Rows are best-effort: an object or array-of-objects becomes rows; a
	// scalar/array-of-scalars subtree has no row shape and yields no rows
	// (the caller reads 'value' instead), rather than failing the whole job.
	var rowsOut []map[string]any
	if r, err := rowsFromValue(value); err == nil {
		rowsOut = r
	} else {
		rowsOut = []map[string]any{}
	}
	if err := capRows(len(rowsOut)); err != nil {
		return errResult(job, "too_large", err.Error()), nil
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

// xmlToValue parses an XML document into a generic Go value by converting its
// root element and unwrapping the root name (so the result is the root's
// content). See the manifest for the attribute/child/text convention.
func xmlToValue(s string) (any, error) {
	dec := xml.NewDecoder(strings.NewReader(s))
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return nil, fmt.Errorf("no XML element found")
		}
		if err != nil {
			return nil, err
		}
		if start, ok := tok.(xml.StartElement); ok {
			return decodeElement(dec, start, 0)
		}
		// Skip the XML declaration, comments, DOCTYPE, leading whitespace.
	}
}

// decodeElement consumes tokens up to start's matching end tag and returns the
// element's converted value: a bare string for a text-only element with no
// attributes, otherwise a map of @attributes, child elements (repeats folded
// into a list), and an optional "#text".
func decodeElement(dec *xml.Decoder, start xml.StartElement, depth int) (any, error) {
	if depth > maxXMLDepth {
		return nil, fmt.Errorf("XML nested deeper than %d levels", maxXMLDepth)
	}

	node := map[string]any{}
	for _, a := range start.Attr {
		if a.Name.Local == "xmlns" || a.Name.Space == "xmlns" {
			continue // drop namespace declarations
		}
		node["@"+a.Name.Local] = a.Value
	}

	var text strings.Builder
	for {
		tok, err := dec.Token()
		if err != nil {
			return nil, err // includes an unexpected EOF (unclosed tag)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			val, err := decodeElement(dec, t, depth+1)
			if err != nil {
				return nil, err
			}
			addChild(node, t.Name.Local, val)
		case xml.CharData:
			text.Write(t)
		case xml.EndElement:
			trimmed := strings.TrimSpace(text.String())
			if len(node) == 0 {
				return trimmed, nil // pure-text (or empty) element → its text
			}
			if trimmed != "" {
				node["#text"] = trimmed
			}
			return node, nil
		}
	}
}

// addChild sets key=val, folding a repeated element name into a growing list
// so <item/><item/> becomes a two-element array under "item".
func addChild(node map[string]any, key string, val any) {
	existing, ok := node[key]
	if !ok {
		node[key] = val
		return
	}
	if arr, ok := existing.([]any); ok {
		node[key] = append(arr, val)
		return
	}
	node[key] = []any{existing, val}
}
