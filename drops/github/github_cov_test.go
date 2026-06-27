package github

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
)

// withGHAuthErr points the token lookup at a failing resolver so the
// connectors take their "auth" error branch.
func withGHAuthErr(t *testing.T) {
	t.Helper()
	SetHTTPBase("http://unused.invalid")
	SetTokenLookup(func(_ context.Context, _ string) (string, error) {
		return "", errors.New("no github account connected")
	})
	t.Cleanup(func() {
		SetHTTPBase("https://api.github.com")
		SetTokenLookup(nil)
	})
}

// withGHToken sets a working token lookup but points the base at a URL that
// fails to connect (so githubDo* returns a transport error).
func withGHHTTPErr(t *testing.T) {
	t.Helper()
	// Reserved TEST-NET-1 address that won't connect; egress is allowed
	// private in tests, so the failure is a transport/dial error, exercising
	// the github_http_error branch.
	SetHTTPBase("http://192.0.2.1:1")
	SetTokenLookup(func(_ context.Context, account string) (string, error) { return "ghp-" + account, nil })
	t.Cleanup(func() {
		SetHTTPBase("https://api.github.com")
		SetTokenLookup(nil)
	})
}

// --- executeGitHubAddComment gaps -----------------------------------------

func TestGitHubAddComment_ParamGuards(t *testing.T) {
	cases := []struct {
		name     string
		params   map[string]any
		wantCode string
	}{
		{"missing owner", map[string]any{"repo": "r", "issue_number": 1, "body": "x"}, "bad_param"},
		{"missing repo", map[string]any{"owner": "o", "issue_number": 1, "body": "x"}, "bad_param"},
		{"zero issue number", map[string]any{"owner": "o", "repo": "r", "issue_number": 0, "body": "x"}, "bad_param"},
		{"negative issue number", map[string]any{"owner": "o", "repo": "r", "issue_number": -3, "body": "x"}, "bad_param"},
	}
	withGHEnv(t, "http://unused.invalid")
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res, err := executeGitHubAddComment(context.Background(), core.Job{Params: c.params}, nil)
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if res.Status != core.StatusError || res.Error.Code != c.wantCode {
				t.Fatalf("status=%q code=%v, want %q", res.Status, res.Error, c.wantCode)
			}
		})
	}
}

