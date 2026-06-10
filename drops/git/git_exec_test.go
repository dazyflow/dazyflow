package git

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"

	"git.sr.ht/~klahr/hazyflow/core"
)

// newRepo initialises a git repo in a fresh temp dir and returns the dir
// and a worktree handle for staging commits.
func newRepo(t *testing.T) (string, *gogit.Worktree) {
	t.Helper()
	dir := t.TempDir()
	repo, err := gogit.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("worktree: %v", err)
	}
	return dir, wt
}

// commit writes name=content into the repo and commits it, returning
// nothing — tests resolve revisions by ref (HEAD, HEAD~1) instead.
func commit(t *testing.T, dir string, wt *gogit.Worktree, name, content, msg string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := wt.Add(name); err != nil {
		t.Fatalf("add: %v", err)
	}
	_, err := wt.Commit(msg, &gogit.CommitOptions{
		Author: &object.Signature{Name: "Tester", Email: "t@test.invalid", When: time.Now()},
	})
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
}

func TestExecuteGitLog_ReturnsCommits(t *testing.T) {
	dir, wt := newRepo(t)
	commit(t, dir, wt, "f.txt", "v1\n", "first")
	commit(t, dir, wt, "f.txt", "v2\n", "second")
	commit(t, dir, wt, "f.txt", "v3\n", "third")

	res, err := executeGitLog(t.Context(), core.Job{ID: "j", WorkspaceRoot: dir}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("status = %q, err = %+v", res.Status, res.Error)
	}
	commits, ok := res.Output["commits"].Inline.([]map[string]any)
	if !ok {
		t.Fatalf("commits output is %T", res.Output["commits"].Inline)
	}
	if len(commits) != 3 {
		t.Fatalf("got %d commits, want 3", len(commits))
	}
	// Log walks newest-first.
	if commits[0]["summary"] != "third" {
		t.Errorf("commits[0].summary = %v, want third", commits[0]["summary"])
	}
	if commits[0]["author"] != "Tester" {
		t.Errorf("author = %v, want Tester", commits[0]["author"])
	}
	meta := res.Output["meta"].Inline.(map[string]any)
	if meta["count"] != 3 || meta["truncated"] != false {
		t.Errorf("meta = %+v, want count=3 truncated=false", meta)
	}
}

func TestExecuteGitLog_RespectsLimit(t *testing.T) {
	dir, wt := newRepo(t)
	for _, m := range []string{"c1", "c2", "c3", "c4"} {
		commit(t, dir, wt, "f.txt", m+"\n", m)
	}
	res, _ := executeGitLog(t.Context(), core.Job{
		ID: "j", WorkspaceRoot: dir,
		Params: map[string]any{"limit": 2},
	}, nil)
	commits := res.Output["commits"].Inline.([]map[string]any)
	if len(commits) != 2 {
		t.Fatalf("limit=2 returned %d commits, want 2", len(commits))
	}
}

func TestExecuteGitLog_Errors(t *testing.T) {
	repoDir, wt := newRepo(t)
	commit(t, repoDir, wt, "f.txt", "v1\n", "only")

	cases := []struct {
		name     string
		job      core.Job
		wantCode string
	}{
		{"no sandbox", core.Job{ID: "j"}, "no_sandbox"},
		{"not a repo", core.Job{ID: "j", WorkspaceRoot: t.TempDir()}, "open"},
		{"sandbox escape", core.Job{ID: "j", WorkspaceRoot: repoDir, Params: map[string]any{"path": "../../etc"}}, "sandbox_escape"},
		{"bad ref", core.Job{ID: "j", WorkspaceRoot: repoDir, Params: map[string]any{"ref": "no-such-ref"}}, "bad_ref"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res, _ := executeGitLog(t.Context(), c.job, nil)
			if res.Status != core.StatusError {
				t.Fatalf("status = %q, want error", res.Status)
			}
			if res.Error.Code != c.wantCode {
				t.Errorf("code = %q, want %q", res.Error.Code, c.wantCode)
			}
		})
	}
}

