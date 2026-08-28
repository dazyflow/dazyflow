// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package git

import (
	"context"
	"crypto/tls"
	"errors"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"git.sr.ht/~klahr/dazyflow/core"
	hfnet "git.sr.ht/~klahr/dazyflow/drops/net"
	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// gitHTTPBackend locates git-http-backend, skipping the test when the host
// has no git installed (CI images without it).
func gitHTTPBackend(t *testing.T) string {
	t.Helper()
	for _, p := range []string{
		"/usr/lib/git-core/git-http-backend",
		"/usr/libexec/git-core/git-http-backend",
	} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed; skipping http-backend clone test")
	}
	t.Skip("git-http-backend not found; skipping http-backend clone test")
	return ""
}

// serveBareRepoHTTPS publishes src (a working repo dir) as a bare repo over a
// loopback HTTPS server backed by git-http-backend, and installs a go-git
// https transport that trusts the test cert. It returns the clone URL. Private
// egress is enabled so guardRepoURL lets the loopback host through. Everything
// is restored on test cleanup.
func serveBareRepoHTTPS(t *testing.T, src string) string {
	t.Helper()
	backend := gitHTTPBackend(t)

	// Make a bare mirror of src so smart-http can serve it.
	root := t.TempDir()
	bare := filepath.Join(root, "repo.git")
	if out, err := exec.Command("git", "clone", "--bare", src, bare).CombinedOutput(); err != nil {
		t.Fatalf("git clone --bare: %v\n%s", err, out)
	}
	// Allow fetching from the bare repo over http.
	_ = exec.Command("git", "-C", bare, "config", "http.receivepack", "false").Run()

	handler := &cgi.Handler{
		Path: backend,
		Env: []string{
			"GIT_PROJECT_ROOT=" + root,
			"GIT_HTTP_EXPORT_ALL=1",
		},
	}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)

	// go-git https transport that trusts the httptest cert.
	client := &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}}
	InstallGuardedHTTPTransport(client)
	t.Cleanup(func() { InstallGuardedHTTPTransport(nil) })

	hfnet.SetAllowPrivateEgress(true)
	t.Cleanup(func() { hfnet.SetAllowPrivateEgress(false) })

	return srv.URL + "/repo.git"
}

