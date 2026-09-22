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

type MigrateResult struct {
	Flows     int
	Revisions int
	Published int
	Truncated []string
}

// Migrate copies every flow in src into dst, preserving each revision's id,
// author, message, timestamp and label, and re-pointing the environments.
//
// Revision ids are carried over, which is what makes this safe to run against a
// live install: a published pointer, a link in someone's tab or a rollback in
// progress still resolves afterwards.
//
// Only flows that currently exist are moved — a flow deleted beforehand keeps
// its history in the git workspace, which is a reason to archive that directory
// rather than delete it. dst must be Postgres-backed. Idempotent per revision.
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
		inRevs := make(map[string]bool, len(revs))
		for _, r := range revs {
			inRevs[r.Commit] = true
		}

		// A git published tag names a WORKSPACE commit — often another flow's
		// save — which is not in this flow's History and so never becomes a
		// revision here. Map it to the flow's own revision at that point, and
		// carry that revision over first (lowest seq) when truncation left it
		// out, so the pointer always names content that loads.
		pub, err := src.PublishedCommit(id)
		if err != nil {
			return res, fmt.Errorf("published %s: %w", id, err)
		}
		pubRev := ""
		if pub != "" {
			if pubRev, err = flowRevisionAt(src, id, pub, inRevs); err != nil {
				return res, fmt.Errorf("published %s: %w", id, err)
			}
			if !inRevs[pubRev] {
				g, err := src.LoadAt(pubRev, id)
				if err != nil {
					return res, fmt.Errorf("read published %s@%s: %w", id, pubRev, err)
				}
				content, err := json.Marshal(g)
				if err != nil {
					return res, fmt.Errorf("marshal %s@%s: %w", id, pubRev, err)
				}
				label, _ := src.RevisionLabel(id, pubRev)
				r := Revision{Commit: pubRev, Author: "migration", Label: label,
					Message: fmt.Sprintf("graph: published revision of %s carried over by migration", id)}
				if err := pg.importRevision(ctx, id, r, "", content); err != nil {
					return res, fmt.Errorf("import published %s@%s: %w", id, pubRev, err)
				}
				res.Revisions++
			}
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
		if pubRev != "" {
			if err := dst.PromoteToEnvironment(id, PublishedEnv, pubRev); err != nil {
				return res, fmt.Errorf("publish %s: %w", id, err)
			}
			res.Published++
		}
	}
	return res, nil
}

// flowRevisionAt maps a published commit to the flow's own revision that was
// current at that commit. On git that is the newest commit at or before it that
// touched the flow's file; a Postgres source already points at a per-flow
// revision.
func flowRevisionAt(src *Store, graphID, commit string, known map[string]bool) (string, error) {
	if known[commit] {
		return commit, nil
	}
	g, ok := src.b.(*gitBackend)
	if !ok {
		return commit, nil
	}
	rev, err := g.flowRevisionAt(graphID, commit)
	if err != nil {
		return "", err
	}
	if rev == "" {
		return "", fmt.Errorf("commit %s holds no revision of %s", commit, graphID)
	}
	return rev, nil
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

type VerifyIssue struct {
	GraphID string
	Detail  string
}

type VerifyResult struct {
	Flows     int
	Revisions int
	Issues    []VerifyIssue
}

func (r VerifyResult) OK() bool { return len(r.Issues) == 0 }

func (r *VerifyResult) flag(graphID, format string, args ...any) {
	r.Issues = append(r.Issues, VerifyIssue{GraphID: graphID, Detail: fmt.Sprintf(format, args...)})
}

// VerifyMigration compares dst against src flow by flow — current content,
// published pointer, every revision's id and content, and labels — and reports
// every difference, so "is it safe to delete the git workspaces now?" has an
// answer other than hoping.
//
// An empty issue list means every flow that still EXISTS came across intact. A
// flow deleted before the migration is not in the source's list either, so its
// history lives on only in the git directory.
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

		// The pointer strings may legitimately differ (a git tag names a
		// workspace commit, the migrated pointer the flow's own revision), so
		// what is compared is what goes live: the published CONTENT must load
		// on both sides and match.
		wantPub, werr := src.LoadPublished(id)
		gotPub, gerr := dst.LoadPublished(id)
		switch {
		case errors.Is(werr, ErrNotPublished) && errors.Is(gerr, ErrNotPublished):
		case errors.Is(werr, ErrNotPublished):
			res.flag(id, "published in the migrated workspace but not in the source")
		case werr != nil:
			return res, fmt.Errorf("source published %s: %w", id, werr)
		case gerr != nil:
			res.flag(id, "published version does not load after migration: %v", gerr)
		case !sameGraph(wantPub, gotPub):
			res.flag(id, "published content differs")
		}

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

func sameGraph(a, b core.Graph) bool {
	ab, err1 := json.Marshal(a)
	bb, err2 := json.Marshal(b)
	return err1 == nil && err2 == nil && string(ab) == string(bb)
}
