// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package slack

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/internal/params"
	"github.com/dazyflow/dazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "slack_list_channels",
			Version:     "1.0",
			Label:       "Slack",
			Subtitle:    "List channels",
			Summary:     "Get the list of Slack channels your connected bot can see.",
			Description: "Get the list of channels your Slack bot can see, as one row per channel. Connect the Channels output into a For each to do something per channel — for example, send the same announcement to every room the bot is in.",
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
				// The channel list IS the product of this step (one row per
				// channel), so it stays a pin — it's data to wire onward, not
				// a debugging blob. The universal pass-through pin is added by
				// core.WithPassthrough (this is an input-less action, not a
				// flow source), so it isn't declared here.
				{Port: "channels", Label: "Channels", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"base_url":{"type":"string","description":"Override the API host (proxy / self-hosted / testing)."},
					"account":{"type":"string","default":"default"},
					"token":{"type":"string","description":"Raw bot token; overrides 'account'."},
					"types":{"type":"string","title":"Which channels","default":"public_channel,private_channel","enum":["public_channel","public_channel,private_channel","im,mpim","public_channel,private_channel,im,mpim"],"enumNames":["Public channels","Public + private channels","Direct messages","Everything the bot can see"]},
					"limit":{"type":"integer","title":"Max channels","x_advanced":true,"default":200,"minimum":1,"maximum":1000},
					"exclude_archived":{"type":"boolean","title":"Skip archived channels","default":true},
					"timeout_ms":{"type":"integer","default":15000,"minimum":1,"description":"Hard deadline for the request, in milliseconds."}
				}
			}`),
			Idempotent: true,
		},
		Execute: executeSlackListChannels,
	})
}

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

	channels, env, err := listConversations(ctx, job, token, q, params.IntDefault(job.Params, "limit", 200))
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
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{"channels": {MIME: "application/json", Inline: channels}},
	}, nil
}

func ListChannels(ctx context.Context, job core.Job) ([]core.AccountResource, error) {
	token, err := resolveToken(ctx, job)
	if err != nil {
		return nil, err
	}
	q := url.Values{}
	q.Set("types", "public_channel,private_channel")
	q.Set("limit", "1000")
	q.Set("exclude_archived", "true")
	chans, env, err := listConversations(ctx, job, token, q, maxPickerChannels)
	if err != nil {
		return nil, err
	}
	if !env.OK {
		msg := env.Error
		if msg == "" {
			msg = "unknown error"
		}
		return nil, fmt.Errorf("slack rejected channel list: %s", msg)
	}
	out := []core.AccountResource{}
	for _, c := range chans {
		m, ok := c.(map[string]any)
		if !ok {
			continue
		}
		id, _ := m["id"].(string)
		if id == "" {
			continue
		}
		label := id
		if name, _ := m["name"].(string); name != "" {
			label = "#" + name
		}
		out = append(out, core.AccountResource{ID: id, Name: label})
	}
	return out, nil
}

// maxPickerChannels bounds the channel picker; maxChannelPages bounds how many
// cursor pages one call walks.
const (
	maxPickerChannels = 5000
	maxChannelPages   = 50
)

// listConversations follows next_cursor until want channels are collected or
// the list ends. Slack pages conversations.list well below the requested limit
// (archived and filtered channels still count against a page), so one request
// could return a handful of channels with more waiting behind the cursor.
func listConversations(ctx context.Context, job core.Job, token string, q url.Values, want int) ([]any, slackEnvelope, error) {
	channels := []any{}
	timeout := params.IntDefault(job.Params, "timeout_ms", 15000)
	for page := 0; page < maxChannelPages && len(channels) < want; page++ {
		q.Set("limit", strconv.Itoa(min(want-len(channels), 1000)))
		env, raw, err := slackDo(ctx, "GET", slackBaseURL(job)+"/conversations.list?"+q.Encode(), token, nil, timeout)
		if err != nil || !env.OK {
			return nil, env, err
		}
		if c, ok := raw["channels"].([]any); ok {
			channels = append(channels, c...)
		}
		meta, _ := raw["response_metadata"].(map[string]any)
		next, _ := meta["next_cursor"].(string)
		if next == "" {
			break
		}
		q.Set("cursor", next)
	}
	if len(channels) > want {
		channels = channels[:want]
	}
	return channels, slackEnvelope{OK: true}, nil
}