// TestExecuteGitCheckout_Success drives the full success path of
// executeGitCheckout against a loopback HTTPS git server: a fresh clone, then
// a re-run that pulls.
func TestExecuteGitCheckout_Success(t *testing.T) {
	src, _ := buildSource(t)
	url := serveBareRepoHTTPS(t, src)
	ws := t.TempDir()

	ch, collect := drainProgress(t)
	job := core.Job{ID: "j", NodeID: "n", GraphID: "g", WorkspaceRoot: ws, Params: map[string]any{"url": url}}
	res, err := executeGitCheckout(t.Context(), job, ch)
	got := collect()
	if err != nil {
		t.Fatalf("unexpected go error: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("status = %q, err = %+v", res.Status, res.Error)
	}
	if res.Output["sha"].Inline == "" {
		t.Error("expected a non-empty sha output")
	}
	meta := res.Output["meta"].Inline.(map[string]any)
	if meta["mode"] != "cloned" {
		t.Errorf("mode = %v, want cloned", meta["mode"])
	}
	if len(got) == 0 {
		t.Error("expected progress events from clone")
	}

	// Re-run over the existing clone ⇒ pulled.
	res2, _ := executeGitCheckout(t.Context(), job, nil)
	if res2.Status != core.StatusOK {
		t.Fatalf("re-run status = %q, err = %+v", res2.Status, res2.Error)
	}
	meta2 := res2.Output["meta"].Inline.(map[string]any)
	if meta2["mode"] != "pulled" {
		t.Errorf("re-run mode = %v, want pulled", meta2["mode"])
	}
}

// TestExecuteGitCheckout_WithRef clones a specific branch end-to-end so the
// ref-targeting branches in executeGitCheckout/openOrClone run.
func TestExecuteGitCheckout_WithRef(t *testing.T) {
	src, _ := buildSource(t)
	url := serveBareRepoHTTPS(t, src)
	ws := t.TempDir()

	job := core.Job{ID: "j", NodeID: "n", GraphID: "g", WorkspaceRoot: ws,
		Params: map[string]any{"url": url, "ref": "develop", "depth": float64(1)}}
	res, _ := executeGitCheckout(t.Context(), job, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status = %q, err = %+v", res.Status, res.Error)
	}
	rel := res.Output["path"].Inline.(string)
	if got := fileIn(t, filepath.Join(ws, rel), "f.txt"); got != "develop-1\n" {
		t.Errorf("f.txt = %q, want develop-1", got)
	}
}

// TestExecuteGitCheckout_CloneFailed covers the clone_failed error mode: a
// valid https URL (passes guardRepoURL with private egress on) pointing at a
// path the backend can't serve.
func TestExecuteGitCheckout_CloneFailed(t *testing.T) {
	src, _ := buildSource(t)
	url := serveBareRepoHTTPS(t, src)
	// Point at a nonexistent repo under the same server.
	badURL := strings.Replace(url, "/repo.git", "/nope.git", 1)
	ws := t.TempDir()

	res, _ := executeGitCheckout(t.Context(), core.Job{
		ID: "j", NodeID: "n", GraphID: "g", WorkspaceRoot: ws,
		Params: map[string]any{"url": badURL},
	}, nil)
	if res.Status != core.StatusError {
		t.Fatalf("status = %q, want error", res.Status)
	}
	if res.Error.Code != "clone_failed" {
		t.Errorf("code = %q, want clone_failed", res.Error.Code)
	}
}

// TestOpenOrClone_AuthFailed covers the git_auth_failed branch: an ssh URL
// resolving a credential that has no key.
func TestOpenOrClone_AuthFailed(t *testing.T) {
	SetGitCredLookup(nil)
	_, mode, err := openOrClone(t.Context(), filepath.Join(t.TempDir(), "c"),
		"git@github.com:x/y.git", "", 0, nil, core.Job{ID: "j"})
	if err == nil {
		t.Fatal("expected git_auth_failed error")
	}
	if mode != "git_auth_failed" {
		t.Errorf("mode = %q, want git_auth_failed", mode)
	}
}

// TestExecuteGitCheckout_AuthFailed exercises executeGitCheckout's
// openOrClone error return (line 109-111) via the same ssh-no-key path.
func TestExecuteGitCheckout_AuthFailed(t *testing.T) {
	SetGitCredLookup(nil)
	res, _ := executeGitCheckout(t.Context(), core.Job{
		ID: "j", NodeID: "n", GraphID: "g", WorkspaceRoot: t.TempDir(),
		Params: map[string]any{"url": "ssh://git@93.184.216.34/x/y.git"},
	}, nil)
	if res.Status != core.StatusError {
		t.Fatalf("status = %q, want error", res.Status)
	}
	if res.Error.Code != "git_auth_failed" {
		t.Errorf("code = %q, want git_auth_failed", res.Error.Code)
	}
}

// TestOpenOrClone_StatFailed covers the stat_failed branch: dst's parent is a
// file, so os.Stat(dst) returns a non-IsNotExist error (ENOTDIR).
func TestOpenOrClone_StatFailed(t *testing.T) {
	base := t.TempDir()
	notDir := filepath.Join(base, "afile")
	if err := os.WriteFile(notDir, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(notDir, "child") // stat through a file ⇒ ENOTDIR
	_, mode, err := openOrClone(t.Context(), dst, "https://93.184.216.34/x.git", "", 0, nil, core.Job{ID: "j"})
	if err == nil {
		t.Fatal("expected stat_failed error")
	}
	if mode != "stat_failed" {
		t.Errorf("mode = %q, want stat_failed", mode)
	}
}

// TestGuardRepoURL_InvalidURL covers the url.Parse failure branch.
func TestGuardRepoURL_InvalidURL(t *testing.T) {
	if err := guardRepoURL(context.Background(), "https://exa mple.com/x.git"); err == nil {
		t.Fatal("expected parse error for a malformed https URL")
	}
}

// TestHostKeyDB_WithUserKnownHosts covers the userKnownHosts append branch in
// hostKeyDB (a valid extra line is combined with the bundled set).
func TestHostKeyDB_WithUserKnownHosts(t *testing.T) {
	line := "git.internal ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIMZvRd4EtM7R+IHVMWmDkVU3VLQTSwQDSAvW0t2Tkj60"
	db, err := hostKeyDB(line)
	if err != nil {
		t.Fatalf("hostKeyDB with user known_hosts: %v", err)
	}
	if db == nil {
		t.Fatal("expected a non-nil host-key db")
	}
}

// TestAuthForURL_SSHWithPort covers the explicit-port branch in authForURL
// (host+":"+port instead of the default :22).
func TestAuthForURL_SSHWithPort(t *testing.T) {
	SetGitCredLookup(nil)
	auth, err := authForURL(t.Context(),
		core.Job{Params: map[string]any{"ssh_private_key": genKeyPEM(t)}},
		"ssh://git@github.com:2222/x/y.git")
	if err != nil {
		t.Fatalf("authForURL ssh with port: %v", err)
	}
	if auth == nil {
		t.Fatal("expected a non-nil ssh auth method")
	}
}

// TestExecuteGitDiff_InputPathViaRef covers git_diff's input["path"].Ref
// fallback branch (Inline empty, relPath empty, Ref set).
func TestExecuteGitDiff_InputPathViaRef(t *testing.T) {
	ws := t.TempDir()
	repoDir := filepath.Join(ws, "repo")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	r, _ := gogit.PlainInit(repoDir, false)
	wt, _ := r.Worktree()
	commit(t, repoDir, wt, "f.txt", "v1\n", "first")
	commit(t, repoDir, wt, "f.txt", "v1\nv2\n", "second")

	res, _ := executeGitDiff(t.Context(), core.Job{
		ID: "j", WorkspaceRoot: ws,
		Input: map[string]core.Ref{"path": {Ref: "repo"}},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status = %q, err = %+v", res.Status, res.Error)
	}
}

// TestExecuteGitDiff_OpenError covers git_diff's "open" error branch.
func TestExecuteGitDiff_OpenError(t *testing.T) {
	res, _ := executeGitDiff(t.Context(), core.Job{
		ID: "j", WorkspaceRoot: t.TempDir(),
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "open" {
		t.Fatalf("got %+v, want error/open", res.Error)
	}
}

// TestExecuteGitDiff_BadToRef covers the "to" bad_ref branch (from resolves,
// to does not).
func TestExecuteGitDiff_BadToRef(t *testing.T) {
	dir, wt := newRepo(t)
	commit(t, dir, wt, "f.txt", "a\n", "first")
	commit(t, dir, wt, "f.txt", "b\n", "second")

	res, _ := executeGitDiff(t.Context(), core.Job{
		ID: "j", WorkspaceRoot: dir,
		Params: map[string]any{"from": "HEAD", "to": "no-such-ref"},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_ref" {
		t.Fatalf("got %+v, want error/bad_ref", res.Error)
	}
}

// TestExecuteGitDiff_InputPathInline covers git_diff's input["path"].Inline
// string branch (a wired value overriding the typed path).
func TestExecuteGitDiff_InputPathInline(t *testing.T) {
	ws := t.TempDir()
	repoDir := filepath.Join(ws, "repo")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	r, _ := gogit.PlainInit(repoDir, false)
	wt, _ := r.Worktree()
	commit(t, repoDir, wt, "f.txt", "v1\n", "first")
	commit(t, repoDir, wt, "f.txt", "v1\nv2\n", "second")

	res, _ := executeGitDiff(t.Context(), core.Job{
		ID: "j", WorkspaceRoot: ws,
		Input: map[string]core.Ref{"path": {Inline: "repo"}},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status = %q, err = %+v", res.Status, res.Error)
	}
}

// TestExecuteGitDiff_SandboxEscape covers git_diff's sandbox_escape branch.
func TestExecuteGitDiff_SandboxEscape(t *testing.T) {
	res, _ := executeGitDiff(t.Context(), core.Job{
		ID: "j", WorkspaceRoot: t.TempDir(),
		Params: map[string]any{"path": "../../etc"},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "sandbox_escape" {
		t.Fatalf("got %+v, want error/sandbox_escape", res.Error)
	}
}

// TestExecuteGitLog_ShallowTruncated clones depth=1 over https and then walks
// the log, hitting the ErrObjectNotFound "history truncated" branch when the
// walk runs past the single locally-present commit.
func TestExecuteGitLog_ShallowTruncated(t *testing.T) {
	dir, wt := newRepo(t)
	commit(t, dir, wt, "f.txt", "c1\n", "c1")
	commit(t, dir, wt, "f.txt", "c2\n", "c2")
	commit(t, dir, wt, "f.txt", "c3\n", "c3")

	url := serveBareRepoHTTPS(t, dir)
	ws := t.TempDir()
	if _, _, err := openOrClone(t.Context(), filepath.Join(ws, "repo"), url, "master", 1, nil, core.Job{ID: "c"}); err != nil {
		t.Fatalf("shallow clone: %v", err)
	}
	res, _ := executeGitLog(t.Context(), core.Job{
		ID: "j", WorkspaceRoot: ws,
		Params: map[string]any{"path": "repo", "limit": 20},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status = %q, err = %+v", res.Status, res.Error)
	}
	meta := res.Output["meta"].Inline.(map[string]any)
	if meta["truncated"] != true {
		t.Errorf("truncated = %v, want true (shallow walk should stop at the graft)", meta["truncated"])
	}
}

// TestExecuteGitLog_LimitClamp covers the limit<1 and limit>1000 clamps.
func TestExecuteGitLog_LimitClamp(t *testing.T) {
	dir, wt := newRepo(t)
	commit(t, dir, wt, "f.txt", "a\n", "only")

	for _, lim := range []float64{0, 5000} {
		res, _ := executeGitLog(t.Context(), core.Job{
			ID: "j", WorkspaceRoot: dir, Params: map[string]any{"limit": lim},
		}, nil)
		if res.Status != core.StatusOK {
			t.Fatalf("limit=%v status = %q, err = %+v", lim, res.Status, res.Error)
		}
	}
}

// drainProgress returns a buffered progress channel and a func collecting the
// events emitted to it (read after the call under test returns).
func drainProgress(t *testing.T) (chan core.Progress, func() []core.Progress) {
	t.Helper()
	ch := make(chan core.Progress, 256)
	return ch, func() []core.Progress {
		close(ch)
		var got []core.Progress
		for p := range ch {
			got = append(got, p)
		}
		return got
	}
}

func TestExecuteGitCheckout_EarlyErrors(t *testing.T) {
	cases := []struct {
		name     string
		job      core.Job
		wantCode string
	}{
		{"missing url", core.Job{ID: "j", WorkspaceRoot: t.TempDir()}, "bad_param"},
		{"blocked local path", core.Job{ID: "j", WorkspaceRoot: t.TempDir(), Params: map[string]any{"url": "/srv/repos/x.git"}}, "blocked"},
		{"blocked loopback", core.Job{ID: "j", WorkspaceRoot: t.TempDir(), Params: map[string]any{"url": "https://127.0.0.1/x.git"}}, "blocked"},
		{"no sandbox", core.Job{ID: "j", Params: map[string]any{"url": "https://93.184.216.34/x.git"}}, "no_sandbox"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res, err := executeGitCheckout(t.Context(), c.job, nil)
			if err != nil {
				t.Fatalf("unexpected go error: %v", err)
			}
			if res.Status != core.StatusError {
				t.Fatalf("status = %q, want error", res.Status)
			}
			if res.Error.Code != c.wantCode {
				t.Errorf("code = %q, want %q", res.Error.Code, c.wantCode)
			}
		})
	}
}

// TestExecuteGitLog_EmitsProgress drives a populated repo through executeGitLog
// with a live progress channel so the per-commit emitLogProgress path is run.
func TestExecuteGitLog_EmitsProgress(t *testing.T) {
	dir, wt := newRepo(t)
	commit(t, dir, wt, "f.txt", "a\n", "first")
	commit(t, dir, wt, "f.txt", "b\n", "second")

	ch, collect := drainProgress(t)
	res, _ := executeGitLog(t.Context(), core.Job{ID: "j", NodeID: "n", WorkspaceRoot: dir}, ch)
	got := collect()
	if res.Status != core.StatusOK {
		t.Fatalf("status = %q, err = %+v", res.Status, res.Error)
	}
	if len(got) == 0 {
		t.Fatal("expected progress events from git_log")
	}
}

func TestExecuteGitDiff_EmitsProgress(t *testing.T) {
	dir, wt := newRepo(t)
	commit(t, dir, wt, "f.txt", "a\n", "first")
	commit(t, dir, wt, "f.txt", "a\nb\n", "second")

	ch, collect := drainProgress(t)
	res, _ := executeGitDiff(t.Context(), core.Job{ID: "j", NodeID: "n", WorkspaceRoot: dir}, ch)
	got := collect()
	if res.Status != core.StatusOK {
		t.Fatalf("status = %q, err = %+v", res.Status, res.Error)
	}
	if len(got) == 0 {
		t.Fatal("expected a diff progress event")
	}
}

// TestExecuteGitDiff_NoMergeBase covers the no-common-ancestor branch by
// stitching a second, fully independent root commit into the same object store
// (no parents) and diffing it against the existing history with merge_base.
func TestExecuteGitDiff_NoMergeBase(t *testing.T) {
	dir, wt := newRepo(t)
	commit(t, dir, wt, "f.txt", "m1\n", "m1")

	repo, err := gogit.PlainOpen(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Build an orphan root commit directly via the object storer: an empty
	// tree and no parents, so it shares no ancestor with master.
	emptyTree := &object.Tree{}
	teo := repo.Storer.NewEncodedObject()
	if err := emptyTree.Encode(teo); err != nil {
		t.Fatal(err)
	}
	treeHash, err := repo.Storer.SetEncodedObject(teo)
	if err != nil {
		t.Fatal(err)
	}
	sig := object.Signature{Name: "Tester", Email: "t@test.invalid", When: time.Now()}
	orphanCommit := &object.Commit{Author: sig, Committer: sig, Message: "orphan\n", TreeHash: treeHash}
	ceo := repo.Storer.NewEncodedObject()
	if err := orphanCommit.Encode(ceo); err != nil {
		t.Fatal(err)
	}
	orphan, err := repo.Storer.SetEncodedObject(ceo)
	if err != nil {
		t.Fatal(err)
	}

	res, _ := executeGitDiff(t.Context(), core.Job{
		ID: "j", WorkspaceRoot: dir,
		Params: map[string]any{"from": "master", "to": orphan.String(), "merge_base": true},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "no_merge_base" {
		t.Fatalf("got %+v, want error/no_merge_base", res.Error)
	}
}

// TestExecuteGitLog_InputPath covers the job.Input["path"] resolution branch.
func TestExecuteGitLog_InputPath(t *testing.T) {
	ws := t.TempDir()
	repoDir := filepath.Join(ws, "repo")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	r, err := gogit.PlainInit(repoDir, false)
	if err != nil {
		t.Fatal(err)
	}
	wt, _ := r.Worktree()
	commit(t, repoDir, wt, "f.txt", "v1\n", "only")

	res, _ := executeGitLog(t.Context(), core.Job{
		ID: "j", WorkspaceRoot: ws,
		Input: map[string]core.Ref{"path": {Inline: "repo"}},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status = %q, err = %+v", res.Status, res.Error)
	}
}

func TestExecuteGitLog_InputPathViaRef(t *testing.T) {
	ws := t.TempDir()
	repoDir := filepath.Join(ws, "repo")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	r, _ := gogit.PlainInit(repoDir, false)
	wt, _ := r.Worktree()
	commit(t, repoDir, wt, "f.txt", "v1\n", "only")

	res, _ := executeGitLog(t.Context(), core.Job{
		ID: "j", WorkspaceRoot: ws,
		Input: map[string]core.Ref{"path": {Ref: "repo"}},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status = %q, err = %+v", res.Status, res.Error)
	}
}

func TestOpenOrClone_ExistsNotADir(t *testing.T) {
	ws := t.TempDir()
	dst := filepath.Join(ws, "afile")
	if err := os.WriteFile(dst, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, mode, err := openOrClone(t.Context(), dst, "https://93.184.216.34/x.git", "", 0, nil, core.Job{ID: "j"})
	if err == nil {
		t.Fatal("expected error for non-dir destination")
	}
	if mode != "exists" {
		t.Errorf("mode = %q, want exists", mode)
	}
}

func TestOpenOrClone_NotARepo(t *testing.T) {
	ws := t.TempDir()
	dst := filepath.Join(ws, "plaindir")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	_, mode, err := openOrClone(t.Context(), dst, "https://93.184.216.34/x.git", "", 0, nil, core.Job{ID: "j"})
	if err == nil {
		t.Fatal("expected error opening a non-repo directory")
	}
	if mode != "not_a_repo" {
		t.Errorf("mode = %q, want not_a_repo", mode)
	}
}

// TestOpenOrClone_PulledDetachedHead exercises updateCurrentBranch's detached
// HEAD early return: a re-run with a commit-SHA ref leaves HEAD detached, and a
// later no-ref re-run must be a no-op fast-forward.
func TestOpenOrClone_PulledDetachedHead(t *testing.T) {
	src, masterSHA := buildSource(t)
	dst := filepath.Join(t.TempDir(), "clone")

	if _, _, err := openOrClone(t.Context(), dst, src, masterSHA, 0, nil, core.Job{ID: "j"}); err != nil {
		t.Fatalf("initial detached clone: %v", err)
	}
	// Re-run with no ref: updateCurrentBranch hits its detached-HEAD branch.
	_, mode, err := openOrClone(t.Context(), dst, src, "", 0, nil, core.Job{ID: "j"})
	if err != nil {
		t.Fatalf("re-run: %v", err)
	}
	if mode != "pulled" {
		t.Errorf("mode = %q, want pulled", mode)
	}
}

func TestAuthForURL_BadSSHKey(t *testing.T) {
	SetGitCredLookup(nil)
	_, err := authForURL(t.Context(), core.Job{Params: map[string]any{"ssh_private_key": "not a key"}}, "git@github.com:x/y.git")
	if err == nil {
		t.Fatal("expected error parsing a bad SSH private key")
	}
}

func TestAuthForURL_CredLookupError(t *testing.T) {
	SetGitCredLookup(func(_ context.Context, _ string) (GitCred, error) {
		return GitCred{}, errors.New("store down")
	})
	t.Cleanup(func() { SetGitCredLookup(nil) })

	_, err := authForURL(t.Context(), core.Job{Params: map[string]any{"account": "prod"}}, "https://github.com/x/y.git")
	if err == nil {
		t.Fatal("expected error when credential lookup fails")
	}
}

func TestResolveCred_ViaHook(t *testing.T) {
	SetGitCredLookup(func(_ context.Context, account string) (GitCred, error) {
		if account != "prod" {
			t.Errorf("account = %q, want prod", account)
		}
		return GitCred{Token: "tok", Username: "bot"}, nil
	})
	t.Cleanup(func() { SetGitCredLookup(nil) })

	auth, err := authForURL(t.Context(), core.Job{Params: map[string]any{"account": "prod"}}, "https://github.com/x/y.git")
	if err != nil {
		t.Fatalf("authForURL: %v", err)
	}
	if auth == nil {
		t.Fatal("expected basic auth from the looked-up token")
	}
}

func TestHostKeyDB_BadUserKnownHosts(t *testing.T) {
	// A malformed known_hosts line makes NewKnownHostsDb fail.
	if _, err := hostKeyDB("this-is-not-a-valid-known-hosts-line"); err == nil {
		t.Fatal("expected error for malformed user known_hosts")
	}
}

func TestSSHURLParts_PortAndDefaults(t *testing.T) {
	cases := []struct {
		url       string
		wantUser  string
		wantHost  string
		wantIsSSH bool
	}{
		{"ssh://git.sr.ht/x.git", "git", "git.sr.ht", true}, // no user ⇒ default "git"
		{"ssh://:::bad", "", "", false},                     // unparseable
		{"host.example:path", "git", "host.example", true},  // scp-like, no user
	}
	for _, c := range cases {
		u, h, ok := sshURLParts(c.url)
		if ok != c.wantIsSSH || (ok && (u != c.wantUser || h != c.wantHost)) {
			t.Errorf("sshURLParts(%q) = (%q,%q,%v), want (%q,%q,%v)", c.url, u, h, ok, c.wantUser, c.wantHost, c.wantIsSSH)
		}
	}
}

// TestProgressSink_FlushLeftover covers progressSink.flush emitting a trailing
// partial line (no terminating CR/LF) plus the empty-buffer no-op.
func TestProgressSink_FlushLeftover(t *testing.T) {
	ch, collect := drainProgress(t)
	s := newProgressSink(ch, core.Job{ID: "j", NodeID: "n"})

	// Two complete lines emit immediately; the trailing partial stays buffered.
	if _, err := s.Write([]byte("Counting objects: 1\rResolving deltas\nstill-going")); err != nil {
		t.Fatalf("write: %v", err)
	}
	s.flush() // emits the leftover "still-going"
	s.flush() // empty buffer ⇒ no-op early return
	got := collect()
	if len(got) < 3 {
		t.Fatalf("got %d progress events, want >=3 (two lines + leftover)", len(got))
	}
	last := got[len(got)-1]
	if last.Message != "still-going" {
		t.Errorf("leftover message = %q, want %q", last.Message, "still-going")
	}
}

// TestCheckout_ReRunResetsLocalBranch drives checkout twice for the same branch
// so the second call hits the "local branch already exists" fast-forward path.
func TestCheckout_ReRunResetsLocalBranch(t *testing.T) {
	src, _ := buildSource(t)
	dst := filepath.Join(t.TempDir(), "clone")

	// First run creates a local 'develop' branch from origin/develop.
	if _, _, err := openOrClone(t.Context(), dst, src, "develop", 0, nil, core.Job{ID: "j"}); err != nil {
		t.Fatalf("first develop checkout: %v", err)
	}
	// Advance source develop, then re-run: checkout finds the existing local
	// branch and resets it to the new remote tip.
	srcRepo, _ := gogit.PlainOpen(src)
	srcWT, _ := srcRepo.Worktree()
	if err := srcWT.Checkout(&gogit.CheckoutOptions{Branch: plumbing.NewBranchReferenceName("develop"), Force: true}); err != nil {
		t.Fatalf("src checkout develop: %v", err)
	}
	commit(t, src, srcWT, "f.txt", "develop-2\n", "d2")

	if _, mode, err := openOrClone(t.Context(), dst, src, "develop", 0, nil, core.Job{ID: "j"}); err != nil {
		t.Fatalf("re-run develop: %v (mode %s)", err, mode)
	}
	if got := fileIn(t, dst, "f.txt"); got != "develop-2\n" {
		t.Errorf("after re-run f.txt = %q, want develop-2", got)
	}
}

func TestInstallGuardedHTTPTransport(t *testing.T) {
	// Smoke test: installing the guarded transport must not panic. go-git's
	// protocol registry is process-global, so restore https afterwards.
	t.Cleanup(func() { InstallGuardedHTTPTransport(nil) })
	InstallGuardedHTTPTransport(nil)
}

func TestEmitProgress_NilAndBufferFull(t *testing.T) {
	// nil channel ⇒ no-op, must not panic.
	emitProgress(nil, core.Job{ID: "j"}, 0.5, "x")

	// Full channel ⇒ default branch (dropped), must not block.
	ch := make(chan core.Progress) // unbuffered, no reader
	emitProgress(ch, core.Job{ID: "j"}, 0.5, "x")
}
