// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package ollama

import (
	"context"
	"encoding/json"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/internal/llmtask"
	"github.com/dazyflow/dazyflow/internal/llm"
)

// nomic-embed-text is the model Ollama's own documentation reaches for first
// and the one most people already have pulled. Anything else the operator has
// works too — this is only the default.
const defaultEmbedModel = "nomic-embed-text"

type embedder struct{}

func (embedder) Embed(ctx context.Context, apiKey string, req llm.EmbedRequest) (llm.EmbedResult, *core.JobError) {
	model := req.Model
	if model == "" {
		model = defaultEmbedModel
	}
	body, err := json.Marshal(map[string]any{"model": model, "input": req.Texts})
	if err != nil {
		return llm.EmbedResult{}, &core.JobError{Code: "internal", Message: err.Error()}
	}
	headers := map[string]string{"content-type": "application/json"}
	if apiKey != "" {
		headers["authorization"] = "Bearer " + apiKey
	}
	status, raw, jerr := llmtask.PostJSON(ctx, baseOr(req.BaseURL)+"/api/embed", headers, body, req.TimeoutMS)
	if jerr != nil {
		return llm.EmbedResult{}, jerr
	}
	if status < 200 || status >= 300 {
		return llm.EmbedResult{}, llmtask.HTTPError("ollama", "Ollama", status, ollamaError(raw))
	}

	var parsed struct {
		Model      string      `json:"model"`
		Embeddings [][]float32 `json:"embeddings"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return llm.EmbedResult{}, &core.JobError{Code: "llm_api", Message: "could not read Ollama's answer: " + err.Error()}
	}
	return llm.EmbedResult{Vectors: parsed.Embeddings, Model: parsed.Model}, nil
}
