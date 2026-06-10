package git

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"

	"git.sr.ht/~klahr/hazyflow/core"
	"git.sr.ht/~klahr/hazyflow/engine"
	"git.sr.ht/~klahr/hazyflow/drops/internal/params"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:             "git_diff",
			Version:        "1.0",
			Label:          "Git",
			Subtitle:       "Diff",
			Color:          "#f05033",
			Icon:           "git",
			Category:       "io",
			Provider:       "internal",
			Integration:    "Git",
			Tags:           []string{"git", "diff", "patch", "vcs"},
			Description: "Show what changed between two points in a checked-out repo's history, as a unified diff (patch text). By default it shows the most recent commit's changes (HEAD~1..HEAD), but you can compare any two branches, tags, or commits.",
			Summary:     "Produce a unified diff (patch text) between two git refs in a checked-out workspace repo.",
			Examples: []core.ParamsExample{
				{
					Title:  "Last commit on HEAD",
					Params: json.RawMessage(`{"path":"src/widgets"}`),
					Notes:  "Defaults to HEAD~1..HEAD — the diff introduced by the most recent commit.",
				},
				{
					Title:  "Compare a feature branch against main",
					Params: json.RawMessage(`{"path":"src/widgets","from":"origin/main","to":"feature/new-api"}`),
				},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				// Named after its param so the card shows an inline editable
				// box; a wired value (e.g. git checkout's path output)
				// overrides the typed one.
				{Port: "path", Label: "Repository folder"},
			},
			Outputs: []core.Port{
				// Only the diff text is a pin; the change summary
				// (files_changed, insertions, …) is still EMITTED under
				// "meta" so run records keep it for debugging — it's just
				// not a pin.
				{Port: "diff", Label: "Diff", MIME: []string{"text/plain"}},
			},
			ParamsSchema: json.RawMessage(
				`{
					"type":"object",
					"properties":{
						"path":{"type":"string","title":"Folder","description":"Workspace folder holding the repository. Overridden by the 'Repository folder' input."},
						"from":{"type":"string","title":"Compare from","default":"HEAD~1","description":"Starting point — branch, tag, or commit SHA. Defaults to the commit before the latest (HEAD~1)."},
						"to":{"type":"string","title":"Compare to","default":"HEAD","description":"End point. Defaults to the latest commit (HEAD)."}
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
		return params.Err(job, "no_sandbox", "git_diff requires a workspace sandbox"), nil
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
	from := params.StringDefault(job.Params, "from", "HEAD~1")
	to := params.StringDefault(job.Params, "to", "HEAD")

	repo, err := gogit.PlainOpen(filepath.Join(job.WorkspaceRoot, cleanRel))
	if err != nil {
		return params.Err(job, "open", err.Error()), nil
	}

	fromHash, err := repo.ResolveRevision(plumbing.Revision(from))
	if err != nil {
		return params.Err(job, "bad_ref", fmt.Sprintf("from %q: %v", from, err)), nil
	}
	toHash, err := repo.ResolveRevision(plumbing.Revision(to))
	if err != nil {
		return params.Err(job, "bad_ref", fmt.Sprintf("to %q: %v", to, err)), nil
	}

	fromCommit, err := repo.CommitObject(*fromHash)
	if err != nil {
		return params.Err(job, "no_commit", fmt.Sprintf("from: %v", err)), nil
	}
	toCommit, err := repo.CommitObject(*toHash)
	if err != nil {
		return params.Err(job, "no_commit", fmt.Sprintf("to: %v", err)), nil
	}

	emitLogProgress(progress, job, "git", fmt.Sprintf("diff %s..%s", fromHash.String()[:8], toHash.String()[:8]))
	patch, err := fromCommit.Patch(toCommit)
	if err != nil {
		return params.Err(job, "diff_failed", err.Error()), nil
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
			"diff": {MIME: "text/plain", Inline: diffText},
			"meta": {MIME: "application/json", Inline: meta},
		},
	}, nil
}
