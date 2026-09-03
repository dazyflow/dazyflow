// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package github

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/internal/params"
	"github.com/dazyflow/dazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "github_create_issue",
			Version:     "1.0",
			Label:       "GitHub",
			Subtitle:    "Create issue",
			Summary:     "Open a new issue on a GitHub repo with title, body, labels and assignees.",
			Description: "Open a new issue on a GitHub repo. Title and Body can be typed on the step or connected in from another step (the matching input overrides the typed value); the body supports Markdown. Outputs the new issue's link and number so a follow-up step can post it somewhere or comment on it.",
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
				// Named after their params so the card shows inline editable
				// boxes (Unreal-style); a wired value overrides the typed one.
				{Port: "title", Label: "Title", MIME: []string{"text/plain"}},
				{Port: "body", Label: "Body"},
			},
			Outputs: []core.Port{
				// Only the friendly scalars are pins; the full issue metadata
				// (id, node_id, state, …) is still EMITTED under "meta" so run
				// records keep it for debugging — it's just not a pin (same as
				// gmail send / sheets append).
				{Port: "issue_url", Label: "Issue link", MIME: []string{"text/plain"}, Example: json.RawMessage(`"https://github.com/dazyflow/dazyflow/issues/128"`)},
				{Port: "issue_number", Label: "Issue number", MIME: []string{"text/plain"}, Example: json.RawMessage(`"128"`)},
				{Port: "meta", Label: "Details", MIME: []string{"application/json"}, Example: json.RawMessage(`{"number":128,"html_url":"https://github.com/dazyflow/dazyflow/issues/128","id":2447108392,"node_id":"I_kwDOMv1cD84Ojd2o","state":"open"}`)},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"account":{"type":"string","default":"default"},
					"token":{"type":"string","description":"Raw access token; overrides 'account'."},
					"owner":{"type":"string","title":"Repo owner","description":"The username or organization the repo lives under."},
					"repo":{"type":"string","title":"Repo name","description":"The repo's name, without the owner part."},
					"title":{"type":"string","title":"Title","description":"Issue title. Overridden by the 'Title' input."},
					"body":{"type":"string","title":"Body","description":"Issue text (Markdown works). Overridden by the 'Body' input."},
					"labels":{"type":"array","title":"Labels","items":{"type":"string"},"description":"Labels to attach — they must already exist on the repo."},
					"assignees":{"type":"array","title":"Assignees","items":{"type":"string"},"description":"GitHub usernames to assign."},
					"milestone":{"type":"integer","title":"Milestone number","x_advanced":true,"description":"Milestone number (not name)."},
					"timeout_ms":{"type":"integer","default":15000,"minimum":1,"description":"Hard deadline for the request, in milliseconds."}
				},
				"required":["owner","repo","title"]
			}`),
			Idempotent:  false,
			RetryPolicy: core.RetryExponentialBackoff,
			// A non-idempotent external write: opt into engine-side dedupe so an
			// expired-lease reclaim or crash recovery replays the recorded result
			// instead of firing the write a second time. Matches the other
			// send-style drops (discord/gmail/sheets/twilio/klarna/nshift/elks).
			DedupeWrites: true,
		},
		Execute: executeGitHubCreateIssue,
	})
}

func executeGitHubCreateIssue(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	owner, _ := params.StringOpt(job.Params, "owner")
	repo, _ := params.StringOpt(job.Params, "repo")
	if owner == "" || repo == "" {
		return params.Err(job, "bad_param", "'owner' and 'repo' are required"), nil
	}
	// The Title input overrides the param when wired (input-overrides-param).
	title, ok := params.TextInputOr(job, "title", params.StringDefault(job.Params, "title", ""))
	if !ok {
		return params.Err(job, "bad_input", "'Title' input must be text"), nil
	}
	if title == "" {
		return params.Err(job, "bad_param", "title must not be empty — set it or connect the 'Title' input"), nil
	}
	token, err := resolveToken(ctx, job)
	if err != nil {
		return params.Err(job, "auth", err.Error()), nil
	}

	payload := map[string]any{"title": title}
	if body := resolveBody(job); body != "" {
		payload["body"] = body
	}
	if labels := params.StringSlice(job.Params, "labels"); len(labels) > 0 {
		payload["labels"] = labels
	}
	if assignees := params.StringSlice(job.Params, "assignees"); len(assignees) > 0 {
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
	// Creating an issue is a non-idempotent POST and this drop is a
	// terminal leaf, so the engine auto-retries it — pass the job's stable
	// Idempotency-Key (GitHub honors the header) so a replay dedupes
	// server-side instead of opening a duplicate issue.
	status, respBody, err := githubDoIdem(ctx, "POST", endpoint, token, raw, params.IntDefault(job.Params, "timeout_ms", 15000), job.IdempotencyKey())
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
		Output: map[string]core.Ref{
			"issue_url":    {MIME: "text/plain", Inline: i.HTMLURL},
			"issue_number": {MIME: "text/plain", Inline: strconv.Itoa(i.Number)},
			"meta": {MIME: "application/json", Inline: map[string]any{
				"number": i.Number, "html_url": i.HTMLURL, "id": i.ID, "node_id": i.NodeID, "state": i.State,
			}},
		},
	}, nil
}
