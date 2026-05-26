package notion

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"git.sr.ht/~klahr/hazy-flow/core"
	"git.sr.ht/~klahr/hazy-flow/engine"
	"git.sr.ht/~klahr/hazy-flow/integrations/internal/params"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:             "notion_query_database",
			Version:        "1.0",
			Label:          "Notion query database",
			Color:          "#000000",
			Icon:           "database",
			BrandLogo:      "/brands/notion.svg",
			Category:       "network",
			Provider:       "internal",
			Integration:    "Notion",
			Tags:           []string{"notion", "database", "query", "filter", "list"},
			Description:    "Query a Notion database. `filter` and `sorts` follow Notion's database-query schema. `page_size` defaults to 100 (max per Notion). Pair with `start_cursor` and the emitted `next_cursor` / `has_more` outputs to paginate, or with secret_set + poll_trigger to dedupe across runs (fire-on-new-row pattern). Returns the raw page objects on `pages`; rows are easier to consume via a downstream compute_rows that pulls out the properties you care about.",
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Outputs: []core.Port{
				{Port: "pages", Label: "Array of Notion page objects matching the query", MIME: []string{"application/json"}},
				{Port: "next_cursor", Label: "Cursor for the next page (empty when has_more=false)", MIME: []string{"text/plain"}},
				{Port: "has_more", Label: "Whether more pages exist beyond this batch", MIME: []string{"text/plain"}},
				{Port: "meta", Label: "Full Notion list-response object", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"account":      {"type":"string","default":"default"},
					"token":        {"type":"string","description":"Raw Notion bot token. Overrides 'account'."},
					"database_id":  {"type":"string","description":"UUID of the database to query."},
					"filter":       {"type":"object","description":"Notion filter object. See https://developers.notion.com/reference/post-database-query-filter."},
					"sorts":        {"type":"array","items":{},"description":"Notion sort objects."},
					"page_size":    {"type":"integer","default":100,"minimum":1,"maximum":100},
					"start_cursor": {"type":"string","description":"Pagination cursor from a prior call's next_cursor."},
					"timeout_ms":   {"type":"integer","default":15000,"minimum":1}
				},
				"required":["database_id"]
			}`),
			Idempotent: true,
		},
		Execute: executeNotionQueryDatabase,
	})
}

func executeNotionQueryDatabase(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	dbID, err := params.String(job.Params, "database_id")
	if err != nil {
		return params.Err(job, "bad_param", err.Error()), nil
	}
	token, err := resolveToken(ctx, job)
	if err != nil {
		return params.Err(job, "auth", err.Error()), nil
	}

	payload := map[string]any{}
	if filter, ok := job.Params["filter"]; ok && filter != nil {
		payload["filter"] = filter
	}
	if sorts, ok := job.Params["sorts"]; ok && sorts != nil {
		payload["sorts"] = sorts
	}
	if cur, ok := params.StringOpt(job.Params, "start_cursor"); ok && cur != "" {
		payload["start_cursor"] = cur
	}
	if ps := params.IntDefault(job.Params, "page_size", 0); ps > 0 {
		payload["page_size"] = ps
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return params.Err(job, "internal", fmt.Sprintf("marshal: %v", err)), nil
	}

	endpoint := fmt.Sprintf("%s/databases/%s/query", currentHTTPBase(), url.PathEscape(dbID))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return params.Err(job, "internal", err.Error()), nil
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Notion-Version", notionAPIVersion)

	timeoutMs := params.IntDefault(job.Params, "timeout_ms", 15000)
	client := &http.Client{Timeout: time.Duration(timeoutMs) * time.Millisecond}
	resp, err := client.Do(req)
	if err != nil {
		return params.Err(job, "send_failed", err.Error()), nil
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 5*1024*1024))

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return params.Err(job, "notion_error", decodeNotionError(respBody, resp.StatusCode)), nil
	}

	var parsed struct {
		Results    []map[string]any `json:"results"`
		NextCursor string           `json:"next_cursor"`
		HasMore    bool             `json:"has_more"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return params.Err(job, "parse", fmt.Sprintf("parse response: %v", err)), nil
	}
	var raw any
	_ = json.Unmarshal(respBody, &raw)

	hasMoreStr := "false"
	if parsed.HasMore {
		hasMoreStr = "true"
	}
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"pages":       {MIME: "application/json", Inline: parsed.Results},
			"next_cursor": {MIME: "text/plain", Inline: parsed.NextCursor},
			"has_more":    {MIME: "text/plain", Inline: hasMoreStr},
			"meta":        {MIME: "application/json", Inline: raw},
		},
	}, nil
}
