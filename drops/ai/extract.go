package ai

import (
	"context"
	"encoding/json"
	"strings"

	"git.sr.ht/~klahr/hazyflow/core"
	"git.sr.ht/~klahr/hazyflow/drops/internal/params"
	"git.sr.ht/~klahr/hazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "ai_extract",
			Version:     "1.0",
			Label:       "Claude",
			Subtitle:    "Extract fields",
			Summary:     "Pull structured fields out of messy text into clean JSON.",
			Description: "Describe the fields you want — like amount, due date, or customer name — and AI reads the text and fills them in. Great for turning invoices, emails, and form replies into rows you can store or branch on.",
			Integration: "Claude",
			Category:    "ai",
			Icon:        "claude",
			Color:       "#cc7755",
			Provider:    "internal",
			Tags:        []string{"ai", "claude", "extract", "parse", "structured", "fields", "json"},
			Examples: []core.ParamsExample{
				{Title: "Parse an invoice email", Params: json.RawMessage(`{"fields":[{"name":"amount","description":"Total amount due","type":"number"},{"name":"due_date","description":"When payment is due","type":"date"},{"name":"vendor","description":"Who sent the invoice"}]}`), Notes: "Wire the email body into the Text input. Output 'data' is {amount, due_date, vendor}."},
			},
			ConnectionFields: connectionFields,
			ExecutionModel:   core.ExecutionBatch,
			ProcessModel:     core.ProcessLongLived,
			Inputs: []core.Port{
				{Port: "text", Label: "Text"},
			},
			Outputs: []core.Port{
				{Port: "data", Label: "Fields", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"text":{"type":"string","format":"multiline","title":"Text to read","description":"Or wire it into the Text input."},
					"fields":{
						"type":"array","title":"Fields to extract","minItems":1,
						"items":{"type":"object","properties":{
							"name":{"type":"string","title":"Field name"},
							"description":{"type":"string","title":"What it is"},
							"type":{"type":"string","title":"Type","enum":["string","number","boolean","date"],"enumNames":["Text","Number","Yes/No","Date"],"default":"string"}
						},"required":["name"]}
					},
					"on_missing":{"type":"string","title":"If a field isn't found","enum":["null","fail"],"enumNames":["Leave it empty","Fail the step"],"default":"null","x_advanced":true},
					"model":{"type":"string","x_advanced":true,"description":"Claude model id.","default":"claude-sonnet-4-6"},
					"api_key":{"type":"string","x_advanced":true,"description":"Injected from the Claude connection — leave unset."},
					"base_url":{"type":"string","x_advanced":true,"description":"Override the API host."},
					"timeout_ms":{"type":"integer","default":60000,"minimum":1}
				},
				"required":["fields"]
			}`),
			Idempotent:  true,
			RetryPolicy: core.RetryExponentialBackoff,
		},
		Execute: executeExtract,
	})
}

func executeExtract(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	text := resolveText(job)
	fields := paramObjList(job.Params, "fields")
	if len(fields) == 0 {
		return params.Err(job, "bad_param", "no fields to extract — add at least one field"), nil
	}
	onMissing := params.StringDefault(job.Params, "on_missing", "null")

	tool := buildExtractTool(fields, onMissing)
	if tool == nil {
		return params.Err(job, "bad_param", "no valid fields — each field needs a name"), nil
	}

	temp := 0.0
	out, je := callClaude(ctx, job, callOpts{
		system:      "Extract the requested fields from the user's text and return them via the tool. Read carefully and do not invent values. If a value is genuinely not present in the text, return null for that field.",
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
		return params.Err(job, "ai_no_output", "the model did not return any extracted fields"), nil
	}

	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"data":     {MIME: "application/json", Inline: out.tool},
			"response": {MIME: "application/json", Inline: out.raw},
		},
	}, nil
}

// buildExtractTool turns the author's field list into a forced tool whose
// input_schema is an object of those fields. With on_missing=="fail" every
// field is required and single-typed, so a model that omits one fails schema
// validation; otherwise each field's type is a [type, "null"] union so the
// model can legitimately return null for a field it can't find.
func buildExtractTool(fields []map[string]any, onMissing string) *toolDef {
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
	return &toolDef{
		name:        "extract",
		description: "Return the requested fields extracted from the text.",
		schema:      schema,
	}
}
