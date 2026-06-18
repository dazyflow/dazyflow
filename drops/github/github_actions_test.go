package github

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
)

type ghTestServer struct {
	*httptest.Server
	lastMethod string
	lastPath   string
	lastQuery  string
	lastBody   map[string]any
	status     int
	resp       any
}

func newGHServer(t *testing.T, status int, resp any) *ghTestServer {
	t.Helper()
	s := &ghTestServer{status: status, resp: resp}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.lastMethod = r.Method
		s.lastPath = r.URL.Path
		s.lastQuery = r.URL.RawQuery
		if b, _ := io.ReadAll(r.Body); len(b) > 0 {
			_ = json.Unmarshal(b, &s.lastBody)
		}
		w.WriteHeader(s.status)
		_ = json.NewEncoder(w).Encode(s.resp)
	}))
	t.Cleanup(s.Close)
	return s
}

func withGHEnv(t *testing.T, base string) {
	t.Helper()
	SetHTTPBase(base)
	SetTokenLookup(func(_ context.Context, account string) (string, error) { return "ghp-" + account, nil })
	t.Cleanup(func() {
		SetHTTPBase("https://api.github.com")
		SetTokenLookup(nil)
	})
}

func TestGitHubCreateIssue_Posts(t *testing.T) {
	srv := newGHServer(t, 201, map[string]any{"number": 7, "html_url": "https://gh/i/7", "id": 99, "state": "open"})
	withGHEnv(t, srv.URL)

	res, err := executeGitHubCreateIssue(context.Background(), core.Job{
		Params: map[string]any{"owner": "o", "repo": "r", "title": "boom", "labels": []any{"bug"}},
	}, nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v / %v", res.Status, res.Error, err)
	}
	if srv.lastMethod != "POST" || srv.lastPath != "/repos/o/r/issues" {
		t.Errorf("%s %s", srv.lastMethod, srv.lastPath)
	}
	if srv.lastBody["title"] != "boom" {
		t.Errorf("body = %+v", srv.lastBody)
	}
	// The friendly scalar pins carry the link and number; the full
	// metadata blob is still emitted under "meta" (undeclared pin).
	if got := res.Output["issue_url"].Inline; got != "https://gh/i/7" {
		t.Errorf("issue_url = %v", got)
	}
	if got := res.Output["issue_number"].Inline; got != "7" {
		t.Errorf("issue_number = %v", got)
	}
	meta := res.Output["meta"].Inline.(map[string]any)
	if meta["number"] != 7 {
		t.Errorf("meta = %+v", meta)
	}
}

