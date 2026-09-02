// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package transform

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/internal/params"
	"github.com/dazyflow/dazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "build_yaml",
			Version:     "1.0",
			Label:       "Write YAML",
			Subtitle:    "Rows into YAML text",
			Icon:        "file-code",
			Category:    "transformation",
			Provider:    "internal",
			Tags:        []string{"transform", "yaml", "export", "rows", "config", "serialize"},
			Description: "Turn rows into YAML text — the inverse of Read YAML. Connect rows from a query or a transform and get YAML to write to a config file, commit to a repo, or hand to a tool that reads it. Rows become a list of mappings; set 'single mapping' when there's one row and the file should be that mapping itself. 'Separate documents' writes each row as its own \"---\" document, which is how a bundle of manifests is shaped. Quoting and indentation are handled properly, so a value containing a colon or a leading zero survives.",
			Summary:     "Serialize rows into a YAML string for config files and tools that read them.",
			Examples: []core.ParamsExample{
				{
					Title:  "Rows to a YAML list",
					Params: json.RawMessage(`{}`),
				},
				{
					Title:  "One row as a config file",
					Params: json.RawMessage(`{"single":true}`),
					Notes:  "Emits the mapping on its own rather than a list holding it.",
				},
				{
					Title:  "A bundle of documents",
					Params: json.RawMessage(`{"documents":true}`),
					Notes:  "Each row becomes its own \"---\" document.",
				},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				{Port: "rows", Label: "Rows", Required: true, MIME: []string{"application/json"}},
			},
			Outputs: []core.Port{
				{Port: "out", Label: "YAML", MIME: []string{"text/plain", "application/yaml"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"single":{"type":"boolean","default":false,"title":"Single mapping","description":"When on and there is exactly one row, emit that mapping on its own instead of a list holding it. What a config file usually wants."},
					"documents":{"type":"boolean","default":false,"title":"Separate documents","description":"Write each row as its own document, separated by \"---\". How a bundle of Kubernetes manifests is shaped. Ignored when 'Single mapping' applies."},
					"indent":{"type":"integer","default":2,"minimum":1,"maximum":8,"title":"Indent","description":"How many spaces each level is indented by. 2 is the convention almost everything uses."},
					"columns":{"type":"array","items":{"type":"string"},"title":"Columns","description":"Optional explicit field order/subset. When empty, the rows' own column order is used."}
				}
			}`),
			Idempotent: true,
		},
		Execute: executeBuildYAML,
	})
}

func executeBuildYAML(_ context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	rows, headers, errRes, ok := loadRowsAndHeaders(job)
	if !ok {
		return errRes, nil
	}
	cols, errRes, ok := chosenColumns(job, headers)
	if !ok {
		return errRes, nil
	}
	projected := projectRows(rows, cols)
	indent := params.ClampInt(params.IntDefault(job.Params, "indent", 2), 1, 8)

	// Field ORDER is the reason rows go out as yaml.MapSlice-equivalents
	// rather than plain maps: yaml.v3 sorts a Go map's keys, so a config file
	// would come out alphabetised instead of in the order the author chose.
	// A config diff that reorders every line on each run is unusable in a
	// repo, which is where these files live.
	encode := func(v any) (string, error) {
		var buf bytes.Buffer
		enc := yaml.NewEncoder(&buf)
		enc.SetIndent(indent)
		if err := enc.Encode(v); err != nil {
			_ = enc.Close()
			return "", err
		}
		if err := enc.Close(); err != nil {
			return "", err
		}
		return buf.String(), nil
	}

	single := params.BoolDefault(job.Params, "single", false) && len(projected) == 1
	perDoc := params.BoolDefault(job.Params, "documents", false)

	var out string
	switch {
	case single:
		s, err := encode(orderedMap(projected[0], cols))
		if err != nil {
			return params.Err(job, "bad_input", "those rows can't be written as YAML: "+err.Error()), nil
		}
		out = s
	case perDoc:
		var parts []string
		for _, row := range projected {
			s, err := encode(orderedMap(row, cols))
			if err != nil {
				return params.Err(job, "bad_input", "those rows can't be written as YAML: "+err.Error()), nil
			}
			parts = append(parts, strings.TrimRight(s, "\n"))
		}
		// The leading "---" is included: a stream that starts with one is
		// unambiguous to every reader, and tools that emit bundles do it.
		out = "---\n" + strings.Join(parts, "\n---\n") + "\n"
	default:
		list := make([]any, 0, len(projected))
		for _, row := range projected {
			list = append(list, orderedMap(row, cols))
		}
		s, err := encode(list)
		if err != nil {
			return params.Err(job, "bad_input", "those rows can't be written as YAML: "+err.Error()), nil
		}
		out = s
	}

	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"out": {MIME: "application/yaml", Inline: out},
		},
	}, nil
}

// orderedMap turns one row into a yaml.Node that keeps the column order.
//
// yaml.v3 sorts the keys of a Go map, so encoding a row directly alphabetises
// it. A yaml.Node mapping preserves the order it was built in, which is what
// keeps a generated config file diffable against the previous run.
//
// Columns absent from the row are skipped rather than written as null: an
// explicit `key: null` in a config file often means something different from
// the key being absent, and guessing wrong changes behaviour. (Write JSON
// takes the opposite line, because a schema-validating API usually wants the
// key present — the formats are used differently.)
func orderedMap(row map[string]any, cols []string) *yaml.Node {
	order := cols
	if len(order) == 0 {
		order = rowKeyOrder(row)
	}
	node := &yaml.Node{Kind: yaml.MappingNode}
	for _, c := range order {
		v, present := row[c]
		if !present {
			continue
		}
		key := &yaml.Node{Kind: yaml.ScalarNode, Value: c}
		val := &yaml.Node{}
		if err := val.Encode(v); err != nil {
			// A value yaml can't represent is written as its text rendering
			// rather than failing the whole file.
			val = &yaml.Node{Kind: yaml.ScalarNode, Value: params.Truncate(fmt.Sprint(v), 4096)}
		}
		node.Content = append(node.Content, key, val)
	}
	return node
}

// rowKeyOrder is the field order for a row with no declared columns. Go map
// iteration is random, so without this a file's key order would change
// between runs of the same flow — the exact thing orderedMap exists to stop.
// Sorted, because a stable arbitrary order beats an unstable one.
func rowKeyOrder(row map[string]any) []string {
	out := make([]string, 0, len(row))
	for k := range row {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
