// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package transform

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/internal/params"
	"github.com/dazyflow/dazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:       "lookup",
			Version:  "1.0",
			Label:    "Look up",
			Subtitle: "Swap one value for another",
			Icon:     "arrow-left-right",
			Category: "transformation",
			Provider: "internal",
			// Deliberately NOT "translate": this maps a value through a table,
			// and someone asking to translate wants an AI step.
			Tags: []string{"lookup", "table", "map", "convert", "match",
				"code", "country", "replace", "dictionary", "key", "value"},
			Description: "Turn one value into another using a little table you fill in — country code to country name, " +
				"plan to discount, status code to the words you want people to read.\n\n" +
				"Write the table as pairs: what comes in on the left, what should come out on the right. Wire the value " +
				"into 'in' and the match leaves on 'out'. Capitalisation is ignored unless you turn on 'Match " +
				"capitalisation', so \"se\" finds \"SE\".\n\n" +
				"Anything not in the table takes 'When nothing matches', if you set one. Leave that empty and the value " +
				"leaves on the 'No match' output instead, carrying what came in — so a flow can handle the unknown ones " +
				"rather than pretending they were fine. Exactly one of the two outputs fires, never both.\n\n" +
				"For a handful of paths that each do something different, use Switch; this is for turning a value into " +
				"another value and carrying on.",
			Summary: "Map a value through a table of pairs, with a fallback or a separate output for anything unlisted.",
			Examples: []core.ParamsExample{
				{
					Title:  "Country code to country name",
					Params: json.RawMessage(`{"table":{"SE":"Sweden","NO":"Norway","DK":"Denmark"},"fallback":"Unknown"}`),
					Notes:  "Anything but those three comes out as \"Unknown\".",
				},
				{
					Title:  "Plan to discount, handling the unknown ones separately",
					Params: json.RawMessage(`{"table":{"gold":"20","silver":"10","bronze":"5"}}`),
					Notes:  "With no fallback, an unlisted plan leaves on 'No match' carrying its own value.",
				},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				{Port: "in", Label: "Value", Required: true},
			},
			Outputs: []core.Port{
				{Port: "out", Label: "Result"},
				{Port: "unmatched", Label: "No match"},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"table":{"type":"object","title":"The table","additionalProperties":{"type":"string"},"x_key_placeholder":"SE","x_value_placeholder":"Sweden","description":"The pairs to look through: what comes in on the left, what comes out on the right."},
					"fallback":{"type":"string","title":"When nothing matches","description":"What to send out for a value the table does not list. Leave empty and unlisted values leave on the 'No match' output instead."},
					"case_sensitive":{"type":"boolean","default":false,"title":"Match capitalisation","description":"Off (the default) means \"se\", \"SE\" and \"Se\" all find the same row. On means they are three different keys."}
				},
				"required":["table"]
			}`),
			Idempotent: true,
		},
		Execute: executeLookup,
	})
}

func executeLookup(_ context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	raw, ok := job.Params["table"]
	if !ok || raw == nil {
		return params.Err(job, "bad_param", "'The table' is required — add at least one pair"), nil
	}
	pairs, ok := raw.(map[string]any)
	if !ok {
		return params.Err(job, "bad_param", fmt.Sprintf("'The table' should be a set of pairs, got %T", raw)), nil
	}
	if len(pairs) == 0 {
		return params.Err(job, "bad_param", "'The table' is empty — add at least one pair"), nil
	}

	ref, ok := job.Input["in"]
	if !ok {
		return params.Err(job, "missing_input", "input port 'in' is required — wire in the value to look up"), nil
	}
	key := lookupKey(ref.Inline)

	sensitive := params.BoolDefault(job.Params, "case_sensitive", false)
	// Folding once beats folding every candidate inside the loop, and it means
	// two keys that differ only in case resolve the same way whichever order
	// the map happens to be walked in.
	table := make(map[string]any, len(pairs))
	for k, v := range pairs {
		if !sensitive {
			k = strings.ToLower(k)
		}
		table[k] = v
	}
	if !sensitive {
		key = strings.ToLower(key)
	}

	if hit, found := table[key]; found {
		return lookupResult(job, "out", hit), nil
	}
	if fallback, set := params.StringOpt(job.Params, "fallback"); set && fallback != "" {
		return lookupResult(job, "out", fallback), nil
	}
	// No match and nothing to fall back on: hand the original value out the
	// other side so the flow can deal with it knowingly.
	return lookupResult(job, "unmatched", ref.Inline), nil
}

// lookupKey renders the incoming value as the text a table is written in, so a
// number wired in from an earlier step still finds the row someone typed.
func lookupKey(v any) string {
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	case nil:
		return ""
	case float64:
		return strings.TrimSpace(fmt.Sprintf("%g", t))
	}
	return strings.TrimSpace(fmt.Sprint(v))
}

func lookupResult(job core.Job, port string, value any) core.Result {
	mime := "application/json"
	switch value.(type) {
	case string:
		mime = "text/plain"
	case bool:
		mime = core.MIMEBool
	}
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{port: {MIME: mime, Inline: value}},
	}
}