func TestGitHubCreateIssue_TitleInputOverridesParam(t *testing.T) {
	srv := newGHServer(t, 201, map[string]any{"number": 2})
	withGHEnv(t, srv.URL)
	res, _ := executeGitHubCreateIssue(context.Background(), core.Job{
		Params: map[string]any{"owner": "o", "repo": "r", "title": "typed"},
		Input:  map[string]core.Ref{"title": {Inline: "wired"}},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	if srv.lastBody["title"] != "wired" {
		t.Errorf("title = %v, want the wired input to win", srv.lastBody["title"])
	}
}

func TestGitHubCreateIssue_BodyInputStructuredFenced(t *testing.T) {
	srv := newGHServer(t, 201, map[string]any{"number": 1})
	withGHEnv(t, srv.URL)
	_, _ = executeGitHubCreateIssue(context.Background(), core.Job{
		Params: map[string]any{"owner": "o", "repo": "r", "title": "t"},
		Input:  map[string]core.Ref{"body": {Inline: map[string]any{"k": "v"}}},
	}, nil)
	body, _ := srv.lastBody["body"].(string)
	if body == "" || body[:7] != "```json" {
		t.Errorf("structured body should be fenced JSON, got %q", body)
	}
}

func TestGitHubCreateIssue_MissingOwner(t *testing.T) {
	withGHEnv(t, "http://unused")
	res, _ := executeGitHubCreateIssue(context.Background(), core.Job{
		Params: map[string]any{"repo": "r", "title": "t"},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("status=%q code=%v", res.Status, res.Error)
	}
}

func TestGitHubCreateIssue_APIError(t *testing.T) {
	srv := newGHServer(t, 422, map[string]any{"message": "Validation Failed", "errors": []any{map[string]any{"field": "title", "code": "missing", "message": "is required"}}})
	withGHEnv(t, srv.URL)
	res, _ := executeGitHubCreateIssue(context.Background(), core.Job{
		Params: map[string]any{"owner": "o", "repo": "r", "title": "t"},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "github_error" {
		t.Fatalf("status=%q code=%v", res.Status, res.Error)
	}
}

func TestGitHubAddComment_Posts(t *testing.T) {
	srv := newGHServer(t, 201, map[string]any{"id": 5, "html_url": "https://gh/c/5"})
	withGHEnv(t, srv.URL)
	res, err := executeGitHubAddComment(context.Background(), core.Job{
		Params: map[string]any{"owner": "o", "repo": "r", "issue_number": 12, "body": "hi"},
	}, nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	if srv.lastPath != "/repos/o/r/issues/12/comments" {
		t.Errorf("path = %q", srv.lastPath)
	}
	// The friendly scalar pin carries the link; the full metadata blob
	// is still emitted under "meta" (undeclared pin).
	if got := res.Output["comment_url"].Inline; got != "https://gh/c/5" {
		t.Errorf("comment_url = %v", got)
	}
	if res.Output["meta"].Inline == nil {
		t.Error("meta key should still be emitted")
	}
}

func TestGitHubAddComment_EmptyBody(t *testing.T) {
	withGHEnv(t, "http://unused")
	res, _ := executeGitHubAddComment(context.Background(), core.Job{
		Params: map[string]any{"owner": "o", "repo": "r", "issue_number": 1},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_input" {
		t.Errorf("status=%q code=%v", res.Status, res.Error)
	}
}

func TestGitHubListIssues_QueryAndResult(t *testing.T) {
	srv := newGHServer(t, 200, []any{map[string]any{"number": 1}, map[string]any{"number": 2}})
	withGHEnv(t, srv.URL)
	res, err := executeGitHubListIssues(context.Background(), core.Job{
		Params: map[string]any{"owner": "o", "repo": "r", "state": "all", "labels": []any{"bug", "p1"}, "per_page": 50},
	}, nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	if srv.lastPath != "/repos/o/r/issues" {
		t.Errorf("path = %q", srv.lastPath)
	}
	issues := res.Output["issues"].Inline.([]map[string]any)
	if len(issues) != 2 {
		t.Errorf("got %d issues", len(issues))
	}
	meta := res.Output["meta"].Inline.(map[string]any)
	if meta["count"] != 2 || meta["truncated"] != false {
		t.Errorf("meta = %+v, want count=2 truncated=false", meta)
	}
}

// TestGitHubListIssues_FiltersPRs locks in that pull requests (which the
// issues endpoint returns, marked by a pull_request key) are excluded by
// default and kept when include_prs is set.
func TestGitHubListIssues_FiltersPRs(t *testing.T) {
	mixed := []any{
		map[string]any{"number": 1, "title": "real issue"},
		map[string]any{"number": 2, "title": "a PR", "pull_request": map[string]any{"url": "https://gh/pulls/2"}},
		map[string]any{"number": 3, "title": "another issue"},
	}

	srv := newGHServer(t, 200, mixed)
	withGHEnv(t, srv.URL)
	res, _ := executeGitHubListIssues(context.Background(), core.Job{
		Params: map[string]any{"owner": "o", "repo": "r"},
	}, nil)
	issues := res.Output["issues"].Inline.([]map[string]any)
	if len(issues) != 2 {
		t.Fatalf("default got %d, want 2 (PR filtered out)", len(issues))
	}
	for _, it := range issues {
		if _, isPR := it["pull_request"]; isPR {
			t.Errorf("a pull request leaked through: %+v", it)
		}
	}

	srv2 := newGHServer(t, 200, mixed)
	withGHEnv(t, srv2.URL)
	res2, _ := executeGitHubListIssues(context.Background(), core.Job{
		Params: map[string]any{"owner": "o", "repo": "r", "include_prs": true},
	}, nil)
	if got := len(res2.Output["issues"].Inline.([]map[string]any)); got != 3 {
		t.Errorf("include_prs got %d, want 3", got)
	}
}

// pagedServer serves the given pages of issues, emitting a Link:
// rel="next" header pointing back at itself (?page=N) until the last page.
func pagedServer(t *testing.T, pages [][]any) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		idx := 0
		if p := r.URL.Query().Get("page"); p != "" {
			if n, err := strconv.Atoi(p); err == nil {
				idx = n - 1
			}
		}
		if idx < 0 || idx >= len(pages) {
			idx = 0
		}
		if idx < len(pages)-1 {
			w.Header().Set("Link", fmt.Sprintf(`<%s/repos/o/r/issues?page=%d>; rel="next"`, srv.URL, idx+2))
		}
		w.WriteHeader(200)
		_ = json.NewEncoder(w).Encode(pages[idx])
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestGitHubListIssues_Paginates(t *testing.T) {
	srv := pagedServer(t, [][]any{
		{map[string]any{"number": 1}, map[string]any{"number": 2}},
		{map[string]any{"number": 3}, map[string]any{"number": 4}},
		{map[string]any{"number": 5}},
	})
	withGHEnv(t, srv.URL)
	res, _ := executeGitHubListIssues(context.Background(), core.Job{
		Params: map[string]any{"owner": "o", "repo": "r"},
	}, nil)
	issues := res.Output["issues"].Inline.([]map[string]any)
	if len(issues) != 5 {
		t.Fatalf("got %d issues across pages, want 5", len(issues))
	}
	meta := res.Output["meta"].Inline.(map[string]any)
	if meta["truncated"] != false {
		t.Errorf("truncated = %v, want false (all pages fit)", meta["truncated"])
	}
}

func TestGitHubListIssues_TruncatesAtMaxResults(t *testing.T) {
	srv := pagedServer(t, [][]any{
		{map[string]any{"number": 1}, map[string]any{"number": 2}},
		{map[string]any{"number": 3}, map[string]any{"number": 4}},
	})
	withGHEnv(t, srv.URL)
	res, _ := executeGitHubListIssues(context.Background(), core.Job{
		Params: map[string]any{"owner": "o", "repo": "r", "max_results": 3},
	}, nil)
	issues := res.Output["issues"].Inline.([]map[string]any)
	if len(issues) != 3 {
		t.Fatalf("got %d, want 3 (capped at max_results)", len(issues))
	}
	meta := res.Output["meta"].Inline.(map[string]any)
	if meta["truncated"] != true {
		t.Errorf("truncated = %v, want true", meta["truncated"])
	}
}

// TestParseNextLink covers the Link-header pagination parser's edge cases —
// the heart of multi-page fetches. Only rel="next" should be followed.
func TestParseNextLink(t *testing.T) {
	cases := []struct {
		name, header, want string
	}{
		{"empty", "", ""},
		{"only next", `<https://api.github.com/x?page=2>; rel="next"`, "https://api.github.com/x?page=2"},
		{"next among prev/next/last",
			`<https://api.github.com/x?page=1>; rel="prev", <https://api.github.com/x?page=3>; rel="next", <https://api.github.com/x?page=9>; rel="last"`,
			"https://api.github.com/x?page=3"},
		{"last page has no next (only prev/first)",
			`<https://api.github.com/x?page=8>; rel="prev", <https://api.github.com/x?page=1>; rel="first"`, ""},
		{"segment without a rel is skipped", `<https://api.github.com/x?page=2>`, ""},
		{"whitespace around rel tolerated",
			`<https://api.github.com/x?page=2>;   rel="next"`, "https://api.github.com/x?page=2"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := parseNextLink(c.header); got != c.want {
				t.Errorf("parseNextLink(%q) = %q, want %q", c.header, got, c.want)
			}
		})
	}
}

// TestGitHubListIssues_RateLimited covers the rate-limit response path: a 403
// with GitHub's rate-limit body (and X-RateLimit headers) surfaces a clear
// github_error carrying GitHub's message, not a silent empty result.
func TestGitHubListIssues_RateLimited(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Limit", "60")
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", "1700000000")
		w.WriteHeader(403)
		_, _ = w.Write([]byte(`{"message":"API rate limit exceeded for 1.2.3.4.","documentation_url":"https://docs.github.com/rest/overview/rate-limits"}`))
	}))
	t.Cleanup(srv.Close)
	withGHEnv(t, srv.URL)

	res, err := executeGitHubListIssues(context.Background(), core.Job{
		Params: map[string]any{"owner": "o", "repo": "r"},
	}, nil)
	if err != nil {
		t.Fatalf("execute err: %v", err)
	}
	if res.Status != core.StatusError || res.Error.Code != "github_error" {
		t.Fatalf("res = %+v, want github_error", res)
	}
	if !strings.Contains(res.Error.Message, "rate limit exceeded") {
		t.Errorf("message = %q, want GitHub's rate-limit text", res.Error.Message)
	}
}

// TestGitHubListIssues_PaginationCap covers the maxPages safety net: a server
// that always advertises a next page must not loop forever — the drop stops
// and marks the result truncated.
func TestGitHubListIssues_PaginationCap(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Always point at a further page → unbounded without the cap.
		w.Header().Set("Link", fmt.Sprintf(`<%s/repos/o/r/issues?page=999>; rel="next"`, srv.URL))
		w.WriteHeader(200)
		_ = json.NewEncoder(w).Encode([]any{map[string]any{"number": 1}})
	}))
	t.Cleanup(srv.Close)
	withGHEnv(t, srv.URL)

	res, _ := executeGitHubListIssues(context.Background(), core.Job{
		Params: map[string]any{"owner": "o", "repo": "r", "max_results": 100000},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("res = %+v", res)
	}
	meta := res.Output["meta"].Inline.(map[string]any)
	if meta["truncated"] != true {
		t.Errorf("truncated = %v, want true (hit the maxPages cap)", meta["truncated"])
	}
}
