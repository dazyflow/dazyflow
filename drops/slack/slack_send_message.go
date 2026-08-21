// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package slack

import (
	"context"
	"encoding/json"
	"strings"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/drops/internal/params"
	"git.sr.ht/~klahr/dazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "slack_send_message",
			Version:     "1.0",
			Label:       "Slack",
			Subtitle:    "Send message",
			Summary:     "Send a message to a Slack channel as your connected bot.",
			Description: "Send a message to a Slack channel. Channel and Message can be typed on the step or wired in from another step (a wired input overrides the typed value) — handy for sending a sheet row, an email summary, or any upstream text straight into Slack.",
			Integration: "Slack",
			Category:    "network",
			Icon:        "message-square",
			BrandLogo:   "/brands/slack.svg",
			Color:       "#4A154B",
			Provider:    "internal",
			Tags:        []string{"slack", "chat", "notify", "send"},
			Examples: []core.ParamsExample{
				{
					Title:  "Plain message to a channel",
					Params: json.RawMessage(`{"account":"default","channel":"#general","text":"Deploy finished — see https://ci.example.com/run/123"}`),
				},
				{
					Title:  "Threaded reply by channel ID",
					Params: json.RawMessage(`{"account":"default","channel":"C0123ABC","text":"All clear — closing the incident.","thread_ts":"1714060800.000100"}`),
					Notes:  "Use the channel ID (Cxxx) for DMs and private channels; #name works for public ones.",
				},
			},
			RequiresConnections: []core.ConnectionRequirement{
				{Kind: "oauth", Name: "slack", Note: "Slack OAuth — chat:write etc. scopes."},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				// channel and text are named after their params so the card
				// shows inline editable boxes (Unreal-style); a wired value
				// overrides the typed one.
				{Port: "channel", Label: "Channel", MIME: []string{"text/plain"}},
				{Port: "text", Label: "Message", MIME: []string{"text/plain"}},
				{Port: "blocks", Label: "Blocks"},
				// Replying under a message means the parent's timestamp came
				// from upstream — a mention trigger, an earlier post — so it
				// takes a wire, not just a typed value.
				{Port: "thread_ts", Label: "Reply in thread", MIME: []string{"text/plain"}},
			},
			// No declared outputs: sending a message is a "do" step — "after
			// it sends, do X" chains through the pass-through pin, which fires
			// on success. The channel id and message timestamp are still
			// EMITTED under "meta" (see the Execute result) so run records
			// keep them for debugging; they're just not pins. Re-expose ts as
			// a named port if a reply-in-thread feature ever needs to wire it.
			Outputs: []core.Port{
				{Port: "meta", Label: "Details", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"base_url":{"type":"string","description":"Override the API host (proxy / self-hosted / testing)."},
					"account":{"type":"string","default":"default","description":"Name of the connected Slack workspace."},
					"token":{"type":"string","description":"Raw bot token (xoxb-…). Overrides 'account'."},
					"channel":{"type":"string","format":"slack-channel","title":"Channel","description":"Pick a channel from your connected workspace, or type a name like #general / a channel ID. The bot must already be a member. Overridden by the 'Channel' input."},
					"text":{"type":"string","title":"Message","description":"The text to send. Overridden by the 'Message' input."},
					"thread_ts":{"type":"string","title":"Reply in thread","x_advanced":true,"description":"Timestamp of a parent message to reply under. Wire the Reply-in-thread input to answer the message that started the flow — an On mention trigger's Timestamp, say."},
					"blocks":{"type":"array","items":{},"title":"Blocks","x_advanced":true,"description":"Slack Block Kit layout for rich messages; replaces the plain text rendering."},
					"timeout_ms":{"type":"integer","default":15000,"minimum":1,"description":"Hard deadline for the request, in milliseconds."}
				},
				"required":["channel"]
			}`),
			Idempotent: false,
			// Slack's chat.postMessage does NOT honor a generic
			// Idempotency-Key, so a retried POST would post the message
			// twice. This drop is a terminal leaf, which the engine
			// auto-retries on backoff — so retries must be off here.
			RetryPolicy: core.RetryNever,
			// A non-idempotent external write: opt into engine-side dedupe so an
			// expired-lease reclaim or crash recovery replays the recorded result
			// instead of firing the write a second time. Matches the other
			// send-style drops (discord/gmail/sheets/twilio/klarna/nshift/elks).
			DedupeWrites: true,
		},
		Execute: executeSlackSendMessage,
	})
}

// executeSlackSendMessage posts to chat.postMessage as the connected bot.
// channel / text each take their value from the matching input port when one
// is wired, otherwise from the param (the "input overrides param" pattern);
// the legacy 'body' port still feeds text for flows saved before the rename.
// Slack's logical failures (HTTP 200 + {ok:false}) surface as slack_error.
func executeSlackSendMessage(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	token, err := resolveToken(ctx, job)
	if err != nil {
		return params.Err(job, "auth", err.Error()), nil
	}
	channel, ok := params.TextInputOr(job, "channel", params.StringDefault(job.Params, "channel", ""))
	if !ok {
		return params.Err(job, "bad_input", "'Channel' input must be text"), nil
	}
	if channel == "" {
		return params.Err(job, "bad_param", "'channel' is required — set it or wire the 'Channel' input"), nil
	}

	text, ok := params.TextInputOr(job, "text", params.StringDefault(job.Params, "text", ""))
	if !ok {
		return params.Err(job, "bad_input", "'Message' input must be text"), nil
	}

	blocks, jerr := resolveBlocks(job)
	if jerr != nil {
		return core.Result{JobID: job.ID, Status: core.StatusError, Error: jerr}, nil
	}

	if text == "" && blocks == nil {
		return params.Err(job, "bad_input", "This Slack message has no content. Type a message, wire the 'Message' input, or provide blocks."), nil
	}

	payload := map[string]any{"channel": channel}
	if text != "" {
		payload["text"] = text
	}
	threadTS, tsOK := params.TextInputOr(job, "thread_ts", params.StringDefault(job.Params, "thread_ts", ""))
	if !tsOK {
		return params.Err(job, "bad_input", "the 'Reply in thread' input must be text"), nil
	}
	if ts := strings.TrimSpace(threadTS); ts != "" {
		payload["thread_ts"] = ts
	}
	if blocks != nil {
		payload["blocks"] = blocks
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return params.Err(job, "internal", err.Error()), nil
	}

	env, raw, err := slackDo(ctx, "POST", slackBaseURL(job)+"/chat.postMessage", token, body, params.IntDefault(job.Params, "timeout_ms", 15000))
	if err != nil {
		return params.Err(job, "slack_http_error", err.Error()), nil
	}
	if !env.OK {
		msg := env.Error
		if msg == "" {
			msg = "unknown error"
		}
		return params.Err(job, "slack_error", "Slack rejected message: "+msg), nil
	}

	meta := map[string]any{"ok": true}
	if raw != nil {
		meta["channel"] = raw["channel"]
		meta["ts"] = raw["ts"]
	}
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{"meta": {MIME: "application/json", Inline: meta}},
	}, nil
}
