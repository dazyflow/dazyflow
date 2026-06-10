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
			Label:       "Gmail",
			Subtitle:    "Search emails",
			Summary:     "Find emails in the connected mailbox, using the same search you'd type in Gmail.",
			Description: "Find emails in the connected mailbox. The search works exactly like Gmail's own search box (e.g. 'from:boss@company.com is:unread' or 'newer_than:1d'). Matching emails come out as a list of message IDs — wire it into a For each and read each one with Gmail · Read email.",
			Integration: "Gmail",
			Category:    "network",
			Icon:        "search",
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
			Inputs: []core.Port{
				// Editable on the card (inline pin editor — the port name
				// matches the string param) and wireable from upstream; a
				// wired value overrides the param.
				{Port: "query", Label: "Search", MIME: []string{"text/plain"}},
			},
			Outputs: []core.Port{
				// Matching emails is a list of {id, threadId} stubs (all the
				// Gmail search API returns) — feed it to For each + Read email
				// to expand. next_page_token is still EMITTED for API callers
				// that paginate by hand, but not declared: pagination is dev
				// plumbing a flow can't loop on anyway.
				{Port: "messages", Label: "Matching emails", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"base_url":{"type":"string","description":"Override the API host (testing)."},
					"account":{"type":"string","default":"default"},
					"token":{"type":"string","description":"Raw access token; overrides 'account'."},
					"query":{"type":"string","title":"Search","examples":["from:boss@company.com is:unread"],"description":"Works exactly like Gmail's search box, e.g. 'is:unread', 'newer_than:1d', 'from:someone@example.com'."},
					"max_results":{"type":"integer","title":"Max emails","default":50,"minimum":1,"maximum":500},
					"page_token":{"type":"string","title":"Page token","x_advanced":true,"description":"Pagination token from a prior run's next_page_token output (advanced)."},
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
	// The Search input pin overrides the param when wired (same pattern as
	// gmail send's to/subject/body).
	queryParam, _ := params.StringOpt(job.Params, "query")
	query, ok := textInputOr(job, "query", queryParam)
	if !ok {
		return params.Err(job, "bad_input", "input port 'query' must be text"), nil
	}
	if query != "" {
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
