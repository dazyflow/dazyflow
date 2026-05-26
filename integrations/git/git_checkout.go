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
	"os"
	"path/filepath"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"

	"git.sr.ht/~klahr/hazy-flow/core"
	"git.sr.ht/~klahr/hazy-flow/engine"
	"git.sr.ht/~klahr/hazy-flow/integrations/internal/params"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:             "git_checkout",
			Version:        "1.0",
			Label:          "Git checkout",
			Color:          "#f05033",
			Icon:           "git",
			Category:       "io",
			Provider:       "internal",
			Integration:    "Git",
			Tags:           []string{"git", "clone", "checkout", "vcs"},
			Description:    "Clone a git repository into your workspace, optionally checking out a specific branch, tag, or commit. The cloned files become available to downstream nodes — useful for inspecting source code, pulling templates, or staging files for processing.",
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Outputs: []core.Port{
				{Port: "path", Label: "Repository path (workspace-relative)"},
				{Port: "meta", Label: "Checkout metadata (JSON)"},
			},
			ParamsSchema: json.RawMessage(
				`{
					"type":"object",
					"properties":{
						"url":{"type":"string","description":"Repository URL (https or ssh). Use ${env:NAME} placeholders for tokens embedded in the URL."},
						"ref":{"type":"string","description":"Branch, tag, or commit SHA to check out. Defaults to the remote HEAD."},
						"path":{"type":"string","description":"Workspace-relative directory to clone into. Must not already exist."},
						"depth":{"type":"integer","minimum":0,"description":"Shallow-clone depth. 0 (default) clones the full history."}
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

	repo, mode, err := openOrClone(ctx, dst, url, depth, progress, job)
	if err != nil {
		return params.Err(job, mode, err.Error()), nil
	}

	if ref != "" {
		wt, wErr := repo.Worktree()
		if wErr != nil {
			return params.Err(job, "worktree", wErr.Error()), nil
		}
		co := &gogit.CheckoutOptions{Force: true}
		if hash, hErr := repo.ResolveRevision(plumbing.Revision(ref)); hErr == nil {
			co.Hash = *hash
		} else {
			co.Branch = plumbing.NewBranchReferenceName(ref)
		}
		if cErr := wt.Checkout(co); cErr != nil {
			return params.Err(job, "checkout_failed", cErr.Error()), nil
		}
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
			"meta": {MIME: "application/json", Inline: meta},
		},
	}, nil
}

// openOrClone returns the repository at dst — opening it when the
// directory already holds a git repo (and fetching to update refs), or
// cloning fresh when it does not. mode reports which path was taken, so
// callers can surface it in metadata and shape error codes accordingly.
//
// When dst exists but is not a git repo we refuse rather than wipe it —
// the sandbox holds workspace data the user owns, and silently
// overwriting it would be hostile.
func openOrClone(ctx context.Context, dst, url string, depth int, progress chan<- core.Progress, job core.Job) (*gogit.Repository, string, error) {
	info, statErr := os.Stat(dst)
	if statErr != nil && !os.IsNotExist(statErr) {
		return nil, "stat", statErr
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
			Progress: logSink,
		})
		if fetchErr != nil && fetchErr != gogit.NoErrAlreadyUpToDate {
			return nil, "fetch_failed", fetchErr
		}
		return repo, "pulled", nil
	}
	emitLogProgress(progress, job, "git", "clone "+url)
	repo, cloneErr := gogit.PlainCloneContext(ctx, dst, false, &gogit.CloneOptions{
		URL:      url,
		Depth:    depth,
		Progress: logSink,
	})
	if cloneErr != nil {
		return nil, "clone_failed", cloneErr
	}
	return repo, "cloned", nil
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
