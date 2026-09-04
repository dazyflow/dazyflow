// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/dazyflow/dazyflow/core"
)

// maxMigratedRevisions caps how much of a flow's past one migration carries.
// A workspace edited daily for years stays well inside it, and the alternative
// — an unbounded read of every revision of every flow in one pass — is how a
// migration OOMs an install too big to migrate twice.
const maxMigratedRevisions = 10_000

// MigrateResult reports what a migration moved.
type MigrateResult struct {
	Flows     int
	Revisions int
	Published int
	// Truncated names flows whose history was longer than
	// maxMigratedRevisions; their newest revisions came over and the oldest
	// were left behind, so the operator can decide whether that matters.
	Truncated []string
}

// Migrate copies every flow in src into dst, preserving each revision's id,
// author, message, timestamp and label, and re-pointing the environments.
//
// Revision IDS ARE CARRIED OVER, which is what makes the migration safe to run
// against a live install: anything already holding a revision id — a published
// pointer, a link in someone's tab, a rollback the user is about to do — still
// resolves afterwards.
//
// Only flows that currently exist are moved. A flow deleted before the
// migration keeps its history in the git workspace and does not come across;
// the git directory is the archive for those, which is a reason to keep it
// rather than delete it the moment the migration succeeds.
//
// dst must be Postgres-backed. Idempotent per revision, so a failed run can be
// repeated.
func Migrate(ctx context.Context, dst, src *Store) (MigrateResult, error) {
	var res MigrateResult
	pg, ok := dst.b.(*pgBackend)
	if !ok {
		return res, errors.New("migration destination must be a Postgres-backed workspace")
	}
	ids, err := src.ListGraphs()
	if err != nil {
		return res, fmt.Errorf("list flows: %w", err)
	}
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		revs, err := src.History(id, maxMigratedRevisions)
		if err != nil {
			return res, fmt.Errorf("history %s: %w", id, err)
		}
		if len(revs) == maxMigratedRevisions {
			res.Truncated = append(res.Truncated, id)
		}
		// History is newest-first; replay oldest-first so each revision's
		// parent is already there.
		var parent string
		for i := len(revs) - 1; i >= 0; i-- {
			r := revs[i]
			var content []byte
			g, lerr := src.LoadAt(r.Commit, id)
			switch {
			case lerr == nil:
				if content, err = json.Marshal(g); err != nil {
					return res, fmt.Errorf("marshal %s@%s: %w", id, r.Commit, err)
				}
			case errors.Is(lerr, ErrGraphNotFound):
				// A revision that removed the flow. It is part of the history
				// and is carried as the tombstone it is.
				content = nil
			default:
				return res, fmt.Errorf("read %s@%s: %w", id, r.Commit, lerr)
			}
			if err := pg.importRevision(ctx, id, r, parent, content); err != nil {
				return res, fmt.Errorf("import %s@%s: %w", id, r.Commit, err)
			}
			parent = r.Commit
			res.Revisions++
		}
		if parent != "" {
			if err := pg.setHead(ctx, id, parent); err != nil {
				return res, fmt.Errorf("set head %s: %w", id, err)
			}
		}
		res.Flows++

		// Environment pointers. Published is the one that decides whether a
		// flow is live, so a migration that dropped it would take every
		// scheduled and webhook-triggered flow in the install offline.
		pub, err := src.PublishedCommit(id)
		if err != nil {
			return res, fmt.Errorf("published %s: %w", id, err)
		}
		if pub != "" {
			if err := dst.PromoteToEnvironment(id, PublishedEnv, pub); err != nil {
				return res, fmt.Errorf("publish %s: %w", id, err)
			}
			res.Published++
		}
	}
	return res, nil
}

// importRevision writes a revision verbatim. Upsert rather than insert so a
// re-run after a partial failure converges instead of colliding.
func (p *pgBackend) importRevision(ctx context.Context, graphID string, r Revision, parent string, content []byte) error {
	when := r.When
	if when.IsZero() {
		when = time.Now()
	}
	_, err := p.pool.Exec(ctx,
		`INSERT INTO flow_revisions (tenant, workspace, graph_id, revision, parent, author, message, label, content, created_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		 ON CONFLICT (tenant, workspace, graph_id, revision) DO UPDATE
		   SET parent=EXCLUDED.parent, author=EXCLUDED.author, message=EXCLUDED.message,
		       label=EXCLUDED.label, content=EXCLUDED.content, created_at=EXCLUDED.created_at`,
		p.tenant, p.workspace, graphID, r.Commit, parent, r.Author, r.Message, r.Label, content, when)
	return err
}

func (p *pgBackend) setHead(ctx context.Context, graphID, revision string) error {
	_, err := p.pool.Exec(ctx,
		`INSERT INTO flow_heads (tenant, workspace, graph_id, revision) VALUES ($1,$2,$3,$4)
		 ON CONFLICT (tenant, workspace, graph_id) DO UPDATE SET revision=EXCLUDED.revision, updated_at=now()`,
		p.tenant, p.workspace, graphID, revision)
	return err
}

