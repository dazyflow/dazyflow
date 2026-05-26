package github

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"git.sr.ht/~klahr/hazy-flow/core"
)

// fakeGitHub stubs the three endpoints we hit. Same pattern as the
// other connector fakes.
type fakeGitHub struct {
	server *httptest.Server

	mu sync.Mutex

	// create issue
	lastCreatePath string
	lastCreateBody []byte
	lastCreateAuth string
	lastCreateAccept string
	lastCreateAPIVer string
	createStatus int
	createResp   string

	// list issues
	lastListPath string
	lastListQuery string
	lastListAuth string
	listStatus   int
	listResp     string

	// add comment
	lastCommentPath string
	lastCommentBody []byte
	commentStatus   int
	commentResp     string
}

func newFakeGitHub(t *testing.T) *fakeGitHub {
	t.Helper()
	f := &fakeGitHub{
		createStatus: 201,
		createResp: `{"id":123456,"number":42,"node_id":"I_kwDO","html_url":"https://github.com/octo/repo/issues/42","state":"open"}`,
		listStatus: 200,
		listResp: `[
			{"number":1,"title":"first","state":"open","user":{"login":"alice"}},
			{"number":2,"title":"second","state":"open","user":{"login":"bob"}}
		]`,
		commentStatus: 201,
		commentResp: `{"id":99,"node_id":"IC_kw","html_url":"https://github.com/octo/repo/issues/42#issuecomment-99"}`,
	}
	handler := func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		switch {
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/comments"):
			f.mu.Lock()
			f.lastCommentPath = r.URL.Path
			f.lastCommentBody = body
			status := f.commentStatus
			resp := f.commentResp
			f.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = io.WriteString(w, resp)
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/issues"):
			f.mu.Lock()
			f.lastCreatePath = r.URL.Path
			f.lastCreateBody = body
			f.lastCreateAuth = r.Header.Get("Authorization")
			f.lastCreateAccept = r.Header.Get("Accept")
			f.lastCreateAPIVer = r.Header.Get("X-GitHub-Api-Version")
			status := f.createStatus
			resp := f.createResp
			f.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = io.WriteString(w, resp)
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/issues"):
			f.mu.Lock()
			f.lastListPath = r.URL.Path
			f.lastListQuery = r.URL.RawQuery
			f.lastListAuth = r.Header.Get("Authorization")
			status := f.listStatus
			resp := f.listResp
			f.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = io.WriteString(w, resp)
		default:
			w.WriteHeader(404)
		}
	}
	f.server = httptest.NewServer(http.HandlerFunc(handler))
	t.Cleanup(f.server.Close)
	prev := currentHTTPBase()
	SetHTTPBase(f.server.URL)
	t.Cleanup(func() { SetHTTPBase(prev) })
	return f
}

func installTokenLookup(t *testing.T, fn TokenLookup) {
	t.Helper()
	tokenLookupMu.RLock()
	prev := tokenLookup
	tokenLookupMu.RUnlock()
	SetTokenLookup(fn)
	t.Cleanup(func() { SetTokenLookup(prev) })
}

// ===== github_create_issue ==========================================

