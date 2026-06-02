package slack

import (
	"context"
	"encoding/json"

	"git.sr.ht/~klahr/hazy-flow/core"
	"git.sr.ht/~klahr/hazy-flow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:             "slack_on_mention",
			Version:        "1.0",
			Label:          "Slack on mention",
			Color:          "#4A154B",
			Icon:           "mail", // fallback; BrandLogo wins in the UI
			BrandLogo:      "/brands/slack.svg",
			Category:       "trigger",
			Provider:       "internal",
			Integration:    "Slack",
			Tags:           []string{"slack", "trigger", "mention", "event", "events-api"},
			Description:    "Fires when someone @-mentions your bot in Slack. Each fire receives the message text, the channel, the user who sent it, and the raw event for advanced use. Filter by channel via the channel_filter param if you only want to react in certain rooms.",
			Summary:        "Trigger that fires on every Slack app_mention event, optionally narrowed to one channel.",
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
				{Port: "text", Label: "Message text (the user's message that mentioned the bot)", MIME: []string{"text/plain"}},
				{Port: "user", Label: "Slack user_id of the mentioner", MIME: []string{"text/plain"}},
				{Port: "channel", Label: "Slack channel_id the mention happened in", MIME: []string{"text/plain"}},
				{Port: "team", Label: "Slack team_id (workspace) the mention came from", MIME: []string{"text/plain"}},
				{Port: "ts", Label: "Message timestamp (Slack ts; use this to reply in thread)", MIME: []string{"text/plain"}},
				{Port: "event", Label: "Full Slack event payload as JSON — advanced use", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"channel_filter": {"type":"string","description":"Optional channel ID (e.g. C0123) the graph should fire for. When set, mentions in other channels DO NOT fire this graph — the events handler skips dispatch at the gateway, saving the worker round-trip. Empty (default) means fire for every channel the bot is mentioned in."}
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
// Standalone-run (e.g. `hzctl graph run` or the editor's Run button
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
