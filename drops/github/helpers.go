// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package github hosts the GitHub launch connector — fourth T1
// stop after Slack, Gmail, and Sheets. Three action drops cover
// the common Zapier-shape patterns:
//
//	github_create_issue  — open a new issue on a repo
//	github_list_issues   — query issues (pairs with poll_trigger for
//	                       "fire on new issue" workflows)
//	github_add_comment   — comment on an issue or PR
//
// Webhook-driven triggers (`github_on_push`, `github_on_new_pr`)
// are queued separately — they need the same shape of work as
// slack_on_mention: HMAC-SHA256 signature verification against the
// webhook secret, plus tenant routing by installation/repo. v1
// here ships the action drops, which unlock both manual workflows
// and "every 5 min: list issues since cursor → fire" composition
// with poll_trigger.
package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/internal/apibase"
	"github.com/dazyflow/dazyflow/drops/internal/oauthtok"
	"github.com/dazyflow/dazyflow/drops/internal/params"
	hfnet "github.com/dazyflow/dazyflow/drops/net"
)

// maxResponseBytes caps how much of an API response we buffer, so a
// hostile or buggy upstream (reachable via the base_url override) can't
// OOM the daemon by streaming an unbounded body.
const maxResponseBytes = 64 << 20 // 64 MiB

// tokenHook holds the daemon's per-account GitHub OAuth lookup ("github" in
// dzd's provider registry) plus the resolve sequence shared with the other
// OAuth connectors (drops/internal/oauthtok).
var tokenHook = oauthtok.New("GitHub", "github", "GitHub")

func SetTokenLookup(fn oauthtok.Lookup) { tokenHook.Set(fn) }

func resolveToken(ctx context.Context, job core.Job) (string, error) {
	return tokenHook.Resolve(ctx, job)
}

var httpBase = apibase.New("https://api.github.com")

// SetHTTPBase swaps the GitHub API root. Tests point this at an
// httptest server so they never hit api.github.com. (Also lets
// GitHub Enterprise deployments self-host with their own API
// origin if anyone needs that down the line.)
func SetHTTPBase(base string) { httpBase.Set(base) }

func currentHTTPBase() string { return httpBase.Get() }

// gitHubErrorEnvelope mirrors GitHub's REST v3 error shape. Most
// errors include a message + documentation_url; some include a
// list of detailed errors. The extractor returns the most useful
// human-readable string so users see something like "Validation
// Failed: title is too long" instead of a raw JSON blob.
type gitHubErrorEnvelope struct {
	Message          string `json:"message"`
	DocumentationURL string `json:"documentation_url"`
	Errors           []struct {
		Resource string `json:"resource"`
		Field    string `json:"field"`
		Code     string `json:"code"`
		Message  string `json:"message"`
	} `json:"errors"`
}

// githubDoIdem is githubDo with an explicit Idempotency-Key. The engine
// auto-retries terminal/leaf nodes (and OnErrorRetry edges) with a stable
// key per node per run, so a side-effecting POST — create issue, add
// comment — could fire twice. GitHub REST honors the Idempotency-Key
// header, deduping a replayed POST server-side, so threading
// job.IdempotencyKey() here makes a retry safe. An empty idemKey sends no
// header (read-only calls don't need one).
func githubDoIdem(ctx context.Context, method, url, token string, body []byte, timeoutMS int, idemKey string) (int, []byte, error) {
	status, raw, _, err := githubDoIdemH(ctx, method, url, token, body, timeoutMS, idemKey)
	return status, raw, err
}

// githubDoH is githubDo plus the response headers, for callers that need
// them (e.g. list pagination follows the Link header's rel="next").
func githubDoH(ctx context.Context, method, url, token string, body []byte, timeoutMS int) (int, []byte, http.Header, error) {
	return githubDoIdemH(ctx, method, url, token, body, timeoutMS, "")
}

// githubDoIdemH is the shared implementation: githubDoH plus an optional
// Idempotency-Key header (sent only when idemKey is non-empty).
func githubDoIdemH(ctx context.Context, method, url, token string, body []byte, timeoutMS int, idemKey string) (int, []byte, http.Header, error) {
	if timeoutMS <= 0 {
		timeoutMS = 15000
	}
	headers := map[string]string{
		"Authorization":        "Bearer " + token,
		"Accept":               "application/vnd.github+json",
		"X-GitHub-Api-Version": "2022-11-28",
	}
	if idemKey != "" {
		headers["Idempotency-Key"] = idemKey
	}
	if body != nil {
		headers["Content-Type"] = "application/json"
	}

	// The request URL is guarded on every call — including pagination's
	// rel="next" links, which come back from the API response and are followed
	// verbatim. net.Do's SSRF client blocks loopback/private/link-local targets
	// and the egress allowlist (when set) bounds which public hosts the bearer
	// token may be sent to.
	return hfnet.Do(ctx, method, url, headers, body, timeoutMS, maxResponseBytes)
}

// parseNextLink extracts the rel="next" URL from a GitHub Link header,
// returning "" when there is no next page. GitHub's pagination header looks
// like:
//
//	<https://api.github.com/...&page=2>; rel="next", <...&page=9>; rel="last"
func parseNextLink(header string) string {
	if header == "" {
		return ""
	}
	for _, part := range strings.Split(header, ",") {
		segs := strings.Split(part, ";")
		if len(segs) < 2 {
			continue
		}
		isNext := false
		for _, s := range segs[1:] {
			if strings.TrimSpace(s) == `rel="next"` {
				isNext = true
				break
			}
		}
		if !isNext {
			continue
		}
		u := strings.TrimSpace(segs[0])
		u = strings.TrimPrefix(u, "<")
		u = strings.TrimSuffix(u, ">")
		return u
	}
	return ""
}

// resolveBody figures out the issue/comment body: params.body, overridden
// by the 'body' input port. A string passes through; a structured value
// is rendered as a fenced JSON block (so a rows-list wired in still lands
// as readable Markdown rather than failing). Matches the former scripted
// behaviour.
func resolveBody(job core.Job) string {
	body := params.StringDefault(job.Params, "body", "")
	if in, ok := job.Input["body"]; ok && in.Inline != nil {
		switch v := in.Inline.(type) {
		case string:
			body = v
		case []byte:
			body = string(v)
		default:
			if b, err := json.MarshalIndent(v, "", "  "); err == nil {
				body = "```json\n" + string(b) + "\n```"
			}
		}
	}
	return body
}

func extractGitHubError(body []byte) string {
	var env gitHubErrorEnvelope
	if err := json.Unmarshal(body, &env); err == nil && env.Message != "" {
		if len(env.Errors) > 0 {
			// Surface the first detailed validation error inline —
			// GitHub puts the actually-helpful detail there ("field
			// 'title' is missing" rather than just "Validation Failed").
			e := env.Errors[0]
			if e.Message != "" {
				return fmt.Sprintf("%s: %s", env.Message, e.Message)
			}
			if e.Field != "" {
				return fmt.Sprintf("%s: field %q (%s)", env.Message, e.Field, e.Code)
			}
		}
		return env.Message
	}
	if len(body) > 512 {
		return string(body[:512]) + "…"
	}
	return string(body)
}
