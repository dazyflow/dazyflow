// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package value contains literal/constant source drops — nodes that
// take no input and emit a graph-author-supplied value on their
// output port. Useful for inline prompts, templates, snippets, and
// other small bits of data that don't deserve a workspace file.
package value

import (
	"context"
	"encoding/json"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "text",
			Version:     "1.0",
			Label:       "Text",
			Color:       "#888",
			Icon:        "text",
			Category:    "transformation",
			Provider:    "internal",
			Tags:        []string{"text", "string", "constant", "literal"},
			Description: "Emit a literal string value. The 'text' param can be multi-line; downstream consumers see it as text/plain on the 'out' port.",
			Summary:     "Emit a graph-author-supplied literal string on the 'out' port.",
			Examples: []core.ParamsExample{
				{
					Title:  "Short constant",
					Params: json.RawMessage(`{"text":"hello world"}`),
				},
				{
					Title:  "Inline prompt template",
					Params: json.RawMessage(`{"text":"You are a helpful assistant.\nAnswer the user's question in one sentence."}`),
					Notes:  "Multi-line strings work fine — useful as a system prompt feeding into a Claude node.",
				},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			// A literal value source: its output IS the `text` param, so it
			// originates data and takes no pass pin (you can't wire into a
			// literal). See core.WithPassthrough / Manifest.ValueSource.
			ValueSource:  true,
			Outputs:      []core.Port{{Port: "out", Label: "Text", MIME: []string{"text/plain"}}},
			ParamsSchema: json.RawMessage(`{"type":"object","properties":{"text":{"type":"string","format":"multiline","title":"Text"}},"required":["text"]}`),
			Idempotent:   true,
		},
		Execute: executeText,
	})
}

func executeText(_ context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	text, _ := job.Params["text"].(string)
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"out": {MIME: "text/plain", Inline: text},
		},
	}, nil
}
