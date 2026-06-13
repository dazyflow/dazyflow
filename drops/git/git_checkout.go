// Package git provides nodes that clone repositories and run builds
// against the resulting working tree. Output is workspace-relative so
// downstream nodes (file_read, shell, ...) can pick it up via the
// shared sandbox.
package git

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/storage/memory"

	"git.sr.ht/~klahr/hazyflow/core"
	"git.sr.ht/~klahr/hazyflow/engine"
	"git.sr.ht/~klahr/hazyflow/drops/internal/params"
	hfnet "git.sr.ht/~klahr/hazyflow/drops/net"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:             "git_checkout",
			Version:        "1.0",
			Label:          "Git",
			Subtitle:       "Checkout",
			Color:          "#f05033",
			Icon:           "git",
			Category:       "io",
			Provider:       "internal",
			Integration:    "Git",
			Tags:           []string{"git", "clone", "checkout", "vcs"},
			Description: "Fetch a copy of a git repository into your workspace, optionally switching to a specific branch, tag, or commit. The files become available to the steps after this one — useful for inspecting source code, pulling templates, or staging files for processing.",
			Summary:     "Clone a remote git repository into the workspace and optionally check out a specific branch, tag, or commit SHA.",
			Examples: []core.ParamsExample{
				{
					Title:  "Full clone of a public repo",
					Params: json.RawMessage(`{"url":"https://github.com/example/widgets.git","path":"src/widgets"}`),
				},
				{
					Title:  "Shallow checkout of a release tag",
					Params: json.RawMessage(`{"url":"https://github.com/example/widgets.git","path":"src/widgets","ref":"v1.4.2","depth":1}`),
					Notes:  "depth:1 keeps the clone small when you only need the tip of a tag or branch.",
				},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Outputs: []core.Port{
				// Only the friendly scalars are pins; the full checkout
				// metadata (url, ref, mode, …) is still EMITTED under "meta"
				// so run records keep it for debugging — it's just not a pin.
				{Port: "path", Label: "Repository folder", MIME: []string{"text/plain"}},
				{Port: "sha", Label: "Commit SHA", MIME: []string{"text/plain"}},
			},
			ParamsSchema: json.RawMessage(
				`{
					"type":"object",
					"properties":{
						"url":{"type":"string","title":"Repository URL","description":"Where the repository lives (https or ssh address). Use ${secret.NAME} placeholders for tokens embedded in the URL."},
						"ref":{"type":"string","title":"Branch, tag, or commit","description":"What to switch to after fetching. Leave empty for the repo's default branch."},
						"path":{"type":"string","title":"Folder","description":"Workspace folder to put the files in."},
						"depth":{"type":"integer","title":"Clone depth","x_advanced":true,"minimum":0,"description":"Shallow-clone depth. 0 (default) clones the full history."}
					},
					"required":["url","path"]
				}`,
			),
			Idempotent:  true,
			RetryPolicy: core.RetryExponentialBackoff,
		},
		Execute: executeGitCheckout,
	})
}

func executeGitCheckout(ctx context.Context, job core.Job, progress chan<- core.Progress) (core.Result, error) {
	url, err := params.String(job.Params, "url")
	if err != nil {
		return params.Err(job, "bad_param", err.Error()), nil
	}
	if err := guardRepoURL(url); err != nil {
		return params.Err(job, "blocked", err.Error()), nil
	}
	relPath, err := params.String(job.Params, "path")
	if err != nil {
		return params.Err(job, "bad_param", err.Error()), nil
	}
	if job.WorkspaceRoot == "" {
		return params.Err(job, "no_sandbox", "git_checkout requires a workspace sandbox"), nil
	}
	ref := params.StringDefault(job.Params, "ref", "")
	depth := params.IntDefault(job.Params, "depth", 0)

	cleanRel, err := sandboxRel(relPath)
	if err != nil {
		return params.Err(job, "sandbox_escape", err.Error()), nil
	}
	if cleanRel == "." {
		return params.Err(job, "bad_param", "path must be a subdirectory, not the workspace root"), nil
	}
	dst := filepath.Join(job.WorkspaceRoot, cleanRel)

	repo, mode, err := openOrClone(ctx, dst, url, ref, depth, progress, job)
	if err != nil {
		return params.Err(job, mode, err.Error()), nil
	}

	var sha string
	if head, hErr := repo.Head(); hErr == nil {
		sha = head.Hash().String()
	}

	emitProgress(progress, job, 1.0, "done")
	meta := map[string]any{
		"url":  url,
		"ref":  ref,
		"sha":  sha,
		"mode": mode,
		"path": cleanRel,
	}
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"path": {MIME: "text/plain", Ref: cleanRel, Inline: cleanRel},
			"sha":  {MIME: "text/plain", Inline: sha},
			"meta": {MIME: "application/json", Inline: meta},
		},
	}, nil
}

