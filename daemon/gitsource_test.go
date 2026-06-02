package daemon

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	hfnet "git.sr.ht/~klahr/hazy-flow/drops/net"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	gitclient "github.com/go-git/go-git/v5/plumbing/transport/client"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
)

// makeRepo builds a local git repo in a temp dir with the given files and a
// lightweight tag, and returns its path — a real repo GitSource can clone
// without any network.
func makeRepo(t *testing.T, files map[string]string, tag string) string {
	t.Helper()
	dir := t.TempDir()
	repo, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("worktree: %v", err)
	}
	for name, content := range files {
		full := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := wt.Add(name); err != nil {
			t.Fatalf("add %s: %v", name, err)
		}
	}
	h, err := wt.Commit("release "+tag, &git.CommitOptions{
		Author: &object.Signature{Name: "t", Email: "t@example.test", When: time.Unix(1700000000, 0)},
	})
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if _, err := repo.CreateTag(tag, h, nil); err != nil {
		t.Fatalf("tag: %v", err)
	}
	return dir
}

func TestGitSource_FetchAtTag(t *testing.T) {
	repo := makeRepo(t, map[string]string{
		"integration.json": `{"id":"google","version":"1.0.0"}`,
		"drops/gmail.ts":   "export default {};",
	}, "v1.0.0")

	fetched, err := GitSource{}.Fetch(context.Background(), repo, "v1.0.0")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(fetched.Commit) != 40 {
		t.Errorf("commit digest looks wrong: %q", fetched.Commit)
	}
	b, err := fetched.File("integration.json")
	if err != nil || !strings.Contains(string(b), `"google"`) {
		t.Errorf("read integration.json: %q err=%v", b, err)
	}
	files, err := fetched.List(".ts")
	if err != nil || len(files) != 1 || files[0] != "drops/gmail.ts" {
		t.Errorf("list .ts = %v err=%v", files, err)
	}
	if _, err := fetched.File("missing.json"); err == nil {
		t.Error("reading a missing file should error")
	}
	if _, err := (GitSource{}).Fetch(context.Background(), repo, "v9.9.9"); err == nil {
		t.Error("fetching a nonexistent tag should error")
	}
}

// With the guarded transport installed (as the daemon does at boot), an https
// repo URL that resolves to a private/loopback address is refused at dial by the
// SSRF guard — not merely failing to connect. This closes the https SSRF hole
// the scheme allowlist can't (https is allowed).
func TestGitSource_GuardedHTTPSBlocksPrivate(t *testing.T) {
	InstallGuardedHTTPTransport(hfnet.SafeHTTPClient(2*time.Second, false))
	t.Cleanup(func() { gitclient.InstallProtocol("https", githttp.DefaultClient) })

	_, err := (GitSource{}).Fetch(context.Background(), "https://127.0.0.1:1/repo.git", "v1.0.0")
	if err == nil {
		t.Fatal("expected a loopback https fetch to be blocked")
	}
	if !hfnet.IsSSRFError(err) {
		t.Fatalf("expected an ssrf_blocked dial error, got: %v", err)
	}
}

// Fetch rejects URL schemes that would turn a marketplace fetch into a local
// file read (file://) or an unauthenticated/SSRF-prone fetch (http://, git://).
func TestGitSource_RejectsScheme(t *testing.T) {
	for _, u := range []string{
		"file:///etc/passwd",
		"http://169.254.169.254/repo.git",
		"git://internal.host/repo",
	} {
		if _, err := (GitSource{}).Fetch(context.Background(), u, "v1.0.0"); err == nil {
			t.Errorf("scheme in %q should be rejected", u)
		}
	}
}

