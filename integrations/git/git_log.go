package git

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
			ID:             "git_log",
			Version:        "1.0",
			Label:          "Git log",
			Color:          "#f05033",
			Icon:           "git",
			Category:       "io",
			Provider:       "internal",
			Integration:    "Git",
			Tags:           []string{"git", "log", "history", "vcs"},
			Description:    "List recent commits in a checked-out repo. Returns each commit's SHA, author, time, and summary. Useful for showing release notes, attributing changes, or building 'what landed today' reports.",
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				{Port: "path", Label: "Repository path (overrides params.path)"},
			},
			Outputs: []core.Port{
				{Port: "commits", Label: "Commit list (JSON array)"},
				{Port: "meta", Label: "Log metadata (JSON)"},
			},
			ParamsSchema: json.RawMessage(
				`{
					"type":"object",
					"properties":{
						"path":{"type":"string","description":"Workspace-relative repository directory. Overridden by the path input port if connected."},
						"ref":{"type":"string","default":"HEAD","description":"Starting ref — branch, tag, or commit SHA. Defaults to HEAD."},
						"limit":{"type":"integer","default":20,"minimum":1,"maximum":1000,"description":"Maximum number of commits to return."}
					}
				}`,
			),
			Idempotent: true,
		},
		Execute: executeGitLog,
	})
}

func executeGitLog(_ context.Context, job core.Job, progress chan<- core.Progress) (core.Result, error) {
	if job.WorkspaceRoot == "" {
		return params.Err(job, "no_sandbox", "git_log requires a workspace sandbox"), nil
	}
	relPath := params.StringDefault(job.Params, "path", "")
	if input, ok := job.Input["path"]; ok {
		if s, ok := input.Inline.(string); ok && s != "" {
			relPath = s
		} else if relPath == "" && input.Ref != "" {
			relPath = input.Ref
		}
	}
	cleanRel, err := sandboxRel(relPath)
	if err != nil {
		return params.Err(job, "sandbox_escape", err.Error()), nil
	}
	ref := params.StringDefault(job.Params, "ref", "HEAD")
	limit := params.IntDefault(job.Params, "limit", 20)
	if limit < 1 {
		limit = 1
	}
	if limit > 1000 {
		limit = 1000
	}

	repo, err := gogit.PlainOpen(filepath.Join(job.WorkspaceRoot, cleanRel))
	if err != nil {
		return params.Err(job, "open", err.Error()), nil
	}
	startHash, err := repo.ResolveRevision(plumbing.Revision(ref))
	if err != nil {
		return params.Err(job, "bad_ref", err.Error()), nil
	}
	iter, err := repo.Log(&gogit.LogOptions{From: *startHash})
	if err != nil {
		return params.Err(job, "log_failed", err.Error()), nil
	}
	defer iter.Close()

	// Manual iteration so we can stop cleanly when a shallow clone runs
	// out of locally-present parents — ErrObjectNotFound from Next()
	// just means "history's been truncated", not a real failure.
	commits := make([]map[string]any, 0, limit)
	truncated := false
	for len(commits) < limit {
		c, nErr := iter.Next()
		if nErr != nil {
			if errors.Is(nErr, io.EOF) {
				break
			}
			if errors.Is(nErr, plumbing.ErrObjectNotFound) {
				truncated = true
				break
			}
			return params.Err(job, "iter_failed", nErr.Error()), nil
		}
		summary := firstLine(c.Message)
		commits = append(commits, map[string]any{
			"sha":     c.Hash.String(),
			"author":  c.Author.Name,
			"email":   c.Author.Email,
			"when":    c.Author.When,
			"summary": summary,
		})
		// Mirror each entry as a progress line so the pipeline console
		// shows the history as it's walked — matches what `git log`
		// users expect to see streaming past.
		emitLogProgress(progress, job, "git",
			fmt.Sprintf("%s  %-20s  %s",
				c.Hash.String()[:8],
				truncate(c.Author.Name, 20),
				summary,
			))
	}
	if truncated {
		emitLogProgress(progress, job, "git", "(history truncated — shallow clone)")
	}

	meta := map[string]any{
		"start":     startHash.String(),
		"count":     len(commits),
		"truncated": truncated,
	}
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"commits": {MIME: "application/json", Inline: commits},
			"meta":    {MIME: "application/json", Inline: meta},
		},
	}, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}

func firstLine(s string) string {
	for i, r := range s {
		if r == '\n' {
			return s[:i]
		}
	}
	return s
}
