// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package github

import (
	"context"
	"encoding/json"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "github_on_new_pr",
			Version:     "1.0",
			Label:       "GitHub",
			Subtitle:    "On new PR",
			Color:       "#24292f",
			Icon:        "git-merge",
			BrandLogo:   "/brands/github.svg",
			Category:    "trigger",
			Provider:    "internal",
			Integration: "GitHub",
			Tags:        []string{"github", "trigger", "pull-request", "pr", "webhook", "events"},
			Description: "Starts the flow when a pull request is opened on the connected repo. Reopens and pushed-updates don't fire it — it's specifically the 'new PR' moment. Outputs the PR's number, title, description, author, source/target branches, and a link to it.",
			Summary:     "Trigger that fires once when a pull request is opened on a connected GitHub repo.",
			Examples: []core.ParamsExample{
				{
					Title:  "Default — fire on any new PR in the connected repo",
					Params: json.RawMessage(`{}`),
					Notes:  "This trigger is webhook-driven; the daemon's GitHub events handler seeds the step when a pull_request:opened event arrives.",
				},
			},
			RequiresConnections: []core.ConnectionRequirement{
				{Kind: "oauth", Name: "github", Note: "GitHub OAuth — list_connections to confirm before composing."},
			},
			ExecutionModel: core.ExecutionTrigger,
			ProcessModel:   core.ProcessLongLived,
			Outputs: []core.Port{
				// The raw pull_request payload is still EMITTED under "event"
				// by the daemon's webhook seed (see daemon/github_events.go),
				// just not declared as a pin — undeclared outputs can't be
				// wired and don't clutter the card.
				{Port: "number", Label: "PR number", MIME: []string{"text/plain"}},
				{Port: "title", Label: "Title", MIME: []string{"text/plain"}},
				{Port: "body", Label: "Description", MIME: []string{"text/plain"}},
				{Port: "author", Label: "Author", MIME: []string{"text/plain"}},
				{Port: "head_ref", Label: "Source branch", MIME: []string{"text/plain"}},
				{Port: "base_ref", Label: "Target branch", MIME: []string{"text/plain"}},
				{Port: "html_url", Label: "PR link", MIME: []string{"text/plain"}},
				{Port: "repository", Label: "Repository", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{"type":"object"}`),
			Idempotent:   false,
		},
		Execute: executeGitHubOnNewPR,
	})
}

func executeGitHubOnNewPR(_ context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusError,
		Error: &core.JobError{
			Code:    "no_trigger_data",
			Message: "This GitHub new-PR trigger only fires when a real pull_request:opened webhook arrives. To test it, open a PR in your connected repo; running the flow manually leaves the trigger with no event to feed the steps after it.",
			Details: "github_on_new_pr is pre-completed by the daemon's GitHub events handler when a pull_request opened event arrives. Standalone execution has no event payload to emit.",
		},
	}, nil
}
