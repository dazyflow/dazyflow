package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"git.sr.ht/~klahr/hazy-flow/core"
	"git.sr.ht/~klahr/hazy-flow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:             "slack_send_message",
			Version:        "1.0",
			Label:          "Slack send message",
			Color:          "#4A154B",
			Icon:           "mail", // fallback glyph; BrandLogo wins in the UI
			BrandLogo:      "/brands/slack.svg",
			Category:       "network",
			Provider:       "internal",
			Integration:    "Slack",
			Tags:           []string{"slack", "chat", "notify", "send"},
			Description:    "Post a message to a Slack channel. The message text comes from the 'body' input port if connected, otherwise from params.text. Authentication uses an OAuth-connected account (set 'account', default \"default\") or a raw 'token' for one-off use. 'channel' accepts a channel ID (C…) or name (#data-ops).",
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				{Port: "body", Label: "Message text (overrides params.text)"},
			},
			Outputs: []core.Port{
				{Port: "meta", Label: "Delivery metadata", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"account":   {"type":"string","default":"default","description":"Name of the connected Slack workspace (matches the 'account' used during OAuth)."},
					"token":     {"type":"string","description":"Raw bot token (xoxb-…). Overrides 'account'. Use for testing or when you've stored the token in a different secret namespace."},
					"channel":   {"type":"string","description":"Channel ID (C123) or name (#data-ops). Bot must already be a member."},
					"text":      {"type":"string","description":"Plain-text message body. Markdown-lite per Slack's mrkdwn formatting."},
					"thread_ts": {"type":"string","description":"Parent message timestamp to reply in thread."},
					"blocks":    {"type":"array","items":{},"description":"Block Kit elements (https://api.slack.com/block-kit). When set, overrides text rendering for rich messages."},
					"timeout_ms":{"type":"integer","default":15000,"minimum":1}
				},
				"required":["channel"]
			}`),
			Idempotent:  false, // Slack deduplicates only with explicit idempotency keys.
			RetryPolicy: core.RetryExponentialBackoff,
		},
		Execute: executeSlackSendMessage,
	})
}

// executeSlackSendMessage POSTs to chat.postMessage. The body
// resolution priority matches the rest of the network drops:
// input port wins, then params.text. Object bodies aren't
// rendered as Slack messages directly (Slack expects strings or
// Block Kit, not arbitrary JSON) — non-string input is rejected
// with a clear error.
func executeSlackSendMessage(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	channel, err := paramString(job.Params, "channel")
	if err != nil {
		return errResult(job, "bad_param", err.Error()), nil
	}
	token, err := resolveToken(ctx, job)
	if err != nil {
		return errResult(job, "auth", err.Error()), nil
	}

	text, _ := paramStringOpt(job.Params, "text")
	if input, ok := job.Input["body"]; ok && input.Inline != nil {
		switch v := input.Inline.(type) {
		case string:
			text = v
		case []byte:
			text = string(v)
		default:
			return errResult(job, "bad_input",
				fmt.Sprintf("body: Slack messages need a string; got %T (use a transformer to render objects first)", v)), nil
		}
	}

	payload := map[string]any{"channel": channel}
	if text != "" {
		payload["text"] = text
	}
	if ts, ok := paramStringOpt(job.Params, "thread_ts"); ok && ts != "" {
		payload["thread_ts"] = ts
	}
	if blocks, ok := job.Params["blocks"]; ok && blocks != nil {
		payload["blocks"] = blocks
	}
	// Slack accepts an empty text when blocks are set, but not both
	// empty — that's a guaranteed `no_text` error, surface it
	// upfront with a clearer message.
	if text == "" && payload["blocks"] == nil {
		return errResult(job, "bad_input",
			"no message text — set params.text, wire the 'body' input port, or provide blocks"), nil
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return errResult(job, "internal", fmt.Sprintf("marshal: %v", err)), nil
	}

	timeoutMs := paramIntDefault(job.Params, "timeout_ms", 15000)
	url := currentHTTPBase() + "/chat.postMessage"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return errResult(job, "internal", err.Error()), nil
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json; charset=utf-8")

	client := &http.Client{Timeout: time.Duration(timeoutMs) * time.Millisecond}
	resp, err := client.Do(req)
	if err != nil {
		return errResult(job, "send_failed", err.Error()), nil
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return errResult(job, "slack_http_error",
			fmt.Sprintf("Slack returned %d: %s", resp.StatusCode, string(respBody))), nil
	}

	env, raw, err := decodeSlackJSON(respBody)
	if err != nil {
		return errResult(job, "parse", err.Error()), nil
	}
	if !env.OK {
		// Slack-specific error envelope. Surface the error code (e.g.
		// channel_not_found, invalid_auth) so users can debug.
		return errResult(job, "slack_error",
			fmt.Sprintf("Slack rejected message: %s", env.Error)), nil
	}

	meta := map[string]any{
		"ok":      true,
		"channel": stringFromRaw(raw, "channel"),
		"ts":      stringFromRaw(raw, "ts"),
	}
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"meta": {MIME: "application/json", Inline: meta},
		},
	}, nil
}

// stringFromRaw extracts a top-level string field from the decoded
// Slack response, returning "" when absent or wrong-typed. Used to
// keep the meta output schema clean even on partial-response
// edge cases.
func stringFromRaw(raw map[string]any, key string) string {
	if raw == nil {
		return ""
	}
	if s, ok := raw[key].(string); ok {
		return s
	}
	return ""
}
