// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package gemini

import (
	"context"
	"encoding/json"
	"net/url"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/internal/llmtask"
	"github.com/dazyflow/dazyflow/internal/llm"
)

const defaultEmbedModel = "text-embedding-004"

var embedModels = []llm.ModelOption{
	{ID: "text-embedding-004", Label: "Text embedding 004 (768 numbers)"},
	{ID: "gemini-embedding-001", Label: "Gemini embedding 001 (3072 numbers)"},
}

type embedder struct{}

func (embedder) Embed(ctx context.Context, apiKey string, req llm.EmbedRequest) (llm.EmbedResult, *core.JobError) {
	model := req.Model
	if model == "" {
		model = defaultEmbedModel
	}
	requests := make([]any, 0, len(req.Texts))
	for _, text := range req.Texts {
		requests = append(requests, map[string]any{
			"model":   "models/" + model,
			"content": map[string]any{"parts": []any{map[string]any{"text": text}}},
		})
	}
	body, err := json.Marshal(map[string]any{"requests": requests})
	if err != nil {
		return llm.EmbedResult{}, &core.JobError{Code: "internal", Message: err.Error()}
	}

	// The key rides in the query string, as it does for generation — so it
	// must be escaped rather than concatenated.
	endpoint := baseOr(req.BaseURL) + "/v1beta/models/" + url.PathEscape(model) +
		":batchEmbedContents?key=" + url.QueryEscape(apiKey)
	status, raw, jerr := llmtask.PostJSON(ctx, endpoint, map[string]string{
		"content-type": "application/json",
	}, body, req.TimeoutMS)
	if jerr != nil {
		return llm.EmbedResult{}, jerr
	}
	if status < 200 || status >= 300 {
		return llm.EmbedResult{}, llmtask.HTTPError("gemini", "Gemini", status, geminiError(raw))
	}

	var parsed struct {
		Embeddings []struct {
			Values []float32 `json:"values"`
		} `json:"embeddings"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return llm.EmbedResult{}, &core.JobError{Code: "llm_api", Message: "could not read Gemini's answer: " + err.Error()}
	}
	vectors := make([][]float32, 0, len(parsed.Embeddings))
	for _, e := range parsed.Embeddings {
		vectors = append(vectors, e.Values)
	}
	return llm.EmbedResult{Vectors: vectors, Model: model}, nil
}
