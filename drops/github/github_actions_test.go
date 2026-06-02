package github

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"git.sr.ht/~klahr/hazyflow/core"
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
	meta := res.Output["meta"].Inline.(map[string]any)
	if meta["number"] != 7 {
		t.Errorf("meta = %+v", meta)
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
	issues := res.Output["issues"].Inline.([]any)
	if len(issues) != 2 {
		t.Errorf("got %d issues", len(issues))
	}
}
