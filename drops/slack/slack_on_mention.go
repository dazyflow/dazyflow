// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package slack

import (
	"context"
	"encoding/json"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:             "slack_on_mention",
			Version:        "1.0",
			Label:          "Slack",
			Subtitle:       "On mention",
			Color:          "#4A154B",
			Icon:           "mail", // fallback; BrandLogo wins in the UI
			BrandLogo:      "/brands/slack.svg",
			Category:       "trigger",
			Provider:       "internal",
			Integration:    "Slack",
			Tags:           []string{"slack", "trigger", "mention", "event", "events-api"},
			Description:    "Starts this flow whenever someone @-mentions your bot in Slack. The message, who sent it, and where it was sent are available as outputs to wire into the next steps — e.g. reply, log the request to a sheet, or forward it by email. Set 'Only in channel' to react in just one room.",
			Summary:        "Starts the flow when someone @-mentions your bot in Slack.",
			Examples: []core.ParamsExample{
				{
					Title:  "Fire on any @-mention in the workspace",
					Params: json.RawMessage(`{}`),
				},
				{
					Title:  "Only mentions in a specific channel",
					Params: json.RawMessage(`{"channel_filter":"C0123ABC456"}`),
					Notes:  "Use the channel ID (Cxxx), not the #name. Mentions elsewhere are skipped at the gateway and don't enqueue a job.",
				},
			},
			RequiresConnections: []core.ConnectionRequirement{
				{Kind: "oauth", Name: "slack", Note: "Slack OAuth — chat:write etc. scopes."},
			},
			ExecutionModel: core.ExecutionTrigger,
			ProcessModel:   core.ProcessLongLived,
			Outputs: []core.Port{
				{Port: "text", Label: "Message", MIME: []string{"text/plain"}},
				{Port: "user", Label: "From user", MIME: []string{"text/plain"}},
				{Port: "channel", Label: "Channel", MIME: []string{"text/plain"}},
				{Port: "team", Label: "Workspace", MIME: []string{"text/plain"}},
				{Port: "ts", Label: "Time", MIME: []string{"text/plain"}},
				// The whole event stays a wireable pin: compositions template
				// across several of its fields through ONE wire (e.g. the
				// mention→GitHub-issue template uses user+channel+text
				// together), which the scalar pins can't express.
				{Port: "event", Label: "Full event", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"channel_filter": {"type":"string","format":"slack-channel","title":"Only in channel","description":"React only to mentions in this one channel — pick it from your connected workspace (or type a channel ID like C0123). Mentions elsewhere are ignored. Leave empty to react everywhere the bot is mentioned."}
				}
			}`),
			// Same shape as webhook_input: retry of a trigger is
			// meaningless because the fire moment carries the data;
			// the daemon pre-completes the node, so Execute below is
			// only the standalone-run path.
			Idempotent: false,
		},
		Execute: executeSlackOnMention,
	})
}

// executeSlackOnMention runs only when the graph is fired without a
// Slack event having pre-completed this node. The events handler
// pre-completes the node's record with status=succeeded directly,
// bypassing the worker, so Execute never runs in the trigger flow.
// Standalone-run (e.g. `dzctl graph run` or the editor's Run button
// against a slack_on_mention graph) hits this path and gets the same
// "no trigger data" sentinel webhook_input uses — a clear "you need
// a real fire" signal instead of silent zero-value behaviour.
func executeSlackOnMention(_ context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusError,
		Error: &core.JobError{
			Code:    "no_trigger_data",
			Message: "This Slack mention trigger only fires when @-mentioned in Slack. To test it, send a mention from your connected Slack workspace; running the graph manually leaves the trigger with no event to feed downstream.",
			Details: "slack_on_mention is pre-completed by the daemon's Slack events handler when an app_mention event arrives. Standalone execution has no event payload to emit.",
		},
	}, nil
}
