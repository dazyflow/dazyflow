// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/internal/params"
	"github.com/dazyflow/dazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "github_list_issues",
			Version:     "1.0",
			Label:       "GitHub",
			Subtitle:    "List issues",
			Summary:     "List issues on a GitHub repo filtered by state, labels, assignee, and update time. Pull requests are excluded by default.",
			Description: "Fetch a list of issues from a GitHub repo, filtered by open/closed state, labels or assignee. Pull requests are left out by default (GitHub's API counts them as issues) — flip 'Include pull requests' to keep them. Results span multiple pages up to 'Max results'. Pairs with a poll trigger for 'fire on new issue' workflows: filter by 'Updated after' (last-seen timestamp) and process what comes back.",
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
				{Port: "meta", Label: "Details", MIME: []string{"application/json"}},
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
					"include_prs":{"type":"boolean","title":"Include pull requests","x_advanced":true,"default":false,"description":"GitHub's issues API also returns pull requests. Off (default) filters them out so you get only real issues."},
					"max_results":{"type":"integer","title":"Max results","default":100,"minimum":1,"maximum":1000,"description":"Stop after this many issues, fetching across pages as needed."},
					"per_page":{"type":"integer","title":"Page size","x_advanced":true,"default":100,"minimum":1,"maximum":100,"description":"How many issues to request per page while paginating."},
					"timeout_ms":{"type":"integer","default":15000,"minimum":1,"description":"Hard deadline for the request, in milliseconds."}
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

	maxResults := params.ClampInt(params.IntDefault(job.Params, "max_results", 100), 1, 1000)
	perPage := params.ClampInt(params.IntDefault(job.Params, "per_page", 100), 1, 100)
	includePRs := params.BoolDefault(job.Params, "include_prs", false)
	timeoutMS := params.IntDefault(job.Params, "timeout_ms", 15000)

	q := url.Values{}
	q.Set("state", params.StringDefault(job.Params, "state", "open"))
	q.Set("per_page", strconv.Itoa(perPage))
	if labels := params.StringSlice(job.Params, "labels"); len(labels) > 0 {
		q.Set("labels", strings.Join(labels, ","))
	}
	if a, _ := params.StringOpt(job.Params, "assignee"); a != "" {
		q.Set("assignee", a)
	}
	if s, _ := params.StringOpt(job.Params, "since"); s != "" {
		q.Set("since", s)
	}

	// Follow the Link: rel="next" header across pages until we have
	// max_results issues or run out, bounded by maxPages as a safety net.
	const maxPages = 20
	next := currentHTTPBase() + "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repo) + "/issues?" + q.Encode()
	issues := make([]map[string]any, 0, maxResults)
	truncated := false
	for pages := 0; next != ""; pages++ {
		if pages >= maxPages {
			truncated = true
			break
		}
		status, respBody, hdr, hErr := githubDoH(ctx, "GET", next, token, nil, timeoutMS)
		if hErr != nil {
			return params.Err(job, "github_http_error", hErr.Error()), nil
		}
		if status < 200 || status >= 300 {
			return params.Err(job, "github_error", extractGitHubError(respBody)), nil
		}
		var page []map[string]any
		if err := json.Unmarshal(respBody, &page); err != nil {
			// A 2xx with a non-array body is unexpected (e.g. an abuse-rate
			// interstitial or a truncated body). Surface it rather than
			// silently skipping the page — a poll workflow must not miss
			// issues without any signal.
			return params.Err(job, "bad_response", fmt.Sprintf("unexpected GitHub response on page %d: %v", pages+1, err)), nil
		}
		for _, it := range page {
			// GitHub returns PRs from the issues endpoint; they carry a
			// "pull_request" key, which is how the API distinguishes them.
			if !includePRs {
				if _, isPR := it["pull_request"]; isPR {
					continue
				}
			}
			issues = append(issues, it)
		}
		next = parseNextLink(hdr.Get("Link"))
		if len(issues) >= maxResults {
			truncated = len(issues) > maxResults || next != ""
			break
		}
	}
	if len(issues) > maxResults {
		issues = issues[:maxResults]
	}

	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"issues": {MIME: "application/json", Inline: issues},
			// Emitted (not a declared pin), like the action drops' meta.
			"meta": {MIME: "application/json", Inline: map[string]any{
				"count": len(issues), "truncated": truncated,
			}},
		},
	}, nil
}
