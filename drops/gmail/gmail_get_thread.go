// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package gmail

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/drops/internal/params"
	"git.sr.ht/~klahr/dazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "gmail_get_thread",
			Version:     "1.0",
			Label:       "Gmail",
			Subtitle:    "Read conversation",
			Summary:     "Read a whole email conversation and tell whether the other side has replied.",
			Description: "Read every message in one conversation, oldest first, and — the useful part — say whether anyone has answered yet. 'Replied' is No while the newest message in the thread is still one of yours, which is exactly the 'they haven't got back to me' test a follow-up flow needs. Wire Search emails' Matching emails into a For each and put this step in the loop body with Conversation = the row's threadId. 'Summary' is one row per conversation (subject, who last wrote, when, how many messages, replied) — collect those with Collect loop results to get a table of what's outstanding.",
			Integration: "Gmail",
			Category:    "network",
			Icon:        "mail-open",
			BrandLogo:   "/brands/gmail.svg",
			Color:       "#D14836",
			Provider:    "internal",
			Tags:        []string{"gmail", "email", "thread", "conversation", "reply", "follow-up", "sla"},
			Examples: []core.ParamsExample{
				{
					Title:  "Has this one been answered? (inside For each)",
					Params: json.RawMessage(`{"account":"default","id":"${item.threadId}"}`),
					Notes:  "Collect the Summary output with Collect loop results, then keep the rows where Replied is No.",
				},
			},
			RequiresConnections: []core.ConnectionRequirement{
				{Kind: "oauth", Name: "google", Note: "Google OAuth — gmail.readonly scope."},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				// Accepts a thread id, or a message row / search result whose
				// threadId is read out of it — so the obvious drag works.
				{Port: "id", Label: "Conversation", MIME: []string{"text/plain", "application/json"}},
			},
			Outputs: []core.Port{
				{Port: "replied", Label: "Replied", MIME: []string{core.MIMEBool}},
				{Port: "summary", Label: "Summary", MIME: []string{"application/json"}},
				{Port: "messages", Label: "Messages", MIME: []string{"application/json"}},
				{Port: "count", Label: "How many", MIME: []string{"text/plain"}},
				{Port: "last_from", Label: "Last from", MIME: []string{"text/plain"}},
				{Port: "last_date", Label: "Last message", MIME: []string{"text/plain"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"base_url":{"type":"string","description":"Override the API host (testing)."},
					"account":{"type":"string","default":"default"},
					"token":{"type":"string","description":"Raw access token; overrides 'account'."},
					"id":{"type":"string","title":"Conversation","description":"The conversation to read. Overridden by the Conversation input when wired."},
					"timeout_ms":{"type":"integer","default":15000,"minimum":1,"description":"Hard deadline for the request, in milliseconds."}
				},
				"required":["id"]
			}`),
			Idempotent: true,
		},
		Execute: executeGmailGetThread,
	})
}

func executeGmailGetThread(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	id, ok := resolveThreadID(job)
	if !ok {
		return params.Err(job, "bad_input", "input port 'id' must be a conversation id or a message it belongs to"), nil
	}
	if id == "" {
		return params.Err(job, "bad_param", "'id' is required"), nil
	}
	token, err := resolveToken(ctx, job)
	if err != nil {
		return params.Err(job, "auth", err.Error()), nil
	}

	endpoint := baseURL(job) + "/users/me/threads/" + url.PathEscape(id) + "?format=full"
	status, body, err := gmailDo(ctx, "GET", endpoint, token, "", nil, params.IntDefault(job.Params, "timeout_ms", 15000))
	if err != nil {
		return params.Err(job, "gmail_http_error", err.Error()), nil
	}
	if status < 200 || status >= 300 {
		return params.Err(job, "gmail_error", extractGmailError(body)), nil
	}
	var parsed struct {
		ID       string           `json:"id"`
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return params.Err(job, "gmail_error", "could not parse conversation: "+err.Error()), nil
	}

	rows := make([]map[string]any, 0, len(parsed.Messages))
	lastSent := true // a thread with no messages reads as "nothing came back"
	for _, raw := range parsed.Messages {
		flat := flatten(raw)
		row := friendlyMessage(flat)
		sent := hasLabel(raw, "SENT")
		row["sent"] = sent
		rows = append(rows, row)
		lastSent = sent
	}

	subject, lastFrom, lastDate := "", "", ""
	if n := len(rows); n > 0 {
		subject = str(rows[0]["subject"])
		lastFrom = str(rows[n-1]["from"])
		lastDate = str(rows[n-1]["date"])
	}
	// Gmail returns a thread's messages oldest first, so "have they answered?"
	// is simply "is the newest message not one of mine?".
	replied := len(rows) > 0 && !lastSent

	summary := map[string]any{
		"thread_id": id,
		"subject":   subject,
		"last_from": lastFrom,
		"last_date": lastDate,
		"count":     len(rows),
		"replied":   replied,
	}

	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"replied":   {MIME: core.MIMEBool, Inline: replied},
			"summary":   {MIME: "application/json", Inline: summary},
			"messages":  {MIME: "application/json", Inline: rows},
			"count":     {MIME: "text/plain", Inline: strconv.Itoa(len(rows))},
			"last_from": {MIME: "text/plain", Inline: lastFrom},
			"last_date": {MIME: "text/plain", Inline: lastDate},
		},
	}, nil
}

// hasLabel reports whether a raw Gmail message carries a label id.
func hasLabel(raw map[string]any, label string) bool {
	labels, _ := raw["labelIds"].([]any)
	for _, l := range labels {
		if strings.EqualFold(str(l), label) {
			return true
		}
	}
	return false
}

// resolveThreadID reads the conversation id from the wired input (a plain id,
// a message row carrying threadId, or a search result list — the first match),
// falling back to the typed param. Mirrors resolveMessageID so both steps
// accept the same obvious drags.
func resolveThreadID(job core.Job) (string, bool) {
	in, present := job.Input["id"]
	if !present || in.Inline == nil {
		return strings.TrimSpace(params.StringDefault(job.Params, "id", "")), true
	}
	switch v := in.Inline.(type) {
	case string:
		if s := strings.TrimSpace(v); s != "" {
			return s, true
		}
		return strings.TrimSpace(params.StringDefault(job.Params, "id", "")), true
	case map[string]any:
		return threadIDFromRow(v)
	case []any:
		for _, el := range v {
			if row, isRow := el.(map[string]any); isRow {
				return threadIDFromRow(row)
			}
		}
		return "", true // an empty list is "nothing to read", not a wiring mistake
	}
	return "", false
}

func threadIDFromRow(row map[string]any) (string, bool) {
	if s := strings.TrimSpace(str(row["threadId"])); s != "" {
		return s, true
	}
	// A row with only an id (e.g. a message record) still identifies its
	// conversation: Gmail's thread id equals the first message's id.
	if s := strings.TrimSpace(str(row["id"])); s != "" {
		return s, true
	}
	return "", false
}
