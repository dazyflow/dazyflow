package github

import (
	"context"
	"encoding/json"

	"git.sr.ht/~klahr/hazy-flow/core"
	"git.sr.ht/~klahr/hazy-flow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:             "github_on_new_pr",
			Version:        "1.0",
			Label:          "GitHub on new PR",
			Color:          "#24292f",
			Icon:           "git-merge",
			BrandLogo:      "/brands/github.svg",
			Category:       "trigger",
			Provider:       "internal",
			Integration:    "GitHub",
			Tags:           []string{"github", "trigger", "pull-request", "pr", "webhook", "events"},
			Description:    "Fires when a pull request is OPENED in a repository whose webhook points at this daemon. Subscribes to the pull_request event with action=opened — reopened or synchronized PRs don't fire here (filter downstream if you need them). Configure the repo's webhook URL to POST /api/v1/events/github/<tenant> with a Secret matching --github-webhook-secret.",
			ExecutionModel: core.ExecutionTrigger,
			ProcessModel:   core.ProcessLongLived,
			Outputs: []core.Port{
				{Port: "number", Label: "PR number", MIME: []string{"text/plain"}},
				{Port: "title", Label: "PR title", MIME: []string{"text/plain"}},
				{Port: "body", Label: "PR description (markdown)", MIME: []string{"text/plain"}},
				{Port: "author", Label: "Login of the PR author", MIME: []string{"text/plain"}},
				{Port: "head_ref", Label: "Source branch name", MIME: []string{"text/plain"}},
				{Port: "base_ref", Label: "Target branch name", MIME: []string{"text/plain"}},
				{Port: "html_url", Label: "Web URL of the PR", MIME: []string{"text/plain"}},
				{Port: "repository", Label: "Repository object", MIME: []string{"application/json"}},
				{Port: "event", Label: "Raw pull_request event payload — advanced use", MIME: []string{"application/json"}},
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
			Message: "This GitHub new-PR trigger only fires when a real pull_request:opened webhook arrives. To test it, open a PR in your connected repo; running the graph manually leaves the trigger with no event to feed downstream.",
			Details: "github_on_new_pr is pre-completed by the daemon's GitHub events handler when a pull_request opened event arrives. Standalone execution has no event payload to emit.",
		},
	}, nil
}
