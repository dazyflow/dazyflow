package notion

import (
	"context"
	"encoding/json"
	"net/url"

	"git.sr.ht/~klahr/hazyflow/core"
	"git.sr.ht/~klahr/hazyflow/drops/internal/params"
	"git.sr.ht/~klahr/hazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "notion_query_database",
			Version:     "1.0",
			Label:       "Notion query database",
			Summary:     "Fetch a page of Notion database rows by filter and sort, returning the page objects plus pagination cursor.",
			Description: "Query a Notion database with optional filters and sorting. Page objects come back raw; a downstream compute_rows extracts the properties you care about. Cursor outputs support polling.",
			Integration: "Notion",
			Category:    "network",
			Icon:        "database",
			BrandLogo:   "/brands/notion.svg",
			Color:       "#000000",
			Provider:    "internal",
			Tags:        []string{"notion", "database", "query", "list"},
			Examples: []core.ParamsExample{
				{Title: "Latest 25 Todo items", Params: json.RawMessage(`{"account":"default","database_id":"11111111-2222-3333-4444-555555555555","filter":{"property":"Status","select":{"equals":"Todo"}},"sorts":[{"property":"Created","direction":"descending"}],"page_size":25}`)},
			},
			RequiresConnections: []core.ConnectionRequirement{
				{Kind: "secret", Name: "NOTION_TOKEN", Note: "Notion integration token (or connect via OAuth)."},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Outputs: []core.Port{
				{Port: "pages", Label: "Array of Notion page objects", MIME: []string{"application/json"}},
				{Port: "next_cursor", Label: "Cursor for the next page (empty when done)", MIME: []string{"text/plain"}},
				{Port: "has_more", Label: "Whether more pages exist", MIME: []string{core.MIMEBool}},
				{Port: "meta", Label: "Full Notion list-response object", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"account":{"type":"string","default":"default"},
					"token":{"type":"string","description":"Raw Notion token; overrides 'account'."},
					"database_id":{"type":"string","description":"The Notion database UUID."},
					"filter":{"type":"object","description":"Notion filter object (passed through verbatim)."},
					"sorts":{"type":"array","items":{},"description":"Notion sorts array (passed through verbatim)."},
					"page_size":{"type":"integer","minimum":1,"maximum":100,"description":"Rows per page (Notion caps at 100)."},
					"start_cursor":{"type":"string","description":"Pagination cursor from a prior next_cursor."},
					"timeout_ms":{"type":"integer","default":15000,"minimum":1}
				},
				"required":["database_id"]
			}`),
			Idempotent: true,
		},
		Execute: executeNotionQueryDatabase,
	})
}

func executeNotionQueryDatabase(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	dbID, _ := params.StringOpt(job.Params, "database_id")
	if dbID == "" {
		return params.Err(job, "bad_param", "'database_id' is required"), nil
	}
	token, err := resolveToken(ctx, job)
	if err != nil {
		return params.Err(job, "auth", err.Error()), nil
	}

	payload := map[string]any{}
	if f, ok := job.Params["filter"]; ok && f != nil {
		payload["filter"] = f
	}
	if s, ok := job.Params["sorts"]; ok && s != nil {
		payload["sorts"] = s
	}
	if c, _ := params.StringOpt(job.Params, "start_cursor"); c != "" {
		payload["start_cursor"] = c
	}
	if ps := params.IntDefault(job.Params, "page_size", 0); ps > 0 {
		payload["page_size"] = ps
	}
	raw, _ := json.Marshal(payload)

	endpoint := currentHTTPBase() + "/databases/" + url.PathEscape(dbID) + "/query"
	status, body, err := notionDo(ctx, "POST", endpoint, token, raw, params.IntDefault(job.Params, "timeout_ms", 15000))
	if err != nil {
		return params.Err(job, "notion_http_error", err.Error()), nil
	}
	if status < 200 || status >= 300 {
		return params.Err(job, "notion_error", notionError(status, body)), nil
	}

	var r struct {
		Results    []any  `json:"results"`
		NextCursor string `json:"next_cursor"`
		HasMore    bool   `json:"has_more"`
	}
	_ = json.Unmarshal(body, &r)
	var meta map[string]any
	_ = json.Unmarshal(body, &meta)
	if r.Results == nil {
		r.Results = []any{}
	}
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"pages":       {MIME: "application/json", Inline: r.Results},
			"next_cursor": {MIME: "text/plain", Inline: r.NextCursor},
			"has_more":    {MIME: core.MIMEBool, Inline: r.HasMore},
			"meta":        {MIME: "application/json", Inline: meta},
		},
	}, nil
}