// guardRepoURL enforces the SSRF/egress policy on a tenant-supplied repo
// URL before go-git is allowed to dial it. go-git's default transport
// registry still serves http://, git://, and file:// (only the
// marketplace's https transport is overridden daemon-side), so without
// this a tenant could clone file:///etc/passwd (host-file read),
// git://internal-host/... (internal git daemon), or
// http://169.254.169.254/... (cloud metadata) — the same SSRF class the
// net drops already guard. Only https and ssh are permitted, and the
// resolved host is run through the shared SSRF pre-flight
// (hfnet.CheckDialHost: refuses loopback/private/link-local) plus the
// operator egress allowlist (hfnet.EgressAllowed). The pre-flight is a
// resolve-then-check (a rebinding window remains, like the SMTP/MySQL
// drops, since go-git exposes no dial hook), but it closes the common
// "point me at an internal host or a local file" case. When the operator
// has opted into private egress, the net helpers no-op, matching the
// http drops.
func guardRepoURL(rawURL string) error {
	raw := strings.TrimSpace(rawURL)
	if raw == "" {
		return fmt.Errorf("url is required")
	}
	// scp-like ssh syntax (git@host:path) carries no scheme and trips
	// url.Parse ("first path segment cannot contain colon"), so detect
	// and SSRF-check it before parsing.
	if !strings.Contains(raw, "://") {
		if host, ok := scpLikeHost(raw); ok {
			return hfnet.CheckDialHost(host)
		}
		// No scheme and not scp-like ⇒ a local-filesystem path; refuse
		// it the same as file://.
		return fmt.Errorf("repo URL scheme not allowed (use https:// or ssh://)")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid repo URL: %w", err)
	}
	switch u.Scheme {
	case "https":
		if err := hfnet.EgressAllowed(raw); err != nil {
			return err
		}
		return hfnet.CheckDialHost(u.Host)
	case "ssh":
		return hfnet.CheckDialHost(u.Host)
	default:
		return fmt.Errorf("repo URL scheme %q not allowed (use https:// or ssh://)", u.Scheme)
	}
}

// scpLikeHost extracts the host from scp-like ssh syntax
// ("[user@]host:path", no "://"). It reports false when the string isn't
// scp-like — a colon that follows a slash is a path, not a host:path
// separator, so a bare path like "/srv/repos/x.git" is correctly not
// treated as a remote.
func scpLikeHost(s string) (string, bool) {
	colon := strings.Index(s, ":")
	if colon < 0 {
		return "", false
	}
	if slash := strings.Index(s, "/"); slash >= 0 && slash < colon {
		return "", false
	}
	hostPart := s[:colon]
	if at := strings.LastIndex(hostPart, "@"); at >= 0 {
		hostPart = hostPart[at+1:]
	}
	if hostPart == "" {
		return "", false
	}
	return hostPart, true
}

