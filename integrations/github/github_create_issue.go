package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"git.sr.ht/~klahr/hazy-flow/core"
	"git.sr.ht/~klahr/hazy-flow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:             "github_create_issue",
			Version:        "1.0",
			Label:          "GitHub create issue",
			Color:          "#24292f",
			Icon:           "git-branch",
			BrandLogo:      "/brands/github.svg",
			Category:       "network",
			Provider:       "internal",
			Integration:    "GitHub",
			Tags:           []string{"github", "issue", "create"},
			Description:    "Open a new issue on a GitHub repo. Body comes from the 'body' input port if connected, otherwise from params.body (Markdown supported). Common shape: a webhook or poll trigger detects something noteworthy, this drop opens an issue on the team's tracker.",
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
					"account":    {"type":"string","default":"default"},
					"token":      {"type":"string","description":"Raw access token (PAT or OAuth user token); overrides 'account'."},
					"owner":      {"type":"string","description":"Repo owner — username or organization."},
					"repo":       {"type":"string","description":"Repo name (without the owner prefix)."},
					"title":      {"type":"string","description":"Issue title."},
					"body":       {"type":"string","description":"Issue body (Markdown). Overridden by the 'body' input port if connected."},
					"labels":     {"type":"array","items":{"type":"string"},"description":"Labels to attach. Must already exist on the repo."},
					"assignees":  {"type":"array","items":{"type":"string"},"description":"GitHub usernames to assign."},
					"milestone":  {"type":"integer","description":"Milestone number (not name) to attach to."},
					"timeout_ms":{"type":"integer","default":15000,"minimum":1}
				},
				"required":["owner","repo","title"]
			}`),
			Idempotent:  false, // Each call opens a new issue; not safe to retry on success.
			RetryPolicy: core.RetryExponentialBackoff,
		},
		Execute: executeGitHubCreateIssue,
	})
}

// executeGitHubCreateIssue POSTs to /repos/{owner}/{repo}/issues.
// Body resolution mirrors the rest of the network drops: input port
// wins, then params.body. Empty body is allowed (GitHub accepts
// title-only issues — useful for short "deploy failed" alerts).
func executeGitHubCreateIssue(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	owner, err := paramString(job.Params, "owner")
	if err != nil {
		return errResult(job, "bad_param", err.Error()), nil
	}
	repo, err := paramString(job.Params, "repo")
	if err != nil {
		return errResult(job, "bad_param", err.Error()), nil
	}
	title, err := paramString(job.Params, "title")
	if err != nil {
		return errResult(job, "bad_param", err.Error()), nil
	}
	if strings.TrimSpace(title) == "" {
		return errResult(job, "bad_param", "title must not be empty"), nil
	}
	token, err := resolveToken(ctx, job)
	if err != nil {
		return errResult(job, "auth", err.Error()), nil
	}

	body, _ := paramStringOpt(job.Params, "body")
	if input, ok := job.Input["body"]; ok && input.Inline != nil {
		switch v := input.Inline.(type) {
		case string:
			body = v
		case []byte:
			body = string(v)
		default:
			// Marshal objects/maps to JSON in a fenced code block —
			// GitHub Markdown renders this nicely, matching what a
			// developer reading "the failed event payload was: …"
			// would expect to see in an issue body.
			raw, mErr := json.MarshalIndent(v, "", "  ")
			if mErr != nil {
				return errResult(job, "bad_input", mErr.Error()), nil
			}
			body = "```json\n" + string(raw) + "\n```"
		}
	}

	payload := map[string]any{"title": title}
	if body != "" {
		payload["body"] = body
	}
	if labels := paramStringSlice(job.Params, "labels"); len(labels) > 0 {
		payload["labels"] = labels
	}
	if assignees := paramStringSlice(job.Params, "assignees"); len(assignees) > 0 {
		payload["assignees"] = assignees
	}
	if ms := paramIntDefault(job.Params, "milestone", 0); ms > 0 {
		payload["milestone"] = ms
	}
	jsonBody, _ := json.Marshal(payload)

	endpoint := fmt.Sprintf("%s/repos/%s/%s/issues",
		currentHTTPBase(), url.PathEscape(owner), url.PathEscape(repo))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(jsonBody))
	if err != nil {
		return errResult(job, "internal", err.Error()), nil
	}
	// GitHub accepts both Bearer and `token` schemes for OAuth user
	// tokens. Bearer matches every other connector in this codebase
	// so the auth-header pattern stays consistent.
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("Content-Type", "application/json")

	timeoutMs := paramIntDefault(job.Params, "timeout_ms", 15000)
	client := &http.Client{Timeout: time.Duration(timeoutMs) * time.Millisecond}
	resp, err := client.Do(req)
	if err != nil {
		return errResult(job, "send_failed", err.Error()), nil
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 256*1024))

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return errResult(job, "github_error",
			fmt.Sprintf("GitHub returned %d: %s", resp.StatusCode, extractGitHubError(respBody))), nil
	}

	var parsed map[string]any
	_ = json.Unmarshal(respBody, &parsed)
	meta := map[string]any{
		"number":   numberField(parsed, "number"),
		"html_url": stringField(parsed, "html_url"),
		"id":       numberField(parsed, "id"),
		"node_id":  stringField(parsed, "node_id"),
		"state":    stringField(parsed, "state"),
	}
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"meta": {MIME: "application/json", Inline: meta},
		},
	}, nil
}

func stringField(m map[string]any, k string) string {
	if m == nil {
		return ""
	}
	if s, ok := m[k].(string); ok {
		return s
	}
	return ""
}

// numberField extracts a JSON number into an int64. GitHub returns
// issue numbers and IDs as JSON numbers, which Go's encoding/json
// surfaces as float64 unless you decode into a typed struct.
func numberField(m map[string]any, k string) int64 {
	if m == nil {
		return 0
	}
	switch v := m[k].(type) {
	case float64:
		return int64(v)
	case int:
		return int64(v)
	case int64:
		return v
	}
	return 0
}
