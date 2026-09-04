// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-billy/v5/osfs"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/cache"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/storer"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/storage/filesystem"

	"github.com/dazyflow/dazyflow/core"
)

// Mirroring a Postgres-backed workspace.
//
// A mirror is a push of a real git repository, and Postgres has none — so one
// is SYNTHESIZED from the revision log: every revision becomes a commit, in
// `seq` order, carrying the author, message and timestamp the row records.
//
// The whole design turns on one property: synthesis must be DETERMINISTIC.
// Whichever replica holds the mirror lock rebuilds the repository from the same
// rows, and if two replicas derived different commit hashes for the same
// history, every failover would turn the next push into a force-push and the
// customer's mirror would lose its history. So nothing here may depend on the
// machine, the clock, or the order rows happen to arrive in:
//
//   - the committer is a fixed identity, never the pod;
//   - every timestamp comes from the row and is normalized to UTC;
//   - the flow's JSON is re-encoded canonically rather than echoed;
//   - commits follow `seq`, the order Postgres already assigned.
//
// Given that, the on-disk repository is a pure CACHE. Losing it costs one
// rebuild (~390 revisions/sec, measured) and nothing else, which is why it can
// live on whichever replica currently mirrors.

// synthTrailer records which revision a synthesized commit came from, so the
// repository describes its own sync point and needs no side-file to resume.
const synthTrailer = "Dazyflow-Revision: "

// synthCommitter is the fixed committer identity. A pod's own identity here
// would make the same history hash differently on every replica.
var synthCommitter = object.Signature{Name: "dazyflow", Email: "dazyflow@local"}

// pgMirror derives a git repository from a Postgres workspace and pushes it.
type pgMirror struct {
	pg  *pgBackend
	dir string

	// mu serializes sync+push for this workspace. Cross-process safety comes
	// from the caller electing a single mirroring replica; this guards the
	// in-process case, where the mirror queue may coalesce concurrent triggers.
	mu sync.Mutex
}

func (m *pgMirror) push(ctx context.Context, remoteURL string, auth transport.AuthMethod, allowUnrelated bool) (PushResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	g, err := m.sync(ctx)
	if err != nil {
		return PushResult{}, err
	}
	if g == nil {
		// Nothing has ever been saved in this workspace; there is no history to
		// mirror yet. A successful no-op, not a failure.
		return PushResult{}, nil
	}
	return g.push(ctx, remoteURL, auth, allowUnrelated)
}

// sync brings the derived repository up to date with the revision log and
// returns it, or nil when the workspace holds no revisions at all.
func (m *pgMirror) sync(ctx context.Context) (*gitBackend, error) {
	g, err := openSynthRepo(m.dir)
	if err != nil {
		return nil, err
	}
	from, err := m.resumeSeq(ctx, g)
	if err != nil {
		return nil, err
	}
	if from < 0 {
		// The repository no longer agrees with the log — a revision it was
		// built on is gone. Derived state, so the answer is to rebuild it.
		if err := os.RemoveAll(m.dir); err != nil {
			return nil, fmt.Errorf("discard stale mirror cache: %w", err)
		}
		if g, err = openSynthRepo(m.dir); err != nil {
			return nil, err
		}
		from = 0
	}
	n, err := m.replay(ctx, g, from)
	if err != nil {
		return nil, err
	}
	if _, err := g.repo.Head(); err != nil {
		if errors.Is(err, plumbing.ErrReferenceNotFound) {
			return nil, nil // no revisions anywhere yet
		}
		return nil, err
	}
	if err := m.syncRefs(ctx, g); err != nil {
		return nil, err
	}
	_ = n
	return g, nil
}