// openOrClone returns the repository at dst — opening it when the
// directory already holds a git repo (and fetching + updating the working
// tree), or cloning fresh when it does not. mode reports which path was
// taken, so callers can surface it in metadata and shape error codes
// accordingly.
//
// ref (a branch, tag, or commit SHA; empty for the default branch) is
// honoured on both paths. On a fresh clone it is pushed into the clone
// itself via ReferenceName so that (a) a non-default branch is fetched and
// checked out — go-git's post-clone ResolveRevision never expands a bare
// branch name to refs/remotes/origin/<name>, so a separate checkout of one
// would fail — and (b) a shallow clone targets the requested ref instead
// of the default branch, which is the only thing depth would otherwise
// fetch. A commit SHA can't be a ReferenceName, so it forces a full clone
// (depth is meaningless for an arbitrary commit) and a detached checkout.
//
// When dst exists but is not a git repo we refuse rather than wipe it —
// the sandbox holds workspace data the user owns, and silently
// overwriting it would be hostile.
func openOrClone(ctx context.Context, dst, url, ref string, depth int, progress chan<- core.Progress, job core.Job) (*gogit.Repository, string, error) {
	info, statErr := os.Stat(dst)
	if statErr != nil && !os.IsNotExist(statErr) {
		return nil, "stat_failed", statErr
	}
	logSink := newProgressSink(progress, job)
	defer logSink.flush()
	if statErr == nil {
		if !info.IsDir() {
			return nil, "exists", fmt.Errorf("destination %q exists and is not a directory", dst)
		}
		repo, openErr := gogit.PlainOpen(dst)
		if openErr != nil {
			return nil, "not_a_repo", fmt.Errorf("open %q: %w", dst, openErr)
		}
		emitLogProgress(progress, job, "git", "fetch "+url)
		fetchErr := repo.FetchContext(ctx, &gogit.FetchOptions{
			Depth:    depth,
			Tags:     gogit.AllTags, // so a tag ref still resolves on re-runs
			Progress: logSink,
		})
		if fetchErr != nil && fetchErr != gogit.NoErrAlreadyUpToDate {
			return nil, "fetch_failed", fetchErr
		}
		// A fetch only moves remote-tracking refs; bring the working tree up
		// to date too, or a re-run silently returns the stale checkout.
		if ref != "" {
			if err := checkout(repo, ref); err != nil {
				return nil, "checkout_failed", err
			}
		} else if err := updateCurrentBranch(repo); err != nil {
			return nil, "update_failed", err
		}
		return repo, "pulled", nil
	}

	opts := &gogit.CloneOptions{URL: url, Depth: depth, Progress: logSink}
	shallow := depth > 0
	sha := ref != "" && looksLikeSHA(ref)

	// shallowTarget: the one path where the clone lands directly on the ref
	// (ReferenceName), which fetches *only* that ref. Every other path does a
	// full all-refs clone and checks the ref out afterwards.
	shallowTarget := false
	switch {
	case ref == "":
		// Default branch — nothing to target.
	case sha:
		// A commit SHA can't be a ReferenceName and may live anywhere in
		// history, so it always needs a full clone; depth can't help.
		if shallow {
			opts.Depth = 0
			emitLogProgress(progress, job, "git", "ignoring depth: a commit SHA needs full history")
		}
	default:
		// Branch or tag. Validate up front (fast-fail with a clear error
		// before downloading anything) and classify branch-vs-tag.
		rn, rErr := remoteRefName(ctx, url, ref)
		if rErr != nil {
			return nil, "ref_not_found", rErr
		}
		// Only target the ref (single-ref fetch) when shallow, so depth
		// applies to it. A full clone keeps the default all-refs fetch so
		// every branch/tag is present and cross-branch git_log/git_diff keep
		// working; it checks the ref out below.
		if shallow {
			opts.ReferenceName = rn
			opts.SingleBranch = true
			shallowTarget = true
		}
	}

	emitLogProgress(progress, job, "git", "clone "+url)
	repo, cloneErr := gogit.PlainCloneContext(ctx, dst, false, opts)
	if cloneErr != nil {
		return nil, "clone_failed", cloneErr
	}
	if ref != "" && !shallowTarget {
		if err := checkout(repo, ref); err != nil {
			return nil, "checkout_failed", err
		}
	}
	return repo, "cloned", nil
}

// shaPattern matches an abbreviated-or-full hex commit id (git's minimum
// unambiguous abbreviation is 7). A ref this shape is treated as a commit
// rather than a branch/tag name; a branch literally named in hex is a rare
// collision we accept against the common case of pasting a SHA.
var shaPattern = regexp.MustCompile(`^[0-9a-fA-F]{7,40}$`)

func looksLikeSHA(ref string) bool { return shaPattern.MatchString(ref) }

// remoteRefName classifies a short ref against the remote's advertised
// refs, returning the fully-qualified name to clone (refs/heads/<ref> or
// refs/tags/<ref>). It prefers a branch over a same-named tag, matching
// git's precedence, and accepts an already-qualified ref verbatim. Failing
// fast here keeps a typo'd ref from writing a half-clone into the sandbox.
func remoteRefName(ctx context.Context, url, ref string) (plumbing.ReferenceName, error) {
	rem := gogit.NewRemote(memory.NewStorage(), &config.RemoteConfig{
		Name: "origin",
		URLs: []string{url},
	})
	refs, err := rem.ListContext(ctx, &gogit.ListOptions{})
	if err != nil {
		return "", fmt.Errorf("list remote refs: %w", err)
	}
	branch := plumbing.NewBranchReferenceName(ref)
	tag := plumbing.NewTagReferenceName(ref)
	var hasBranch, hasTag bool
	for _, r := range refs {
		switch r.Name() {
		case plumbing.ReferenceName(ref):
			return r.Name(), nil // already fully-qualified
		case branch:
			hasBranch = true
		case tag:
			hasTag = true
		}
	}
	switch {
	case hasBranch:
		return branch, nil
	case hasTag:
		return tag, nil
	default:
		return "", fmt.Errorf("ref %q not found on remote (no such branch or tag)", ref)
	}
}

