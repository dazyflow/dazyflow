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
			Description: "Read one email as friendly Date / From / Subject / Body values. Wire Search emails' Matching emails straight into Message ID to read the FIRST match — or, to read every match, wire Matching emails into a For each and put this step in the loop body with Message ID = the row's id.",
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
				// overrides the param. Accepts EITHER a single message ID
				// (text, e.g. ${item.id} inside a For each) OR Search emails'
				// "Matching emails" list wired straight in — then the FIRST
				// match is read. That makes the obvious drag (Matching
				// emails → Message ID) just work.
				{Port: "id", Label: "Email", MIME: []string{"text/plain", "application/json"}},
			},
			Outputs: []core.Port{
				// Friendly scalar pins instead of a JSON blob — same move as
				// sheets append. The full flattened message is still EMITTED
				// under "message" for run records/debugging, just not a pin.
				{Port: "date", Label: "Date", MIME: []string{"text/plain"}},
				{Port: "from", Label: "From", MIME: []string{"text/plain"}},
				{Port: "subject", Label: "Subject", MIME: []string{"text/plain"}},
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
					"timeout_ms":{"type":"integer","default":15000,"minimum":1,"description":"Hard deadline for the request, in milliseconds."}
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
	id, ok := resolveMessageID(job)
	if !ok {
		return params.Err(job, "bad_input", "input port 'id' must be a message ID or a list of matches"), nil
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

// resolveMessageID extracts the message ID to read. The Message ID input
// accepts three shapes so the obvious wirings all work; the param is the
// fallback when nothing is wired:
//
//   - a plain ID string (or templated ${item.id} / ${upstream…} text);
//   - ONE search stub {id, threadId} — e.g. a single match plucked upstream;
//   - a LIST of stubs (Search emails' "Matching emails" wired straight in) —
//     the FIRST match is read.
//
// ok=false only for a wired value of an unusable shape.
func resolveMessageID(job core.Job) (id string, ok bool) {
	fallback, _ := params.StringOpt(job.Params, "id")
	in, present := job.Input["id"]
	if !present || in.Inline == nil {
		return fallback, true
	}
	stubID := func(v any) string {
		m, isMap := v.(map[string]any)
		if !isMap {
			return ""
		}
		s, _ := m["id"].(string)
		return s
	}
	switch v := in.Inline.(type) {
	case string:
		if v != "" {
			return v, true
		}
		return fallback, true
	case []byte:
		if len(v) > 0 {
			return string(v), true
		}
		return fallback, true
	case map[string]any:
		if s := stubID(v); s != "" {
			return s, true
		}
		return "", false
	case []any:
		if len(v) == 0 {
			// An empty match list isn't a wiring mistake — fall back to the
			// param (and to the clear "'id' is required" error when unset).
			return fallback, true
		}
		if s := stubID(v[0]); s != "" {
			return s, true
		}
		if s, isStr := v[0].(string); isStr && s != "" {
			return s, true
		}
		return "", false
	}
	return "", false
}
