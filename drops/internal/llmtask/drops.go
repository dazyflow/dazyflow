// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package llmtask

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/drops/internal/params"
	"git.sr.ht/~klahr/dazyflow/engine"
)

// ---- Ask -------------------------------------------------------------------

func askDrop(cfg Config) engine.NativeDrop {
	props := baseProps(cfg)
	props["prompt"] = map[string]any{"type": "string", "format": "multiline", "title": "Prompt", "description": "Single user message (used when no Prompt input and no messages)."}
	props["system"] = map[string]any{"type": "string", "format": "multiline", "title": "System prompt", "description": "Optional instruction that frames the reply."}
	props["messages"] = map[string]any{"type": "array", "items": map[string]any{}, "x_advanced": true, "description": "Full conversation history ({role, content}); overrides the prompt."}
	props["max_tokens"] = map[string]any{"type": "integer", "default": 1024, "minimum": 1}
	props["temperature"] = map[string]any{"type": "number", "x_advanced": true}
	return engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          cfg.AskID,
			Version:     "1.0",
			Label:       cfg.Integration,
			Subtitle:    "Ask",
			Summary:     "Send a prompt and get a single text response back.",
			Description: "Send a prompt and get a response — summarise upstream text, classify inputs, or generate text. The graph itself is your agent loop.",
			Integration: cfg.Integration,
			Category:    "ai",
			Icon:        cfg.Icon,
			Color:       cfg.Color,
			BrandLogo:   cfg.BrandLogo,
			Provider:    "internal",
			Tags:        tags(cfg, "prompt", "llm", "ask"),
			Examples: []core.ParamsExample{
				{Title: "One-shot summary", Params: json.RawMessage(`{"prompt":"Summarize the upstream text in one sentence."}`), Notes: "Wire the text into the Prompt input. The API key comes from the connection — leave api_key unset."},
			},
			ConnectionFields: connFields(cfg),
			ExecutionModel:   core.ExecutionBatch,
			ProcessModel:     core.ProcessLongLived,
			Inputs:           []core.Port{{Port: "prompt", Label: "Prompt"}},
			// Port id stays "text" (existing wires reference it); the label is
			// the role, "Response", matching the task drops' Summary/Reply.
			Outputs:      []core.Port{{Port: "text", Label: "Response", MIME: []string{"text/plain"}}},
			ParamsSchema: schemaJSON(props, nil),
			Idempotent:   true,
			RetryPolicy:  core.RetryExponentialBackoff,
		},
		Execute: func(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
			apiKey, jerr := resolveKey(job, cfg)
			if jerr != nil {
				return params.Err(job, jerr.Code, jerr.Message), nil
			}
			var userText string
			var messages []any
			if in, ok := job.Input["prompt"]; ok && in.Inline != nil {
				userText = coerceText(in.Inline)
			}
			if userText == "" {
				if m, ok := job.Params["messages"].([]any); ok && len(m) > 0 {
					messages = m
				}
			}
			if userText == "" && messages == nil {
				userText = params.StringDefault(job.Params, "prompt", "")
			}
			if userText == "" && len(messages) == 0 {
				return params.Err(job, "bad_input", "no prompt — provide the Prompt input, params.prompt, or params.messages"), nil
			}
			system, _ := params.StringOpt(job.Params, "system")
			var temp *float64
			if t, ok := job.Params["temperature"].(float64); ok {
				temp = &t
			}
			out, jerr := cfg.Provider.Call(ctx, apiKey, Request{
				Model: model(job, cfg), System: system, UserText: userText, Messages: messages,
				MaxTokens: params.IntDefault(job.Params, "max_tokens", 1024), Temperature: temp,
				TimeoutMS: timeoutMS(job), BaseURL: baseURL(job),
			})
			if jerr != nil {
				return params.Err(job, jerr.Code, jerr.Message), nil
			}
			return core.Result{JobID: job.ID, Status: core.StatusOK, Output: map[string]core.Ref{
				"text":     {MIME: "text/plain", Inline: strings.TrimSpace(out.Text)},
				"response": {MIME: "application/json", Inline: out.Raw},
			}}, nil
		},
	}
}

// ---- Summarize -------------------------------------------------------------

