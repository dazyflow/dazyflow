// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package git

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	gogit "github.com/go-git/go-git/v5"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/internal/params"
	"github.com/dazyflow/dazyflow/drops/internal/sandbox"
	"github.com/dazyflow/dazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "git_diff",
			Version:     "1.0",
			Label:       "Git",
			Subtitle:    "Diff",
			Color:       "#f05033",
			Icon:        "git",
			Category:    "io",
			Provider:    "internal",
			Integration: "Git",
			Tags:        []string{"git", "diff", "patch", "vcs"},
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
					Params: json.RawMessage(`{"path":"src/widgets","from":"origin/main","to":"feature/new-api","merge_base":true}`),
					Notes:  "merge_base:true shows only what the feature branch changed since it diverged from main — what you'd expect from comparing two branches.",
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
				{Port: "meta", Label: "Details", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(
				`{
					"type":"object",
					"properties":{
						"path":{"type":"string","title":"Repository folder","format":"workspace-dir","description":"Workspace folder holding the repository — pick a checked-out repo folder. Overridden by the 'Repository folder' input."},
						"from":{"type":"string","title":"Compare from","default":"HEAD~1","description":"Starting point — branch, tag, or commit SHA. Defaults to the commit before the latest (HEAD~1)."},
						"to":{"type":"string","title":"Compare to","default":"HEAD","description":"End point. Defaults to the latest commit (HEAD)."},
						"merge_base":{"type":"boolean","default":false,"title":"Since common ancestor","description":"Compare from where the two refs diverged (git's three-dot A...B) so you see only what changed on the 'to' side. Off compares the two refs directly, which can show unrelated changes from the 'from' side."}
					}
				}`,
			),
			Idempotent: true,
		},
		Execute: executeGitDiff,
	})
}

// maxDiffBytes caps the patch text we buffer into the run record, matching
// the response-size caps the network drops enforce (stripe/gmail/slack). A
// diff across a large or generated-file change can be huge; we keep the
// accurate file/line counts (from patch.Stats(), which parses the whole
// patch regardless) and only truncate the text.
const maxDiffBytes = 16 << 20 // 16 MiB

func executeGitDiff(ctx context.Context, job core.Job, progress chan<- core.Progress) (core.Result, error) {
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
	// Root-confined resolve (not a string clean) so a symlink inside the
	// workspace can't point go-git at a repository outside it.
	repoDir, _, err := sandbox.ResolveDir(job.WorkspaceRoot, relPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return params.Err(job, "bad_param",
				fmt.Sprintf("folder %q doesn't exist in the workspace", relPath)), nil
		}
		return params.Err(job, "sandbox_escape", err.Error()), nil
	}
	from := params.StringDefault(job.Params, "from", "HEAD~1")
	to := params.StringDefault(job.Params, "to", "HEAD")
	mergeBase := params.BoolDefault(job.Params, "merge_base", false)

	repo, err := gogit.PlainOpen(repoDir)
	if err != nil {
		return params.Err(job, "open", err.Error()), nil
	}

	fromHash, err := resolveRevision(repo, from)
	if err != nil {
		return params.Err(job, "bad_ref", fmt.Sprintf("from %q: %v", from, err)), nil
	}
	toHash, err := resolveRevision(repo, to)
	if err != nil {
		return params.Err(job, "bad_ref", fmt.Sprintf("to %q: %v", to, err)), nil
	}

	fromCommit, err := repo.CommitObject(fromHash)
	if err != nil {
		return params.Err(job, "no_commit", fmt.Sprintf("from: %v", err)), nil
	}
	toCommit, err := repo.CommitObject(toHash)
	if err != nil {
		return params.Err(job, "no_commit", fmt.Sprintf("to: %v", err)), nil
	}

	// Three-dot mode: diff from where the two refs diverged, so the result
	// is just what the 'to' side added — not the 'from' side's unrelated
	// commits (which a direct two-dot comparison would show as deletions).
	if mergeBase {
		bases, mErr := fromCommit.MergeBase(toCommit)
		if mErr != nil {
			return params.Err(job, "merge_base_failed", mErr.Error()), nil
		}
		if len(bases) == 0 {
			return params.Err(job, "no_merge_base", fmt.Sprintf("%q and %q share no common ancestor", from, to)), nil
		}
		fromCommit = bases[0]
	}

	emitLogProgress(progress, job, "git", fmt.Sprintf("diff %s..%s", fromCommit.Hash.String()[:8], toHash.String()[:8]))
	patch, err := fromCommit.PatchContext(ctx, toCommit)
	if err != nil {
		if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return params.Err(job, "cancelled", err.Error()), nil
		}
		return params.Err(job, "diff_failed", err.Error()), nil
	}

	// Stats reflect the full diff even when the text is truncated, so the
	// counts stay accurate.
	stats := patch.Stats()
	filesChanged := len(stats)
	added, deleted := 0, 0
	for _, s := range stats {
		added += s.Addition
		deleted += s.Deletion
	}

	diffText := patch.String()
	truncated := false
	if len(diffText) > maxDiffBytes {
		diffText = capDiff(diffText, maxDiffBytes)
		truncated = true
		emitLogProgress(progress, job, "git", fmt.Sprintf("diff truncated at %d bytes", maxDiffBytes))
	}

	meta := map[string]any{
		"from":          fromCommit.Hash.String(),
		"to":            toHash.String(),
		"merge_base":    mergeBase,
		"files_changed": filesChanged,
		"insertions":    added,
		"deletions":     deleted,
		"truncated":     truncated,
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

// capDiff trims patch text to max bytes at the last whole line so the
// output stays valid (no mid-line/mid-rune cut) and ends with a marker
// pointing at the still-accurate stats in meta.
func capDiff(s string, max int) string {
	cut := s[:max]
	if i := strings.LastIndexByte(cut, '\n'); i > 0 {
		cut = cut[:i+1]
	}
	return cut + fmt.Sprintf("… diff truncated at %d bytes — see files_changed / insertions / deletions for full totals …\n", max)
}