func TestExecuteGitDiff_ReportsChanges(t *testing.T) {
	dir, wt := newRepo(t)
	commit(t, dir, wt, "f.txt", "line1\n", "base")
	commit(t, dir, wt, "f.txt", "line1\nline2\n", "add a line")

	res, err := executeGitDiff(t.Context(), core.Job{ID: "j", WorkspaceRoot: dir}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("status = %q, err = %+v", res.Status, res.Error)
	}
	meta := res.Output["meta"].Inline.(map[string]any)
	if meta["files_changed"] != 1 {
		t.Errorf("files_changed = %v, want 1", meta["files_changed"])
	}
	if meta["insertions"] != 1 {
		t.Errorf("insertions = %v, want 1", meta["insertions"])
	}
	if diff, _ := res.Output["diff"].Inline.(string); diff == "" {
		t.Error("diff text is empty")
	}
}

func TestExecuteGitDiff_Errors(t *testing.T) {
	dir, wt := newRepo(t)
	commit(t, dir, wt, "f.txt", "v1\n", "only") // single commit ⇒ HEAD~1 unresolvable

	if res, _ := executeGitDiff(t.Context(), core.Job{ID: "j"}, nil); res.Error == nil || res.Error.Code != "no_sandbox" {
		t.Errorf("no sandbox: got %+v, want no_sandbox", res.Error)
	}
	// HEAD~1 doesn't exist with a single commit ⇒ bad_ref on "from".
	res, _ := executeGitDiff(t.Context(), core.Job{ID: "j", WorkspaceRoot: dir}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_ref" {
		t.Errorf("got %+v, want error/bad_ref", res.Error)
	}
}

func TestScpLikeHost(t *testing.T) {
	cases := []struct {
		in       string
		wantHost string
		wantOK   bool
	}{
		{"git@github.com:org/repo.git", "github.com", true},
		{"github.com:org/repo.git", "github.com", true},
		{"user@host.example:path", "host.example", true},
		{"/srv/repos/local.git", "", false}, // no colon ⇒ not scp-like
		{"./relative:withcolon", "", false}, // slash before colon ⇒ a path
		{":no-host", "", false},             // empty host part
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			host, ok := scpLikeHost(c.in)
			if ok != c.wantOK || (ok && host != c.wantHost) {
				t.Errorf("scpLikeHost(%q) = (%q, %v), want (%q, %v)", c.in, host, ok, c.wantHost, c.wantOK)
			}
		})
	}
}

func TestSandboxRel(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"", ".", false},
		{"sub", "sub", false},
		{"a/b", "a/b", false},
		{"  sub  ", "sub", false},
		{"/abs", "", true},
		{"..", "", true},
		{"../x", "", true},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got, err := sandboxRel(c.in)
			if (err != nil) != c.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, c.wantErr)
			}
			if !c.wantErr && got != c.want {
				t.Errorf("sandboxRel(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestTruncate(t *testing.T) {
	cases := []struct {
		s    string
		n    int
		want string
	}{
		{"hello", 10, "hello"}, // shorter than limit
		{"hello", 5, "hello"},  // exactly the limit
		{"hello", 3, "he…"},    // truncated with ellipsis
		{"hello", 1, "h"},      // n<=1 ⇒ raw cut, no ellipsis
		{"hello", 0, ""},
	}
	for _, c := range cases {
		if got := truncate(c.s, c.n); got != c.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", c.s, c.n, got, c.want)
		}
	}
}

func TestFirstLine(t *testing.T) {
	cases := map[string]string{
		"single line":   "single line",
		"first\nsecond": "first",
		"\nleading":     "",
		"trailing\n":    "trailing",
		"a\nb\nc":       "a",
	}
	for in, want := range cases {
		if got := firstLine(in); got != want {
			t.Errorf("firstLine(%q) = %q, want %q", in, got, want)
		}
	}
}
