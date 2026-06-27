// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package slack

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/drops/internal/params"
	"git.sr.ht/~klahr/dazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "slack_list_channels",
			Version:     "1.0",
			Label:       "Slack",
			Subtitle:    "List channels",
			Summary:     "Get the list of Slack channels your connected bot can see.",
			Description: "Get the list of channels your Slack bot can see, as one row per channel. Wire the Channels output into a For each to do something per channel — for example, send the same announcement to every room the bot is in.",
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

// ListChannels powers the "slack-channel" resource picker: the public and
// private channels the connected bot can see, one AccountResource each (ID =
// channel id like C0123ABC, Name = "#general"). It mirrors the
// slack_list_channels conversations.list call but maps to the picker shape —
// the dropdown stores the ID (which resolves for private channels and DMs
// too) while showing the friendly #name. job carries the account/token in its
// Params; the daemon wires the lister so resolveToken can reach the OAuth
// registry. A nil/empty workspace yields an empty list, not an error.
func ListChannels(ctx context.Context, job core.Job) ([]core.AccountResource, error) {
	token, err := resolveToken(ctx, job)
	if err != nil {
		return nil, err
	}
	q := url.Values{}
	q.Set("types", "public_channel,private_channel")
	q.Set("limit", "1000")
	q.Set("exclude_archived", "true")
	endpoint := slackBaseURL(job) + "/conversations.list?" + q.Encode()
	env, raw, err := slackDo(ctx, "GET", endpoint, token, nil, params.IntDefault(job.Params, "timeout_ms", 15000))
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
	if raw == nil {
		return out, nil
	}
	chans, ok := raw["channels"].([]any)
	if !ok {
		return out, nil
	}
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
