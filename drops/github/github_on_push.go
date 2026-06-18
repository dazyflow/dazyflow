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
			ID:             "github_on_push",
			Version:        "1.0",
			Label:          "GitHub",
			Subtitle:       "On push",
			Color:          "#24292f",
			Icon:           "git-branch",
			BrandLogo:      "/brands/github.svg",
			Category:       "trigger",
			Provider:       "internal",
			Integration:    "GitHub",
			Tags:           []string{"github", "trigger", "push", "webhook", "events"},
			Description: "Starts the flow when commits are pushed to the connected repo. Outputs the branch (ref), the before/after commit SHAs, the commits list, the repo, and who pushed. Common uses: post a deploy alert when commits land on main, or kick off a CI-shaped pipeline.",
			Summary:     "Trigger that fires when commits are pushed to a connected GitHub repo, with ref, SHAs, and commit list.",
			Examples: []core.ParamsExample{
				{
					Title:  "Default — fire on every push to the connected repo",
					Params: json.RawMessage(`{}`),
					Notes:  "Filter to a specific branch downstream by checking `ref == \"refs/heads/main\"`.",
				},
			},
			RequiresConnections: []core.ConnectionRequirement{
				{Kind: "oauth", Name: "github", Note: "GitHub OAuth — list_connections to confirm before composing."},
			},
			ExecutionModel: core.ExecutionTrigger,
			ProcessModel:   core.ProcessLongLived,
			Outputs: []core.Port{
				{Port: "ref", Label: "Branch ref", MIME: []string{"text/plain"}},
				{Port: "before", Label: "Commit before", MIME: []string{"text/plain"}},
				{Port: "after", Label: "Commit after", MIME: []string{"text/plain"}},
				{Port: "commits", Label: "Commits", MIME: []string{"application/json"}},
				{Port: "repository", Label: "Repository", MIME: []string{"application/json"}},
				{Port: "pusher", Label: "Pushed by", MIME: []string{"application/json"}},
				// The whole push payload stays a wireable pin: compositions
				// template across several of its fields through ONE wire
				// (e.g. the push→Slack template renders commits+ref+pusher
				// together), which the scalar pins can't express.
				{Port: "event", Label: "Raw event", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{"type":"object"}`),
			Idempotent:   false,
		},
		Execute: executeGitHubOnPush,
	})
}

// executeGitHubOnPush is the standalone-execution path — only called
// when a graph is run manually (no webhook event seeded the node).
// Mirrors webhook_input / slack_on_mention.
func executeGitHubOnPush(_ context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusError,
		Error: &core.JobError{
			Code:    "no_trigger_data",
			Message: "This GitHub push trigger only fires when a real push webhook arrives. To test it, push to your connected repo; running the graph manually leaves the trigger with no event to feed downstream.",
			Details: "github_on_push is pre-completed by the daemon's GitHub events handler when a push event arrives. Standalone execution has no event payload to emit.",
		},
	}, nil
}
