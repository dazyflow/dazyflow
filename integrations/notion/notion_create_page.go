package notion

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"git.sr.ht/~klahr/hazy-flow/core"
	"git.sr.ht/~klahr/hazy-flow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:             "notion_create_page",
			Version:        "1.0",
			Label:          "Notion create page",
			Color:          "#000000",
			Icon:           "file-output",
			BrandLogo:      "/brands/notion.svg",
			Category:       "network",
			Provider:       "internal",
			Integration:    "Notion",
			Tags:           []string{"notion", "page", "create", "database", "write"},
			Description:    "Create a Notion page. Set exactly one of `parent_database_id` (page becomes a row in that database — `properties` must match the database schema) or `parent_page_id` (page becomes a child of that page — typically `title` plus optional `children` blocks). `properties` is a Notion property-value object; `children` is an optional array of Block objects. Outputs the created page's id, url, and the full Notion response for advanced use.",
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Outputs: []core.Port{
				{Port: "id", Label: "Created page ID", MIME: []string{"text/plain"}},
				{Port: "url", Label: "Web URL of the new page", MIME: []string{"text/plain"}},
				{Port: "meta", Label: "Full Notion page object", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"account":            {"type":"string","default":"default","description":"OAuth account name. Matches the account used during /api/v1/oauth/notion/authorize."},
					"token":              {"type":"string","description":"Raw Notion bot token (secret_…). Overrides 'account'."},
					"parent_database_id": {"type":"string","description":"UUID of the parent database. Mutually exclusive with parent_page_id."},
					"parent_page_id":     {"type":"string","description":"UUID of the parent page. Mutually exclusive with parent_database_id."},
					"properties":         {"type":"object","description":"Notion property-value object. For database rows, keys must match the database schema."},
					"children":           {"type":"array","items":{},"description":"Optional Block objects to append as page content."},
					"timeout_ms":         {"type":"integer","default":15000,"minimum":1}
				},
				"required":["properties"]
			}`),
			Idempotent:  false, // Notion doesn't dedupe creates server-side.
			RetryPolicy: core.RetryExponentialBackoff,
		},
		Execute: executeNotionCreatePage,
	})
}

func executeNotionCreatePage(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	dbID, _ := paramStringOpt(job.Params, "parent_database_id")
	pgID, _ := paramStringOpt(job.Params, "parent_page_id")
	if (dbID == "") == (pgID == "") {
		return errResult(job, "bad_param",
			"set exactly one of parent_database_id (page becomes a database row) or parent_page_id (page becomes a child of an existing page)"), nil
	}
	propsRaw, ok := job.Params["properties"]
	if !ok || propsRaw == nil {
		return errResult(job, "bad_param", "missing param \"properties\""), nil
	}
	token, err := resolveToken(ctx, job)
	if err != nil {
		return errResult(job, "auth", err.Error()), nil
	}

	payload := map[string]any{"properties": propsRaw}
	if dbID != "" {
		payload["parent"] = map[string]any{"database_id": dbID}
	} else {
		payload["parent"] = map[string]any{"page_id": pgID}
	}
	if children, ok := job.Params["children"]; ok && children != nil {
		payload["children"] = children
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return errResult(job, "internal", fmt.Sprintf("marshal: %v", err)), nil
	}

	url := currentHTTPBase() + "/pages"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return errResult(job, "internal", err.Error()), nil
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Notion-Version", notionAPIVersion)

	timeoutMs := paramIntDefault(job.Params, "timeout_ms", 15000)
	client := &http.Client{Timeout: time.Duration(timeoutMs) * time.Millisecond}
	resp, err := client.Do(req)
	if err != nil {
		return errResult(job, "send_failed", err.Error()), nil
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 256*1024))

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return errResult(job, "notion_error", decodeNotionError(respBody, resp.StatusCode)), nil
	}

	var page map[string]any
	if err := json.Unmarshal(respBody, &page); err != nil {
		return errResult(job, "parse", fmt.Sprintf("parse response: %v", err)), nil
	}
	id, _ := page["id"].(string)
	pageURL, _ := page["url"].(string)
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"id":   {MIME: "text/plain", Inline: id},
			"url":  {MIME: "text/plain", Inline: pageURL},
			"meta": {MIME: "application/json", Inline: page},
		},
	}, nil
}