func TestGitHubCreateIssue_HappyPath(t *testing.T) {
	fg := newFakeGitHub(t)
	res, err := executeGitHubCreateIssue(t.Context(), core.Job{
		Params: map[string]any{
			"token": "gho_test",
			"owner": "octo", "repo": "repo",
			"title": "Build failed", "body": "Job #42 errored",
		},
	}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	fg.mu.Lock()
	defer fg.mu.Unlock()
	if fg.lastCreatePath != "/repos/octo/repo/issues" {
		t.Errorf("path = %q", fg.lastCreatePath)
	}
	if fg.lastCreateAuth != "Bearer gho_test" {
		t.Errorf("auth = %q", fg.lastCreateAuth)
	}
	// Required GitHub REST v3 headers — Accept signals JSON envelope
	// version, X-GitHub-Api-Version pins the API revision so a
	// future bump doesn't silently change behavior.
	if fg.lastCreateAccept != "application/vnd.github+json" {
		t.Errorf("Accept = %q", fg.lastCreateAccept)
	}
	if fg.lastCreateAPIVer != "2022-11-28" {
		t.Errorf("X-GitHub-Api-Version = %q", fg.lastCreateAPIVer)
	}
	var sent map[string]any
	_ = json.Unmarshal(fg.lastCreateBody, &sent)
	if sent["title"] != "Build failed" || sent["body"] != "Job #42 errored" {
		t.Errorf("body = %+v", sent)
	}

	meta := res.Output["meta"].Inline.(map[string]any)
	if meta["number"] != int64(42) {
		t.Errorf("meta.number = %v (%T)", meta["number"], meta["number"])
	}
	if meta["html_url"] != "https://github.com/octo/repo/issues/42" {
		t.Errorf("meta.html_url = %v", meta["html_url"])
	}
}

func TestGitHubCreateIssue_BodyInputWinsOverParams(t *testing.T) {
	fg := newFakeGitHub(t)
	_, _ = executeGitHubCreateIssue(t.Context(), core.Job{
		Params: map[string]any{
			"token": "x", "owner": "o", "repo": "r",
			"title": "t", "body": "from-params",
		},
		Input: map[string]core.Ref{
			"body": {Inline: "from-port"},
		},
	}, nil)
	fg.mu.Lock()
	defer fg.mu.Unlock()
	var sent map[string]any
	_ = json.Unmarshal(fg.lastCreateBody, &sent)
	if sent["body"] != "from-port" {
		t.Errorf("body = %v, want from-port", sent["body"])
	}
}

func TestGitHubCreateIssue_ObjectBodyBecomesJSONCodeBlock(t *testing.T) {
	// A common shape is "I got this event payload — file an issue
	// with the JSON quoted." We render objects/maps as a fenced
	// json code block so GitHub Markdown displays them cleanly.
	fg := newFakeGitHub(t)
	_, _ = executeGitHubCreateIssue(t.Context(), core.Job{
		Params: map[string]any{
			"token": "x", "owner": "o", "repo": "r", "title": "t",
		},
		Input: map[string]core.Ref{
			"body": {Inline: map[string]any{"event": "deploy_failed"}},
		},
	}, nil)
	fg.mu.Lock()
	defer fg.mu.Unlock()
	var sent map[string]any
	_ = json.Unmarshal(fg.lastCreateBody, &sent)
	body, _ := sent["body"].(string)
	if !strings.HasPrefix(body, "```json\n") || !strings.HasSuffix(body, "\n```") {
		t.Errorf("body not a fenced json block: %q", body)
	}
	if !strings.Contains(body, "deploy_failed") {
		t.Errorf("body missing payload: %q", body)
	}
}

func TestGitHubCreateIssue_LabelsAndAssignees(t *testing.T) {
	fg := newFakeGitHub(t)
	_, _ = executeGitHubCreateIssue(t.Context(), core.Job{
		Params: map[string]any{
			"token": "x", "owner": "o", "repo": "r",
			"title": "t", "body": "x",
			"labels":    []string{"bug", "p1"},
			"assignees": []string{"alice", "bob"},
			"milestone": 5,
		},
	}, nil)
	fg.mu.Lock()
	defer fg.mu.Unlock()
	var sent map[string]any
	_ = json.Unmarshal(fg.lastCreateBody, &sent)
	if labels, _ := sent["labels"].([]any); len(labels) != 2 || labels[0] != "bug" {
		t.Errorf("labels = %v", sent["labels"])
	}
	if a, _ := sent["assignees"].([]any); len(a) != 2 || a[1] != "bob" {
		t.Errorf("assignees = %v", sent["assignees"])
	}
	if sent["milestone"] != float64(5) {
		t.Errorf("milestone = %v", sent["milestone"])
	}
}

func TestGitHubCreateIssue_TitleRequired(t *testing.T) {
	_ = newFakeGitHub(t)
	res, _ := executeGitHubCreateIssue(t.Context(), core.Job{
		Params: map[string]any{"token": "x", "owner": "o", "repo": "r"},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("status=%q code=%q", res.Status, res.Error.Code)
	}
}

func TestGitHubCreateIssue_BlankTitleRejected(t *testing.T) {
	_ = newFakeGitHub(t)
	res, _ := executeGitHubCreateIssue(t.Context(), core.Job{
		Params: map[string]any{
			"token": "x", "owner": "o", "repo": "r",
			"title": "   ", "body": "x",
		},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("status=%q code=%q", res.Status, res.Error.Code)
	}
}

func TestGitHubCreateIssue_GitHubErrorEnvelope(t *testing.T) {
	// Validation Failed responses include a nested `errors` array
	// with the detailed message — surface that, not just "Validation
	// Failed".
	fg := newFakeGitHub(t)
	fg.createStatus = 422
	fg.createResp = `{"message":"Validation Failed","errors":[{"resource":"Issue","field":"title","code":"missing","message":"title is required"}]}`
	res, _ := executeGitHubCreateIssue(t.Context(), core.Job{
		Params: map[string]any{
			"token": "x", "owner": "o", "repo": "r", "title": "t", "body": "x",
		},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "github_error" {
		t.Fatalf("status=%q code=%q", res.Status, res.Error.Code)
	}
	if !strings.Contains(res.Error.Message, "Validation Failed") {
		t.Errorf("missing top-level message: %q", res.Error.Message)
	}
	if !strings.Contains(res.Error.Message, "title is required") {
		t.Errorf("missing detailed error: %q", res.Error.Message)
	}
}

func TestGitHubCreateIssue_OAuthLookupUsed(t *testing.T) {
	fg := newFakeGitHub(t)
	var sawAccount string
	installTokenLookup(t, func(_ context.Context, account string) (string, error) {
		sawAccount = account
		return "gho_from_oauth", nil
	})
	res, _ := executeGitHubCreateIssue(t.Context(), core.Job{
		Params: map[string]any{
			"account": "team-bot",
			"owner":   "o", "repo": "r", "title": "t", "body": "x",
		},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	if sawAccount != "team-bot" {
		t.Errorf("account = %q", sawAccount)
	}
	fg.mu.Lock()
	defer fg.mu.Unlock()
	if fg.lastCreateAuth != "Bearer gho_from_oauth" {
		t.Errorf("auth = %q", fg.lastCreateAuth)
	}
}

func TestGitHubCreateIssue_NoTokenIsAuthError(t *testing.T) {
	_ = newFakeGitHub(t)
	installTokenLookup(t, nil)
	res, _ := executeGitHubCreateIssue(t.Context(), core.Job{
		Params: map[string]any{
			"owner": "o", "repo": "r", "title": "t", "body": "x",
		},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "auth" {
		t.Errorf("status=%q code=%q", res.Status, res.Error.Code)
	}
}

// ===== github_list_issues ===========================================

func TestGitHubListIssues_Basic(t *testing.T) {
	fg := newFakeGitHub(t)
	res, _ := executeGitHubListIssues(t.Context(), core.Job{
		Params: map[string]any{
			"token": "x", "owner": "o", "repo": "r",
		},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	issues := res.Output["issues"].Inline.([]any)
	if len(issues) != 2 {
		t.Fatalf("len(issues) = %d, want 2", len(issues))
	}
	first := issues[0].(map[string]any)
	if first["title"] != "first" {
		t.Errorf("issues[0].title = %v", first["title"])
	}
	fg.mu.Lock()
	defer fg.mu.Unlock()
	if !strings.Contains(fg.lastListQuery, "state=open") {
		t.Errorf("default state filter missing: %q", fg.lastListQuery)
	}
}

func TestGitHubListIssues_FiltersAreForwarded(t *testing.T) {
	fg := newFakeGitHub(t)
	_, _ = executeGitHubListIssues(t.Context(), core.Job{
		Params: map[string]any{
			"token": "x", "owner": "o", "repo": "r",
			"state":    "all",
			"labels":   []string{"bug", "p1"},
			"assignee": "alice",
			"since":    "2026-05-01T00:00:00Z",
			"per_page": 100,
		},
	}, nil)
	fg.mu.Lock()
	defer fg.mu.Unlock()
	q := fg.lastListQuery
	if !strings.Contains(q, "state=all") {
		t.Errorf("state missing: %q", q)
	}
	if !strings.Contains(q, "labels=bug%2Cp1") {
		t.Errorf("labels not comma-joined + URL-encoded: %q", q)
	}
	if !strings.Contains(q, "assignee=alice") {
		t.Errorf("assignee missing: %q", q)
	}
	if !strings.Contains(q, "since=2026-05-01T00%3A00%3A00Z") {
		t.Errorf("since not URL-encoded: %q", q)
	}
	if !strings.Contains(q, "per_page=100") {
		t.Errorf("per_page missing: %q", q)
	}
}

func TestGitHubListIssues_EmptyResponseStillEmitsArray(t *testing.T) {
	// GitHub returns an empty JSON array (not omitted) for no
	// results, but defensive code in our drop coalesces nil → []
	// either way so downstream nodes never see nil.
	fg := newFakeGitHub(t)
	fg.listResp = `[]`
	res, _ := executeGitHubListIssues(t.Context(), core.Job{
		Params: map[string]any{"token": "x", "owner": "o", "repo": "r"},
	}, nil)
	issues, ok := res.Output["issues"].Inline.([]any)
	if !ok {
		t.Fatalf("issues = %T, want []any", res.Output["issues"].Inline)
	}
	if len(issues) != 0 {
		t.Errorf("len(issues) = %d, want 0", len(issues))
	}
}

func TestGitHubListIssues_RatelimitedSurfaces(t *testing.T) {
	// 403 with rate-limit message is the most common GitHub error;
	// the human-readable message must make it into our error.
	fg := newFakeGitHub(t)
	fg.listStatus = 403
	fg.listResp = `{"message":"API rate limit exceeded for user","documentation_url":"https://docs.github.com/rest/overview/resources-in-the-rest-api#rate-limiting"}`
	res, _ := executeGitHubListIssues(t.Context(), core.Job{
		Params: map[string]any{"token": "x", "owner": "o", "repo": "r"},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "github_error" {
		t.Fatalf("status=%q code=%q", res.Status, res.Error.Code)
	}
	if !strings.Contains(res.Error.Message, "rate limit exceeded") {
		t.Errorf("missing GitHub message: %q", res.Error.Message)
	}
}

// ===== github_add_comment ===========================================

func TestGitHubAddComment_HappyPath(t *testing.T) {
	fg := newFakeGitHub(t)
	res, _ := executeGitHubAddComment(t.Context(), core.Job{
		Params: map[string]any{
			"token": "x", "owner": "o", "repo": "r",
			"issue_number": 42,
			"body":         "Looks good to me",
		},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	fg.mu.Lock()
	defer fg.mu.Unlock()
	if fg.lastCommentPath != "/repos/o/r/issues/42/comments" {
		t.Errorf("path = %q", fg.lastCommentPath)
	}
	var sent map[string]any
	_ = json.Unmarshal(fg.lastCommentBody, &sent)
	if sent["body"] != "Looks good to me" {
		t.Errorf("body = %v", sent["body"])
	}
	meta := res.Output["meta"].Inline.(map[string]any)
	if meta["id"] != int64(99) {
		t.Errorf("meta.id = %v", meta["id"])
	}
}

func TestGitHubAddComment_MissingIssueNumber(t *testing.T) {
	_ = newFakeGitHub(t)
	res, _ := executeGitHubAddComment(t.Context(), core.Job{
		Params: map[string]any{
			"token": "x", "owner": "o", "repo": "r", "body": "x",
		},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("status=%q code=%q", res.Status, res.Error.Code)
	}
}

func TestGitHubAddComment_EmptyBodyRejected(t *testing.T) {
	_ = newFakeGitHub(t)
	res, _ := executeGitHubAddComment(t.Context(), core.Job{
		Params: map[string]any{
			"token": "x", "owner": "o", "repo": "r", "issue_number": 1,
		},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_input" {
		t.Errorf("status=%q code=%q", res.Status, res.Error.Code)
	}
}

func TestGitHubAddComment_BodyPortOverridesParams(t *testing.T) {
	fg := newFakeGitHub(t)
	_, _ = executeGitHubAddComment(t.Context(), core.Job{
		Params: map[string]any{
			"token": "x", "owner": "o", "repo": "r",
			"issue_number": 1, "body": "from-params",
		},
		Input: map[string]core.Ref{
			"body": {Inline: "from-port"},
		},
	}, nil)
	fg.mu.Lock()
	defer fg.mu.Unlock()
	var sent map[string]any
	_ = json.Unmarshal(fg.lastCommentBody, &sent)
	if sent["body"] != "from-port" {
		t.Errorf("body = %v", sent["body"])
	}
}
