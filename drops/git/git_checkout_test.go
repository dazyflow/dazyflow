package git

import (
	"os"
	"path/filepath"
	"testing"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"

	"git.sr.ht/~klahr/hazyflow/core"
)

// buildSource creates a local source repo to clone from in the checkout
// tests. It has: master (f.txt="master-1"), a develop branch
// (f.txt="develop-1"), and a lightweight tag v1 pointing at master's first
// commit. It returns the repo dir and master's first-commit SHA.
func buildSource(t *testing.T) (dir, masterSHA string) {
	t.Helper()
	dir, wt := newRepo(t)
	commit(t, dir, wt, "f.txt", "master-1\n", "m1")

	repo, err := gogit.PlainOpen(dir)
	if err != nil {
		t.Fatalf("open source: %v", err)
	}
	head, err := repo.Head()
	if err != nil {
		t.Fatalf("head: %v", err)
	}
	masterSHA = head.Hash().String()
	if _, err := repo.CreateTag("v1", head.Hash(), nil); err != nil {
		t.Fatalf("tag: %v", err)
	}
	if err := wt.Checkout(&gogit.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName("develop"),
		Create: true,
	}); err != nil {
		t.Fatalf("branch develop: %v", err)
	}
	commit(t, dir, wt, "f.txt", "develop-1\n", "d1")
	if err := wt.Checkout(&gogit.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName("master"),
		Force:  true,
	}); err != nil {
		t.Fatalf("back to master: %v", err)
	}
	return dir, masterSHA
}

// fileIn reads a tracked file from a checked-out clone.
func fileIn(t *testing.T, dir, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}

func TestOpenOrClone_RefVariants(t *testing.T) {
	src, masterSHA := buildSource(t)

	cases := []struct {
		name  string
		ref   string
		depth int
		want  string
	}{
		{"default branch", "", 0, "master-1\n"},
		// Regression for bug #1: a non-default branch is fetched and checked
		// out by its plain name (go-git's post-clone resolution can't).
		{"non-default branch", "develop", 0, "develop-1\n"},
		// Regression for bug #2: a shallow clone targets the requested tag,
		// not the default branch it would otherwise be limited to.
		{"shallow tag", "v1", 1, "master-1\n"},
		{"commit sha", masterSHA, 1, "master-1\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dst := filepath.Join(t.TempDir(), "clone")
			repo, mode, err := openOrClone(t.Context(), dst, src, c.ref, c.depth, nil, core.Job{ID: "j"})
			if err != nil {
				t.Fatalf("openOrClone(%q) mode=%q: %v", c.ref, mode, err)
			}
			if mode != "cloned" {
				t.Errorf("mode = %q, want cloned", mode)
			}
			if got := fileIn(t, dst, "f.txt"); got != c.want {
				t.Errorf("f.txt = %q, want %q", got, c.want)
			}
			if _, err := repo.Head(); err != nil {
				t.Errorf("head: %v", err)
			}
		})
	}
}

func TestOpenOrClone_RefNotFound(t *testing.T) {
	src, _ := buildSource(t)
	dst := filepath.Join(t.TempDir(), "clone")
	_, mode, err := openOrClone(t.Context(), dst, src, "no-such-ref", 0, nil, core.Job{ID: "j"})
	if err == nil {
		t.Fatal("openOrClone with missing ref = nil, want error")
	}
	if mode != "ref_not_found" {
		t.Errorf("mode = %q, want ref_not_found", mode)
	}
	// A typo'd ref must not leave a half-written clone in the sandbox.
	if _, statErr := os.Stat(dst); !os.IsNotExist(statErr) {
		t.Errorf("destination exists after failed clone (stat err = %v)", statErr)
	}
}

// TestOpenOrClone_PulledUpdatesWorktree is the regression for bug #3: a
// re-run over an existing clone must fast-forward the working tree, not
// silently return the stale checkout.
func TestOpenOrClone_PulledUpdatesWorktree(t *testing.T) {
	src, _ := buildSource(t)
	dst := filepath.Join(t.TempDir(), "clone")

	if _, _, err := openOrClone(t.Context(), dst, src, "", 0, nil, core.Job{ID: "j"}); err != nil {
		t.Fatalf("initial clone: %v", err)
	}
	if got := fileIn(t, dst, "f.txt"); got != "master-1\n" {
		t.Fatalf("after clone f.txt = %q, want master-1", got)
	}

	// Advance the source's master, then re-run with no ref.
	srcRepo, _ := gogit.PlainOpen(src)
	srcWT, _ := srcRepo.Worktree()
	commit(t, src, srcWT, "f.txt", "master-2\n", "m2")

	_, mode, err := openOrClone(t.Context(), dst, src, "", 0, nil, core.Job{ID: "j"})
	if err != nil {
		t.Fatalf("re-run: %v", err)
	}
	if mode != "pulled" {
		t.Errorf("mode = %q, want pulled", mode)
	}
	if got := fileIn(t, dst, "f.txt"); got != "master-2\n" {
		t.Errorf("after pull f.txt = %q, want master-2 (working tree not updated)", got)
	}
}

