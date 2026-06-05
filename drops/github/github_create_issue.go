package github

import (
	"context"
	"encoding/json"
	"net/url"

	"git.sr.ht/~klahr/hazyflow/core"
	"git.sr.ht/~klahr/hazyflow/drops/internal/params"
	"git.sr.ht/~klahr/hazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "github_create_issue",
			Version:     "1.0",
			Label:       "GitHub create issue",
			Summary:     "Open a new issue on a GitHub repo with title, body, labels and assignees.",
			Description: "Open a new issue on a GitHub repo. The body supports Markdown and can come from the 'body' input or params.",
			Integration: "GitHub",
			Category:    "network",
			Icon:        "git-branch",
			BrandLogo:   "/brands/github.svg",
			Color:       "#24292f",
			Provider:    "internal",
			Tags:        []string{"github", "issue", "create", "tracker"},
			Examples: []core.ParamsExample{
				{Title: "Minimal bug report", Params: json.RawMessage(`{"owner":"example","repo":"widgets","title":"Deploy failed: prod-eu-west","token":"${secret.GITHUB_TOKEN}"}`)},
				{Title: "Triage issue with labels and assignee", Params: json.RawMessage(`{"owner":"example","repo":"widgets","title":"5xx spike on /checkout","body":"Error rate jumped to 4.1%.","labels":["bug","priority/high"],"assignees":["alice"],"token":"${secret.GITHUB_TOKEN}"}`)},
			},
			RequiresConnections: []core.ConnectionRequirement{
				{Kind: "oauth", Name: "github", Note: "GitHub OAuth — repo scope (issues:write)."},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				{Port: "body", Label: "Issue body (overrides params.body; Markdown)"},
			},
			Outputs: []core.Port{
				{Port: "meta", Label: "Created issue metadata", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"account":{"type":"string","default":"default"},
					"token":{"type":"string","description":"Raw access token; overrides 'account'."},
					"owner":{"type":"string","description":"Repo owner — username or organization."},
					"repo":{"type":"string","description":"Repo name (without the owner prefix)."},
					"title":{"type":"string","description":"Issue title."},
					"body":{"type":"string","description":"Issue body (Markdown). Overridden by the 'body' input."},
					"labels":{"type":"array","items":{"type":"string"},"description":"Labels to attach (must exist)."},
					"assignees":{"type":"array","items":{"type":"string"},"description":"GitHub usernames to assign."},
					"milestone":{"type":"integer","description":"Milestone number (not name)."},
					"timeout_ms":{"type":"integer","default":15000,"minimum":1}
				},
				"required":["owner","repo","title"]
			}`),
			Idempotent:  false,
			RetryPolicy: core.RetryExponentialBackoff,
		},
		Execute: executeGitHubCreateIssue,
	})
}

func executeGitHubCreateIssue(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	owner, _ := params.StringOpt(job.Params, "owner")
	repo, _ := params.StringOpt(job.Params, "repo")
	title, _ := params.StringOpt(job.Params, "title")
	if owner == "" || repo == "" {
		return params.Err(job, "bad_param", "'owner' and 'repo' are required"), nil
	}
	if title == "" {
		return params.Err(job, "bad_param", "title must not be empty"), nil
	}
	token, err := resolveToken(ctx, job)
	if err != nil {
		return params.Err(job, "auth", err.Error()), nil
	}

	payload := map[string]any{"title": title}
	if body := resolveBody(job); body != "" {
		payload["body"] = body
	}
	if labels := paramStringSlice(job.Params, "labels"); len(labels) > 0 {
		payload["labels"] = labels
	}
	if assignees := paramStringSlice(job.Params, "assignees"); len(assignees) > 0 {
		payload["assignees"] = assignees
	}
	if ms := params.IntDefault(job.Params, "milestone", 0); ms > 0 {
		payload["milestone"] = ms
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return params.Err(job, "internal", err.Error()), nil
	}

	endpoint := currentHTTPBase() + "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repo) + "/issues"
	status, respBody, err := githubDo(ctx, "POST", endpoint, token, raw, params.IntDefault(job.Params, "timeout_ms", 15000))
	if err != nil {
		return params.Err(job, "github_http_error", err.Error()), nil
	}
	if status < 200 || status >= 300 {
		return params.Err(job, "github_error", extractGitHubError(respBody)), nil
	}

	var i struct {
		Number  int    `json:"number"`
		HTMLURL string `json:"html_url"`
		ID      int64  `json:"id"`
		NodeID  string `json:"node_id"`
		State   string `json:"state"`
	}
	_ = json.Unmarshal(respBody, &i)
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{"meta": {MIME: "application/json", Inline: map[string]any{
			"number": i.Number, "html_url": i.HTMLURL, "id": i.ID, "node_id": i.NodeID, "state": i.State,
		}}},
	}, nil
}
