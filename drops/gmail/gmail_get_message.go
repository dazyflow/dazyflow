package gmail

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
			ID:          "gmail_get_message",
			Version:     "1.0",
			Label:       "Gmail get message",
			Summary:     "Fetch a single Gmail message by ID and return its headers, snippet, and decoded body text.",
			Description: "Fetch one Gmail message by ID. Outputs the common headers (from, to, subject, date), a short snippet, and the plain-text/HTML body when present. Typically used after the search drop with a for_each to expand IDs into full messages.",
			Integration: "Gmail",
			Category:    "network",
			Icon:        "file-input",
			BrandLogo:   "/brands/gmail.svg",
			Color:       "#D14836",
			Provider:    "internal",
			Tags:        []string{"gmail", "email", "message", "fetch"},
			Examples: []core.ParamsExample{
				{Title: "Full message body for an ID from search", Params: json.RawMessage(`{"id":"18f9d3a2c0e1b4a5","token":"${secret.GMAIL_OAUTH}"}`)},
				{Title: "Headers-only fetch (faster)", Params: json.RawMessage(`{"id":"18f9d3a2c0e1b4a5","format":"metadata","token":"${secret.GMAIL_OAUTH}"}`)},
			},
			RequiresConnections: []core.ConnectionRequirement{
				{Kind: "oauth", Name: "google", Note: "Google OAuth — gmail.readonly scope."},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Outputs: []core.Port{
				{Port: "message", Label: "Message details", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"base_url":{"type":"string","description":"Override the API host (testing)."},
					"account":{"type":"string","default":"default"},
					"token":{"type":"string","description":"Raw access token; overrides 'account'."},
					"id":{"type":"string","description":"Gmail message ID (from gmail_search_messages)."},
					"format":{"type":"string","enum":["full","metadata","minimal"],"default":"full","description":"How much of the message to fetch."},
					"timeout_ms":{"type":"integer","default":15000,"minimum":1}
				},
				"required":["id"]
			}`),
			Idempotent: true,
		},
		Execute: executeGmailGetMessage,
	})
}

func executeGmailGetMessage(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	id, _ := params.StringOpt(job.Params, "id")
	if id == "" {
		return params.Err(job, "bad_param", "'id' is required"), nil
	}
	token, err := resolveToken(ctx, job)
	if err != nil {
		return params.Err(job, "auth", err.Error()), nil
	}

	q := url.Values{}
	q.Set("format", params.StringDefault(job.Params, "format", "full"))
	endpoint := baseURL(job) + "/users/me/messages/" + url.PathEscape(id) + "?" + q.Encode()
	status, body, err := gmailDo(ctx, "GET", endpoint, token, "", nil, params.IntDefault(job.Params, "timeout_ms", 15000))
	if err != nil {
		return params.Err(job, "gmail_http_error", err.Error()), nil
	}
	if status < 200 || status >= 300 {
		return params.Err(job, "gmail_error", extractGmailError(body)), nil
	}

	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return params.Err(job, "gmail_error", "could not parse message: "+err.Error()), nil
	}
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{"message": {MIME: "application/json", Inline: flatten(raw)}},
	}, nil
}