func summarizeDrop(cfg Config) engine.NativeDrop {
	props := baseProps(cfg)
	props["text"] = map[string]any{"type": "string", "format": "multiline", "title": "Text to summarize", "description": "Or wire it into the Text input."}
	props["style"] = map[string]any{"type": "string", "title": "Style", "enum": []any{"one_line", "paragraph", "bullets"}, "enumNames": []any{"One sentence", "Short paragraph", "Bullet points"}, "default": "paragraph"}
	props["max_words"] = map[string]any{"type": "integer", "title": "Roughly how many words", "minimum": 5, "maximum": 500, "default": 60}
	props["language"] = map[string]any{"type": "string", "title": "Output language", "x_advanced": true, "description": "Leave blank to match the input's language."}
	return engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          taskID(cfg, "summarize"),
			Version:     "1.0",
			Label:       cfg.Integration,
			Subtitle:    "Summarize",
			Summary:     "Turn long text into a short summary in the style you pick.",
			Description: "Feed in any text — an email, a document, a row of notes — and get a concise summary back. Choose how short and whether you want a sentence, a paragraph, or bullet points.",
			Integration: cfg.Integration, Category: "ai", Icon: cfg.Icon, Color: cfg.Color, BrandLogo: cfg.BrandLogo,
			Provider: "internal", Tags: tags(cfg, "summary", "summarize", "text", "tldr"),
			Examples: []core.ParamsExample{
				{Title: "One-sentence summary", Params: json.RawMessage(`{"style":"one_line"}`), Notes: "Wire the text to summarize into the Text input; leave api_key unset."},
			},
			ConnectionFields: connFields(cfg),
			ExecutionModel:   core.ExecutionBatch, ProcessModel: core.ProcessLongLived,
			Inputs:       []core.Port{{Port: "text", Label: "Text"}},
			Outputs:      []core.Port{{Port: "summary", Label: "Summary", MIME: []string{"text/plain"}}},
			ParamsSchema: schemaJSON(props, nil),
			Idempotent:   true, RetryPolicy: core.RetryExponentialBackoff,
		},
		Execute: func(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
			apiKey, jerr := resolveKey(job, cfg)
			if jerr != nil {
				return params.Err(job, jerr.Code, jerr.Message), nil
			}
			text := resolveText(job)
			if strings.TrimSpace(text) == "" {
				return params.Err(job, "bad_input", "no text — wire the Text input or fill the text field"), nil
			}
			temp := 0.3
			out, jerr := cfg.Provider.Call(ctx, apiKey, Request{
				Model:    model(job, cfg),
				System:   summarizeSystem(params.StringDefault(job.Params, "style", "paragraph"), params.IntDefault(job.Params, "max_words", 60), params.StringDefault(job.Params, "language", "")),
				UserText: text, Temperature: &temp, TimeoutMS: timeoutMS(job), BaseURL: baseURL(job),
			})
			if jerr != nil {
				return params.Err(job, jerr.Code, jerr.Message), nil
			}
			return core.Result{JobID: job.ID, Status: core.StatusOK, Output: map[string]core.Ref{
				"summary":  {MIME: "text/plain", Inline: strings.TrimSpace(out.Text)},
				"response": {MIME: "application/json", Inline: out.Raw},
			}}, nil
		},
	}
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
	b.WriteString("Output only the summary itself, with no preamble.")
	return b.String()
}

// ---- Extract fields --------------------------------------------------------

