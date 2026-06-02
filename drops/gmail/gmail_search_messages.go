package gmail

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"

	"git.sr.ht/~klahr/hazyflow/core"
	"git.sr.ht/~klahr/hazyflow/drops/internal/params"
	"git.sr.ht/~klahr/hazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "gmail_search_messages",
			Version:     "1.0",
			Label:       "Gmail search messages",
			Summary:     "Search Gmail and return matching message IDs plus a pagination token.",
			Description: "Search the connected mailbox with a Gmail query string (e.g. 'newer_than:1d label:inbox'). Returns message ID stubs; pair with gmail_get_message via for_each to expand them.",
			Integration: "Gmail",
			Category:    "network",
			Icon:        "globe",
			BrandLogo:   "/brands/gmail.svg",
			Color:       "#D14836",
			Provider:    "internal",
			Tags:        []string{"gmail", "email", "search", "list"},
			Examples: []core.ParamsExample{
				{Title: "Unread from the last day", Params: json.RawMessage(`{"account":"default","query":"newer_than:1d is:unread","max_results":20}`)},
			},
			RequiresConnections: []core.ConnectionRequirement{
				{Kind: "oauth", Name: "google", Note: "Google OAuth — gmail.readonly scope."},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Outputs: []core.Port{
				{Port: "messages", Label: "Message ID stubs ({id, threadId})", MIME: []string{"application/json"}},
				{Port: "next_page_token", Label: "Pagination token (empty when done)", MIME: []string{"text/plain"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"base_url":{"type":"string","description":"Override the API host (testing)."},
					"account":{"type":"string","default":"default"},
					"token":{"type":"string","description":"Raw access token; overrides 'account'."},
					"query":{"type":"string","description":"Gmail search query (the same syntax as the Gmail search box)."},
					"max_results":{"type":"integer","default":50,"minimum":1,"maximum":500},
					"page_token":{"type":"string","description":"Pagination token from a prior next_page_token."},
					"timeout_ms":{"type":"integer","default":15000,"minimum":1}
				}
			}`),
			Idempotent: true,
		},
		Execute: executeGmailSearch,
	})
}

func executeGmailSearch(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	token, err := resolveToken(ctx, job)
	if err != nil {
		return params.Err(job, "auth", err.Error()), nil
	}
	q := url.Values{}
	q.Set("maxResults", strconv.Itoa(params.IntDefault(job.Params, "max_results", 50)))
	if query, _ := params.StringOpt(job.Params, "query"); query != "" {
		q.Set("q", query)
	}
	if pt, _ := params.StringOpt(job.Params, "page_token"); pt != "" {
		q.Set("pageToken", pt)
	}

	endpoint := baseURL(job) + "/users/me/messages?" + q.Encode()
	status, body, err := gmailDo(ctx, "GET", endpoint, token, "", nil, params.IntDefault(job.Params, "timeout_ms", 15000))
	if err != nil {
		return params.Err(job, "gmail_http_error", err.Error()), nil
	}
	if status < 200 || status >= 300 {
		return params.Err(job, "gmail_error", extractGmailError(body)), nil
	}

	var parsed struct {
		Messages      []any  `json:"messages"`
		NextPageToken string `json:"nextPageToken"`
	}
	_ = json.Unmarshal(body, &parsed)
	if parsed.Messages == nil {
		parsed.Messages = []any{}
	}
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"messages":        {MIME: "application/json", Inline: parsed.Messages},
			"next_page_token": {MIME: "text/plain", Inline: parsed.NextPageToken},
		},
	}, nil
}
