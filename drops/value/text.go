// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package value

import (
	"context"
	"encoding/json"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:       "text",
			Version:  "1.0",
			Label:    "Text",
			Color:    "#888",
			Icon:     "text",
			Category: "transformation",
			Provider: "internal",
			// A literal you type on the step. Its id is a common noun, so in a
			// sentence like "pull the invoice number out of the text" it scored
			// as the best answer for two of the words. A one-word search for
			// "text" still wins outright — matchScore returns before the boost.
			SearchBoost: -60,
			Tags: []string{
				"text", "string", "constant", "literal",
				"code", "script", "snippet", "sql", "yaml", "json", "shell",
			},
			Description: "Emit a literal string value. The 'text' param can be multi-line; later steps see it as text/plain on the 'out' port. Set 'Written in' to a language and the box becomes a code editor — monospace, syntax-coloured, and it stops wrapping long lines — which is how you keep a SQL query, a shell script or a chunk of YAML in a flow. It is still a plain string on the way out; the JSON step is the one that parses.",
			Summary:     "Emit a fixed string you type on the step, on the 'out' port.",
			Examples: []core.ParamsExample{
				{
					Title:  "Short constant",
					Params: json.RawMessage(`{"text":"hello world"}`),
				},
				{
					Title:  "Inline prompt template",
					Params: json.RawMessage(`{"text":"You are a helpful assistant.\nAnswer the user's question in one sentence."}`),
					Notes:  "Multi-line strings work fine — useful as a system prompt feeding into a Claude step.",
				},
				{
					Title:  "A query, kept as code",
					Params: json.RawMessage(`{"language":"sql","text":"select id, total\n  from orders\n where status = 'unpaid'"}`),
					Notes:  "'Written in' only changes the editor — the value is still the same plain string.",
				},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			// A literal value source: its output IS the `text` param, so it
			// originates data and takes no pass pin (you can't wire into a
			// literal). See core.WithPassthrough / Manifest.ValueSource.
			ValueSource: true,
			Outputs:     []core.Port{{Port: "out", Label: "Text", MIME: []string{"text/plain"}, Example: json.RawMessage(`"Faktura 4471 är betald."`)}},
			ParamsSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "text": {
      "type": "string",
      "format": "multiline",
      "x_lang_param": "language",
      "title": "Text",
      "description": "The text to emit."
    },
    "language": {
      "type": "string",
      "title": "Written in",
      "default": "plain",
      "enum": ["plain", "shell", "python", "javascript", "sql", "yaml", "json", "powershell"],
      "enumNames": ["Plain text", "Shell", "Python", "JavaScript", "SQL", "YAML", "JSON", "PowerShell"],
      "description": "Changes the EDITOR, not the value: pick a language and the box above becomes monospace, syntax-coloured, and stops wrapping long lines. What comes out is the same plain string either way. Leave it on plain text for prose — a system prompt or an email body reads worse in a monospace font, not better."
    }
  },
  "required": ["text"]
}`),
			Idempotent: true,
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
