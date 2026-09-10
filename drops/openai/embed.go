// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/internal/llmtask"
	"github.com/dazyflow/dazyflow/internal/llm"
)

const defaultEmbedModel = "text-embedding-3-small"

var embedModels = []llm.ModelOption{
	{ID: "text-embedding-3-small", Label: "Embedding 3 small (cheap, 1536 numbers)"},
	{ID: "text-embedding-3-large", Label: "Embedding 3 large (better, 3072 numbers)"},
}

type embedder struct{}

func (embedder) Embed(ctx context.Context, apiKey string, req llm.EmbedRequest) (llm.EmbedResult, *core.JobError) {
	base := baseOrDefault(req.BaseURL)
	body, err := json.Marshal(map[string]any{"model": req.Model, "input": req.Texts})
	if err != nil {
		return llm.EmbedResult{}, &core.JobError{Code: "internal", Message: err.Error()}
	}
	status, raw, jerr := llmtask.PostJSON(ctx, base+"/v1/embeddings", map[string]string{
		"authorization": "Bearer " + apiKey,
		"content-type":  "application/json",
	}, body, req.TimeoutMS)
	if jerr != nil {
		return llm.EmbedResult{}, jerr
	}
	if status < 200 || status >= 300 {
		return llm.EmbedResult{}, llmtask.HTTPError("openai", "ChatGPT", status, openaiError(raw))
	}

	var parsed struct {
		Model string `json:"model"`
		Data  []struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return llm.EmbedResult{}, &core.JobError{Code: "llm_api", Message: "could not read ChatGPT's answer: " + err.Error()}
	}
	// The API documents index but does not promise order, and the caller
	// matches vectors to texts by position.
	vectors := make([][]float32, len(parsed.Data))
	for _, d := range parsed.Data {
		if d.Index < 0 || d.Index >= len(vectors) {
			return llm.EmbedResult{}, &core.JobError{
				Code: "llm_api", Message: fmt.Sprintf("ChatGPT returned an out-of-range index (%d)", d.Index)}
		}
		vectors[d.Index] = d.Embedding
	}
	return llm.EmbedResult{Vectors: vectors, Model: parsed.Model}, nil
}

func baseOrDefault(base string) string {
	base = strings.TrimRight(base, "/")
	if base == "" {
		return defaultBase
	}
	return base
}
