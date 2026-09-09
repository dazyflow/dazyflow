// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Which trigger steps can be fired from the editor with a pasted payload, and
// what a believable payload looks like for each.
//
// The samples are the shape the PROVIDER posts, not the shape of the step's
// outputs: the daemon runs them through the same parser a real delivery goes
// through (daemon/triggerseed.go), so what lands on the ports here is what
// lands on them in production. That is the whole point — a test that produced
// a different payload shape would be worse than no test.

// Mirrors daemon.TestTriggerSeedModules; TestTriggerSeedModulesAreTriggers
// checks that half against the catalog. Schedule triggers are absent because
// they derive their own fire moment — plain Run already exercises those.
export const TEST_TRIGGER_MODULES = new Set([
  "webhook_input",
  "request_input",
  "form_input",
  "slack_on_mention",
  "github_on_push",
  "github_on_new_pr",
]);

export function canTestFire(moduleID: string | undefined): boolean {
  return moduleID !== undefined && TEST_TRIGGER_MODULES.has(moduleID);
}

// The Events API envelope, not a bare event: it carries team_id, which the
// seed reads for the Workspace port (a bare event is accepted too, but then
// that port is empty and the flow under test sees less than production).
function slackMentionSample(): Record<string, unknown> {
  return {
    type: "event_callback",
    team_id: "T024BE7LD",
    event: {
      type: "app_mention",
      user: "U024BE7LH",
      text: "<@U0LAN0Z89> can you run the invoice flow again?",
      channel: "C024BE91L",
      ts: "1770883924.000200",
    },
  };
}

function githubPushSample(): Record<string, unknown> {
  return {
    ref: "refs/heads/main",
    before: "9049f1265b7d61be4a8904a9a27120d2064dab3b",
    after: "0d1a26e67d8f5eaf1f6ba5c57fc3c7d91ac0fd1c",
    commits: [
      {
        id: "0d1a26e67d8f5eaf1f6ba5c57fc3c7d91ac0fd1c",
        message: "Fix the invoice rounding",
        author: { name: "Jane Example", email: "jane@example.com" },
      },
    ],
    repository: { full_name: "acme/widgets", default_branch: "main" },
    pusher: { name: "jane", email: "jane@example.com" },
  };
}

// action: "opened" is required, not decorative — the live handler ignores every
// other pull_request action, so the daemon refuses a payload that names one.
function githubPRSample(): Record<string, unknown> {
  return {
    action: "opened",
    pull_request: {
      number: 128,
      title: "Add the widget",
      body: "Fixes #1",
      html_url: "https://github.com/acme/widgets/pull/128",
      user: { login: "jane" },
      head: { ref: "feat/widget" },
      base: { ref: "main" },
    },
    repository: { full_name: "acme/widgets", default_branch: "main" },
  };
}

// buildTriggerSample returns the payload to seed the dialog with for one
// trigger step, or null for the webhook family — those keep their own sample,
// which is derived from the step's declared form fields.
export function buildTriggerSample(moduleID: string): Record<string, unknown> | null {
  switch (moduleID) {
    case "slack_on_mention":
      return slackMentionSample();
    case "github_on_push":
      return githubPushSample();
    case "github_on_new_pr":
      return githubPRSample();
    default:
      return null;
  }
}
