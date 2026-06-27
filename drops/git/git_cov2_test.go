// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package git

import (
	"context"
	"crypto/tls"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	gogit "github.com/go-git/go-git/v5"

	"git.sr.ht/~klahr/dazyflow/core"
	hfnet "git.sr.ht/~klahr/dazyflow/drops/net"
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
