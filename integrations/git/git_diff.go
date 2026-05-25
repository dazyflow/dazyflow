package git

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"

	"git.sr.ht/~klahr/hazy-flow/core"
	"git.sr.ht/~klahr/hazy-flow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:             "git_diff",
			Version:        "1.0",
			Label:          "Git diff",
			Color:          "#f05033",
			Icon:           "git",
			Category:       "io",
			Provider:       "internal",
			Integration:    "Git",
			Tags:           []string{"git", "diff", "patch", "vcs"},
			Description:    "Compute a unified diff between two refs in an already-checked-out repository. Defaults to HEAD~1..HEAD (the most recent commit). Emits the patch text plus a metadata object with the resolved commit SHAs and a short summary.",
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				{Port: "path", Label: "Repository path (overrides params.path)"},
			},
			Outputs: []core.Port{
				{Port: "diff", Label: "Unified diff (patch text)"},
				{Port: "meta", Label: "Diff metadata (JSON)"},
			},
			ParamsSchema: json.RawMessage(
				`{
					"type":"object",
					"properties":{
						"path":{"type":"string","description":"Workspace-relative repository directory. Overridden by the path input port if connected."},
						"from":{"type":"string","default":"HEAD~1","description":"Base ref — branch, tag, or commit SHA. Defaults to HEAD~1."},
						"to":{"type":"string","default":"HEAD","description":"Target ref. Defaults to HEAD."}
					}
				}`,
			),
			Idempotent: true,
		},
		Execute: executeGitDiff,
	})
}

func executeGitDiff(_ context.Context, job core.Job, progress chan<- core.Progress) (core.Result, error) {
	if job.WorkspaceRoot == "" {
		return errResult(job, "no_sandbox", "git_diff requires a workspace sandbox"), nil
	}
	relPath := paramStringDefault(job.Params, "path", "")
	if input, ok := job.Input["path"]; ok {
		if s, ok := input.Inline.(string); ok && s != "" {
			relPath = s
		} else if relPath == "" && input.Ref != "" {
			relPath = input.Ref
		}
	}
	cleanRel, err := sandboxRel(relPath)
	if err != nil {
		return errResult(job, "sandbox_escape", err.Error()), nil
	}
	from := paramStringDefault(job.Params, "from", "HEAD~1")
	to := paramStringDefault(job.Params, "to", "HEAD")

	repo, err := gogit.PlainOpen(filepath.Join(job.WorkspaceRoot, cleanRel))
	if err != nil {
		return errResult(job, "open", err.Error()), nil
	}

	fromHash, err := repo.ResolveRevision(plumbing.Revision(from))
	if err != nil {
		return errResult(job, "bad_ref", fmt.Sprintf("from %q: %v", from, err)), nil
	}
	toHash, err := repo.ResolveRevision(plumbing.Revision(to))
	if err != nil {
		return errResult(job, "bad_ref", fmt.Sprintf("to %q: %v", to, err)), nil
	}

	fromCommit, err := repo.CommitObject(*fromHash)
	if err != nil {
		return errResult(job, "no_commit", fmt.Sprintf("from: %v", err)), nil
	}
	toCommit, err := repo.CommitObject(*toHash)
	if err != nil {
		return errResult(job, "no_commit", fmt.Sprintf("to: %v", err)), nil
	}

	emitLogProgress(progress, job, "git", fmt.Sprintf("diff %s..%s", fromHash.String()[:8], toHash.String()[:8]))
	patch, err := fromCommit.Patch(toCommit)
	if err != nil {
		return errResult(job, "diff_failed", err.Error()), nil
	}
	diffText := patch.String()

	stats := patch.Stats()
	filesChanged := len(stats)
	added, deleted := 0, 0
	for _, s := range stats {
		added += s.Addition
		deleted += s.Deletion
	}

	meta := map[string]any{
		"from":          fromHash.String(),
		"to":            toHash.String(),
		"files_changed": filesChanged,
		"insertions":    added,
		"deletions":     deleted,
	}
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"diff": {MIME: "text/x-diff", Inline: diffText},
			"meta": {MIME: "application/json", Inline: meta},
		},
	}, nil
}
