package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"git.sr.ht/~klahr/hazy-flow/core"
	"git.sr.ht/~klahr/hazy-flow/engine"
	"git.sr.ht/~klahr/hazy-flow/integrations/internal/params"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:             "github_add_comment",
			Version:        "1.0",
			Label:          "GitHub add comment",
			Color:          "#24292f",
			Icon:           "git-branch",
			BrandLogo:      "/brands/github.svg",
			Category:       "network",
			Provider:       "internal",
			Integration:    "GitHub",
			Tags:           []string{"github", "issue", "pr", "comment"},
			Description:    "Add a comment to a GitHub issue or pull request. Works for both — GitHub treats them as the same thing under the hood. Comment body supports Markdown and can come from the 'body' input or from params.",
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
					"account":     {"type":"string","default":"default"},
					"token":       {"type":"string"},
					"owner":       {"type":"string"},
					"repo":        {"type":"string"},
					"issue_number":{"type":"integer","description":"Issue OR PR number (GitHub uses the same number space for both)."},
					"body":        {"type":"string","description":"Comment body. Overridden by the 'body' input port if connected."},
					"timeout_ms":  {"type":"integer","default":15000,"minimum":1}
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
	owner, err := params.String(job.Params, "owner")
	if err != nil {
		return params.Err(job, "bad_param", err.Error()), nil
	}
	repo, err := params.String(job.Params, "repo")
	if err != nil {
		return params.Err(job, "bad_param", err.Error()), nil
	}
	issueNumber := params.IntDefault(job.Params, "issue_number", 0)
	if issueNumber <= 0 {
		return params.Err(job, "bad_param", "issue_number must be a positive integer"), nil
	}
	token, err := resolveToken(ctx, job)
	if err != nil {
		return params.Err(job, "auth", err.Error()), nil
	}

	body, _ := params.StringOpt(job.Params, "body")
	if input, ok := job.Input["body"]; ok && input.Inline != nil {
		switch v := input.Inline.(type) {
		case string:
			body = v
		case []byte:
			body = string(v)
		default:
			raw, mErr := json.MarshalIndent(v, "", "  ")
			if mErr != nil {
				return params.Err(job, "bad_input", mErr.Error()), nil
			}
			body = "```json\n" + string(raw) + "\n```"
		}
	}
	if body == "" {
		return params.Err(job, "bad_input", "comment body is empty"), nil
	}

	payload := map[string]any{"body": body}
	jsonBody, _ := json.Marshal(payload)

	endpoint := fmt.Sprintf("%s/repos/%s/%s/issues/%d/comments",
		currentHTTPBase(), url.PathEscape(owner), url.PathEscape(repo), issueNumber)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(jsonBody))
	if err != nil {
		return params.Err(job, "internal", err.Error()), nil
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("Content-Type", "application/json")
	// Idempotency-Key dedupes retries of the same node-record.
	req.Header.Set("Idempotency-Key", job.IdempotencyKey())

	timeoutMs := params.IntDefault(job.Params, "timeout_ms", 15000)
	client := &http.Client{Timeout: time.Duration(timeoutMs) * time.Millisecond}
	resp, err := client.Do(req)
	if err != nil {
		return params.Err(job, "send_failed", err.Error()), nil
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 256*1024))

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return params.Err(job, "github_error",
			fmt.Sprintf("GitHub returned %d: %s", resp.StatusCode, extractGitHubError(respBody))), nil
	}
	var parsed map[string]any
	_ = json.Unmarshal(respBody, &parsed)
	meta := map[string]any{
		"id":       numberField(parsed, "id"),
		"node_id":  stringField(parsed, "node_id"),
		"html_url": stringField(parsed, "html_url"),
	}
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"meta": {MIME: "application/json", Inline: meta},
		},
	}, nil
}
