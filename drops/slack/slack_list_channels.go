package slack

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
			ID:          "slack_list_channels",
			Version:     "1.0",
			Label:       "Slack list channels",
			Summary:     "List the Slack channels the connected bot can see, optionally filtered by channel type.",
			Description: "List the channels your Slack bot can see. Useful for filling a channel picker, or for flows that fan out to every matching channel.",
			Integration: "Slack",
			Category:    "network",
			Icon:        "globe",
			BrandLogo:   "/brands/slack.svg",
			Color:       "#4A154B",
			Provider:    "internal",
			Tags:        []string{"slack", "channels", "list", "discover"},
			Examples: []core.ParamsExample{
				{
					Title:  "Public + private channels (default)",
					Params: json.RawMessage(`{"account":"default","types":"public_channel,private_channel","exclude_archived":true,"limit":200}`),
				},
				{
					Title:  "DMs and group DMs only",
					Params: json.RawMessage(`{"account":"default","types":"im,mpim","limit":500}`),
					Notes:  "Fan out alerts to every direct conversation the bot is in.",
				},
			},
			RequiresConnections: []core.ConnectionRequirement{
				{Kind: "oauth", Name: "slack", Note: "Slack OAuth — channels:read scope."},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Outputs: []core.Port{
				{Port: "channels", Label: "Channels", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"base_url":{"type":"string","description":"Override the API host (proxy / self-hosted / testing)."},
					"account":{"type":"string","default":"default"},
					"token":{"type":"string","description":"Raw bot token; overrides 'account'."},
					"types":{"type":"string","default":"public_channel,private_channel","description":"Comma-separated channel types: public_channel, private_channel, mpim, im."},
					"limit":{"type":"integer","default":200,"minimum":1,"maximum":1000},
					"exclude_archived":{"type":"boolean","default":true},
					"timeout_ms":{"type":"integer","default":15000,"minimum":1}
				}
			}`),
			Idempotent: true,
		},
		Execute: executeSlackListChannels,
	})
}

// executeSlackListChannels lists conversations the bot can see. One page
// up to `limit`, matching the former scripted drop; downstream nodes
// paginate if they need more.
func executeSlackListChannels(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	token, err := resolveToken(ctx, job)
	if err != nil {
		return params.Err(job, "auth", err.Error()), nil
	}

	q := url.Values{}
	q.Set("types", params.StringDefault(job.Params, "types", "public_channel,private_channel"))
	q.Set("limit", strconv.Itoa(params.IntDefault(job.Params, "limit", 200)))
	if params.BoolDefault(job.Params, "exclude_archived", true) {
		q.Set("exclude_archived", "true")
	}

	endpoint := slackBaseURL(job) + "/conversations.list?" + q.Encode()
	env, raw, err := slackDo(ctx, "GET", endpoint, token, nil, params.IntDefault(job.Params, "timeout_ms", 15000))
	if err != nil {
		return params.Err(job, "slack_http_error", err.Error()), nil
	}
	if !env.OK {
		msg := env.Error
		if msg == "" {
			msg = "unknown error"
		}
		return params.Err(job, "slack_error", "Slack rejected list: "+msg), nil
	}

	channels := []any{}
	if raw != nil {
		if c, ok := raw["channels"].([]any); ok {
			channels = c
		}
	}
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{"channels": {MIME: "application/json", Inline: channels}},
	}, nil
}
