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
			ID:          "ai_classify",
			Version:     "1.0",
			Label:       "Claude",
			Subtitle:    "Classify",
			Summary:     "Sort text into one of your categories so you can route it.",
			Description: "Give AI a list of categories and it picks the single best match — route support emails, tag leads, or flag spam. Wire the Category output into a Branch to send each category somewhere different.",
			Integration: "Claude",
			Category:    "ai",
			Icon:        "claude",
			Color:       "#cc7755",
			Provider:    "internal",
			Tags:        []string{"ai", "claude", "classify", "categorize", "route", "label", "tag"},
			Examples: []core.ParamsExample{
				{Title: "Route a support email", Params: json.RawMessage(`{"categories":[{"name":"billing","description":"Payments, invoices, refunds"},{"name":"technical","description":"Bugs and how-to questions"},{"name":"sales","description":"Pricing and new purchases"}]}`), Notes: "Wire the email into Text; wire the Category output into a Branch."},
			},
			ConnectionFields: connectionFields,
			ExecutionModel:   core.ExecutionBatch,
			ProcessModel:     core.ProcessLongLived,
			Inputs: []core.Port{
				{Port: "text", Label: "Text"},
			},
			Outputs: []core.Port{
				{Port: "category", Label: "Category", MIME: []string{"text/plain"}},
				{Port: "confidence", Label: "Confidence", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"text":{"type":"string","format":"multiline","title":"Text to classify","description":"Or wire it into the Text input."},
					"categories":{
						"type":"array","title":"Categories","minItems":2,
						"items":{"type":"object","properties":{
							"name":{"type":"string","title":"Category"},
							"description":{"type":"string","title":"When to use it"}
						},"required":["name"]}
					},
					"allow_none":{"type":"boolean","title":"Allow \"none of these\"","description":"Let the AI answer 'none' when nothing fits.","default":false},
					"model":{"type":"string","x_advanced":true,"description":"Claude model id.","default":"claude-sonnet-4-6"},
					"api_key":{"type":"string","x_advanced":true,"description":"Injected from the Claude connection — leave unset."},
					"base_url":{"type":"string","x_advanced":true,"description":"Override the API host."},
					"timeout_ms":{"type":"integer","default":60000,"minimum":1}
				},
				"required":["categories"]
			}`),
			Idempotent:  true,
			RetryPolicy: core.RetryExponentialBackoff,
		},
		Execute: executeClassify,
	})
}

func executeClassify(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	text := resolveText(job)
	cats := paramObjList(job.Params, "categories")
	allowNone := params.BoolDefault(job.Params, "allow_none", false)

	names, descLines := categoryNames(cats)
	if len(names) < 2 {
		return params.Err(job, "bad_param", "add at least two categories to classify into"), nil
	}
	if allowNone {
		names = append(names, "none")
	}

	tool := &toolDef{
		name:        "classify",
		description: "Return the single best-matching category.",
		schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"category":   map[string]any{"type": "string", "enum": names, "description": "The one category that best matches the text."},
				"confidence": map[string]any{"type": "number", "description": "How confident, from 0 to 1."},
				"reason":     map[string]any{"type": "string", "description": "One short sentence on why."},
			},
			"required": []any{"category"},
		},
	}

	system := "Classify the user's text into exactly one of these categories, then return your choice via the tool:\n" + descLines
	if allowNone {
		system += "\nIf none of the categories fit, answer \"none\"."
	}

	temp := 0.0
	out, je := callClaude(ctx, job, callOpts{
		system:      system,
		userText:    text,
		tool:        tool,
		temperature: &temp,
		model:       params.StringDefault(job.Params, "model", defaultModel),
		timeoutMS:   params.IntDefault(job.Params, "timeout_ms", 60000),
	})
	if je != nil {
		return params.Err(job, je.Code, je.Message), nil
	}
	if out.tool == nil {
		return params.Err(job, "ai_no_output", "the model did not return a category"), nil
	}

	category, _ := out.tool["category"].(string)
	if category == "" {
		return params.Err(job, "ai_no_output", "the model returned an empty category"), nil
	}

	output := map[string]core.Ref{
		"category": {MIME: "text/plain", Inline: category},
		"response": {MIME: "application/json", Inline: out.raw},
	}
	if conf, ok := out.tool["confidence"]; ok {
		output["confidence"] = core.Ref{MIME: "application/json", Inline: conf}
	}

	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: output,
	}, nil
}

// categoryNames returns the category names and a "- name: description" block
// for the system prompt, skipping unnamed entries.
func categoryNames(cats []map[string]any) (names []string, descLines string) {
	var b strings.Builder
	for _, c := range cats {
		name, _ := c["name"].(string)
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		names = append(names, name)
		desc, _ := c["description"].(string)
		if desc = strings.TrimSpace(desc); desc != "" {
			fmt.Fprintf(&b, "- %s: %s\n", name, desc)
		} else {
			fmt.Fprintf(&b, "- %s\n", name)
		}
	}
	return names, b.String()
}
