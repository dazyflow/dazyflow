package slack

import (
	"context"
	"encoding/json"

	"git.sr.ht/~klahr/hazyflow/core"
	"git.sr.ht/~klahr/hazyflow/drops/internal/params"
	"git.sr.ht/~klahr/hazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "slack_send_message",
			Version:     "1.0",
			Label:       "Slack send message",
			Summary:     "Post a message to a Slack channel as the connected bot, with optional thread_ts and Block Kit.",
			Description: "Post a message to a Slack channel. The simplest path: set the channel and either type your message in 'text' or wire upstream text into the 'body' input. For richer formatting — buttons, dividers, images — use Block Kit blocks instead of plain text.",
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
				{Port: "body", Label: "Message text (overrides params.text)"},
				{Port: "blocks", Label: "Block Kit array (overrides params.blocks; text becomes the push-notification fallback)"},
			},
			Outputs: []core.Port{
				{Port: "meta", Label: "Delivery metadata", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"base_url":{"type":"string","description":"Override the API host (proxy / self-hosted / testing)."},
					"account":{"type":"string","default":"default","description":"Name of the connected Slack workspace."},
					"token":{"type":"string","description":"Raw bot token (xoxb-…). Overrides 'account'."},
					"channel":{"type":"string","description":"Channel ID (C123) or name (#data-ops). Bot must already be a member."},
					"text":{"type":"string","description":"Plain-text message body (Slack mrkdwn)."},
					"thread_ts":{"type":"string","description":"Parent message timestamp to reply in thread."},
					"blocks":{"type":"array","items":{},"description":"Block Kit elements; overrides text rendering for rich messages."},
					"timeout_ms":{"type":"integer","default":15000,"minimum":1}
				},
				"required":["channel"]
			}`),
			Idempotent:  false,
			RetryPolicy: core.RetryExponentialBackoff,
		},
		Execute: executeSlackSendMessage,
	})
}

// executeSlackSendMessage posts to chat.postMessage as the connected bot.
// Behaviour mirrors the former scripted drop: the 'body' input overrides
// params.text, the 'blocks' input/param carries Block Kit, and Slack's
// logical failures (HTTP 200 + {ok:false}) are surfaced as slack_error.
func executeSlackSendMessage(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	token, err := resolveToken(ctx, job)
	if err != nil {
		return params.Err(job, "auth", err.Error()), nil
	}
	channel, _ := params.StringOpt(job.Params, "channel")
	if channel == "" {
		return params.Err(job, "bad_param", "'channel' is required"), nil
	}

	text := params.StringDefault(job.Params, "text", "")
	if t, ok, jerr := bodyInputText(job); jerr != nil {
		return core.Result{JobID: job.ID, Status: core.StatusError, Error: jerr}, nil
	} else if ok {
		text = t
	}

	blocks, jerr := resolveBlocks(job)
	if jerr != nil {
		return core.Result{JobID: job.ID, Status: core.StatusError, Error: jerr}, nil
	}

	if text == "" && blocks == nil {
		return params.Err(job, "bad_input", "This Slack message has no content. Set 'text', wire its 'body' input, or provide Block Kit blocks."), nil
	}

	payload := map[string]any{"channel": channel}
	if text != "" {
		payload["text"] = text
	}
	if ts, _ := params.StringOpt(job.Params, "thread_ts"); ts != "" {
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
