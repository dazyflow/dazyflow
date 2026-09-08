// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

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
	gogittransport "github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/storage/memory"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/internal/params"
	hfnet "github.com/dazyflow/dazyflow/drops/net"
	"github.com/dazyflow/dazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "git_checkout",
			Version:     "1.0",
			Label:       "Git",
			Subtitle:    "Checkout",
			Color:       "#f05033",
			Icon:        "git",
			Category:    "io",
			Provider:    "internal",
			Integration: "Git",
			Tags:        []string{"git", "clone", "checkout", "vcs"},
			Description: "Fetch a copy of a git repository into your workspace, optionally switching to a specific branch, tag, or commit. The files become available to the steps after this one — useful for inspecting source code, pulling templates, or staging files for processing.",
			Summary:     "Clone a remote git repository into the workspace and optionally check out a specific branch, tag, or commit SHA.",
			Examples: []core.ParamsExample{
				{
					Title:  "Full clone of a public repo",
					Params: json.RawMessage(`{"url":"https://github.com/example/widgets.git"}`),
				},
				{
					Title:  "Shallow checkout of a release tag",
					Params: json.RawMessage(`{"url":"https://github.com/example/widgets.git","ref":"v1.4.2","depth":1}`),
					Notes:  "depth:1 keeps the clone small when you only need the tip of a tag or branch.",
				},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Outputs: []core.Port{
				{Port: "path", Label: "Repository folder", MIME: []string{"text/plain"}, Example: json.RawMessage(`"repos/dazyflow"`)},
				{Port: "sha", Label: "Commit SHA", MIME: []string{"text/plain"}, Example: json.RawMessage(`"c0c3608e7a1d4f9b2e8c5a3d6f0b1e4a9c7d2f83"`)},
				{Port: "meta", Label: "Details", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(
				`{
					"type":"object",
					"properties":{
						"url":{"type":"string","title":"Repository URL","description":"Where the repository lives (https or ssh address). Use ${secret.NAME} placeholders for tokens embedded in the URL."},
						"ref":{"type":"string","title":"Branch, tag, or commit","description":"What to switch to after fetching. Leave empty for the repo's default branch."},
						"account":{"type":"string","title":"Git credential","format":"git-account","default":"default","description":"Which saved Git credential to authenticate with — an SSH key for git@/ssh:// URLs, or an access token (PAT) for https:// URLs. Manage these on the Git credentials page. Public repos need none."},
						"depth":{"type":"integer","title":"Clone depth","x_advanced":true,"minimum":0,"description":"Shallow-clone depth. 0 (default) clones the full history."}
					},
					"required":["url"]
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
	if err := guardRepoURL(ctx, url); err != nil {
		return params.Err(job, "blocked", err.Error()), nil
	}
	if job.WorkspaceRoot == "" {
		return params.Err(job, "no_sandbox", "git_checkout requires a workspace sandbox"), nil
	}
	ref := params.StringDefault(job.Params, "ref", "")
	depth := params.IntDefault(job.Params, "depth", 0)

	cleanRel := core.GitCheckoutRel(job.GraphID, job.NodeID)
	dst := filepath.Join(job.WorkspaceRoot, cleanRel)

	if job.QuotaLimit > 0 && job.QuotaUsed >= job.QuotaLimit {
		return params.Err(job, "quota_exceeded", fmt.Sprintf(
			"this organization is at its %d-byte storage limit (%d used); free space before checking out a repository",
			job.QuotaLimit, job.QuotaUsed)), nil
	}
	var sizeBefore int64
	if job.QuotaLimit > 0 {
		sizeBefore = dirSize(dst)
	}

	repo, mode, err := openOrClone(ctx, dst, url, ref, depth, progress, job)
	if err != nil {
		return params.Err(job, mode, err.Error()), nil
	}
	if res, ok := checkoutFitsQuota(job, dst, cleanRel, mode, sizeBefore); !ok {
		return res, nil
	}

	var sha string
	if head, hErr := repo.Head(); hErr == nil {
		sha = head.Hash().String()
	}

	params.EmitProgress(progress, job, 1.0, "done")
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

// The repo URL is tenant-supplied, so it gets the same guard a step's call does.
func guardRepoURL(ctx context.Context, rawURL string) error {
	raw := strings.TrimSpace(rawURL)
	if raw == "" {
		return fmt.Errorf("url is required")
	}
	// scp-like syntax carries no scheme, so it must be normalised before parsing.
	if !strings.Contains(raw, "://") {
		if host, ok := scpLikeHost(raw); ok {
			return hfnet.CheckDialHost(host)
		}
		return fmt.Errorf("repo URL scheme not allowed (use https:// or ssh://)")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid repo URL: %w", err)
	}
	switch u.Scheme {
	case "https":
		if err := hfnet.EgressAllowedFor(ctx, raw); err != nil {
			return err
		}
		return hfnet.CheckDialHost(u.Host)
	case "ssh":
		return hfnet.CheckDialHost(u.Host)
	default:
		return fmt.Errorf("repo URL scheme %q not allowed (use https:// or ssh://)", u.Scheme)
	}
}

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

// A re-run fetches and resets rather than re-cloning, which is the cache.
func openOrClone(ctx context.Context, dst, url, ref string, depth int, progress chan<- core.Progress, job core.Job) (*gogit.Repository, string, error) {
	info, statErr := os.Stat(dst)
	if statErr != nil && !os.IsNotExist(statErr) {
		return nil, "stat_failed", statErr
	}
	// Resolved once: the credential must not be re-read per operation.
	auth, authErr := authForURL(ctx, job, url)
	if authErr != nil {
		return nil, "git_auth_failed", authErr
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
			Auth:     auth,
		})
		if fetchErr != nil && fetchErr != gogit.NoErrAlreadyUpToDate {
			return nil, "fetch_failed", fetchErr
		}
		if ref != "" {
			if err := checkout(repo, ref); err != nil {
				return nil, "checkout_failed", err
			}
		} else if err := updateCurrentBranch(repo); err != nil {
			return nil, "update_failed", err
		}
		return repo, "pulled", nil
	}

	opts := &gogit.CloneOptions{URL: url, Depth: depth, Progress: logSink, Auth: auth}
	shallow := depth > 0
	sha := ref != "" && looksLikeSHA(ref)

	shallowTarget := false
	switch {
	case ref == "":
	case sha:
		if shallow {
			opts.Depth = 0
			emitLogProgress(progress, job, "git", "ignoring depth: a commit SHA needs full history")
		}
	default:
		rn, rErr := remoteRefName(ctx, url, ref, auth)
		if rErr != nil {
			return nil, "ref_not_found", rErr
		}
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

var shaPattern = regexp.MustCompile(`^[0-9a-fA-F]{7,40}$`)

func looksLikeSHA(ref string) bool { return shaPattern.MatchString(ref) }

func remoteRefName(ctx context.Context, url, ref string, auth gogittransport.AuthMethod) (plumbing.ReferenceName, error) {
	rem := gogit.NewRemote(memory.NewStorage(), &config.RemoteConfig{
		Name: "origin",
		URLs: []string{url},
	})
	refs, err := rem.ListContext(ctx, &gogit.ListOptions{Auth: auth})
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

// go-git resolves a branch and a tag differently, so both are tried.
func checkout(repo *gogit.Repository, ref string) error {
	wt, err := repo.Worktree()
	if err != nil {
		return err
	}
	if rr, err := repo.Reference(plumbing.NewRemoteReferenceName("origin", ref), true); err == nil {
		local := plumbing.NewBranchReferenceName(ref)
		if _, existsErr := repo.Reference(local, false); existsErr == nil {
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
