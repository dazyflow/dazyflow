// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package transform

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"strings"
	"unicode"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/internal/params"
	"github.com/dazyflow/dazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "build_xml",
			Version:     "1.0",
			Label:       "Write XML",
			Subtitle:    "Rows into XML text",
			Icon:        "code",
			Category:    "transformation",
			Provider:    "internal",
			Tags:        []string{"transform", "xml", "export", "rows", "serialize", "edi"},
			Description: "Turn rows into XML text — the inverse of Read XML. Each row becomes an element, each field a child element, wrapped in a root you name. Useful for the older systems that still want XML: an EDI or bookkeeping import, a feed another service polls, a file dropped on an SFTP server. Escaping is handled, so an ampersand in a company name doesn't produce a file nothing can read. For a specific schema or a SOAP envelope, build the document with Fill a template instead — this makes a straightforward row-per-element file, not an arbitrary shape.",
			Summary:     "Serialize rows into a simple row-per-element XML document.",
			Examples: []core.ParamsExample{
				{
					Title:  "Rows to an XML file",
					Params: json.RawMessage(`{"root":"invoices","item":"invoice"}`),
					Notes:  "Gives <invoices><invoice><id>…</id></invoice>…</invoices>.",
				},
				{
					Title:  "Pretty-printed, specific fields",
					Params: json.RawMessage(`{"root":"orders","item":"order","indent":true,"columns":["id","total","customer"]}`),
				},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				{Port: "rows", Label: "Rows", Required: true, MIME: []string{"application/json"}},
			},
			Outputs: []core.Port{
				{Port: "out", Label: "XML", MIME: []string{"text/plain", "application/xml"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"root":{"type":"string","default":"rows","title":"Root element","examples":["invoices"],"description":"The element everything is wrapped in."},
					"item":{"type":"string","default":"row","title":"Row element","examples":["invoice"],"description":"The element each row becomes."},
					"indent":{"type":"boolean","default":false,"title":"Pretty-print","description":"Lay the XML out over several lines. Easier to read in a file; leave off to keep a payload small."},
					"declaration":{"type":"boolean","default":true,"title":"Include <?xml …?> line","description":"Write the XML declaration at the top. Most systems that read XML files expect it; some APIs reject it inside a request body."},
					"columns":{"type":"array","items":{"type":"string"},"title":"Columns","description":"Optional explicit field order/subset. When empty, the rows' own column order is used."}
				}
			}`),
			Idempotent: true,
		},
		Execute: executeBuildXML,
	})
}

func executeBuildXML(_ context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	rows, headers, errRes, ok := loadRowsAndHeaders(job)
	if !ok {
		return errRes, nil
	}
	cols, errRes, ok := chosenColumns(job, headers)
	if !ok {
		return errRes, nil
	}

	root, err := elementName(params.StringDefault(job.Params, "root", "rows"), "root")
	if err != nil {
		return params.Err(job, "bad_param", err.Error()), nil
	}
	item, err := elementName(params.StringDefault(job.Params, "item", "row"), "item")
	if err != nil {
		return params.Err(job, "bad_param", err.Error()), nil
	}

	var sb strings.Builder
	if params.BoolDefault(job.Params, "declaration", true) {
		sb.WriteString(xml.Header)
	}
	indent := params.BoolDefault(job.Params, "indent", false)

	nl, pad := "", ""
	if indent {
		nl, pad = "\n", "  "
	}
	sb.WriteString("<" + root + ">" + nl)
	for _, row := range rows {
		sb.WriteString(pad + "<" + item + ">" + nl)
		order := cols
		if len(order) == 0 {
			order = rowKeyOrder(row)
		}
		for _, c := range order {
			// A field name that isn't a legal element name is the one failure
			// worth naming rather than mangling: a column called "total (SEK)"
			// or "2026" would produce a document nothing can parse, and
			// silently renaming it puts the wrong tag in someone's import
			// file.
			tag, err := elementName(c, "column")
			if err != nil {
				return params.Err(job, "bad_param", fmt.Sprintf("column %q can't be an XML element name: %v — rename it with Choose & rename columns first, or list the columns you want", c, err)), nil
			}
			sb.WriteString(pad + pad + "<" + tag + ">")
			if err := xml.EscapeText(&sb, []byte(cellText(row[c]))); err != nil {
				return params.Err(job, "bad_input", "couldn't write that value as XML: "+err.Error()), nil
			}
			sb.WriteString("</" + tag + ">" + nl)
		}
		sb.WriteString(pad + "</" + item + ">" + nl)
	}
	sb.WriteString("</" + root + ">" + nl)

	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"out": {MIME: "application/xml", Inline: sb.String()},
		},
	}, nil
}

// cellText renders one field as element text. A nested value has no
// row-per-element representation, so it is written as its JSON — visible and
// recoverable, rather than Go's "map[...]" rendering which nothing can read
// back.
func cellText(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case map[string]any, []any:
		if b, err := json.Marshal(t); err == nil {
			return string(b)
		}
		return fmt.Sprint(t)
	default:
		return fmt.Sprint(t)
	}
}

// elementName validates a name against XML's rules for an element: a letter
// or underscore first, then letters, digits, hyphens, underscores or dots.
//
// Validated rather than sanitised on purpose. Sanitising would put a tag in
// someone's import file that neither they nor the receiving system asked for,
// and the failure would surface at the far end as a schema mismatch. Refusing
// says which column is the problem while the flow is being built.
func elementName(name, what string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("the %s element name is empty", what)
	}
	for i, r := range name {
		switch {
		case unicode.IsLetter(r) || r == '_':
			continue
		case i > 0 && (unicode.IsDigit(r) || r == '-' || r == '.'):
			continue
		case i == 0:
			return "", fmt.Errorf("%q must start with a letter or underscore", name)
		default:
			return "", fmt.Errorf("%q contains %q, which an XML name can't hold", name, string(r))
		}
	}
	// "xml" in any case is reserved by the spec for the XML machinery itself.
	if len(name) >= 3 && strings.EqualFold(name[:3], "xml") {
		return "", fmt.Errorf("%q starts with \"xml\", which the XML spec reserves", name)
	}
	return name, nil
}
