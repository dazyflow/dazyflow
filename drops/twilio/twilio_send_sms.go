// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package twilio

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/drops/internal/params"
	"git.sr.ht/~klahr/dazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "twilio_send_sms",
			Version:     "1.0",
			Label:       "Twilio",
			Subtitle:    "Send SMS",
			Summary:     "Send an SMS text message via Twilio.",
			Description: "Send an SMS via Twilio. The recipient ('To') and message ('Body') can be typed on the step or wired in from upstream (the matching input port overrides the param). Send from one of your Twilio numbers ('From', in E.164 like +15551234567) or set a Messaging Service SID instead. Connect your Twilio account once on the Apps page.",
			Integration: "Twilio",
			Category:    "network",
			Icon:        "message-square",
			BrandLogo:   "/brands/twilio.svg",
			Color:       "#F22F46",
			Provider:    "internal",
			Tags:        []string{"twilio", "sms", "text", "message", "notify"},
			Examples: []core.ParamsExample{
				{Title: "Alert to a phone number", Params: json.RawMessage(`{"to":"+15558675309","from":"+15551234567","body":"Your order has shipped."}`), Notes: "Wire a trigger's phone/message outputs into the 'To'/'Body' pins instead of typing them."},
				{Title: "Send via a Messaging Service", Params: json.RawMessage(`{"to":"+15558675309","messaging_service_sid":"MGxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx","body":"Your order has shipped."}`)},
			},
			// Per-tenant service connection: Account SID + Auth Token entered once
			// on the Apps page (stored as conn.twilio.*), injected at run time —
			// not node params, so credentials never live in the graph.
			ConnectionFields: []core.ConnectionField{
				{Key: "account_sid", Label: "Account SID", Required: true, Placeholder: "ACxxxxxxxx…"},
				{Key: "auth_token", Label: "Auth token", Secret: true, Required: true},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				{Port: "to", Label: "To", Required: true, MIME: []string{"text/plain"}},
				{Port: "body", Label: "Body", Required: true, MIME: []string{"text/plain"}},
			},
			Outputs: []core.Port{
				{Port: "message_sid", Label: "Message ID", MIME: []string{"text/plain"}},
				{Port: "status", Label: "Status", MIME: []string{"text/plain"}},
				{Port: "meta", Label: "Details", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"to":{"type":"string","title":"To","examples":["+15558675309"],"description":"Recipient phone number in E.164 format. Overridden by the 'To' input."},
					"from":{"type":"string","title":"From","examples":["+15551234567"],"description":"One of your Twilio phone numbers (E.164). Leave blank if using a Messaging Service SID."},
					"messaging_service_sid":{"type":"string","title":"Messaging Service SID","examples":["MGxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"],"x_advanced":true,"description":"Send via a Twilio Messaging Service instead of a single 'From' number."},
					"body":{"type":"string","title":"Message","description":"The text to send. Overridden by the 'Body' input."},
					"base_url":{"type":"string","description":"Override the API host (testing)."},
					"timeout_ms":{"type":"integer","default":15000,"minimum":1,"description":"Hard deadline for the request, in milliseconds."}
				},
				"required":["to","body"]
			}`),
			Idempotent: false,
			// Twilio's Messages API has no generic idempotency header, so
			// a retried POST sends a second SMS — and double-bills. This
			// drop is a terminal leaf the engine auto-retries on backoff,
			// so retries must be off here.
			RetryPolicy: core.RetryNever,
			// …and the engine dedupes a same-job re-execution (expired-lease
			// reclaim / crash recovery) so a recovered run doesn't re-send.
			DedupeWrites: true,
		},
		Execute: executeSendSMS,
	})
}

func executeSendSMS(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	to, ok := params.TextInputOr(job, "to", params.StringDefault(job.Params, "to", ""))
	if !ok {
		return params.Err(job, "bad_input", "'To' input must be text"), nil
	}
	if strings.TrimSpace(to) == "" {
		return params.Err(job, "bad_param", "'to' is required — set it or wire the 'To' input"), nil
	}
	body, ok := params.TextInputOr(job, "body", params.StringDefault(job.Params, "body", ""))
	if !ok {
		return params.Err(job, "bad_input", "'Body' input must be text"), nil
	}
	if strings.TrimSpace(body) == "" {
		return params.Err(job, "bad_param", "'body' is required — set it or wire the 'Body' input"), nil
	}

	from := strings.TrimSpace(params.StringDefault(job.Params, "from", ""))
	msgService := strings.TrimSpace(params.StringDefault(job.Params, "messaging_service_sid", ""))
	if from == "" && msgService == "" {
		return params.Err(job, "bad_param", "set 'from' (a Twilio number) or 'messaging_service_sid'"), nil
	}

	sid, _, err := resolveCreds(job)
	if err != nil {
		return params.Err(job, "bad_param", err.Error()), nil
	}

	form := url.Values{}
	form.Set("To", to)
	form.Set("Body", body)
	// A Messaging Service takes precedence over a bare From when both are set.
	if msgService != "" {
		form.Set("MessagingServiceSid", msgService)
	} else {
		form.Set("From", from)
	}

	endpoint := baseURL(job) + "/Accounts/" + url.PathEscape(sid) + "/Messages.json"
	status, respBody, err := twilioDo(ctx, job, http.MethodPost, endpoint, form.Encode())
	if err != nil {
		return params.Err(job, "twilio_http_error", err.Error()), nil
	}
	if status < 200 || status >= 300 {
		return params.Err(job, "twilio_error", extractTwilioError(respBody)), nil
	}

	var parsed struct {
		SID    string `json:"sid"`
		Status string `json:"status"`
		To     string `json:"to"`
		From   string `json:"from"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil || parsed.SID == "" {
		return params.Err(job, "twilio_error", "Twilio response had no message sid"), nil
	}
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"message_sid": {MIME: "text/plain", Inline: parsed.SID},
			"status":      {MIME: "text/plain", Inline: parsed.Status},
			"meta": {MIME: "application/json", Inline: map[string]any{
				"sid": parsed.SID, "status": parsed.Status, "to": parsed.To, "from": parsed.From,
			}},
		},
	}, nil
}
