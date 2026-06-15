package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"git.sr.ht/~klahr/hazyflow/core"
	"git.sr.ht/~klahr/hazyflow/drops/internal/params"
	"git.sr.ht/~klahr/hazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "ai_summarize",
			Version:     "1.0",
			Label:       "Claude",
			Subtitle:    "Summarize",
			Summary:     "Turn long text into a short summary in the style you pick.",
			Description: "Feed in any text — an email, a document, a row of notes — and get a concise summary back. Choose how short it should be and whether you want a sentence, a paragraph, or bullet points.",
			Integration: "Claude",
			Category:    "ai",
			Icon:        "claude",
			Color:       "#cc7755",
			Provider:    "internal",
			Tags:        []string{"ai", "claude", "summary", "summarize", "text", "tldr"},
			Examples: []core.ParamsExample{
				{Title: "One-sentence summary", Params: json.RawMessage(`{"style":"one_line"}`), Notes: "Wire the text to summarize into the Text input. The API key comes from the Claude connection — leave api_key unset."},
				{Title: "Bullet points, ~40 words", Params: json.RawMessage(`{"style":"bullets","max_words":40}`)},
			},
			ConnectionFields: connectionFields,
			ExecutionModel:   core.ExecutionBatch,
			ProcessModel:     core.ProcessLongLived,
			Inputs: []core.Port{
				{Port: "text", Label: "Text"},
			},
			Outputs: []core.Port{
				{Port: "summary", Label: "Summary", MIME: []string{"text/plain"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"text":{"type":"string","format":"multiline","title":"Text to summarize","description":"Or wire it into the Text input."},
					"style":{"type":"string","title":"Style","enum":["one_line","paragraph","bullets"],"enumNames":["One sentence","Short paragraph","Bullet points"],"default":"paragraph"},
					"max_words":{"type":"integer","title":"Roughly how many words","minimum":5,"maximum":500,"default":60},
					"language":{"type":"string","title":"Output language","description":"Leave blank to match the input's language.","x_advanced":true},
					"model":{"type":"string","x_advanced":true,"description":"Claude model id.","default":"claude-sonnet-4-6"},
					"api_key":{"type":"string","x_advanced":true,"description":"Injected from the Claude connection — leave unset."},
					"base_url":{"type":"string","x_advanced":true,"description":"Override the API host."},
					"timeout_ms":{"type":"integer","default":60000,"minimum":1}
				}
			}`),
			Idempotent:  true,
			RetryPolicy: core.RetryExponentialBackoff,
		},
		Execute: executeSummarize,
	})
}

func executeSummarize(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	text := resolveText(job)
	style := params.StringDefault(job.Params, "style", "paragraph")
	maxWords := params.IntDefault(job.Params, "max_words", 60)
	language, _ := params.StringOpt(job.Params, "language")

	temp := 0.3
	out, je := callClaude(ctx, job, callOpts{
		system:      summarizeSystem(style, maxWords, language),
		userText:    text,
		temperature: &temp,
		model:       params.StringDefault(job.Params, "model", defaultModel),
		timeoutMS:   params.IntDefault(job.Params, "timeout_ms", 60000),
	})
	if je != nil {
		return params.Err(job, je.Code, je.Message), nil
	}

	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"summary":  {MIME: "text/plain", Inline: strings.TrimSpace(out.text)},
			"response": {MIME: "application/json", Inline: out.raw},
		},
	}, nil
}

func summarizeSystem(style string, maxWords int, language string) string {
	var b strings.Builder
	b.WriteString("You are a precise summarizer. Summarize the user's text faithfully. ")
	switch style {
	case "one_line":
		b.WriteString("Reply with a single sentence. ")
	case "bullets":
		b.WriteString("Reply with concise bullet points, one per line, each starting with \"- \". ")
	default:
		b.WriteString("Reply with a single short paragraph. ")
	}
	if maxWords > 0 {
		fmt.Fprintf(&b, "Keep it under about %d words. ", maxWords)
	}
	if lang := strings.TrimSpace(language); lang != "" {
		fmt.Fprintf(&b, "Write the summary in %s. ", lang)
	}
	b.WriteString("Output only the summary itself, with no preamble or closing remarks.")
	return b.String()
}
