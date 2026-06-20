package discord

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/drops/internal/params"
	"git.sr.ht/~klahr/dazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "discord_send_message",
			Version:     "1.0",
			Label:       "Discord",
			Subtitle:    "Send message",
			Summary:     "Post a message to a Discord channel via a webhook.",
			Description: "Post a message to a Discord channel using a channel webhook. The message ('Content') can be typed on the step or wired in from upstream (the input overrides the param). Optionally override the displayed name and avatar per message. Create the webhook in Discord under Server Settings → Integrations → Webhooks and store its URL as the DISCORD_WEBHOOK_URL secret — no bot or OAuth app needed.",
			Integration: "Discord",
			Category:    "network",
			Icon:        "message-square",
			BrandLogo:   "/brands/discord.svg",
			Color:       "#5865F2",
			Provider:    "internal",
			Tags:        []string{"discord", "message", "chat", "notify", "webhook"},
			Examples: []core.ParamsExample{
				{Title: "Notify a channel", Params: json.RawMessage(`{"content":"Deploy finished ✅"}`), Notes: "Wire an upstream message into the 'Content' input instead of typing it. webhook_url defaults to the DISCORD_WEBHOOK_URL secret."},
				{Title: "With a custom sender name", Params: json.RawMessage(`{"content":"Build broke","username":"CI Bot"}`)},
			},
			RequiresConnections: []core.ConnectionRequirement{
				{Kind: "secret", Name: "DISCORD_WEBHOOK_URL", Note: "Discord channel webhook URL (Server Settings → Integrations → Webhooks → Copy Webhook URL)."},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				{Port: "content", Label: "Content", Required: true, MIME: []string{"text/plain"}},
			},
			Outputs: []core.Port{
				{Port: "message_id", Label: "Message ID", MIME: []string{"text/plain"}},
				{Port: "meta", Label: "Details", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"webhook_url":{"type":"string","title":"Webhook URL","default":"${secret.DISCORD_WEBHOOK_URL}","x_advanced":true,"description":"Discord channel webhook URL. The default reads the DISCORD_WEBHOOK_URL secret; ${vault./aws./gcp.…} references work too."},
					"content":{"type":"string","title":"Message","description":"The message text (up to 2000 characters). Overridden by the 'Content' input."},
					"username":{"type":"string","title":"Sender name","description":"Override the name shown for this message. Leave blank for the webhook's default."},
					"avatar_url":{"type":"string","title":"Avatar URL","x_advanced":true,"description":"Override the avatar shown for this message."},
					"thread_id":{"type":"string","title":"Thread ID","x_advanced":true,"description":"Post into an existing thread in the channel instead of the main channel."},
					"timeout_ms":{"type":"integer","default":15000,"minimum":1,"description":"Hard deadline for the request, in milliseconds."}
				},
				"required":["webhook_url","content"]
			}`),
			Idempotent: false,
			// Discord webhook execution has no generic idempotency header,
			// so a retried POST posts the message twice. This drop is a
			// terminal leaf the engine auto-retries on backoff, so retries
			// must be off here.
			RetryPolicy: core.RetryNever,
		},
		Execute: executeSendMessage,
	})
}

func executeSendMessage(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	webhookURL, _ := params.StringOpt(job.Params, "webhook_url")
	if strings.TrimSpace(webhookURL) == "" {
		return params.Err(job, "bad_param", "no Discord webhook: add a DISCORD_WEBHOOK_URL secret (the webhook_url param resolves it by default) or set webhook_url on the step"), nil
	}
	content, ok := params.TextInputOr(job, "content", params.StringDefault(job.Params, "content", ""))
	if !ok {
		return params.Err(job, "bad_input", "'Content' input must be text"), nil
	}
	if strings.TrimSpace(content) == "" {
		return params.Err(job, "bad_param", "'content' is required — set it or wire the 'Content' input"), nil
	}
	if len(content) > maxContentLen {
		return params.Err(job, "bad_param", fmt.Sprintf("'content' is %d characters; Discord allows at most %d", len(content), maxContentLen)), nil
	}

	payload := map[string]any{"content": content}
	if u := strings.TrimSpace(params.StringDefault(job.Params, "username", "")); u != "" {
		payload["username"] = u
	}
	if a := strings.TrimSpace(params.StringDefault(job.Params, "avatar_url", "")); a != "" {
		payload["avatar_url"] = a
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return params.Err(job, "discord_error", err.Error()), nil
	}

	endpoint, err := buildEndpoint(webhookURL, strings.TrimSpace(params.StringDefault(job.Params, "thread_id", "")))
	if err != nil {
		return params.Err(job, "bad_param", err.Error()), nil
	}

	status, respBody, err := discordDo(ctx, job, endpoint, body)
	if err != nil {
		return params.Err(job, "discord_http_error", err.Error()), nil
	}
	if status < 200 || status >= 300 {
		return params.Err(job, "discord_error", extractDiscordError(respBody)), nil
	}

	// With wait=true Discord returns the created message; parse the id when
	// present (a webhook without wait would 204 with an empty body).
	var parsed struct {
		ID        string `json:"id"`
		ChannelID string `json:"channel_id"`
	}
	_ = json.Unmarshal(respBody, &parsed)
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"message_id": {MIME: "text/plain", Inline: parsed.ID},
			"meta": {MIME: "application/json", Inline: map[string]any{
				"message_id": parsed.ID,
				"channel_id": parsed.ChannelID,
				"status":     status,
			}},
		},
	}, nil
}

// buildEndpoint adds wait=true (so Discord returns the created message rather
// than a bare 204) and an optional thread_id, preserving any query the webhook
// URL already carries.
func buildEndpoint(webhookURL, threadID string) (string, error) {
	u, err := url.Parse(webhookURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return "", fmt.Errorf("webhook_url must be an http:// or https:// address")
	}
	q := u.Query()
	q.Set("wait", "true")
	if threadID != "" {
		q.Set("thread_id", threadID)
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}
