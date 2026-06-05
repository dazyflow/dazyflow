package github

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"

	"git.sr.ht/~klahr/hazyflow/core"
	"git.sr.ht/~klahr/hazyflow/drops/internal/params"
	"git.sr.ht/~klahr/hazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "github_add_comment",
			Version:     "1.0",
			Label:       "GitHub add comment",
			Summary:     "Add a comment to a GitHub issue or pull request.",
			Description: "Comment on a GitHub issue or PR (they share a number space). The comment body supports Markdown and can come from the 'body' input or params.",
			Integration: "GitHub",
			Category:    "network",
			Icon:        "git-branch",
			BrandLogo:   "/brands/github.svg",
			Color:       "#24292f",
			Provider:    "internal",
			Tags:        []string{"github", "comment", "issue", "pr"},
			Examples: []core.ParamsExample{
				{Title: "Acknowledge a triage issue", Params: json.RawMessage(`{"owner":"example","repo":"widgets","issue_number":142,"body":"Thanks — reproduced on main.","token":"${secret.GITHUB_TOKEN}"}`)},
			},
			RequiresConnections: []core.ConnectionRequirement{
				{Kind: "oauth", Name: "github", Note: "GitHub OAuth — repo scope (issues:write)."},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				{Port: "body", Label: "Comment body (overrides params.body; Markdown)"},
			},
			Outputs: []core.Port{
				{Port: "meta", Label: "Created comment metadata", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"account":{"type":"string","default":"default"},
					"token":{"type":"string"},
					"owner":{"type":"string"},
					"repo":{"type":"string"},
					"issue_number":{"type":"integer","description":"Issue OR PR number (shared number space)."},
					"body":{"type":"string","description":"Comment body. Overridden by the 'body' input port."},
					"timeout_ms":{"type":"integer","default":15000,"minimum":1}
				},
				"required":["owner","repo","issue_number"]
			}`),
			Idempotent:  false,
			RetryPolicy: core.RetryExponentialBackoff,
		},
		Execute: executeGitHubAddComment,
	})
}

func executeGitHubAddComment(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	owner, _ := params.StringOpt(job.Params, "owner")
	repo, _ := params.StringOpt(job.Params, "repo")
	if owner == "" || repo == "" {
		return params.Err(job, "bad_param", "'owner' and 'repo' are required"), nil
	}
	issueNumber := params.IntDefault(job.Params, "issue_number", 0)
	if issueNumber <= 0 {
		return params.Err(job, "bad_param", "issue_number must be a positive integer"), nil
	}
	token, err := resolveToken(ctx, job)
	if err != nil {
		return params.Err(job, "auth", err.Error()), nil
	}
	body := resolveBody(job)
	if body == "" {
		return params.Err(job, "bad_input", "comment body is empty"), nil
	}
	raw, _ := json.Marshal(map[string]any{"body": body})

	endpoint := currentHTTPBase() + "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repo) + "/issues/" + strconv.Itoa(issueNumber) + "/comments"
	status, respBody, err := githubDo(ctx, "POST", endpoint, token, raw, params.IntDefault(job.Params, "timeout_ms", 15000))
	if err != nil {
		return params.Err(job, "github_http_error", err.Error()), nil
	}
	if status < 200 || status >= 300 {
		return params.Err(job, "github_error", extractGitHubError(respBody)), nil
	}

	var c struct {
		ID      int64  `json:"id"`
		NodeID  string `json:"node_id"`
		HTMLURL string `json:"html_url"`
	}
	_ = json.Unmarshal(respBody, &c)
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{"meta": {MIME: "application/json", Inline: map[string]any{
			"id": c.ID, "node_id": c.NodeID, "html_url": c.HTMLURL,
		}}},
	}, nil
}