func extractDrop(cfg Config) engine.NativeDrop {
	props := baseProps(cfg)
	props["text"] = map[string]any{"type": "string", "format": "multiline", "title": "Text to read", "description": "Or wire it into the Text input."}
	props["fields"] = map[string]any{
		"type": "array", "title": "Fields to extract", "minItems": 1,
		"items": map[string]any{"type": "object", "properties": map[string]any{
			"name":        map[string]any{"type": "string", "title": "Field name"},
			"description": map[string]any{"type": "string", "title": "What it is"},
			"type":        map[string]any{"type": "string", "title": "Type", "enum": []any{"string", "number", "boolean", "date"}, "enumNames": []any{"Text", "Number", "Yes/No", "Date"}, "default": "string"},
		}, "required": []any{"name"}},
	}
	props["on_missing"] = map[string]any{"type": "string", "title": "If a field isn't found", "enum": []any{"null", "fail"}, "enumNames": []any{"Leave it empty", "Fail the step"}, "default": "null", "x_advanced": true}
	return engine.NativeDrop{
		Manifest: core.Manifest{
			ID:      taskID(cfg, "extract"),
			Version: "1.0",
			Label:   cfg.Integration, Subtitle: "Extract fields",
			Summary:     "Pull structured fields out of messy text into clean JSON.",
			Description: "Describe the fields you want — like amount, due date, or customer name — and AI reads the text and fills them in. Great for turning invoices, emails, and form replies into rows.",
			Integration: cfg.Integration, Category: "ai", Icon: cfg.Icon, Color: cfg.Color, BrandLogo: cfg.BrandLogo,
			Provider: "internal", Tags: tags(cfg, "extract", "parse", "structured", "fields", "json"),
			Examples: []core.ParamsExample{
				{Title: "Parse an invoice email", Params: json.RawMessage(`{"fields":[{"name":"amount","description":"Total due","type":"number"},{"name":"vendor","description":"Who sent it"}]}`), Notes: "Wire the email body into Text. Output 'data' is {amount, vendor}."},
			},
			ConnectionFields: connFields(cfg),
			ExecutionModel:   core.ExecutionBatch, ProcessModel: core.ProcessLongLived,
			Inputs:       []core.Port{{Port: "text", Label: "Text"}},
			Outputs:      []core.Port{{Port: "data", Label: "Fields", MIME: []string{"application/json"}}},
			ParamsSchema: schemaJSON(props, []string{"fields"}),
			Idempotent:   true, RetryPolicy: core.RetryExponentialBackoff,
		},
		Execute: func(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
			apiKey, jerr := resolveKey(job, cfg)
			if jerr != nil {
				return params.Err(job, jerr.Code, jerr.Message), nil
			}
			text := resolveText(job)
			if strings.TrimSpace(text) == "" {
				return params.Err(job, "bad_input", "no text — wire the Text input or fill the text field"), nil
			}
			fields := paramObjList(job.Params, "fields")
			tool := buildExtractTool(fields, params.StringDefault(job.Params, "on_missing", "null"))
			if tool == nil {
				return params.Err(job, "bad_param", "no fields to extract — add at least one field with a name"), nil
			}
			temp := 0.0
			out, jerr := cfg.Provider.Call(ctx, apiKey, Request{
				Model:    model(job, cfg),
				System:   "Extract the requested fields from the user's text and return them via the tool. Do not invent values. If a value is genuinely not present, return null for that field.",
				UserText: text, Tool: tool, Temperature: &temp, TimeoutMS: timeoutMS(job), BaseURL: baseURL(job),
			})
			if jerr != nil {
				return params.Err(job, jerr.Code, jerr.Message), nil
			}
			if out.Tool == nil {
				return params.Err(job, "ai_no_output", "the model did not return any extracted fields"), nil
			}
			return core.Result{JobID: job.ID, Status: core.StatusOK, Output: map[string]core.Ref{
				"data":     {MIME: "application/json", Inline: out.Tool},
				"response": {MIME: "application/json", Inline: out.Raw},
			}}, nil
		},
	}
}

func buildExtractTool(fields []map[string]any, onMissing string) *Tool {
	props := map[string]any{}
	var required []string
	for _, f := range fields {
		name, _ := f["name"].(string)
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		desc, _ := f["description"].(string)
		jsonType := "string"
		switch t, _ := f["type"].(string); t {
		case "number":
			jsonType = "number"
		case "boolean":
			jsonType = "boolean"
		case "date":
			jsonType = "string"
			desc = strings.TrimSpace(desc + " (ISO 8601 date, YYYY-MM-DD)")
		}
		var prop map[string]any
		if onMissing == "fail" {
			prop = map[string]any{"type": jsonType}
			required = append(required, name)
		} else {
			prop = map[string]any{"type": []any{jsonType, "null"}}
		}
		if desc != "" {
			prop["description"] = desc
		}
		props[name] = prop
	}
	if len(props) == 0 {
		return nil
	}
	schema := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		schema["required"] = required
	}
	return &Tool{Name: "extract", Description: "Return the requested fields extracted from the text.", Schema: schema}
}

// ---- Classify --------------------------------------------------------------