// TestOpenOrClone_PulledSwitchesBranch covers re-running an existing clone
// with a branch ref that has no local branch yet — it must be created at
// the remote tip and checked out.
func TestOpenOrClone_PulledSwitchesBranch(t *testing.T) {
	src, _ := buildSource(t)
	dst := filepath.Join(t.TempDir(), "clone")

	if _, _, err := openOrClone(t.Context(), dst, src, "", 0, nil, core.Job{ID: "j"}); err != nil {
		t.Fatalf("initial clone: %v", err)
	}
	_, mode, err := openOrClone(t.Context(), dst, src, "develop", 0, nil, core.Job{ID: "j"})
	if err != nil {
		t.Fatalf("re-run develop: %v", err)
	}
	if mode != "pulled" {
		t.Errorf("mode = %q, want pulled", mode)
	}
	if got := fileIn(t, dst, "f.txt"); got != "develop-1\n" {
		t.Errorf("after switch f.txt = %q, want develop-1", got)
	}
}

func TestRemoteRefName(t *testing.T) {
	src, _ := buildSource(t)
	cases := []struct {
		ref     string
		want    plumbing.ReferenceName
		wantErr bool
	}{
		{"develop", plumbing.NewBranchReferenceName("develop"), false},
		{"master", plumbing.NewBranchReferenceName("master"), false},
		{"v1", plumbing.NewTagReferenceName("v1"), false},
		{"refs/heads/develop", plumbing.NewBranchReferenceName("develop"), false},
		{"ghost", "", true},
	}
	for _, c := range cases {
		t.Run(c.ref, func(t *testing.T) {
			got, err := remoteRefName(t.Context(), src, c.ref, nil)
			if (err != nil) != c.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, c.wantErr)
			}
			if !c.wantErr && got != c.want {
				t.Errorf("remoteRefName(%q) = %q, want %q", c.ref, got, c.want)
			}
		})
	}
}

func TestLooksLikeSHA(t *testing.T) {
	cases := map[string]bool{
		"a1b2c3d": true,  // 7 hex
		"0123456789abcdef0123456789abcdef01234567": true, // 40 hex
		"develop":   false,
		"v1.4.2":    false,
		"abc123":    false, // 6 chars — too short to be unambiguous
		"deadbeefg": false, // non-hex
	}
	for in, want := range cases {
		if got := looksLikeSHA(in); got != want {
			t.Errorf("looksLikeSHA(%q) = %v, want %v", in, got, want)
		}
	}
}

// TestGuardRepoURL_BlocksSSRFAndLocalSchemes locks in the SSRF/egress guard
// on git_checkout's tenant-supplied url. go-git's default transport serves
// http/git/file, so these must be refused before any dial; https/ssh to
// public hosts must still pass. The operator private-egress opt-in is left
// at its default (off) for this test.
func TestGuardRepoURL_BlocksSSRFAndLocalSchemes(t *testing.T) {
	blocked := []string{
		"file:///etc/passwd",                 // host-file read
		"git://internal-host/repo.git",       // internal git daemon
		"http://example.com/repo.git",        // cleartext + SSRF class
		"https://169.254.169.254/repo.git",   // cloud metadata IP
		"https://127.0.0.1/repo.git",         // loopback
		"https://[::1]/repo.git",             // loopback v6
		"https://10.0.0.5/repo.git",          // RFC1918
		"/srv/repos/local.git",               // bare local path
		"../../etc/passwd",                   // relative local path
		"git@127.0.0.1:internal/repo.git",    // scp-like to loopback
		"",                                   // empty
	}
	for _, u := range blocked {
		if err := guardRepoURL(u); err == nil {
			t.Errorf("guardRepoURL(%q) = nil, want blocked", u)
		}
	}

	// Public-IP literals so the test stays hermetic — CheckDialHost only
	// does a DNS lookup for hostnames, so these assert the scheme/host
	// policy without depending on network resolution in CI.
	allowed := []string{
		"https://93.184.216.34/example/widgets.git",
		"ssh://git@93.184.216.34/example/widgets.git",
		"git@93.184.216.34:example/widgets.git", // scp-like to public host
	}
	for _, u := range allowed {
		if err := guardRepoURL(u); err != nil {
			t.Errorf("guardRepoURL(%q) = %v, want allowed", u, err)
		}
	}
}
