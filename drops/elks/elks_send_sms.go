// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package elks

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
			ID:          "elks_send_sms",
			Version:     "1.0",
			Label:       "46elks",
			Subtitle:    "Send SMS",
			Summary:     "Send an SMS text message via 46elks.",
			Description: "Send an SMS via 46elks. The recipient ('To') and message ('Message') can be typed on the step or wired in from upstream (the matching input port overrides the param). 'From' is either one of your 46elks numbers (E.164 like +46700000000) or an alphanumeric sender name (up to 11 characters, e.g. \"Acme\" — must contain a letter, and recipients can't reply to it). Needs ELKS_API_USERNAME and ELKS_API_PASSWORD secrets. Set 'Dry run' to validate without sending (or being billed).",
			Integration: "46elks",
			Category:    "network",
			Icon:        "message-square",
			BrandLogo:   "/brands/46elks.svg",
			Color:       "#0f5499",
			Provider:    "internal",
			Tags:        []string{"46elks", "elks", "sms", "text", "message", "notify", "sweden", "nordic"},
			Examples: []core.ParamsExample{
				{Title: "Alert from a sender name", Params: json.RawMessage(`{"to":"+46700000000","from":"Acme","message":"Your order has shipped."}`), Notes: "Wire a trigger's phone/message outputs into the 'To'/'Message' pins instead of typing them."},
				{Title: "Reply-able, from a number", Params: json.RawMessage(`{"to":"+46700000000","from":"+46700000001","message":"Reply YES to confirm."}`), Notes: "Use one of your 46elks numbers as 'From' so the recipient can reply."},
			},
			RequiresConnections: []core.ConnectionRequirement{
				{Kind: "secret", Name: "ELKS_API_USERNAME", Note: "46elks API username (from the 46elks dashboard)."},
				{Kind: "secret", Name: "ELKS_API_PASSWORD", Note: "46elks API password."},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				{Port: "to", Label: "To", Required: true, MIME: []string{"text/plain"}},
				{Port: "message", Label: "Message", Required: true, MIME: []string{"text/plain"}},
			},
			Outputs: []core.Port{
				{Port: "message_id", Label: "Message ID", MIME: []string{"text/plain"}},
				{Port: "status", Label: "Status", MIME: []string{"text/plain"}},
				{Port: "meta", Label: "Details", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"api_username":{"type":"string","title":"API username","default":"${secret.ELKS_API_USERNAME}","x_advanced":true,"description":"46elks API username. The default reads the ELKS_API_USERNAME secret; ${vault./aws./gcp.…} references work too."},
					"api_password":{"type":"string","title":"API password","default":"${secret.ELKS_API_PASSWORD}","x_advanced":true,"description":"46elks API password. The default reads the ELKS_API_PASSWORD secret."},
					"to":{"type":"string","title":"To","examples":["+46700000000"],"description":"Recipient phone number in E.164 format. Overridden by the 'To' input."},
					"from":{"type":"string","title":"From","examples":["Acme","+46700000001"],"description":"A 46elks number (E.164) the recipient can reply to, or an alphanumeric sender name (max 11 chars, must contain a letter; no replies)."},
					"message":{"type":"string","title":"Message","description":"The text to send. Overridden by the 'Message' input."},
					"dry_run":{"type":"boolean","title":"Dry run","default":false,"x_advanced":true,"description":"Validate the request with 46elks without sending or being billed."},
					"base_url":{"type":"string","description":"Override the API host (testing)."},
					"timeout_ms":{"type":"integer","default":15000,"minimum":1,"description":"Hard deadline for the request, in milliseconds."}
				},
				"required":["api_username","api_password","to","from","message"]
			}`),
			Idempotent: false,
			// 46elks has no idempotency header, so a retried POST sends a second
			// SMS — and double-bills. This drop is a terminal leaf the engine
			// auto-retries on backoff, so retries must be off here.
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
	message, ok := params.TextInputOr(job, "message", params.StringDefault(job.Params, "message", ""))
	if !ok {
		return params.Err(job, "bad_input", "'Message' input must be text"), nil
	}
	if strings.TrimSpace(message) == "" {
		return params.Err(job, "bad_param", "'message' is required — set it or wire the 'Message' input"), nil
	}
	from := strings.TrimSpace(params.StringDefault(job.Params, "from", ""))
	if from == "" {
		return params.Err(job, "bad_param", "'from' is required — a 46elks number (E.164) or an alphanumeric sender name"), nil
	}

	form := url.Values{}
	form.Set("from", from)
	form.Set("to", to)
	form.Set("message", message)
	// dryrun=yes asks 46elks to validate the request without sending it — no
	// SMS goes out and the account isn't charged.
	if params.BoolDefault(job.Params, "dry_run", false) {
		form.Set("dryrun", "yes")
	}

	status, respBody, err := elksDo(ctx, job, http.MethodPost, baseURL(job)+"/sms", form.Encode())
	if err != nil {
		return params.Err(job, "elks_http_error", err.Error()), nil
	}
	if status < 200 || status >= 300 {
		return params.Err(job, "elks_error", extractElksError(respBody)), nil
	}

	var parsed struct {
		ID     string `json:"id"`
		Status string `json:"status"`
		To     string `json:"to"`
		From   string `json:"from"`
		Cost   int    `json:"cost"`
		Parts  int    `json:"parts"`
	}
	// A dry run returns a body without an id — treat that as success and surface
	// whatever validation echo 46elks returned.
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return params.Err(job, "elks_error", "46elks response was not valid JSON"), nil
	}
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"message_id": {MIME: "text/plain", Inline: parsed.ID},
			"status":     {MIME: "text/plain", Inline: parsed.Status},
			"meta": {MIME: "application/json", Inline: map[string]any{
				"id": parsed.ID, "status": parsed.Status, "to": parsed.To,
				"from": parsed.From, "cost": parsed.Cost, "parts": parsed.Parts,
			}},
		},
	}, nil
}
