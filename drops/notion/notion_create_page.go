package notion

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
			ID:          "notion_create_page",
			Version:     "1.0",
			Label:       "Notion create page",
			Summary:     "Create a Notion page under a database or page, with properties and optional body content.",
			Description: "Create a Notion page. Set its properties (title, status, etc.) and an optional body via the 'content' input — plain text becomes paragraph blocks, or pass Notion block objects directly. Parent must be exactly one of a database or a page.",
			Integration: "Notion",
			Category:    "network",
			Icon:        "file-output",
			BrandLogo:   "/brands/notion.svg",
			Color:       "#000000",
			Provider:    "internal",
			Tags:        []string{"notion", "page", "create", "write"},
			Examples: []core.ParamsExample{
				{Title: "Add a row to a tasks database", Params: json.RawMessage(`{"account":"default","parent_database_id":"11111111-2222-3333-4444-555555555555","properties":{"Name":{"title":[{"text":{"content":"Follow up with Ada"}}]}}}`)},
			},
			RequiresConnections: []core.ConnectionRequirement{
				{Kind: "secret", Name: "NOTION_TOKEN", Note: "Notion integration token (or connect via OAuth)."},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				{Port: "content", Label: "Page body — plain text (→ paragraphs) or Notion block objects"},
			},
			Outputs: []core.Port{
				{Port: "id", Label: "Created page ID", MIME: []string{"text/plain"}},
				{Port: "url", Label: "Created page URL", MIME: []string{"text/plain"}},
				{Port: "meta", Label: "Full Notion page object", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"account":{"type":"string","default":"default"},
					"token":{"type":"string","description":"Raw Notion token; overrides 'account'."},
					"parent_database_id":{"type":"string","description":"Parent database UUID. Set exactly one of this or parent_page_id."},
					"parent_page_id":{"type":"string","description":"Parent page UUID. Set exactly one of this or parent_database_id."},
					"properties":{"type":"object","description":"Notion properties object (passed through verbatim)."},
					"children":{"type":"array","items":{},"description":"Notion block objects for the page body."},
					"timeout_ms":{"type":"integer","default":15000,"minimum":1}
				},
				"required":["properties"]
			}`),
			Idempotent:  false,
			RetryPolicy: core.RetryExponentialBackoff,
		},
		Execute: executeNotionCreatePage,
	})
}

func executeNotionCreatePage(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	dbID, _ := params.StringOpt(job.Params, "parent_database_id")
	pgID, _ := params.StringOpt(job.Params, "parent_page_id")
	if (dbID == "") == (pgID == "") {
		return params.Err(job, "bad_param", "set exactly one of parent_database_id or parent_page_id"), nil
	}
	props, ok := job.Params["properties"]
	if !ok || props == nil {
		return params.Err(job, "bad_param", `missing param "properties"`), nil
	}
	token, err := resolveToken(ctx, job)
	if err != nil {
		return params.Err(job, "auth", err.Error()), nil
	}

	parent := map[string]any{}
	if dbID != "" {
		parent["database_id"] = dbID
	} else {
		parent["page_id"] = pgID
	}
	payload := map[string]any{"properties": props, "parent": parent}

	var children []any
	if c, ok := job.Params["children"].([]any); ok {
		children = append(children, c...)
	}
	if in, ok := job.Input["content"]; ok && in.Inline != nil {
		children = append(children, contentBlocks(in.Inline)...)
	}
	if len(children) > 0 {
		payload["children"] = children
	}
	raw, _ := json.Marshal(payload)

	status, body, err := notionDo(ctx, "POST", currentHTTPBase()+"/pages", token, raw, params.IntDefault(job.Params, "timeout_ms", 15000))
	if err != nil {
		return params.Err(job, "notion_http_error", err.Error()), nil
	}
	if status < 200 || status >= 300 {
		return params.Err(job, "notion_error", notionError(status, body)), nil
	}

	var page struct {
		ID  string `json:"id"`
		URL string `json:"url"`
	}
	_ = json.Unmarshal(body, &page)
	var meta map[string]any
	_ = json.Unmarshal(body, &meta)
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"id":   {MIME: "text/plain", Inline: page.ID},
			"url":  {MIME: "text/plain", Inline: page.URL},
			"meta": {MIME: "application/json", Inline: meta},
		},
	}, nil
}

// contentBlocks converts a 'content' input value into Notion block
// objects: an array passes through; a single object is wrapped; a string
// becomes paragraph blocks (split on blank lines, chunked to Notion's
// rich-text length cap).
func contentBlocks(v any) []any {
	switch t := v.(type) {
	case nil:
		return nil
	case []any:
		return t
	case map[string]any:
		return []any{t}
	case string:
		return paragraphsToBlocks(t)
	default:
		return paragraphsToBlocks(stringify(t))
	}
}

func paragraphsToBlocks(text string) []any {
	var blocks []any
	for _, para := range strings.Split(strings.TrimSpace(text), "\n\n") {
		p := strings.TrimSpace(para)
		if p == "" {
			continue
		}
		blocks = append(blocks, map[string]any{
			"object": "block", "type": "paragraph",
			"paragraph": map[string]any{"rich_text": richTextChunks(p)},
		})
	}
	return blocks
}

func richTextChunks(s string) []any {
	runes := []rune(s)
	var out []any
	for i := 0; i < len(runes); i += richTextLimit {
		end := i + richTextLimit
		if end > len(runes) {
			end = len(runes)
		}
		out = append(out, map[string]any{"type": "text", "text": map[string]any{"content": string(runes[i:end])}})
	}
	return out
}

func stringify(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}
