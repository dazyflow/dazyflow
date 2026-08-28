// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package github

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/drops/internal/params"
	"git.sr.ht/~klahr/dazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "github_add_comment",
			Version:     "1.0",
			Label:       "GitHub",
			Subtitle:    "Add comment",
			Summary:     "Add a comment to a GitHub issue or pull request.",
			Description: "Comment on a GitHub issue or PR (they share a number space). The comment can be typed on the step or connected in from another step (the input overrides the typed value); Markdown works. Outputs a link to the posted comment.",
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
				// Named after its param so the card shows an inline editable
				// box (Unreal-style); a wired value overrides the typed one.
				{Port: "body", Label: "Comment"},
			},
			Outputs: []core.Port{
				// Only the friendly scalar is a pin; the full comment metadata
				// (id, node_id, …) is still EMITTED under "meta" so run records
				// keep it for debugging — it's just not a pin.
				{Port: "comment_url", Label: "Comment link", MIME: []string{"text/plain"}},
				{Port: "meta", Label: "Details", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"account":{"type":"string","default":"default"},
					"token":{"type":"string"},
					"owner":{"type":"string","title":"Repo owner","description":"The username or organization the repo lives under."},
					"repo":{"type":"string","title":"Repo name","description":"The repo's name, without the owner part."},
					"issue_number":{"type":"integer","title":"Issue or PR number","description":"The number from the issue/PR link — issues and PRs share one number space."},
					"body":{"type":"string","title":"Comment","description":"What to say (Markdown works). Overridden by the 'Comment' input."},
					"timeout_ms":{"type":"integer","default":15000,"minimum":1,"description":"Hard deadline for the request, in milliseconds."}
				},
				"required":["owner","repo","issue_number"]
			}`),
			Idempotent:  false,
			RetryPolicy: core.RetryExponentialBackoff,
			// A non-idempotent external write: opt into engine-side dedupe so an
			// expired-lease reclaim or crash recovery replays the recorded result
			// instead of firing the write a second time. Matches the other
			// send-style drops (discord/gmail/sheets/twilio/klarna/nshift/elks).
			DedupeWrites: true,
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
	// Posting a comment is a non-idempotent POST and this drop is a
	// terminal leaf, so the engine auto-retries it — pass the job's stable
	// Idempotency-Key (GitHub honors the header) so a replay dedupes
	// server-side instead of posting a duplicate comment.
	status, respBody, err := githubDoIdem(ctx, "POST", endpoint, token, raw, params.IntDefault(job.Params, "timeout_ms", 15000), job.IdempotencyKey())
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
		Output: map[string]core.Ref{
			"comment_url": {MIME: "text/plain", Inline: c.HTMLURL},
			"meta": {MIME: "application/json", Inline: map[string]any{
				"id": c.ID, "node_id": c.NodeID, "html_url": c.HTMLURL,
			}},
		},
	}, nil
}
