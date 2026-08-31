// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package notion

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/internal/params"
	"github.com/dazyflow/dazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "notion_create_page",
			Version:     "1.0",
			Label:       "Notion",
			Subtitle:    "Create page",
			Summary:     "Add a page to Notion — a row in a database or a subpage — with a title and body text.",
			Description: "Add a page to Notion. Type a Title and an optional Page body (blank lines start new paragraphs), then pick where it goes: a database (the page becomes a row) or a parent page (it becomes a subpage). Title and Page body can also be connected from an earlier step — a connection overrides the typed value. Extra database columns (Status, dates, tags…) go in the advanced 'properties' field as raw Notion JSON.",
			Integration: "Notion",
			Category:    "network",
			Icon:        "file-output",
			BrandLogo:   "/brands/notion.svg",
			Color:       "#000000",
			Provider:    "internal",
			Tags:        []string{"notion", "page", "create", "write"},
			Examples: []core.ParamsExample{
				{Title: "Add a row to a tasks database", Params: json.RawMessage(`{"account":"default","title":"Follow up with Ada","parent_database_id":"11111111-2222-3333-4444-555555555555"}`)},
				{Title: "Row with extra columns (advanced)", Params: json.RawMessage(`{"account":"default","title":"Follow up with Ada","parent_database_id":"11111111-2222-3333-4444-555555555555","properties":{"Status":{"select":{"name":"Todo"}}}}`)},
			},
			RequiresConnections: []core.ConnectionRequirement{
				{Kind: "secret", Name: "NOTION_TOKEN", Note: "Notion integration token (or connect via OAuth)."},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				// Editable on the card (inline pin editors — each port name
				// matches its string param) and wireable; a wired value
				// overrides the param. 'content' also accepts Notion block
				// objects wired in — an array/object passes through, plain
				// text becomes paragraph blocks.
				{Port: "title", Label: "Title", MIME: []string{"text/plain"}},
				{Port: "content", Label: "Page body"},
			},
			Outputs: []core.Port{
				// Friendly scalar pins — same move as gmail_get_message. The
				// full Notion page object is still EMITTED under "meta" for
				// run records/debugging, just not a pin.
				{Port: "title", Label: "Title", MIME: []string{"text/plain"}},
				{Port: "url", Label: "Page URL", MIME: []string{"text/plain"}},
				{Port: "id", Label: "Page ID", MIME: []string{"text/plain"}},
				{Port: "meta", Label: "Details", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"account":{"type":"string","default":"default"},
					"token":{"type":"string","description":"Raw Notion token; overrides 'account'."},
					"title":{"type":"string","title":"Title","description":"The page title. Overridden by the 'Title' input when connected."},
					"content":{"type":"string","title":"Page body","description":"Plain text for the page body — blank lines start a new paragraph. Overridden by the 'Page body' input when connected."},
					"parent_database_id":{"type":"string","title":"Add to database","description":"The database the page goes into (as a new row) — paste its ID. Set this or 'Add under page', not both."},
					"parent_page_id":{"type":"string","title":"Add under page","description":"The page the new page goes under (as a subpage) — paste its ID. Set this or 'Add to database', not both."},
					"properties":{"type":"object","title":"Properties (Notion JSON)","x_advanced":true,"description":"Raw Notion properties object for extra database columns, passed through verbatim and merged with Title (advanced)."},
					"children":{"type":"array","items":{},"title":"Body blocks (Notion JSON)","x_advanced":true,"description":"Raw Notion block objects for the page body (advanced)."},
					"timeout_ms":{"type":"integer","default":15000,"minimum":1,"description":"Hard deadline for the request, in milliseconds."}
				}
			}`),
			Idempotent: false,
			// Notion's create-page API has no idempotency key, so a retry
			// genuinely creates a duplicate page — delivery is fire-once
			// (same as gmail/sheets/slack send).
			RetryPolicy: core.RetryNever,
			// A non-idempotent external write: opt into engine-side dedupe so an
			// expired-lease reclaim or crash recovery replays the recorded result
			// instead of firing the write a second time. Matches the other
			// send-style drops (discord/gmail/sheets/twilio/klarna/nshift/elks).
			DedupeWrites: true,
		},
		Execute: executeNotionCreatePage,
	})
}

func executeNotionCreatePage(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	dbID, _ := params.StringOpt(job.Params, "parent_database_id")
	pgID, _ := params.StringOpt(job.Params, "parent_page_id")
	if (dbID == "") == (pgID == "") {
		return params.Err(job, "bad_param", "set exactly one of 'Add to database' or 'Add under page'"), nil
	}
	// The Title input pin overrides the param when wired.
	title, ok := params.TextInputOr(job, "title", params.StringDefault(job.Params, "title", ""))
	if !ok {
		return params.Err(job, "bad_input", "'Title' input must be text"), nil
	}
	props, errMsg := mergedProperties(job, title)
	if errMsg != "" {
		return params.Err(job, "bad_param", errMsg), nil
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
	// The Page body input overrides the param; both turn plain text into
	// paragraph blocks (wired block objects pass through as-is).
	if in, ok := job.Input["content"]; ok && in.Inline != nil {
		children = append(children, contentBlocks(in.Inline)...)
	} else if c := params.StringDefault(job.Params, "content", ""); strings.TrimSpace(c) != "" {
		children = append(children, paragraphsToBlocks(c)...)
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
			"title": {MIME: "text/plain", Inline: pageTitle(meta)},
			"url":   {MIME: "text/plain", Inline: page.URL},
			"id":    {MIME: "text/plain", Inline: page.ID},
			// Full Notion page object — emitted for run records, not a pin.
			"meta": {MIME: "application/json", Inline: meta},
		},
	}, nil
}

// mergedProperties combines the friendly Title with the advanced raw
// 'properties' param. Title becomes the page's title property under the key
// "title" — Notion's fixed ID for the title property, valid for both
// database and page parents — unless the raw properties already carry a
// title-type property (then the raw one wins). A page needs at least a
// title, so empty-both is rejected via the returned message.
func mergedProperties(job core.Job, title string) (props any, errMsg string) {
	raw := job.Params["properties"]
	base, isMap := raw.(map[string]any)
	if raw != nil && !isMap {
		// Non-object 'properties' param: pass it through verbatim and let
		// Notion report the shape error.
		return raw, ""
	}
	merged := make(map[string]any, len(base)+1)
	for k, v := range base {
		merged[k] = v
	}
	if title != "" && !hasTitleProperty(merged) {
		merged["title"] = map[string]any{"title": richTextChunks(title)}
	}
	if len(merged) == 0 {
		return nil, "give the page a Title — set the param, connect the 'Title' input, or set 'properties'"
	}
	return merged, ""
}

// hasTitleProperty reports whether a raw properties object already sets a
// title-type property (a value object carrying a "title" key).
func hasTitleProperty(props map[string]any) bool {
	for _, v := range props {
		if m, ok := v.(map[string]any); ok {
			if _, has := m["title"]; has {
				return true
			}
		}
	}
	return false
}

// pageTitle pulls the plain-text title out of a Notion page object — the
// property whose type is "title", whatever the database named it.
func pageTitle(page map[string]any) string {
	props, _ := page["properties"].(map[string]any)
	for _, v := range props {
		m, ok := v.(map[string]any)
		if !ok {
			continue
		}
		if t, _ := m["type"].(string); t == "title" {
			return richTextPlain(m["title"])
		}
	}
	return ""
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
