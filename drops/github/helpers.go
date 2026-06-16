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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"git.sr.ht/~klahr/hazyflow/core"
	"git.sr.ht/~klahr/hazyflow/drops/internal/params"
	hfnet "git.sr.ht/~klahr/hazyflow/drops/net"
)

// maxResponseBytes caps how much of an API response we buffer, so a
// hostile or buggy upstream (reachable via the base_url override) can't
// OOM the daemon by streaming an unbounded body.
const maxResponseBytes = 64 << 20 // 64 MiB

// TokenLookup matches the per-connector pattern. GitHub's OAuth
// app is registered as "github" in hzd's OAuth provider registry.
type TokenLookup func(ctx context.Context, account string) (string, error)

var (
	tokenLookupMu sync.RWMutex
	tokenLookup   TokenLookup
)

func SetTokenLookup(fn TokenLookup) {
	tokenLookupMu.Lock()
	defer tokenLookupMu.Unlock()
	tokenLookup = fn
}

func resolveToken(ctx context.Context, job core.Job) (string, error) {
	if t, _ := params.StringOpt(job.Params, "token"); t != "" {
		return t, nil
	}
	account, _ := params.StringOpt(job.Params, "account")
	if account == "" {
		account = "default"
	}
	tokenLookupMu.RLock()
	fn := tokenLookup
	tokenLookupMu.RUnlock()
	if fn == nil {
		return "", fmt.Errorf("no GitHub token: pass `token` directly or connect a GitHub account via /api/v1/oauth/github/authorize")
	}
	tok, err := fn(ctx, account)
	if err != nil {
		return "", fmt.Errorf("lookup token for account %q: %w", account, err)
	}
	if tok == "" {
		return "", fmt.Errorf("GitHub account %q is not connected", account)
	}
	return tok, nil
}

var (
	httpBaseMu sync.RWMutex
	httpBase   = "https://api.github.com"
)

// SetHTTPBase swaps the GitHub API root. Tests point this at an
// httptest server so they never hit api.github.com. (Also lets
// GitHub Enterprise deployments self-host with their own API
// origin if anyone needs that down the line.)
func SetHTTPBase(base string) {
	httpBaseMu.Lock()
	defer httpBaseMu.Unlock()
	httpBase = base
}

func currentHTTPBase() string {
	httpBaseMu.RLock()
	defer httpBaseMu.RUnlock()
	return httpBase
}


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

// githubDo runs one authenticated GitHub REST call and returns the
// status code and raw body. Headers match the v3 API contract
// (Bearer token, vnd.github+json, pinned API version). The caller
// decides 2xx vs error so it can run extractGitHubError on the body.
func githubDo(ctx context.Context, method, url, token string, body []byte, timeoutMS int) (int, []byte, error) {
	status, raw, _, err := githubDoH(ctx, method, url, token, body, timeoutMS)
	return status, raw, err
}

// githubDoH is githubDo plus the response headers, for callers that need
// them (e.g. list pagination follows the Link header's rel="next").
func githubDoH(ctx context.Context, method, url, token string, body []byte, timeoutMS int) (int, []byte, http.Header, error) {
	if timeoutMS <= 0 {
		timeoutMS = 15000
	}
	reqCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutMS)*time.Millisecond)
	defer cancel()

	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(reqCtx, method, url, rdr)
	if err != nil {
		return 0, nil, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	// The request URL is guarded on every call — including pagination's
	// rel="next" links, which come back from the API response and are
	// followed verbatim. The SSRF client blocks loopback/private/link-local
	// targets and the egress allowlist (when set) bounds which public hosts
	// the bearer token may be sent to.
	if err := hfnet.EgressAllowedFor(ctx, url); err != nil {
		return 0, nil, nil, err
	}
	resp, err := hfnet.SafeHTTPClient(time.Duration(timeoutMS)*time.Millisecond, hfnet.PrivateEgressAllowed()).Do(req)
	if err != nil {
		return 0, nil, nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return resp.StatusCode, nil, resp.Header, err
	}
	if int64(len(raw)) > maxResponseBytes {
		return resp.StatusCode, nil, resp.Header, fmt.Errorf("github response exceeds %d bytes", maxResponseBytes)
	}
	return resp.StatusCode, raw, resp.Header, nil
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
