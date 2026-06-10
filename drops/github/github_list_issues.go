package github

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"

	"git.sr.ht/~klahr/hazyflow/core"
	"git.sr.ht/~klahr/hazyflow/drops/internal/params"
	"git.sr.ht/~klahr/hazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "github_list_issues",
			Version:     "1.0",
			Label:       "GitHub",
			Subtitle:    "List issues",
			Summary:     "List issues on a GitHub repo filtered by state, labels, assignee, and update time.",
			Description: "Fetch a list of issues from a GitHub repo, filtered by open/closed state, labels or assignee. Pairs with a poll trigger for 'fire on new issue' workflows: filter by 'Updated after' (last-seen timestamp) and process what comes back.",
			Integration: "GitHub",
			Category:    "network",
			Icon:        "git-branch",
			BrandLogo:   "/brands/github.svg",
			Color:       "#24292f",
			Provider:    "internal",
			Tags:        []string{"github", "issue", "list", "query"},
			Examples: []core.ParamsExample{
				{Title: "Open bugs assigned to a teammate", Params: json.RawMessage(`{"owner":"example","repo":"widgets","state":"open","labels":["bug"],"assignee":"alice","token":"${secret.GITHUB_TOKEN}"}`)},
			},
			RequiresConnections: []core.ConnectionRequirement{
				{Kind: "oauth", Name: "github", Note: "GitHub OAuth — repo scope (issues:read)."},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Outputs: []core.Port{
				{Port: "issues", Label: "Issues", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"account":{"type":"string","default":"default"},
					"token":{"type":"string","description":"Raw access token; overrides 'account'."},
					"owner":{"type":"string","title":"Repo owner","description":"The username or organization the repo lives under."},
					"repo":{"type":"string","title":"Repo name","description":"The repo's name, without the owner part."},
					"state":{"type":"string","title":"State","enum":["open","closed","all"],"enumNames":["Open","Closed","All"],"default":"open"},
					"labels":{"type":"array","title":"Labels","items":{"type":"string"},"description":"Only issues carrying ALL of these labels."},
					"assignee":{"type":"string","title":"Assignee","description":"Only issues assigned to this username. 'none' = unassigned, '*' = any."},
					"since":{"type":"string","title":"Updated after","x_advanced":true,"description":"RFC3339 timestamp; only issues updated after this."},
					"per_page":{"type":"integer","title":"Max results","x_advanced":true,"default":30,"minimum":1,"maximum":100},
					"timeout_ms":{"type":"integer","default":15000,"minimum":1}
				},
				"required":["owner","repo"]
			}`),
			Idempotent: true,
		},
		Execute: executeGitHubListIssues,
	})
}

func executeGitHubListIssues(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	owner, _ := params.StringOpt(job.Params, "owner")
	repo, _ := params.StringOpt(job.Params, "repo")
	if owner == "" || repo == "" {
		return params.Err(job, "bad_param", "'owner' and 'repo' are required"), nil
	}
	token, err := resolveToken(ctx, job)
	if err != nil {
		return params.Err(job, "auth", err.Error()), nil
	}

	q := url.Values{}
	q.Set("state", params.StringDefault(job.Params, "state", "open"))
	q.Set("per_page", strconv.Itoa(params.IntDefault(job.Params, "per_page", 30)))
	if labels := paramStringSlice(job.Params, "labels"); len(labels) > 0 {
		q.Set("labels", strings.Join(labels, ","))
	}
	if a, _ := params.StringOpt(job.Params, "assignee"); a != "" {
		q.Set("assignee", a)
	}
	if s, _ := params.StringOpt(job.Params, "since"); s != "" {
		q.Set("since", s)
	}

	endpoint := currentHTTPBase() + "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repo) + "/issues?" + q.Encode()
	status, respBody, err := githubDo(ctx, "GET", endpoint, token, nil, params.IntDefault(job.Params, "timeout_ms", 15000))
	if err != nil {
		return params.Err(job, "github_http_error", err.Error()), nil
	}
	if status < 200 || status >= 300 {
		return params.Err(job, "github_error", extractGitHubError(respBody)), nil
	}

	var issues []any
	if err := json.Unmarshal(respBody, &issues); err != nil {
		issues = []any{}
	}
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{"issues": {MIME: "application/json", Inline: issues}},
	}, nil
}