// VerifyIssue is one difference between the source workspace and the migrated
// copy.
type VerifyIssue struct {
	GraphID string
	Detail  string
}

// VerifyResult reports a comparison of a migrated workspace against its source.
type VerifyResult struct {
	Flows     int
	Revisions int
	Issues    []VerifyIssue
}

// OK reports whether the two workspaces agree.
func (r VerifyResult) OK() bool { return len(r.Issues) == 0 }

func (r *VerifyResult) flag(graphID, format string, args ...any) {
	r.Issues = append(r.Issues, VerifyIssue{GraphID: graphID, Detail: fmt.Sprintf(format, args...)})
}

// VerifyMigration compares dst against src flow by flow — current content,
// published pointer, every revision's id and content, and labels — and reports
// every difference.
//
// It exists so that "is it safe to delete the git workspaces now?" has an
// answer other than hoping. Run it after migrating; an empty issue list means
// every flow that still EXISTS came across intact.
//
// What it deliberately cannot tell you: a flow deleted before the migration is
// not in the source's flow list either, so its history lives on only in the git
// directory. That is the reason to archive that directory rather than delete
// it, even on a clean verification.
func VerifyMigration(ctx context.Context, dst, src *Store) (VerifyResult, error) {
	var res VerifyResult
	srcIDs, err := src.ListGraphs()
	if err != nil {
		return res, fmt.Errorf("list source flows: %w", err)
	}
	dstIDs, err := dst.ListGraphs()
	if err != nil {
		return res, fmt.Errorf("list migrated flows: %w", err)
	}
	inDst := make(map[string]bool, len(dstIDs))
	for _, id := range dstIDs {
		inDst[id] = true
	}
	for _, id := range srcIDs {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		res.Flows++
		if !inDst[id] {
			res.flag(id, "missing from the migrated workspace")
			continue
		}
		delete(inDst, id)

		// Current content.
		want, err := src.Load(id)
		if err != nil {
			return res, fmt.Errorf("read source %s: %w", id, err)
		}
		got, err := dst.Load(id)
		if err != nil {
			res.flag(id, "unreadable after migration: %v", err)
			continue
		}
		if !sameGraph(want, got) {
			res.flag(id, "current content differs")
		}

		// Published pointer.
		wantPub, err := src.PublishedCommit(id)
		if err != nil {
			return res, fmt.Errorf("source published %s: %w", id, err)
		}
		gotPub, err := dst.PublishedCommit(id)
		if err != nil {
			res.flag(id, "published pointer unreadable: %v", err)
		} else if wantPub != gotPub {
			res.flag(id, "published pointer is %q, source has %q", gotPub, wantPub)
		}

		// Every revision: id, content and label.
		wantRevs, err := src.History(id, maxMigratedRevisions)
		if err != nil {
			return res, fmt.Errorf("source history %s: %w", id, err)
		}
		gotRevs, err := dst.History(id, maxMigratedRevisions)
		if err != nil {
			res.flag(id, "history unreadable: %v", err)
			continue
		}
		if len(gotRevs) != len(wantRevs) {
			res.flag(id, "history has %d revisions, source has %d", len(gotRevs), len(wantRevs))
		}
		gotByID := make(map[string]Revision, len(gotRevs))
		for _, r := range gotRevs {
			gotByID[r.Commit] = r
		}
		for _, w := range wantRevs {
			res.Revisions++
			g, ok := gotByID[w.Commit]
			if !ok {
				res.flag(id, "revision %s is missing", w.Commit)
				continue
			}
			if g.Author != w.Author || g.Label != w.Label {
				res.flag(id, "revision %s: author/label differ (%q/%q vs %q/%q)",
					w.Commit, g.Author, g.Label, w.Author, w.Label)
			}
			wantAt, werr := src.LoadAt(w.Commit, id)
			gotAt, gerr := dst.LoadAt(w.Commit, id)
			switch {
			case errors.Is(werr, ErrGraphNotFound) && errors.Is(gerr, ErrGraphNotFound):
				// Both sides agree this revision deleted the flow.
			case werr != nil || gerr != nil:
				res.flag(id, "revision %s unreadable on one side (source %v, migrated %v)", w.Commit, werr, gerr)
			case !sameGraph(wantAt, gotAt):
				res.flag(id, "revision %s: content differs", w.Commit)
			}
		}
	}
	for id := range inDst {
		res.flag(id, "present in the migrated workspace but not in the source (written after the migration?)")
	}
	return res, nil
}

// sameGraph compares two flows by their canonical encoding, so a difference in
// how each side marshalled it is not reported as a difference in content.
func sameGraph(a, b core.Graph) bool {
	ab, err1 := json.Marshal(a)
	bb, err2 := json.Marshal(b)
	return err1 == nil && err2 == nil && string(ab) == string(bb)
}
