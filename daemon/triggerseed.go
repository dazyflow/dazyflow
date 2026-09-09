// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/dazyflow/dazyflow/core"
)

// Seeds for trigger nodes, shared by the live delivery handlers and the
// editor's per-node test fire.
//
// The seed shape is the contract downstream steps and ${trigger.*} read, so it
// is built in ONE place per module: a test that produced different ports from a
// real delivery would be worse than no test at all.

// TestTriggerSeedModules are the trigger drops that can be fired from a pasted
// payload. Mirrored in web/src/lib/testTrigger.ts (TEST_TRIGGER_MODULES) to
// decide which cards show the fire button; TestTriggerSeedModulesAreTriggers
// keeps this half honest against the catalog.
//
// Scheduler-driven triggers are absent on purpose: cron_trigger and
// poll_trigger derive their own fire moment, so a plain Run already exercises
// them and there is no payload to paste.
var TestTriggerSeedModules = map[string]bool{
	webhookInputModuleID:    true,
	core.RequestInputModule: true,
	core.FormInputModule:    true,
	slackOnMentionModuleID:  true,
	githubOnPushModuleID:    true,
	githubOnNewPRModuleID:   true,
}

// testTriggerSeed builds the seed one node would have received had the event
// really arrived. The node, not just its module, because a trigger's own
// filters decide whether a real delivery would have reached it at all.
func testTriggerSeed(n core.Node, rawBody []byte, r *http.Request) (core.Result, error) {
	switch n.Module {
	case webhookInputModuleID, core.RequestInputModule, core.FormInputModule:
		return buildWebhookSeed(rawBody, r), nil
	case slackOnMentionModuleID:
		return slackTestSeed(n, rawBody)
	case githubOnPushModuleID:
		return githubPushTestSeed(rawBody)
	case githubOnNewPRModuleID:
		return githubPRTestSeed(rawBody)
	default:
		return core.Result{}, fmt.Errorf(
			"step %q is a %s step, which cannot be fired with a pasted payload — "+
				"a schedule trigger produces its own fire moment, so use Run instead",
			n.ID, n.Module)
	}
}

// Accepts either a full Events API envelope (what Slack posts, and what the
// editor's sample shows) or a bare app_mention event, since a payload copied
// out of a log is as likely to be one as the other.
func slackTestSeed(n core.Node, rawBody []byte) (core.Result, error) {
	var env slackEventEnvelope
	if err := json.Unmarshal(rawBody, &env); err != nil {
		return core.Result{}, fmt.Errorf("payload is not JSON: %w", err)
	}
	rawEvent := env.Event
	if len(rawEvent) == 0 {
		rawEvent = rawBody
	}
	var ev slackAppMentionEvent
	if err := json.Unmarshal(rawEvent, &ev); err != nil {
		return core.Result{}, fmt.Errorf("payload has no Slack event object: %w", err)
	}
	// Empty passes: a bare event pasted without its "type" is still what the
	// author meant, and Slack only routes app_mention to this trigger anyway.
	if ev.Type != "" && ev.Type != "app_mention" {
		return core.Result{}, fmt.Errorf(
			"this trigger fires on app_mention, but the payload's event type is %q", ev.Type)
	}
	// The gateway drops a mention the filter excludes before any job is
	// enqueued, so firing it here would test a flow that cannot happen.
	if !nodeChannelFilterMatches(n.Params, ev.Channel) {
		return core.Result{}, fmt.Errorf(
			"this step only reacts in channel %v, but the payload's channel is %q — "+
				"a real mention there would not reach this step",
			n.Params["channel_filter"], ev.Channel)
	}
	return slackMentionSeed(env.TeamID, ev, rawEvent), nil
}

func githubPushTestSeed(rawBody []byte) (core.Result, error) {
	var ev pushEvent
	if err := json.Unmarshal(rawBody, &ev); err != nil {
		return core.Result{}, fmt.Errorf("payload is not a GitHub push event: %w", err)
	}
	return githubPushSeed(ev, rawBody), nil
}

func githubPRTestSeed(rawBody []byte) (core.Result, error) {
	var ev pullRequestEvent
	if err := json.Unmarshal(rawBody, &ev); err != nil {
		return core.Result{}, fmt.Errorf("payload is not a GitHub pull_request event: %w", err)
	}
	// GitHub sends every pull_request action to the same webhook and the live
	// handler ignores all but "opened", so accepting one here would fire a run
	// the real delivery never would.
	if ev.Action != "opened" {
		return core.Result{}, fmt.Errorf(
			"this trigger fires on a newly opened pull request, but the payload's action is %q", ev.Action)
	}
	return githubPRSeed(ev, rawBody), nil
}
