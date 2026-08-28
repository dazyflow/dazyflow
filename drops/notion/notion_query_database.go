// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package notion

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/drops/internal/params"
	"git.sr.ht/~klahr/dazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "notion_query_database",
			Version:     "1.0",
			Label:       "Notion",
			Subtitle:    "Query database",
			Summary:     "Read rows from a Notion database — each row comes out as simple column → value pairs.",
			Description: "Read rows from a Notion database. Each matching page becomes a simple record of its columns (Name, Status, dates, tags…) as plain values, plus its id and url — ready to log to a sheet, loop over with For each, or connect into Notion · Create page. Narrow or order the results with raw Notion JSON in the advanced Filter and Sort fields.",
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
			Inputs: []core.Port{
				// Editable on the card (inline pin editor — the port name
				// matches the string param) and wireable, so the target
				// database can be threaded from an upstream step; a wired
				// value overrides the param.
				{Port: "database_id", Label: "Database ID", MIME: []string{"text/plain"}},
			},
			Outputs: []core.Port{
				// Rows is a list of friendly records — {column name: plain
				// value, id, url} — flattened from Notion's page objects, the
				// same shape sheets_read_range emits. The raw page objects
				// ("pages"), pagination fields ("next_cursor", "has_more")
				// and the full list response ("meta") are still EMITTED for
				// run records and API callers that paginate by hand, but not
				// declared: raw Notion JSON and hand-rolled pagination are
				// dev plumbing.
				{Port: "rows", Label: "Rows", MIME: []string{"application/json"}},
				{Port: "meta", Label: "Details", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"account":{"type":"string","default":"default"},
					"token":{"type":"string","description":"Raw Notion token; overrides 'account'."},
					"database_id":{"type":"string","title":"Database ID","description":"Which database to read — paste its ID. Overridden by the 'Database ID' input when connected."},
					"page_size":{"type":"integer","title":"Max results","minimum":1,"maximum":100,"description":"How many rows to return at most (Notion caps this at 100)."},
					"filter":{"type":"object","title":"Filter (Notion JSON)","x_advanced":true,"description":"Raw Notion filter object, passed through verbatim (advanced)."},
					"sorts":{"type":"array","items":{},"title":"Sort (Notion JSON)","x_advanced":true,"description":"Raw Notion sorts array, passed through verbatim (advanced)."},
					"start_cursor":{"type":"string","title":"Start cursor","x_advanced":true,"description":"Pagination cursor from a prior run's next_cursor output (advanced)."},
					"timeout_ms":{"type":"integer","default":15000,"minimum":1,"description":"Hard deadline for the request, in milliseconds."}
				},
				"required":["database_id"]
			}`),
			Idempotent: true,
		},
		Execute: executeNotionQueryDatabase,
	})
}

func executeNotionQueryDatabase(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	// The Database ID input pin overrides the param when wired.
	dbID, ok := params.TextInputOr(job, "database_id", params.StringDefault(job.Params, "database_id", ""))
	if !ok {
		return params.Err(job, "bad_input", "'Database ID' input must be text"), nil
	}
	if dbID == "" {
		return params.Err(job, "bad_param", "'database_id' is required — set it or connect the 'Database ID' input"), nil
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
	rows := make([]any, len(r.Results))
	for i, p := range r.Results {
		rows[i] = flattenNotionPage(p)
	}
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"rows": {MIME: "application/json", Inline: rows},
			// Raw page objects, pagination and the full list response —
			// emitted for run records/API callers, not pins.
			"pages":       {MIME: "application/json", Inline: r.Results},
			"next_cursor": {MIME: "text/plain", Inline: r.NextCursor},
			"has_more":    {MIME: core.MIMEBool, Inline: r.HasMore},
			"meta":        {MIME: "application/json", Inline: meta},
		},
	}, nil
}

// flattenNotionPage turns one Notion page object into a friendly record:
// {column name: plain value} for every property, plus the page's id and url
// (which win on a name collision) so each row can drive a For each or wire
// into other Notion steps. Non-page values pass through as-is.
func flattenNotionPage(p any) any {
	page, ok := p.(map[string]any)
	if !ok {
		return p
	}
	props, _ := page["properties"].(map[string]any)
	row := make(map[string]any, len(props)+2)
	for name, v := range props {
		row[name] = propertyPlain(v)
	}
	if id, ok := page["id"].(string); ok && id != "" {
		row["id"] = id
	}
	if u, ok := page["url"].(string); ok && u != "" {
		row["url"] = u
	}
	// Page-level timestamps ride along too — sync/mirror flows key on them
	// (e.g. the Notion→Postgres mirror template selects created_time and
	// last_edited_time for its upsert).
	if ct, ok := page["created_time"].(string); ok && ct != "" {
		row["created_time"] = ct
	}
	if et, ok := page["last_edited_time"].(string); ok && et != "" {
		row["last_edited_time"] = et
	}
	return row
}

// propertyPlain reduces one Notion property value to the plain text /
// number / name a person sees in Notion, not the API envelope. Unknown
// types fall back to their raw typed payload.
func propertyPlain(v any) any {
	m, ok := v.(map[string]any)
	if !ok {
		return v
	}
	t, _ := m["type"].(string)
	switch t {
	case "title", "rich_text":
		return richTextPlain(m[t])
	case "select", "status":
		return optionName(m[t])
	case "multi_select", "people", "relation":
		return joinPlain(m[t])
	case "date":
		return datePlain(m[t])
	case "created_by", "last_edited_by":
		return optionName(m[t])
	case "formula", "rollup":
		// Nested one level deep with the same {type, <type>: value} shape.
		return propertyPlain(m[t])
	case "":
		return v
	}
	// number, checkbox, url, email, phone_number, created_time, string,
	// boolean, … — the typed payload is already a plain value.
	return m[t]
}

// optionName pulls the human label off a select/status/person object —
// its name, falling back to its id.
func optionName(v any) any {
	m, ok := v.(map[string]any)
	if !ok {
		return v
	}
	if n, ok := m["name"].(string); ok && n != "" {
		return n
	}
	if id, ok := m["id"].(string); ok {
		return id
	}
	return nil
}

// joinPlain renders a list property (multi-select tags, people,
// relations) as "A, B, C".
func joinPlain(v any) any {
	l, ok := v.([]any)
	if !ok {
		return v
	}
	parts := make([]string, 0, len(l))
	for _, e := range l {
		if n, ok := optionName(e).(string); ok && n != "" {
			parts = append(parts, n)
		}
	}
	return strings.Join(parts, ", ")
}

// datePlain renders a Notion date as its start, or "start → end" for a
// range.
func datePlain(v any) any {
	m, ok := v.(map[string]any)
	if !ok {
		return v
	}
	start, _ := m["start"].(string)
	if end, _ := m["end"].(string); end != "" {
		return start + " → " + end
	}
	return start
}

// richTextPlain concatenates a rich-text array's plain text.
func richTextPlain(v any) string {
	l, _ := v.([]any)
	var b strings.Builder
	for _, e := range l {
		m, ok := e.(map[string]any)
		if !ok {
			continue
		}
		if s, ok := m["plain_text"].(string); ok {
			b.WriteString(s)
			continue
		}
		if txt, ok := m["text"].(map[string]any); ok {
			if s, ok := txt["content"].(string); ok {
				b.WriteString(s)
			}
		}
	}
	return b.String()
}