func classifyDrop(cfg Config) engine.NativeDrop {
	props := baseProps(cfg)
	props["text"] = map[string]any{"type": "string", "format": "multiline", "title": "Text to classify", "description": "Or wire it into the Text input."}
	props["categories"] = map[string]any{
		"type": "array", "title": "Categories", "minItems": 2,
		"items": map[string]any{"type": "object", "properties": map[string]any{
			"name":        map[string]any{"type": "string", "title": "Category"},
			"description": map[string]any{"type": "string", "title": "When to use it"},
		}, "required": []any{"name"}},
	}
	props["allow_none"] = map[string]any{"type": "boolean", "title": "Allow \"none of these\"", "default": false}
	return engine.NativeDrop{
		Manifest: core.Manifest{
			ID:      taskID(cfg, "classify"),
			Version: "1.0",
			Label:   cfg.Integration, Subtitle: "Classify",
			Summary:     "Sort text into one of your categories so you can route it.",
			Description: "Give AI a list of categories and it picks the single best match — route support emails, tag leads, flag spam. Wire the Category output into a Branch.",
			Integration: cfg.Integration, Category: "ai", Icon: cfg.Icon, Color: cfg.Color, BrandLogo: cfg.BrandLogo,
			Provider: "internal", Tags: tags(cfg, "classify", "categorize", "route", "label", "tag"),
			Examples: []core.ParamsExample{
				{Title: "Route a support email", Params: json.RawMessage(`{"categories":[{"name":"billing","description":"Payments, refunds"},{"name":"technical","description":"Bugs and how-to"}]}`), Notes: "Wire the email into Text; wire Category into a Branch."},
			},
			ConnectionFields: connFields(cfg),
			ExecutionModel:   core.ExecutionBatch, ProcessModel: core.ProcessLongLived,
			Inputs: []core.Port{{Port: "text", Label: "Text"}},
			Outputs: []core.Port{
				{Port: "category", Label: "Category", MIME: []string{"text/plain"}},
				{Port: "confidence", Label: "Confidence", MIME: []string{"application/json"}},
			},
			ParamsSchema: schemaJSON(props, []string{"categories"}),
			Idempotent:   true, RetryPolicy: core.RetryExponentialBackoff,
		},
		Execute: func(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
			apiKey, jerr := resolveKey(job, cfg)
			if jerr != nil {
				return params.Err(job, jerr.Code, jerr.Message), nil
			}
			text := resolveText(job)
			if strings.TrimSpace(text) == "" {
				return params.Err(job, "bad_input", "no text — wire the Text input or fill the text field"), nil
			}
			names, descLines := categoryNames(paramObjList(job.Params, "categories"))
			if len(names) < 2 {
				return params.Err(job, "bad_param", "add at least two categories to classify into"), nil
			}
			allowNone := params.BoolDefault(job.Params, "allow_none", false)
			if allowNone {
				names = append(names, "none")
			}
			tool := &Tool{Name: "classify", Description: "Return the single best-matching category.", Schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"category":   map[string]any{"type": "string", "enum": names, "description": "The one category that best matches."},
					"confidence": map[string]any{"type": "number", "description": "How confident, 0 to 1."},
					"reason":     map[string]any{"type": "string", "description": "One short sentence on why."},
				},
				"required": []any{"category"},
			}}
			system := "Classify the user's text into exactly one of these categories, then return your choice via the tool:\n" + descLines
			if allowNone {
				system += "\nIf none fit, answer \"none\"."
			}
			temp := 0.0
			out, jerr := cfg.Provider.Call(ctx, apiKey, Request{
				Model: model(job, cfg), System: system, UserText: text, Tool: tool,
				Temperature: &temp, TimeoutMS: timeoutMS(job), BaseURL: baseURL(job),
			})
			if jerr != nil {
				return params.Err(job, jerr.Code, jerr.Message), nil
			}
			if out.Tool == nil {
				return params.Err(job, "ai_no_output", "the model did not return a category"), nil
			}
			category, _ := out.Tool["category"].(string)
			if category == "" {
				return params.Err(job, "ai_no_output", "the model returned an empty category"), nil
			}
			output := map[string]core.Ref{
				"category": {MIME: "text/plain", Inline: category},
				"response": {MIME: "application/json", Inline: out.Raw},
			}
			if conf, ok := out.Tool["confidence"]; ok {
				output["confidence"] = core.Ref{MIME: "application/json", Inline: conf}
			}
			return core.Result{JobID: job.ID, Status: core.StatusOK, Output: output}, nil
		},
	}
}

func categoryNames(cats []map[string]any) (names []string, descLines string) {
	var b strings.Builder
	for _, c := range cats {
		name, _ := c["name"].(string)
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		names = append(names, name)
		if desc, _ := c["description"].(string); strings.TrimSpace(desc) != "" {
			fmt.Fprintf(&b, "- %s: %s\n", name, strings.TrimSpace(desc))
		} else {
			fmt.Fprintf(&b, "- %s\n", name)
		}
	}
	return names, b.String()
}

// ---- Draft reply -----------------------------------------------------------

