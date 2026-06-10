package gmail

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"

	"git.sr.ht/~klahr/hazyflow/core"
	"git.sr.ht/~klahr/hazyflow/drops/internal/params"
	"git.sr.ht/~klahr/hazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "gmail_get_message",
			Version:     "1.0",
			Label:       "Gmail",
			Subtitle:    "Read email",
			Summary:     "Read one email — who sent it, the subject, the date, and the body.",
			Description: "Read a single email by its message ID. The typical pattern: wire Gmail · Search emails into a For each, put this step in the loop body, and set Message ID to the row's id — each matching email then comes out as friendly From / Subject / Date / Body values.",
			Integration: "Gmail",
			Category:    "network",
			Icon:        "mail-open",
			BrandLogo:   "/brands/gmail.svg",
			Color:       "#D14836",
			Provider:    "internal",
			Tags:        []string{"gmail", "email", "message", "fetch"},
			Examples: []core.ParamsExample{
				{Title: "Read each email a search found (inside For each)", Params: json.RawMessage(`{"account":"default","id":"${item.id}"}`)},
			},
			RequiresConnections: []core.ConnectionRequirement{
				{Kind: "oauth", Name: "google", Note: "Google OAuth — gmail.readonly scope."},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				// Editable on the card (inline pin editor — the port name
				// matches the string param) and wireable; a wired value
				// overrides the param. Inside a For each body this is
				// typically ${item.id} from Search emails' rows.
				{Port: "id", Label: "Message ID", MIME: []string{"text/plain"}},
			},
			Outputs: []core.Port{
				// Friendly scalar pins instead of a JSON blob — same move as
				// sheets append. The full flattened message is still EMITTED
				// under "message" for run records/debugging, just not a pin.
				{Port: "from", Label: "From", MIME: []string{"text/plain"}},
				{Port: "subject", Label: "Subject", MIME: []string{"text/plain"}},
				{Port: "date", Label: "Date", MIME: []string{"text/plain"}},
				{Port: "body", Label: "Body", MIME: []string{"text/plain"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"base_url":{"type":"string","description":"Override the API host (testing)."},
					"account":{"type":"string","default":"default"},
					"token":{"type":"string","description":"Raw access token; overrides 'account'."},
					"id":{"type":"string","title":"Message ID","description":"Which email to read. Overridden by the Message ID input when wired."},
					"format":{"type":"string","title":"Fetch detail","x_advanced":true,"enum":["full","metadata","minimal"],"default":"full","description":"How much of the message to fetch (advanced)."},
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
	// The Message ID input pin overrides the param when wired.
	idParam, _ := params.StringOpt(job.Params, "id")
	id, ok := textInputOr(job, "id", idParam)
	if !ok {
		return params.Err(job, "bad_input", "input port 'id' must be text"), nil
	}
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
	msg := flatten(raw)

	// Friendly scalars for the declared pins. Headers are name-keyed as
	// Gmail sent them; look up case-insensitively to be safe. Body prefers
	// the plain-text part, falls back to HTML, then the snippet.
	headers, _ := msg["headers"].(map[string]any)
	header := func(name string) string {
		for k, v := range headers {
			if strings.EqualFold(k, name) {
				return str(v)
			}
		}
		return ""
	}
	bodyText := str(msg["body_text"])
	if bodyText == "" {
		bodyText = str(msg["body_html"])
	}
	if bodyText == "" {
		bodyText = str(msg["snippet"])
	}

	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"from":    {MIME: "text/plain", Inline: header("From")},
			"subject": {MIME: "text/plain", Inline: header("Subject")},
			"date":    {MIME: "text/plain", Inline: header("Date")},
			"body":    {MIME: "text/plain", Inline: bodyText},
			// Full flattened message — emitted for run records, not a pin.
			"message": {MIME: "application/json", Inline: msg},
		},
	}, nil
}
