package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-git/go-billy/v5/memfs"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	gitclient "github.com/go-git/go-git/v5/plumbing/transport/client"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/go-git/go-git/v5/storage/memory"
)

// InstallGuardedHTTPTransport routes all go-git https traffic through the given
// client process-wide. The daemon wires this at startup with the same
// SSRF-guarded + egress-allowlisted client the http_request drop uses, so a
// marketplace fetch of an https repo URL that resolves to a private / loopback /
// link-local address (e.g. the cloud metadata endpoint) is blocked at dial —
// closing the SSRF hole the scheme allowlist can't, since https is allowed.
//
// go-git resolves transports from a process-global registry keyed by scheme, so
// this is a global override: call it once at boot. Non-https schemes (file, git,
// http) are already rejected up front by Fetch's checkRepoScheme.
func InstallGuardedHTTPTransport(client *http.Client) {
	gitclient.InstallProtocol("https", githttp.NewClient(client))
}

// GitSource fetches marketplace artifacts from a git repo at a tag — the
// git-as-source transport. The tag is the human version (v1.2.0); the resolved
// commit hash (FetchedRef.Commit) is the immutable digest (a tag can be
// force-moved, a commit can't). The install path records that commit as the pin
// (daemon Provenance), so an install is reproducible and a re-install whose tag
// has been force-moved to a different commit is detected and warned. Clones are
// shallow + in-memory: the repo is read once, then discarded.
//
// Trust is layered on top, not in here. Fetch returns the EXACT file bytes, and
// the install path runs Ed25519 Keyring.Verify over those bytes (a repo ships a
// detached <file>.sig next to each signed artifact). Reading exact bytes is
// also what dissolves the JSON-canonicalization problem: there's no
// re-serialization between the signed file and verification.
type GitSource struct{}

// maxFetchedFileBytes caps a single artifact read. Manifests and drop sources
// are small (KBs); this bounds how much a hostile repo can make us buffer.
const maxFetchedFileBytes = 4 << 20 // 4 MiB

// checkRepoScheme rejects URL schemes that turn the marketplace fetch into a
// local-file read (file://) or an unauthenticated/SSRF-prone fetch (http://,
// git://). https and ssh are allowed; a bare path (scheme "") stays allowed for
// tests and on-box/enterprise mirrors. Routing the remaining https traffic
// through the egress guard is a separate follow-up.
func checkRepoScheme(repoURL string) error {
	u, err := url.Parse(repoURL)
	if err != nil {
		return fmt.Errorf("git fetch: invalid repo URL: %w", err)
	}
	switch u.Scheme {
	case "", "https", "ssh":
		return nil
	default:
		return fmt.Errorf("git fetch: scheme %q not allowed (use https)", u.Scheme)
	}
}

// FetchedRef is a read-only view of a repo at a resolved commit.
type FetchedRef struct {
	Repo   string
	Ref    string
	Commit string // resolved commit hash — the immutable digest
	tree   *object.Tree
}

// Fetch shallow-clones repoURL at ref (a tag) in memory and returns a handle to
// read files. ref must resolve to a tag; the resolved commit is the pin.
func (GitSource) Fetch(ctx context.Context, repoURL, ref string) (*FetchedRef, error) {
	if repoURL == "" || ref == "" {
		return nil, fmt.Errorf("git fetch: repo and ref are required")
	}
	if err := checkRepoScheme(repoURL); err != nil {
		return nil, err
	}
	repo, err := git.CloneContext(ctx, memory.NewStorage(), memfs.New(), &git.CloneOptions{
		URL:           repoURL,
		ReferenceName: plumbing.NewTagReferenceName(ref),
		SingleBranch:  true,
		Depth:         1,
		Tags:          git.NoTags,
	})
	if err != nil {
		return nil, fmt.Errorf("clone %s@%s: %w", repoURL, ref, err)
	}
	head, err := repo.Head()
	if err != nil {
		return nil, fmt.Errorf("resolve %s@%s: %w", repoURL, ref, err)
	}
	commit, err := repo.CommitObject(head.Hash())
	if err != nil {
		return nil, fmt.Errorf("commit %s: %w", head.Hash(), err)
	}
	tree, err := commit.Tree()
	if err != nil {
		return nil, fmt.Errorf("tree %s: %w", head.Hash(), err)
	}
	return &FetchedRef{Repo: repoURL, Ref: ref, Commit: head.Hash().String(), tree: tree}, nil
}

// File returns the exact bytes of a file at path within the fetched ref.
func (f *FetchedRef) File(path string) ([]byte, error) {
	file, err := f.tree.File(path)
	if err != nil {
		return nil, fmt.Errorf("read %q from %s@%s: %w", path, f.Repo, f.Ref, err)
	}
	if file.Size > maxFetchedFileBytes {
		return nil, fmt.Errorf("read %q from %s@%s: file is %d bytes, exceeds %d limit", path, f.Repo, f.Ref, file.Size, maxFetchedFileBytes)
	}
	contents, err := file.Contents()
	if err != nil {
		return nil, err
	}
	return []byte(contents), nil
}

// List returns the paths of files whose name ends with suffix (e.g. ".ts").
func (f *FetchedRef) List(suffix string) ([]string, error) {
	var out []string
	err := f.tree.Files().ForEach(func(file *object.File) error {
		if strings.HasSuffix(file.Name, suffix) {
			out = append(out, file.Name)
		}
		return nil
	})
	return out, err
}

// Signature reads an optional detached signature file (path + ".sig"),
// returning nil when absent — an unsigned artifact is community, not an error.
func (f *FetchedRef) Signature(path string) (*Signature, error) {
	raw, err := f.File(path + ".sig")
	if err != nil {
		return nil, nil // absent → unsigned
	}
	var sig Signature
	if err := json.Unmarshal(raw, &sig); err != nil {
		return nil, fmt.Errorf("decode %s.sig: %w", path, err)
	}
	return &sig, nil
}
