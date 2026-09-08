// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package transform

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/internal/params"
	"github.com/dazyflow/dazyflow/engine"
)

// maxYAMLDocuments caps a multi-document stream, so a pathological file can't
// turn one step into ten thousand rows. Well above any real manifest.
const maxYAMLDocuments = 1000

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "parse_yaml",
			Version:     "1.0",
			Label:       "Read YAML",
			Subtitle:    "YAML text into rows",
			Icon:        "file-code",
			Category:    "transformation",
			Provider:    "internal",
			Tags:        []string{"transform", "yaml", "parse", "rows", "config", "etl"},
			Description: "Turn YAML text into rows, the same way Read JSON does. Feed it a config file, a Kubernetes manifest, a docker-compose file, an HTTP response — anything that speaks YAML — and it parses into the rows + headers shape Sheets, Excel, Postgres and the transform family consume. Use 'path' to reach a list nested inside the document (e.g. \"spec.containers\"), or to read a single field out — point it at one value and 'Rows' is empty while 'Value' carries it. A file holding several documents separated by \"---\" becomes one row per document, which is how a manifest bundle is usually shaped.",
			Summary:     "Parse YAML text into rows + a structured value; 'path' digs into the document.",
			Examples: []core.ParamsExample{
				{
					Title:  "Read a config file into rows",
					Params: json.RawMessage(`{}`),
					Notes:  "Connect Read file's output into 'in'. A mapping becomes one row; a list of mappings becomes one row each.",
				},
				{
					Title:  "Dig into a nested list",
					Params: json.RawMessage(`{"path":"spec.template.spec.containers"}`),
					Notes:  "For a Kubernetes deployment this gives one row per container.",
				},
				{
					Title:  "Read one setting out",
					Params: json.RawMessage(`{"path":"version"}`),
					Notes:  "A single value is not a table, so 'Rows' comes out empty — take the answer from 'Value'.",
				},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				{Port: "in", Label: "YAML", Required: true},
			},
			Outputs: []core.Port{
				{Port: "rows", Label: "Rows", MIME: []string{"application/json"}},
				{Port: "value", Label: "Value", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"path": {"type":"string","title":"Path","examples":["spec.containers","version"],"description":"Optional dot-path into the parsed YAML before rows are built, e.g. \"spec.containers\". Each segment indexes a mapping key. Point it at a mapping or a list of mappings to get rows; point it at a single value (a version, a name) and 'Rows' comes out empty while 'Value' carries what you asked for."},
					"documents": {"type":"string","title":"When there are several documents","enum":["all","first"],"enumNames":["One row per document","Only the first"],"default":"all","description":"How to treat a file with \"---\" separators. \"One row per document\" suits a manifest bundle; \"Only the first\" suits a file whose later documents are overrides you don't want."}
				}
			}`),
			Idempotent: true,
		},
		Execute: executeParseYAML,
	})
}

func executeParseYAML(_ context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	ref, ok := job.Input["in"]
	if !ok {
		return params.Err(job, "missing_input", "input port 'in' is required"), nil
	}
	if ref.Inline == nil {
		return params.Err(job, "bad_input", "input 'in' is empty"), nil
	}

	var docs []any
	if s, isString := ref.Inline.(string); isString {
		parsed, err := parseYAMLDocuments(s)
		if err != nil {
			return params.Err(job, "bad_input", err.Error()), nil
		}
		docs = parsed
	} else {
		docs = []any{ref.Inline}
	}
	if len(docs) == 0 {
		return params.Err(job, "bad_input", "no YAML documents found in the input"), nil
	}

	if params.StringDefault(job.Params, "documents", "all") == "first" {
		docs = docs[:1]
	}

	var value any = docs[0]
	if len(docs) > 1 {
		value = docs
	}

	pathRaw, _ := job.Params["path"].(string)
	dug := pathRaw != ""

	var rows []map[string]any
	if len(docs) > 1 && !dug {
		// A bundle: each document is a row. Anything not mapping-shaped is
		// named rather than silently skipped — a stream of scalars is a
		// mistake worth reporting.
		for i, d := range docs {
			m, ok := coerceMap(d)
			if !ok {
				return params.Err(job, "not_tabular", fmt.Sprintf("document %d is %T, not a mapping; a YAML stream must hold mappings to become rows", i+1, d)), nil
			}
			rows = append(rows, m)
		}
		return yamlResult(job, rows, value), nil
	}

	target := docs[0]
	if dug {
		var err error
		target, err = digPath(normalizeYAML(target), pathRaw)
		if err != nil {
			return params.Err(job, "bad_param", err.Error()), nil
		}
		value = target
	}
	built, err := rowsFromValue(normalizeYAML(target))
	if err != nil {
		if !dug {
			return params.Err(job, "not_tabular", err.Error()), nil
		}
		built = []map[string]any{}
	}
	return yamlResult(job, built, normalizeYAML(value)), nil
}

func yamlResult(job core.Job, rows []map[string]any, value any) core.Result {
	if rows == nil {
		rows = []map[string]any{}
	}
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"rows":  {MIME: "application/json", Inline: rows, Headers: deriveHeaders(rows)},
			"value": {MIME: "application/json", Inline: value},
		},
	}
}

// parseYAMLDocuments decodes a YAML stream into its documents.
//
// yaml.v3 refuses an alias bomb on its own ("document contains excessive
// aliasing"), which matters because this text can arrive from an
// http_request — so there is no expansion guard here beyond the document cap.
func parseYAMLDocuments(s string) ([]any, error) {
	if strings.TrimSpace(s) == "" {
		return nil, fmt.Errorf("input 'in' is empty")
	}
	dec := yaml.NewDecoder(strings.NewReader(s))
	var out []any
	for {
		var doc any
		err := dec.Decode(&doc)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("couldn't read that as YAML: %w", err)
		}
		if doc == nil {
			continue
		}
		out = append(out, doc)
		if len(out) >= maxYAMLDocuments {
			return nil, fmt.Errorf("more than %d documents in one stream — split the file first", maxYAMLDocuments)
		}
	}
	return out, nil
}

// normalizeYAML rewrites the shapes YAML permits but a JSON-shaped consumer
// cannot read, recursively.
//
// The one that actually turns up is a non-string mapping key: `1: one` and
// `true: yes` are legal YAML and decode to map[interface{}]interface{}, which
// every downstream step here — rows, headers, Sheets, a DB insert — has no
// reading of. Keys are rendered as text rather than dropped, so a port-number
// map stays usable instead of arriving empty.
func normalizeYAML(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[k] = normalizeYAML(val)
		}
		return out
	case map[any]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[yamlKeyString(k)] = normalizeYAML(val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, item := range t {
			out[i] = normalizeYAML(item)
		}
		return out
	default:
		return v
	}
}

func coerceMap(v any) (map[string]any, bool) {
	m, ok := normalizeYAML(v).(map[string]any)
	return m, ok
}

func yamlKeyString(k any) string {
	if s, ok := k.(string); ok {
		return s
	}
	return fmt.Sprint(k)
}
