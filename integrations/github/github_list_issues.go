package github

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"git.sr.ht/~klahr/hazy-flow/core"
	"git.sr.ht/~klahr/hazy-flow/engine"
	"git.sr.ht/~klahr/hazy-flow/integrations/internal/params"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:             "github_list_issues",
			Version:        "1.0",
			Label:          "GitHub list issues",
			Color:          "#24292f",
			Icon:           "git-branch",
			BrandLogo:      "/brands/github.svg",
			Category:       "network",
			Provider:       "internal",
			Integration:    "GitHub",
			Tags:           []string{"github", "issue", "list", "poll", "query"},
			Description: "List issues on a GitHub repo with optional filters (state, labels, assignee, since-date). Pair with a polling trigger to react to new issues as they appear. Heads-up: pull requests also come back from this endpoint — filter them out downstream if you only want issues.",
			Summary:     "Query a repo's issues with filters for state, labels, assignee, and an updated-since cutoff.",
			Examples: []core.ParamsExample{
				{
					Title:  "Open bugs assigned to a teammate",
					Params: json.RawMessage(`{"owner":"example","repo":"widgets","state":"open","labels":["bug"],"assignee":"alice","token":"${secret:GITHUB_TOKEN}"}`),
				},
				{
					Title:  "Poll for issues updated since last run",
					Params: json.RawMessage(`{"owner":"example","repo":"widgets","since":"2026-05-28T00:00:00Z","per_page":100,"token":"${secret:GITHUB_TOKEN}"}`),
					Notes:  "Feed 'since' from a poll_trigger's last-seen cursor to react only to fresh updates.",
				},
			},
			RequiresConnections: []string{"github"},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Outputs: []core.Port{
				{Port: "issues", Label: "Issues (and PRs)", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"account":   {"type":"string","default":"default"},
					"token":     {"type":"string","description":"Raw access token; overrides 'account'."},
					"owner":     {"type":"string"},
					"repo":      {"type":"string"},
					"state":     {"type":"string","enum":["open","closed","all"],"default":"open"},
					"labels":    {"type":"array","items":{"type":"string"},"description":"Comma-joined into the labels filter; multiple labels are AND-ed by GitHub."},
					"assignee":  {"type":"string","description":"Filter to issues assigned to this user. Use \"none\" for unassigned, \"*\" for any assignee."},
					"since":     {"type":"string","description":"ISO-8601 / RFC3339 timestamp; only issues updated after this time. The right param for poll_trigger composition."},
					"per_page":  {"type":"integer","default":30,"minimum":1,"maximum":100},
					"timeout_ms":{"type":"integer","default":15000,"minimum":1}
				},
				"required":["owner","repo"]
			}`),
			Idempotent: true,
		},
		Execute: executeGitHubListIssues,
	})
}

// executeGitHubListIssues calls GET /repos/{owner}/{repo}/issues
// with the listed filters. Returns the raw issue array — same
// shape GitHub gives — so downstream nodes can pick the fields
// they need (number, title, user.login, html_url, etc.) via
// map_rows or compute_rows.
func executeGitHubListIssues(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	owner, err := params.String(job.Params, "owner")
	if err != nil {
		return params.Err(job, "bad_param", err.Error()), nil
	}
	repo, err := params.String(job.Params, "repo")
	if err != nil {
		return params.Err(job, "bad_param", err.Error()), nil
	}
	token, err := resolveToken(ctx, job)
	if err != nil {
		return params.Err(job, "auth", err.Error()), nil
	}

	q := url.Values{}
	q.Set("state", params.StringDefault(job.Params, "state", "open"))
	if labels := paramStringSlice(job.Params, "labels"); len(labels) > 0 {
		// GitHub joins labels with commas in the query value
		// (URL-encoded), and AND-s them server-side.
		q.Set("labels", strings.Join(labels, ","))
	}
	if assignee := params.StringDefault(job.Params, "assignee", ""); assignee != "" {
		q.Set("assignee", assignee)
	}
	if since := params.StringDefault(job.Params, "since", ""); since != "" {
		q.Set("since", since)
	}
	q.Set("per_page", strconv.Itoa(params.IntDefault(job.Params, "per_page", 30)))

	endpoint := fmt.Sprintf("%s/repos/%s/%s/issues?%s",
		currentHTTPBase(), url.PathEscape(owner), url.PathEscape(repo), q.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return params.Err(job, "internal", err.Error()), nil
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	timeoutMs := params.IntDefault(job.Params, "timeout_ms", 15000)
	client := &http.Client{Timeout: time.Duration(timeoutMs) * time.Millisecond}
	resp, err := client.Do(req)
	if err != nil {
		return params.Err(job, "send_failed", err.Error()), nil
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 5<<20))

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return params.Err(job, "github_error",
			fmt.Sprintf("GitHub returned %d: %s", resp.StatusCode, extractGitHubError(body))), nil
	}

	var issues []any
	if err := json.Unmarshal(body, &issues); err != nil {
		return params.Err(job, "parse", err.Error()), nil
	}
	if issues == nil {
		issues = []any{}
	}
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"issues": {MIME: "application/json", Inline: issues},
		},
	}, nil
}