// resumeSeq reports the `seq` the derived repository has already synthesized:
// 0 for an empty one, and -1 when its sync point no longer exists in the log.
func (m *pgMirror) resumeSeq(ctx context.Context, g *gitBackend) (int64, error) {
	head, err := g.repo.Head()
	if errors.Is(err, plumbing.ErrReferenceNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	c, err := g.repo.CommitObject(head.Hash())
	if err != nil {
		// HEAD names an object this repository does not hold — a half-written
		// cache, a truncated disk. Derived state, so rebuild rather than fail
		// every push from here on.
		return -1, nil //nolint:nilerr // an unreadable head means "rebuild"
	}
	rev := revisionFromMessage(c.Message)
	if rev == "" {
		return -1, nil
	}
	var seq int64
	err = m.pg.pool.QueryRow(ctx,
		`SELECT seq FROM flow_revisions WHERE tenant=$1 AND workspace=$2 AND revision=$3`,
		m.pg.tenant, m.pg.workspace, rev).Scan(&seq)
	if err != nil {
		return -1, nil //nolint:nilerr // a missing sync point means "rebuild"
	}
	return seq, nil
}

func revisionFromMessage(msg string) string {
	for line := range strings.SplitSeq(msg, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), synthTrailer); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// replay writes every revision after `from` as a commit, in seq order.
func (m *pgMirror) replay(ctx context.Context, g *gitBackend, from int64) (int, error) {
	rows, err := m.pg.pool.Query(ctx,
		`SELECT seq, graph_id, revision, author, message, content, created_at
		   FROM flow_revisions
		  WHERE tenant=$1 AND workspace=$2 AND seq > $3
		  ORDER BY seq`,
		m.pg.tenant, m.pg.workspace, from)
	if err != nil {
		return 0, err
	}
	type rev struct {
		graphID, revision, author, message string
		content                            []byte
		when                               time.Time
	}
	var pending []rev
	for rows.Next() {
		var r rev
		var seq int64
		if err := rows.Scan(&seq, &r.graphID, &r.revision, &r.author, &r.message, &r.content, &r.when); err != nil {
			rows.Close()
			return 0, err
		}
		pending = append(pending, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	wt, err := g.repo.Worktree()
	if err != nil {
		return 0, err
	}
	for _, r := range pending {
		rel := graphPath(r.graphID)
		if r.content == nil {
			// A deletion. Absent already (a tombstone after a tombstone) is
			// fine; the commit still lands so revisions and commits stay 1:1,
			// which is what resuming depends on.
			if _, err := g.fs.Stat(rel); err == nil {
				if err := g.fs.Remove(rel); err != nil {
					return 0, fmt.Errorf("remove %s: %w", rel, err)
				}
			}
		} else {
			pretty, err := canonicalGraphJSON(r.content)
			if err != nil {
				return 0, fmt.Errorf("encode %s@%s: %w", r.graphID, r.revision, err)
			}
			if err := g.fs.MkdirAll(path.Dir(rel), 0o755); err != nil {
				return 0, err
			}
			f, err := g.fs.Create(rel)
			if err != nil {
				return 0, err
			}
			if _, err := f.Write(pretty); err != nil {
				_ = f.Close()
				return 0, err
			}
			if err := f.Close(); err != nil {
				return 0, err
			}
		}
		if _, err := wt.Add(rel); err != nil {
			return 0, fmt.Errorf("stage %s: %w", rel, err)
		}
		when := r.when.UTC()
		author := object.Signature{Name: r.author, Email: r.author, When: when}
		committer := synthCommitter
		committer.When = when
		if _, err := wt.Commit(r.message+"\n\n"+synthTrailer+r.revision, &git.CommitOptions{
			Author:    &author,
			Committer: &committer,
			// A revision whose content leaves the tree unchanged still gets a
			// commit: the 1:1 mapping is what lets resumeSeq find its place.
			AllowEmptyCommits: true,
		}); err != nil {
			return 0, fmt.Errorf("commit %s@%s: %w", r.graphID, r.revision, err)
		}
	}
	return len(pending), nil
}

// canonicalGraphJSON re-encodes a stored flow so the bytes committed depend
// only on the flow's content — not on how whichever backend wrote it happened
// to marshal it. Indented because a mirror is read by people.
func canonicalGraphJSON(stored []byte) ([]byte, error) {
	var g core.Graph
	if err := json.Unmarshal(stored, &g); err != nil {
		return nil, err
	}
	return json.MarshalIndent(g, "", "  ")
}

// syncRefs rewrites the environment and label tags to match the log. Tags are
// pointers, so they are rebuilt wholesale rather than diffed: a publish, a
// rollback and an unpublish all land as "the pointer is now here".
func (m *pgMirror) syncRefs(ctx context.Context, g *gitBackend) error {
	byRevision, err := m.commitIndex(g)
	if err != nil {
		return err
	}
	// Drop the tags we own, so a cleared pointer disappears from the mirror.
	iter, err := g.repo.References()
	if err != nil {
		return err
	}
	var stale []plumbing.ReferenceName
	if err := iter.ForEach(func(ref *plumbing.Reference) error {
		if strings.HasPrefix(ref.Name().String(), "refs/tags/graphs/") {
			stale = append(stale, ref.Name())
		}
		return nil
	}); err != nil {
		return err
	}
	for _, name := range stale {
		if err := g.repo.Storer.RemoveReference(name); err != nil &&
			!errors.Is(err, plumbing.ErrReferenceNotFound) {
			return err
		}
	}

	envRows, err := m.pg.pool.Query(ctx,
		`SELECT graph_id, env, revision FROM flow_envs WHERE tenant=$1 AND workspace=$2 ORDER BY graph_id, env`,
		m.pg.tenant, m.pg.workspace)
	if err != nil {
		return err
	}
	type envRef struct{ graphID, env, revision string }
	var envs []envRef
	for envRows.Next() {
		var e envRef
		if err := envRows.Scan(&e.graphID, &e.env, &e.revision); err != nil {
			envRows.Close()
			return err
		}
		envs = append(envs, e)
	}
	envRows.Close()
	if err := envRows.Err(); err != nil {
		return err
	}
	for _, e := range envs {
		h, ok := byRevision[e.revision]
		if !ok {
			continue // points at a revision this repo has not synthesized
		}
		name := plumbing.NewTagReferenceName(envTag(e.graphID, e.env))
		if err := g.repo.Storer.SetReference(plumbing.NewHashReference(name, h)); err != nil {
			return fmt.Errorf("tag %s: %w", name, err)
		}
	}

	labelRows, err := m.pg.pool.Query(ctx,
		`SELECT graph_id, revision, label FROM flow_revisions
		  WHERE tenant=$1 AND workspace=$2 AND label <> '' ORDER BY graph_id, seq`,
		m.pg.tenant, m.pg.workspace)
	if err != nil {
		return err
	}
	type labelRef struct{ graphID, revision, label string }
	var labels []labelRef
	for labelRows.Next() {
		var l labelRef
		if err := labelRows.Scan(&l.graphID, &l.revision, &l.label); err != nil {
			labelRows.Close()
			return err
		}
		labels = append(labels, l)
	}
	labelRows.Close()
	if err := labelRows.Err(); err != nil {
		return err
	}
	for _, l := range labels {
		h, ok := byRevision[l.revision]
		if !ok {
			continue
		}
		c, err := g.repo.CommitObject(h)
		if err != nil {
			return err
		}
		// The tagger timestamp comes from the commit, not the clock, for the
		// same reason the commit's own does.
		tagger := synthCommitter
		tagger.When = c.Committer.When
		if _, err := g.repo.CreateTag(labelTag(l.graphID, h.String()), h, &git.CreateTagOptions{
			Message: l.label,
			Tagger:  &tagger,
		}); err != nil {
			return fmt.Errorf("label tag %s: %w", l.graphID, err)
		}
	}
	return nil
}

// commitIndex maps every synthesized revision to its commit, by walking the
// log once and reading each commit's trailer.
func (m *pgMirror) commitIndex(g *gitBackend) (map[string]plumbing.Hash, error) {
	out := map[string]plumbing.Hash{}
	head, err := g.repo.Head()
	if errors.Is(err, plumbing.ErrReferenceNotFound) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	iter, err := g.repo.Log(&git.LogOptions{From: head.Hash()})
	if err != nil {
		return nil, err
	}
	defer iter.Close()
	err = iter.ForEach(func(c *object.Commit) error {
		if rev := revisionFromMessage(c.Message); rev != "" {
			out[rev] = c.Hash
		}
		return nil
	})
	if err != nil && !errors.Is(err, storer.ErrStop) {
		return nil, err
	}
	return out, nil
}

// openSynthRepo opens (or initializes) the derived repository.
//
// Deliberately NOT openDisk: that seeds a first commit stamped with time.Now(),
// which would give every replica a different root and defeat the determinism
// the whole design rests on. A synthesized repository's first commit is the
// workspace's first revision.
func openSynthRepo(dir string) (*gitBackend, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir %q: %w", dir, err)
	}
	wt := osfs.New(dir)
	gitDir, err := wt.Chroot(".git")
	if err != nil {
		return nil, err
	}
	storer := filesystem.NewStorage(gitDir, cache.NewObjectLRU(objectCacheBytes))
	repo, err := git.Open(storer, wt)
	if errors.Is(err, git.ErrRepositoryNotExists) {
		repo, err = git.Init(storer, wt)
	}
	if err != nil {
		return nil, fmt.Errorf("open mirror cache: %w", err)
	}
	return &gitBackend{mu: dirMutex(dir), repo: repo, fs: billy.Filesystem(wt)}, nil
}
