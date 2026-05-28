package slack

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"git.sr.ht/~klahr/hazy-flow/core"
	"git.sr.ht/~klahr/hazy-flow/engine"
	"git.sr.ht/~klahr/hazy-flow/integrations/internal/params"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:             "slack_list_channels",
			Version:        "1.0",
			Label:          "Slack list channels",
			Color:          "#4A154B",
			Icon:           "globe",
			BrandLogo:      "/brands/slack.svg",
			Category:       "network",
			Provider:       "internal",
			Integration:    "Slack",
			Tags:           []string{"slack", "channels", "list", "discover"},
			Description:    "List the channels your Slack bot can see. Useful for filling a channel picker in your UI, or for flows that fan out to every matching channel — e.g. \"post the daily digest to every channel starting with #project-\".",
			Summary:        "List the Slack channels the connected bot can see, optionally filtered by channel type.",
			Examples: []core.ParamsExample{
				{
					Title:  "Public + private channels (default)",
					Params: json.RawMessage(`{"account":"default","types":"public_channel,private_channel","exclude_archived":true,"limit":200}`),
				},
				{
					Title:  "DMs and group DMs only",
					Params: json.RawMessage(`{"account":"default","types":"im,mpim","limit":500}`),
					Notes:  "Useful for flows that fan out alerts to every direct conversation the bot is part of.",
				},
			},
			RequiresConnections: []string{"slack"},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Outputs: []core.Port{
				{Port: "channels", Label: "Channels", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"account":          {"type":"string","default":"default"},
					"token":            {"type":"string","description":"Raw bot token; overrides 'account'."},
					"types":            {"type":"string","default":"public_channel,private_channel","description":"Comma-separated channel types: public_channel, private_channel, mpim, im."},
					"limit":            {"type":"integer","default":200,"minimum":1,"maximum":1000},
					"exclude_archived": {"type":"boolean","default":true},
					"timeout_ms":       {"type":"integer","default":15000,"minimum":1}
				}
			}`),
			Idempotent: true,
		},
		Execute: executeSlackListChannels,
	})
}

// executeSlackListChannels calls conversations.list. Output is the
// raw channels array — same shape Slack returns — so downstream
// nodes can pick the fields they need (id, name, is_member, etc.)
// via map_rows / compute_rows without us second-guessing.
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

	endpoint := currentHTTPBase() + "/conversations.list?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return params.Err(job, "internal", err.Error()), nil
	}
	req.Header.Set("Authorization", "Bearer "+token)

	timeoutMs := params.IntDefault(job.Params, "timeout_ms", 15000)
	client := &http.Client{Timeout: time.Duration(timeoutMs) * time.Millisecond}
	resp, err := client.Do(req)
	if err != nil {
		return params.Err(job, "send_failed", err.Error()), nil
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // channel lists can be larger than messages; cap at 1 MiB

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return params.Err(job, "slack_http_error",
			fmt.Sprintf("Slack returned %d: %s", resp.StatusCode, string(body))), nil
	}
	env, raw, err := decodeSlackJSON(body)
	if err != nil {
		return params.Err(job, "parse", err.Error()), nil
	}
	if !env.OK {
		return params.Err(job, "slack_error",
			fmt.Sprintf("Slack rejected list: %s", env.Error)), nil
	}
	channels, _ := raw["channels"].([]any)
	if channels == nil {
		channels = []any{}
	}
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"channels": {MIME: "application/json", Inline: channels},
		},
	}, nil
}