func TestGitHubAddComment_AuthFailure(t *testing.T) {
	withGHAuthErr(t)
	res, _ := executeGitHubAddComment(context.Background(), core.Job{
		Params: map[string]any{"owner": "o", "repo": "r", "issue_number": 1, "body": "hi"},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "auth" {
		t.Fatalf("status=%q code=%v, want auth", res.Status, res.Error)
	}
}

func TestGitHubAddComment_HTTPError(t *testing.T) {
	withGHHTTPErr(t)
	res, _ := executeGitHubAddComment(context.Background(), core.Job{
		Params: map[string]any{"owner": "o", "repo": "r", "issue_number": 1, "body": "hi", "timeout_ms": 500},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "github_http_error" {
		t.Fatalf("status=%q code=%v, want github_http_error", res.Status, res.Error)
	}
}

func TestGitHubAddComment_APIError(t *testing.T) {
	srv := newGHServer(t, 404, map[string]any{"message": "Not Found"})
	withGHEnv(t, srv.URL)
	res, _ := executeGitHubAddComment(context.Background(), core.Job{
		Params: map[string]any{"owner": "o", "repo": "r", "issue_number": 9, "body": "hi"},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "github_error" {
		t.Fatalf("status=%q code=%v, want github_error", res.Status, res.Error)
	}
	if !strings.Contains(res.Error.Message, "Not Found") {
		t.Errorf("message = %q, want GitHub's text", res.Error.Message)
	}
}

// --- executeGitHubCreateIssue gaps ----------------------------------------

func TestGitHubCreateIssue_EmptyTitle(t *testing.T) {
	withGHEnv(t, "http://unused.invalid")
	res, _ := executeGitHubCreateIssue(context.Background(), core.Job{
		Params: map[string]any{"owner": "o", "repo": "r"},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Fatalf("status=%q code=%v, want bad_param", res.Status, res.Error)
	}
}

func TestGitHubCreateIssue_TitleInputNonText(t *testing.T) {
	withGHEnv(t, "http://unused.invalid")
	res, _ := executeGitHubCreateIssue(context.Background(), core.Job{
		Params: map[string]any{"owner": "o", "repo": "r", "title": "typed"},
		// A non-string, non-[]byte inline value makes TextInputOr return ok=false.
		Input: map[string]core.Ref{"title": {Inline: 42}},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_input" {
		t.Fatalf("status=%q code=%v, want bad_input", res.Status, res.Error)
	}
}

func TestGitHubCreateIssue_AuthFailure(t *testing.T) {
	withGHAuthErr(t)
	res, _ := executeGitHubCreateIssue(context.Background(), core.Job{
		Params: map[string]any{"owner": "o", "repo": "r", "title": "t"},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "auth" {
		t.Fatalf("status=%q code=%v, want auth", res.Status, res.Error)
	}
}

func TestGitHubCreateIssue_HTTPError(t *testing.T) {
	withGHHTTPErr(t)
	res, _ := executeGitHubCreateIssue(context.Background(), core.Job{
		Params: map[string]any{"owner": "o", "repo": "r", "title": "t", "timeout_ms": 500},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "github_http_error" {
		t.Fatalf("status=%q code=%v, want github_http_error", res.Status, res.Error)
	}
}

// TestGitHubCreateIssue_FullPayload exercises the optional payload branches:
// body, labels, assignees and milestone all populate the JSON body.
func TestGitHubCreateIssue_FullPayload(t *testing.T) {
	srv := newGHServer(t, 201, map[string]any{"number": 3, "html_url": "https://gh/i/3"})
	withGHEnv(t, srv.URL)
	res, err := executeGitHubCreateIssue(context.Background(), core.Job{
		Params: map[string]any{
			"owner": "o", "repo": "r", "title": "t", "body": "desc",
			"labels": []any{"bug"}, "assignees": []any{"alice", "bob"}, "milestone": 4,
		},
	}, nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v / %v", res.Status, res.Error, err)
	}
	if srv.lastBody["body"] != "desc" {
		t.Errorf("body = %v", srv.lastBody["body"])
	}
	if srv.lastBody["milestone"] != float64(4) {
		t.Errorf("milestone = %v", srv.lastBody["milestone"])
	}
	if as, ok := srv.lastBody["assignees"].([]any); !ok || len(as) != 2 {
		t.Errorf("assignees = %v", srv.lastBody["assignees"])
	}
}

// --- executeGitHubListIssues gaps -----------------------------------------

func TestGitHubListIssues_ParamGuards(t *testing.T) {
	withGHEnv(t, "http://unused.invalid")
	res, _ := executeGitHubListIssues(context.Background(), core.Job{
		Params: map[string]any{"repo": "r"},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Fatalf("status=%q code=%v, want bad_param", res.Status, res.Error)
	}
}

func TestGitHubListIssues_AuthFailure(t *testing.T) {
	withGHAuthErr(t)
	res, _ := executeGitHubListIssues(context.Background(), core.Job{
		Params: map[string]any{"owner": "o", "repo": "r"},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "auth" {
		t.Fatalf("status=%q code=%v, want auth", res.Status, res.Error)
	}
}

func TestGitHubListIssues_HTTPError(t *testing.T) {
	withGHHTTPErr(t)
	res, _ := executeGitHubListIssues(context.Background(), core.Job{
		Params: map[string]any{"owner": "o", "repo": "r", "timeout_ms": 500},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "github_http_error" {
		t.Fatalf("status=%q code=%v, want github_http_error", res.Status, res.Error)
	}
}

// TestGitHubListIssues_AssigneeSinceQuery covers the assignee + since query
// parameter branches.
func TestGitHubListIssues_AssigneeSinceQuery(t *testing.T) {
	srv := newGHServer(t, 200, []any{map[string]any{"number": 1}})
	withGHEnv(t, srv.URL)
	res, _ := executeGitHubListIssues(context.Background(), core.Job{
		Params: map[string]any{
			"owner": "o", "repo": "r",
			"assignee": "alice", "since": "2024-01-01T00:00:00Z",
		},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	if !strings.Contains(srv.lastQuery, "assignee=alice") {
		t.Errorf("query = %q, want assignee", srv.lastQuery)
	}
	if !strings.Contains(srv.lastQuery, "since=") {
		t.Errorf("query = %q, want since", srv.lastQuery)
	}
}

// TestGitHubListIssues_NonArrayBody covers the bad_response branch when a 2xx
// carries a body that isn't a JSON array.
func TestGitHubListIssues_NonArrayBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"message":"not an array"}`))
	}))
	t.Cleanup(srv.Close)
	withGHEnv(t, srv.URL)
	res, _ := executeGitHubListIssues(context.Background(), core.Job{
		Params: map[string]any{"owner": "o", "repo": "r"},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_response" {
		t.Fatalf("status=%q code=%v, want bad_response", res.Status, res.Error)
	}
}

// --- trigger sentinels (0% functions) -------------------------------------

func TestGitHubTriggers_NoTriggerData(t *testing.T) {
	cases := []struct {
		name string
		fn   func(context.Context, core.Job, chan<- core.Progress) (core.Result, error)
	}{
		{"on new pr", executeGitHubOnNewPR},
		{"on push", executeGitHubOnPush},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res, err := c.fn(context.Background(), core.Job{ID: "j1"}, nil)
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if res.Status != core.StatusError || res.Error == nil || res.Error.Code != "no_trigger_data" {
				t.Fatalf("status=%q error=%+v, want no_trigger_data", res.Status, res.Error)
			}
			if res.JobID != "j1" {
				t.Errorf("JobID = %q, want j1", res.JobID)
			}
		})
	}
}

// --- helpers gaps ----------------------------------------------------------

// TestResolveBody covers resolveBody's string passthrough, []byte case,
// structured-fenced case, and param fallback.
func TestResolveBody(t *testing.T) {
	cases := []struct {
		name   string
		job    core.Job
		want   string
		prefix string
	}{
		{
			name: "param only",
			job:  core.Job{Params: map[string]any{"body": "from param"}},
			want: "from param",
		},
		{
			name: "string input overrides param",
			job: core.Job{
				Params: map[string]any{"body": "from param"},
				Input:  map[string]core.Ref{"body": {Inline: "from input"}},
			},
			want: "from input",
		},
		{
			name: "byte slice input",
			job: core.Job{
				Input: map[string]core.Ref{"body": {Inline: []byte("raw bytes")}},
			},
			want: "raw bytes",
		},
		{
			name: "structured input fenced",
			job: core.Job{
				Input: map[string]core.Ref{"body": {Inline: map[string]any{"k": "v"}}},
			},
			prefix: "```json",
		},
		{
			name: "nil inline falls back to param",
			job: core.Job{
				Params: map[string]any{"body": "p"},
				Input:  map[string]core.Ref{"body": {Inline: nil}},
			},
			want: "p",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := resolveBody(c.job)
			if c.prefix != "" {
				if !strings.HasPrefix(got, c.prefix) {
					t.Errorf("resolveBody = %q, want prefix %q", got, c.prefix)
				}
				return
			}
			if got != c.want {
				t.Errorf("resolveBody = %q, want %q", got, c.want)
			}
		})
	}
}

// TestExtractGitHubError covers the error-envelope extractor's branches:
// first detailed message, field-only fallback, bare message, raw passthrough,
// and >512-byte truncation.
func TestExtractGitHubError(t *testing.T) {
	long := strings.Repeat("x", 600)
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "detailed message wins",
			body: `{"message":"Validation Failed","errors":[{"field":"title","code":"missing","message":"is required"}]}`,
			want: "Validation Failed: is required",
		},
		{
			name: "field-only fallback",
			body: `{"message":"Validation Failed","errors":[{"field":"title","code":"missing"}]}`,
			want: `Validation Failed: field "title" (missing)`,
		},
		{
			name: "bare message",
			body: `{"message":"Not Found"}`,
			want: "Not Found",
		},
		{
			name: "empty errors list keeps message",
			body: `{"message":"Server Error","errors":[]}`,
			want: "Server Error",
		},
		{
			name: "non-envelope raw passthrough",
			body: `plain text error`,
			want: "plain text error",
		},
		{
			name: "no message json passthrough",
			body: `{"foo":"bar"}`,
			want: `{"foo":"bar"}`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := extractGitHubError([]byte(c.body)); got != c.want {
				t.Errorf("extractGitHubError = %q, want %q", got, c.want)
			}
		})
	}

	t.Run("long body truncated", func(t *testing.T) {
		got := extractGitHubError([]byte(long))
		if !strings.HasSuffix(got, "…") || len(got) != 512+len("…") {
			t.Errorf("extractGitHubError truncation len=%d suffix=%v", len(got), strings.HasSuffix(got, "…"))
		}
	})
}

// TestGithubDoIdemH_Branches covers the helper's optional branches: the
// default-timeout path, the idempotency-key header, and the no-body path.
func TestGithubDoIdemH_Branches(t *testing.T) {
	var gotIdem, gotContentType, gotAuth string
	var gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotIdem = r.Header.Get("Idempotency-Key")
		gotContentType = r.Header.Get("Content-Type")
		gotAuth = r.Header.Get("Authorization")
		gotMethod = r.Method
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)

	t.Run("with idem key and body and default timeout", func(t *testing.T) {
		// timeoutMS<=0 exercises the 15000 default.
		status, _, _, err := githubDoIdemH(context.Background(), "POST", srv.URL, "tok", []byte(`{"a":1}`), 0, "idem-123")
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if status != 200 {
			t.Errorf("status = %d", status)
		}
		if gotIdem != "idem-123" {
			t.Errorf("Idempotency-Key = %q", gotIdem)
		}
		if gotContentType != "application/json" {
			t.Errorf("Content-Type = %q", gotContentType)
		}
		if gotAuth != "Bearer tok" {
			t.Errorf("Authorization = %q", gotAuth)
		}
	})

	t.Run("no body no idem key", func(t *testing.T) {
		gotIdem, gotContentType = "", ""
		_, _, _, err := githubDoIdemH(context.Background(), "GET", srv.URL, "tok", nil, 5000, "")
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if gotIdem != "" {
			t.Errorf("Idempotency-Key should be unset, got %q", gotIdem)
		}
		if gotContentType != "" {
			t.Errorf("Content-Type should be unset for nil body, got %q", gotContentType)
		}
		if gotMethod != "GET" {
			t.Errorf("method = %q", gotMethod)
		}
	})
}