func draftReplyDrop(cfg Config) engine.NativeDrop {
	props := baseProps(cfg)
	props["text"] = map[string]any{"type": "string", "format": "multiline", "title": "Message to reply to", "description": "Or wire it into the Message input."}
	props["tone"] = map[string]any{"type": "string", "title": "Tone", "enum": []any{"friendly", "formal", "concise", "apologetic"}, "enumNames": []any{"Friendly", "Formal", "Concise", "Apologetic"}, "default": "friendly"}
	props["guidance"] = map[string]any{"type": "string", "format": "multiline", "title": "Anything to include?", "description": "e.g. 'offer a refund', 'point them to the docs'."}
	props["language"] = map[string]any{"type": "string", "title": "Reply language", "x_advanced": true}
	props["signature"] = map[string]any{"type": "string", "format": "multiline", "title": "Signature", "x_advanced": true, "description": "Appended to the end of the reply."}
	return engine.NativeDrop{
		Manifest: core.Manifest{
			ID:      taskID(cfg, "draft_reply"),
			Version: "1.0",
			Label:   cfg.Integration, Subtitle: "Draft reply",
			Summary:     "Write a suggested reply to an incoming message.",
			Description: "Feed in an incoming email or message and get a ready-to-send draft back. Choose the tone and add guidance. Pair with Await approval before sending.",
			Integration: cfg.Integration, Category: "ai", Icon: cfg.Icon, Color: cfg.Color, BrandLogo: cfg.BrandLogo,
			Provider: "internal", Tags: tags(cfg, "reply", "draft", "email", "response", "message"),
			Examples: []core.ParamsExample{
				{Title: "Friendly support reply", Params: json.RawMessage(`{"tone":"friendly","guidance":"Apologize for the delay."}`), Notes: "Wire the incoming message into Message; wire Reply into Await approval, then a send step."},
			},
			ConnectionFields: connFields(cfg),
			ExecutionModel:   core.ExecutionBatch, ProcessModel: core.ProcessLongLived,
			Inputs:       []core.Port{{Port: "text", Label: "Message"}},
			Outputs:      []core.Port{{Port: "reply", Label: "Reply", MIME: []string{"text/plain"}}},
			ParamsSchema: schemaJSON(props, nil),
			Idempotent:   true, RetryPolicy: core.RetryExponentialBackoff,
		},
		Execute: func(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
			apiKey, jerr := resolveKey(job, cfg)
			if jerr != nil {
				return params.Err(job, jerr.Code, jerr.Message), nil
			}
			text := resolveText(job)
			if strings.TrimSpace(text) == "" {
				return params.Err(job, "bad_input", "no message — wire the Message input or fill the field"), nil
			}
			temp := 0.5
			out, jerr := cfg.Provider.Call(ctx, apiKey, Request{
				Model:    model(job, cfg),
				System:   draftReplySystem(params.StringDefault(job.Params, "tone", "friendly"), params.StringDefault(job.Params, "guidance", ""), params.StringDefault(job.Params, "language", "")),
				UserText: text, Temperature: &temp, TimeoutMS: timeoutMS(job), BaseURL: baseURL(job),
			})
			if jerr != nil {
				return params.Err(job, jerr.Code, jerr.Message), nil
			}
			reply := strings.TrimSpace(out.Text)
			if sig := strings.TrimSpace(params.StringDefault(job.Params, "signature", "")); sig != "" {
				reply = reply + "\n\n" + sig
			}
			return core.Result{JobID: job.ID, Status: core.StatusOK, Output: map[string]core.Ref{
				"reply":    {MIME: "text/plain", Inline: reply},
				"response": {MIME: "application/json", Inline: out.Raw},
			}}, nil
		},
	}
}

func draftReplySystem(tone, guidance, language string) string {
	var b strings.Builder
	b.WriteString("You are drafting a reply to the message the user provides. ")
	switch tone {
	case "formal":
		b.WriteString("Write in a formal, professional tone. ")
	case "concise":
		b.WriteString("Write in a brief, to-the-point tone. ")
	case "apologetic":
		b.WriteString("Write in a warm, apologetic tone. ")
	default:
		b.WriteString("Write in a friendly, helpful tone. ")
	}
	if g := strings.TrimSpace(guidance); g != "" {
		fmt.Fprintf(&b, "Follow this guidance: %s. ", g)
	}
	if lang := strings.TrimSpace(language); lang != "" {
		fmt.Fprintf(&b, "Write the reply in %s. ", lang)
	}
	b.WriteString("Output only the reply body — no subject line, no signature, no preamble.")
	return b.String()
}
