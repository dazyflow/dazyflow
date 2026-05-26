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
			ID:             "github_on_push",
			Version:        "1.0",
			Label:          "GitHub on push",
			Color:          "#24292f",
			Icon:           "git-branch",
			BrandLogo:      "/brands/github.svg",
			Category:       "trigger",
			Provider:       "internal",
			Integration:    "GitHub",
			Tags:           []string{"github", "trigger", "push", "webhook", "events"},
			Description:    "Fires when commits are pushed to your repo. Receives the branch (ref), the before/after commit SHAs, the commits list, the repo, and the pusher. Common uses: post a deploy alert when commits land on main, or kick off a CI-shaped pipeline.",
			ExecutionModel: core.ExecutionTrigger,
			ProcessModel:   core.ProcessLongLived,
			Outputs: []core.Port{
				{Port: "ref", Label: "Pushed git ref (e.g. refs/heads/main)", MIME: []string{"text/plain"}},
				{Port: "before", Label: "Previous HEAD SHA before the push", MIME: []string{"text/plain"}},
				{Port: "after", Label: "New HEAD SHA after the push", MIME: []string{"text/plain"}},
				{Port: "commits", Label: "Array of commit objects in this push", MIME: []string{"application/json"}},
				{Port: "repository", Label: "Repository object (full GitHub payload)", MIME: []string{"application/json"}},
				{Port: "pusher", Label: "User who pushed", MIME: []string{"application/json"}},
				{Port: "event", Label: "Raw push event payload — advanced use", MIME: []string{"application/json"}},
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