// End-to-end through git: a signed integration.json in a repo installs at the
// official tier — proving the signature verifies over the exact fetched bytes
// (the canonicalization concern is gone with git-as-source).
func TestInstaller_InstallIntegrationFromGit(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	manifest := googleIntegrationJSON
	sigJSON, _ := json.Marshal(Signature{KeyID: "hazy", Sig: ed25519.Sign(priv, []byte(manifest))})
	repo := makeRepo(t, map[string]string{
		"integration.json":     manifest,
		"integration.json.sig": string(sigJSON),
	}, "v1.0.0")

	kr := NewKeyring(TrustedKey{ID: "hazy", Publisher: "Hazy", Tier: TierOfficial, PublicKey: pub})
	secrets := newTestSecrets(t)
	in := NewInstaller(NewOAuthRegistry("https://app.example.test", secrets), testScriptedCatalog(t), secrets, kr)

	m, tier, err := in.InstallIntegrationFromGit(context.Background(), repo, "v1.0.0",
		map[string]string{"client_id": "cid", "client_secret": "sec"})
	if err != nil {
		t.Fatalf("install from git: %v", err)
	}
	if m.ID != "google" {
		t.Errorf("id = %q", m.ID)
	}
	if tier != TierOfficial {
		t.Errorf("tier = %v, want official (signature over exact git bytes)", tier)
	}
	if _, ok := in.oauth.Provider("google"); !ok {
		t.Error("provider not registered from git install")
	}
}

// A drop fetched from git installs, gated on its integration like any other.
func TestInstaller_InstallDropFromGit(t *testing.T) {
	repo := makeRepo(t, map[string]string{"gmail.ts": acmeDropSrc}, "v1.0.0")
	secrets := newTestSecrets(t)
	in := NewInstaller(NewOAuthRegistry("https://app.example.test", secrets), testScriptedCatalog(t), secrets, nil)

	// Gated: acme not installed yet.
	if _, _, err := in.InstallDropFromGit(context.Background(), repo, "v1.0.0", "gmail.ts"); err == nil {
		t.Fatal("drop-from-git should be gated on the acme integration")
	}
	// Install acme, then the git drop succeeds.
	if _, _, err := in.InstallIntegration(context.Background(), []byte(acmeIntegrationJSON),
		map[string]string{"client_id": "c", "client_secret": "s"}, nil, Provenance{}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := in.InstallDropFromGit(context.Background(), repo, "v1.0.0", "gmail.ts"); err != nil {
		t.Fatalf("drop-from-git after integration: %v", err)
	}
}

// A git install records the resolved commit as the pin: it's surfaced in the
// listing and replayed by Restore, so the install is reproducible and a moved
// tag is detectable later.
func TestInstaller_RecordsGitProvenance(t *testing.T) {
	ctx := context.Background()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	manifest := googleIntegrationJSON
	sigJSON, _ := json.Marshal(Signature{KeyID: "hazy", Sig: ed25519.Sign(priv, []byte(manifest))})
	repo := makeRepo(t, map[string]string{
		"integration.json":     manifest,
		"integration.json.sig": string(sigJSON),
	}, "v1.0.0")

	// The commit the tag resolves to — the expected pin.
	fetched, err := (GitSource{}).Fetch(ctx, repo, "v1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	wantCommit := fetched.Commit

	kr := NewKeyring(TrustedKey{ID: "hazy", Publisher: "Hazy", Tier: TierOfficial, PublicKey: pub})
	secrets := newTestSecrets(t)
	in := NewInstaller(NewOAuthRegistry("https://app.example.test", secrets), testScriptedCatalog(t), secrets, kr)
	if _, _, err := in.InstallIntegrationFromGit(ctx, repo, "v1.0.0",
		map[string]string{"client_id": "cid", "client_secret": "sec"}); err != nil {
		t.Fatalf("install from git: %v", err)
	}

	check := func(in *Installer, label string) {
		t.Helper()
		for _, ig := range in.InstalledIntegrations() {
			if ig.ID != "google" {
				continue
			}
			if ig.Provenance.Commit != wantCommit || ig.Provenance.Repo != repo || ig.Provenance.Ref != "v1.0.0" {
				t.Fatalf("%s: provenance = %+v, want commit %s repo %s ref v1.0.0", label, ig.Provenance, wantCommit, repo)
			}
			return
		}
		t.Fatalf("%s: google integration not found in listing", label)
	}
	check(in, "after install")

	// Survives a restart: Restore replays the persisted pin.
	in2 := NewInstaller(NewOAuthRegistry("https://app.example.test", secrets), testScriptedCatalog(t), secrets, kr)
	if _, errs := in2.Restore(ctx); len(errs) != 0 {
		t.Fatalf("restore: %v", errs)
	}
	check(in2, "after restore")
}
