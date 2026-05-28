package gmail

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"git.sr.ht/~klahr/hazy-flow/core"
	"git.sr.ht/~klahr/hazy-flow/engine"
	"git.sr.ht/~klahr/hazy-flow/integrations/internal/params"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:             "gmail_search_messages",
			Version:        "1.0",
			Label:          "Gmail search messages",
			Color:          "#D14836",
			Icon:           "globe",
			BrandLogo:      "/brands/gmail.svg",
			Category:       "network",
			Provider:       "internal",
			Integration:    "Gmail",
			Tags:           []string{"gmail", "email", "search", "query", "google", "poll"},
			Description: "Search your Gmail inbox using normal Gmail search syntax (e.g. `is:unread newer_than:5m label:invoices`). Returns message IDs — pair with the get-message drop in a for_each loop to fetch full content. Natural fit for the 'react to new email' pattern with a polling trigger.",
			Summary:     "Search the connected Gmail mailbox with Gmail query syntax and return matching message IDs.",
			Examples: []core.ParamsExample{
				{
					Title:  "Recent unread invoices",
					Params: json.RawMessage(`{"query":"is:unread label:invoices newer_than:1d","max_results":50,"token":"${secret:GMAIL_OAUTH}"}`),
				},
				{
					Title:  "Alert emails from a known sender",
					Params: json.RawMessage(`{"query":"from:alerts@example.com newer_than:5m","max_results":25,"token":"${secret:GMAIL_OAUTH}"}`),
					Notes:  "Pairs cleanly with a poll_trigger using newer_than as the time window.",
				},
				{
					Title:  "Paginate to the next page",
					Params: json.RawMessage(`{"query":"label:invoices","max_results":100,"page_token":"CIDEgPCM...","token":"${secret:GMAIL_OAUTH}"}`),
				},
			},
			RequiresConnections: []string{"gmail"},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Outputs: []core.Port{
				{Port: "messages", Label: "Message IDs", MIME: []string{"application/json"}},
				{Port: "next_page_token", Label: "Pagination cursor (empty when done)", MIME: []string{"text/plain"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"account":     {"type":"string","default":"default"},
					"token":       {"type":"string","description":"Raw access token; overrides 'account'."},
					"query":       {"type":"string","default":"","description":"Gmail search query. Examples: 'is:unread', 'from:alerts@example.com newer_than:1h', 'label:invoices has:attachment'."},
					"max_results": {"type":"integer","default":50,"minimum":1,"maximum":500},
					"page_token":  {"type":"string","description":"Cursor for fetching the next page (from a previous run's next_page_token output)."},
					"timeout_ms":  {"type":"integer","default":15000,"minimum":1}
				}
			}`),
			Idempotent: true,
		},
		Execute: executeGmailSearchMessages,
	})
}

// executeGmailSearchMessages calls users.messages.list. The
// returned `messages` array contains {id, threadId} pairs — no
// snippet, no headers, no body. For full content the user wires
// gmail_get_message downstream (typically via for_each over the
// id list).
//
// Pagination: Gmail returns a next_page_token when there are more
// results. We surface it on its own output port so the user can
// loop until it's empty (or just take the first page, which is the
// common "fire on new" case where query is time-bounded).
func executeGmailSearchMessages(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	token, err := resolveToken(ctx, job)
	if err != nil {
		return params.Err(job, "auth", err.Error()), nil
	}

	q := url.Values{}
	if query, ok := params.StringOpt(job.Params, "query"); ok && query != "" {
		q.Set("q", query)
	}
	q.Set("maxResults", strconv.Itoa(params.IntDefault(job.Params, "max_results", 50)))
	if pt, ok := params.StringOpt(job.Params, "page_token"); ok && pt != "" {
		q.Set("pageToken", pt)
	}

	endpoint := currentHTTPBase() + "/users/me/messages?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return params.Err(job, "internal", err.Error()), nil
	}
	req.Header.Set("Authorization", "Bearer "+token)

	timeoutMs := params.IntDefault(job.Params, "timeout_ms", 15000)
	client := &http.Client{Timeout: time.Duration(timeoutMs) * time.Millisecond}
	resp, err := client.Do(req)
	if err != nil {
		return params.Err(job, "send_failed", err.Error()), nil
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return params.Err(job, "gmail_error",
			fmt.Sprintf("Gmail returned %d: %s", resp.StatusCode, extractGmailError(body))), nil
	}

	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		return params.Err(job, "parse", err.Error()), nil
	}
	messages, _ := parsed["messages"].([]any)
	if messages == nil {
		// Gmail omits the field entirely when there are no results;
		// downstream consumers expect an empty array, not nil.
		messages = []any{}
	}
	nextPageToken := stringField(parsed, "nextPageToken")

	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"messages":        {MIME: "application/json", Inline: messages},
			"next_page_token": {MIME: "text/plain", Inline: nextPageToken},
		},
	}, nil
}
