package notion

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"git.sr.ht/~klahr/hazy-flow/core"
	"git.sr.ht/~klahr/hazy-flow/engine"
	"git.sr.ht/~klahr/hazy-flow/integrations/internal/params"
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
			Description:    "Create a Notion page. Set the parent — a database, in which case the page becomes a row, or another page, in which case it becomes a child. Properties define the field values; optional child blocks fill the page body. Wire upstream text (for example an AI summary or an HTTP response) into the 'content' input and it is appended to the page body as paragraph blocks. That is the way to drop a value computed earlier in the flow into the page. Outputs the new page's id and URL.",
			Summary:        "Create a Notion page as either a database row or a child of another page, with optional block content and a 'content' input for upstream text.",
			Examples: []core.ParamsExample{
				{
					Title:  "Add a row to a tasks database",
					Params: json.RawMessage(`{"account":"default","parent_database_id":"11111111-2222-3333-4444-555555555555","properties":{"Name":{"title":[{"text":{"content":"Review Q3 numbers"}}]},"Status":{"select":{"name":"Todo"}}}}`),
				},
				{
					Title:  "Create a child page with body blocks under another page",
					Params: json.RawMessage(`{"token":"${secret:NOTION_TOKEN}","parent_page_id":"aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee","properties":{"title":[{"text":{"content":"Meeting notes 2026-05-28"}}]},"children":[{"object":"block","type":"paragraph","paragraph":{"rich_text":[{"type":"text","text":{"content":"Attendees: …"}}]}}]}`),
					Notes:  "For child pages the title goes inside properties.title (not into a named property).",
				},
			},
			RequiresConnections: []core.ConnectionRequirement{
				{Kind: "secret", Name: "NOTION_TOKEN", Note: "Notion internal integration token (Notion uses tokens, not OAuth for most server-to-server use)."},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				{Port: "content", Label: "Body text appended as paragraph block(s) (optional)", Required: false},
			},
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
	dbID, _ := params.StringOpt(job.Params, "parent_database_id")
	pgID, _ := params.StringOpt(job.Params, "parent_page_id")
	if (dbID == "") == (pgID == "") {
		return params.Err(job, "bad_param",
			"set exactly one of parent_database_id (page becomes a database row) or parent_page_id (page becomes a child of an existing page)"), nil
	}
	propsRaw, ok := job.Params["properties"]
	if !ok || propsRaw == nil {
		return params.Err(job, "bad_param", "missing param \"properties\""), nil
	}
	token, err := resolveToken(ctx, job)
	if err != nil {
		return params.Err(job, "auth", err.Error()), nil
	}

	payload := map[string]any{"properties": propsRaw}
	if dbID != "" {
		payload["parent"] = map[string]any{"database_id": dbID}
	} else {
		payload["parent"] = map[string]any{"page_id": pgID}
	}
	children := childBlocks(job)
	if c, ok := job.Input["content"]; ok {
		children = append(children, contentBlocks(c.Inline)...)
	}
	if len(children) > 0 {
		payload["children"] = children
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return params.Err(job, "internal", fmt.Sprintf("marshal: %v", err)), nil
	}

	url := currentHTTPBase() + "/pages"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
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
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 256*1024))

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return params.Err(job, "notion_error", decodeNotionError(respBody, resp.StatusCode)), nil
	}

	var page map[string]any
	if err := json.Unmarshal(respBody, &page); err != nil {
		return params.Err(job, "parse", fmt.Sprintf("parse response: %v", err)), nil
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

// childBlocks returns the block array from params.children, or nil. The
// 'content' input is appended after these so a flow can mix authored
// blocks with upstream text.
func childBlocks(job core.Job) []any {
	raw, ok := job.Params["children"]
	if !ok || raw == nil {
		return nil
	}
	if arr, ok := raw.([]any); ok {
		return append([]any(nil), arr...)
	}
	return nil
}

// notionContentLimit is Notion's cap on the character count of a single
// rich-text object. Longer paragraphs are split across several text
// objects within the same block so nothing is silently truncated.
const notionContentLimit = 2000

// contentBlocks turns the 'content' input into Notion paragraph blocks.
// An already-structured block array (or single block object) passes
// through untouched; any other value is stringified, split into
// paragraphs on blank lines, and emitted as paragraph blocks. Empty or
// nil content yields no blocks.
func contentBlocks(inline any) []any {
	switch v := inline.(type) {
	case nil:
		return nil
	case []any:
		// Caller already produced Notion block objects.
		return v
	case map[string]any:
		return []any{v}
	case string:
		return paragraphsToBlocks(v)
	default:
		return paragraphsToBlocks(fmt.Sprint(v))
	}
}

// paragraphsToBlocks splits text on blank lines into paragraph blocks,
// chunking each paragraph's rich-text to Notion's per-object limit.
func paragraphsToBlocks(text string) []any {
	var blocks []any
	for para := range strings.SplitSeq(strings.TrimSpace(text), "\n\n") {
		para = strings.TrimSpace(para)
		if para == "" {
			continue
		}
		blocks = append(blocks, map[string]any{
			"object":    "block",
			"type":      "paragraph",
			"paragraph": map[string]any{"rich_text": richTextChunks(para)},
		})
	}
	return blocks
}

// richTextChunks splits s into Notion rich-text objects no longer than
// notionContentLimit runes each.
func richTextChunks(s string) []any {
	runes := []rune(s)
	var chunks []any
	for len(runes) > 0 {
		n := min(len(runes), notionContentLimit)
		chunks = append(chunks, map[string]any{
			"type": "text",
			"text": map[string]any{"content": string(runes[:n])},
		})
		runes = runes[n:]
	}
	return chunks
}