// checkout moves the working tree to ref, handling the case go-git's
// post-clone resolution misses: a remote-tracking branch with no local
// branch yet. For such a ref it creates (or fast-forwards) a local branch
// at the freshly-fetched remote tip; otherwise it resolves the ref as a
// tag, qualified ref, or commit SHA and checks it out detached.
func checkout(repo *gogit.Repository, ref string) error {
	wt, err := repo.Worktree()
	if err != nil {
		return err
	}
	if rr, err := repo.Reference(plumbing.NewRemoteReferenceName("origin", ref), true); err == nil {
		local := plumbing.NewBranchReferenceName(ref)
		if _, existsErr := repo.Reference(local, false); existsErr == nil {
			// Local branch already exists (a re-run): switch to it, then
			// fast-forward to the updated remote tip.
			if err := wt.Checkout(&gogit.CheckoutOptions{Branch: local, Force: true}); err != nil {
				return err
			}
			return wt.Reset(&gogit.ResetOptions{Mode: gogit.HardReset, Commit: rr.Hash()})
		}
		return wt.Checkout(&gogit.CheckoutOptions{
			Branch: local,
			Hash:   rr.Hash(),
			Create: true,
			Force:  true,
		})
	}
	hash, err := repo.ResolveRevision(plumbing.Revision(ref))
	if err != nil {
		return fmt.Errorf("ref %q not found (no matching branch, tag, or commit)", ref)
	}
	return wt.Checkout(&gogit.CheckoutOptions{Hash: *hash, Force: true})
}

// updateCurrentBranch fast-forwards the checked-out branch to its
// remote-tracking tip after a fetch. It is a no-op when HEAD is detached or
// the branch has no origin counterpart — there is nothing well-defined to
// advance to in those cases.
func updateCurrentBranch(repo *gogit.Repository) error {
	head, err := repo.Head()
	if err != nil {
		return err
	}
	if !head.Name().IsBranch() {
		return nil
	}
	rr, err := repo.Reference(plumbing.NewRemoteReferenceName("origin", head.Name().Short()), true)
	if err != nil {
		return nil
	}
	wt, err := repo.Worktree()
	if err != nil {
		return err
	}
	return wt.Reset(&gogit.ResetOptions{Mode: gogit.HardReset, Commit: rr.Hash()})
}

// progressSink turns the chatty stream go-git writes during clone/fetch
// ("Counting objects: 42%", "Resolving deltas: 100%", …) into one
// progress event per line. go-git uses '\r' for in-place updates, so we
// split on either CR or LF.
type progressSink struct {
	progress chan<- core.Progress
	job      core.Job
	buf      bytes.Buffer
}

func newProgressSink(progress chan<- core.Progress, job core.Job) *progressSink {
	return &progressSink{progress: progress, job: job}
}

func (s *progressSink) Write(p []byte) (int, error) {
	n, _ := s.buf.Write(p)
	for {
		raw := s.buf.Bytes()
		idx := -1
		for i, b := range raw {
			if b == '\n' || b == '\r' {
				idx = i
				break
			}
		}
		if idx < 0 {
			break
		}
		line := string(raw[:idx])
		s.buf.Next(idx + 1)
		if line != "" {
			emitLogProgress(s.progress, s.job, "git", line)
		}
	}
	return n, nil
}

func (s *progressSink) flush() {
	if s.buf.Len() == 0 {
		return
	}
	emitLogProgress(s.progress, s.job, "git", s.buf.String())
	s.buf.Reset()
}

// emitLogProgress emits a line-shaped progress event the frontend
// LiveConsole will display. Kept in this file so git_checkout doesn't
// depend on internals of shell.
func emitLogProgress(ch chan<- core.Progress, job core.Job, stream, line string) {
	if ch == nil {
		return
	}
	select {
	case ch <- core.Progress{
		JobID:   job.ID,
		NodeID:  job.NodeID,
		Message: line,
		Data:    map[string]any{"stream": stream, "line": line},
	}:
	default:
	}
}
