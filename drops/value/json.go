// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package value

import (
	"context"
	"encoding/json"
	"strings"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "json",
			Version:     "1.0",
			Label:       "JSON",
			Color:       "#888",
			Icon:        "braces",
			Category:    "transformation",
			Provider:    "internal",
			Tags:        []string{"json", "object", "array", "constant", "literal", "blocks"},
			Description: "Emit a literal JSON value, decoded. Type a JSON array or object and it parses once at run time, emitting the real value (not a string) on the 'out' port — so it wires straight into ports that want structured JSON, like a Slack message's Blocks. Distinct from Text, which emits its content as a plain string.",
			Summary:     "Emit a graph-author-supplied JSON value, parsed into a real array/object on the 'out' port.",
			Examples: []core.ParamsExample{
				{
					Title:  "Slack Block Kit array",
					Params: json.RawMessage(`{"json":"[{\"type\":\"section\",\"text\":{\"type\":\"mrkdwn\",\"text\":\"*Deploy finished* :rocket:\"}},{\"type\":\"divider\"}]"}`),
					Notes:  "Wire 'out' into a Send message node's Blocks input.",
				},
				{
					Title:  "A config object",
					Params: json.RawMessage(`{"json":"{\"retries\":3,\"channel\":\"#alerts\"}"}`),
				},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			// A literal value source: its output IS the parsed `json` param, so
			// it originates data and takes no pass pin (you can't wire into a
			// literal). See core.WithPassthrough / Manifest.ValueSource.
			ValueSource:  true,
			Outputs:      []core.Port{{Port: "out", Label: "JSON", MIME: []string{"application/json"}}},
			ParamsSchema: json.RawMessage(`{"type":"object","properties":{"json":{"type":"string","format":"json","title":"JSON","description":"A JSON value — e.g. an array of Slack Block Kit blocks like [ {…}, {…} ]. Parsed and emitted decoded on 'out'."}},"required":["json"]}`),
			Idempotent:   true,
		},
		Execute: executeJSON,
	})
}

// executeJSON parses the `json` param into a real value and emits it on 'out'.
// The param is normally the string a graph author typed; a structured value
// (programmatically-built param) is passed through untouched. Invalid JSON
// surfaces as a clear bad_json error rather than a downstream type mismatch —
// the whole point of authoring JSON at a typed source instead of a Text node.
func executeJSON(_ context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	raw, ok := job.Params["json"]
	if !ok || raw == nil {
		return badJSON(job, "Enter a JSON value — e.g. an array of Slack blocks like [ {…}, {…} ].", "")
	}

	var value any
	switch v := raw.(type) {
	case string:
		s := strings.TrimSpace(v)
		if s == "" {
			return badJSON(job, "Enter a JSON value — e.g. an array of Slack blocks like [ {…}, {…} ].", "")
		}
		if err := json.Unmarshal([]byte(s), &value); err != nil {
			return badJSON(job, "This isn't valid JSON. Check for missing quotes, trailing commas, or single quotes — and remember an array must be wrapped in [ … ].", err.Error())
		}
	default:
		// Already structured (e.g. a param built by another tool) — emit as-is.
		value = v
	}

	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{"out": {MIME: "application/json", Inline: value}},
	}, nil
}

func badJSON(job core.Job, msg, details string) (core.Result, error) {
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusError,
		Error:  &core.JobError{Code: "bad_json", Message: msg, Details: details},
	}, nil
}
