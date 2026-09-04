// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package workspace

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/dazyflow/dazyflow/core"
)

// pgBackend keeps a workspace's flows as a revision log in Postgres.
//
// The shape mirrors what git gave us, because the semantics the daemon depends
// on are git's: an append-only chain of revisions per flow, movable pointers
// naming a revision per environment, and a label attachable to any revision.
// What it drops is the working tree — which is the point, since a working tree
// on shared storage is what stops two dzd processes editing one workspace.
//
// Revisions are per FLOW rather than per workspace. Git had no choice but to
// give the whole workspace one history (a commit is a whole-tree snapshot), so
// saving flow A appeared in flow B's timeline as an unchanged neighbour. Here
// a save touches only its own chain, and History reads it directly instead of
// walking every commit filtering by path.
type pgBackend struct {
	pool      *pgxpool.Pool
	tenant    string
	workspace string
	// mirrorDir is where synthesis keeps this workspace's derived git
	// repository. Empty disables mirroring — see gitsynth.go.
	mirrorDir string
}

// PgOption configures a Postgres-backed workspace.
type PgOption func(*pgBackend)

// WithMirrorCache enables git mirroring for a Postgres-backed workspace, using
// dir for the repository synthesis derives from the revision log.
//
// The directory is a CACHE: deleting it costs one rebuild and nothing else. Only
// the replica that mirrors needs it, and only one replica may — see pgMirror.
func WithMirrorCache(dir string) PgOption {
	return func(b *pgBackend) { b.mirrorDir = dir }
}

const pgWorkspaceSchema = `
CREATE TABLE IF NOT EXISTS flow_revisions (
    tenant     TEXT NOT NULL,
    workspace  TEXT NOT NULL,
    graph_id   TEXT NOT NULL,
    revision   TEXT NOT NULL,
    parent     TEXT NOT NULL DEFAULT '',
    author     TEXT NOT NULL,
    message    TEXT NOT NULL,
    label      TEXT NOT NULL DEFAULT '',
    -- NULL content is a deletion: the flow's chain continues (its history
    -- survives the delete, which is where a mistaken one is recovered from)
    -- but the flow reads as gone.
    content    JSONB,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    seq        BIGSERIAL NOT NULL,
    PRIMARY KEY (tenant, workspace, graph_id, revision)
);
-- History reads one flow newest-first; Head reads the whole workspace's
-- high-water mark. Both are covered here.
CREATE INDEX IF NOT EXISTS flow_revisions_history_idx
    ON flow_revisions (tenant, workspace, graph_id, seq DESC);
CREATE INDEX IF NOT EXISTS flow_revisions_ws_idx
    ON flow_revisions (tenant, workspace, seq DESC);

-- The current revision of each flow: git's HEAD, per flow.
CREATE TABLE IF NOT EXISTS flow_heads (
    tenant     TEXT NOT NULL,
    workspace  TEXT NOT NULL,
    graph_id   TEXT NOT NULL,
    revision   TEXT NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant, workspace, graph_id)
);

-- Environment pointers: git's refs/tags/graphs/<id>/<env>.
CREATE TABLE IF NOT EXISTS flow_envs (
    tenant    TEXT NOT NULL,
    workspace TEXT NOT NULL,
    graph_id  TEXT NOT NULL,
    env       TEXT NOT NULL,
    revision  TEXT NOT NULL,
    PRIMARY KEY (tenant, workspace, graph_id, env)
);
`

// EnsurePgWorkspaceSchema creates the tables a Postgres-backed workspace needs.
// Idempotent; run once at startup.
func EnsurePgWorkspaceSchema(ctx context.Context, pool *pgxpool.Pool) error {
	if pool == nil {
		return errors.New("nil pool")
	}
	_, err := pool.Exec(ctx, pgWorkspaceSchema)
	return err
}

// OpenPostgres returns a Store keeping one (tenant, workspace) pair's flows in
// Postgres. The schema must already exist — see EnsurePgWorkspaceSchema.
func OpenPostgres(pool *pgxpool.Pool, tenant, workspace string, opts ...PgOption) (*Store, error) {
	if pool == nil {
		return nil, errors.New("nil pool")
	}
	if tenant == "" || workspace == "" {
		return nil, errors.New("tenant and workspace required")
	}
	b := &pgBackend{pool: pool, tenant: tenant, workspace: workspace}
	for _, opt := range opts {
		opt(b)
	}
	return &Store{b: b}, nil
}

// newRevisionID mints a revision id.
//
// Deliberately random rather than a hash of the content: an amend REPLACES a
// revision's content in place (see save), so a content-derived id would either
// have to change — invalidating the id the editor is holding and any pointer at
// it — or lie about what it addresses. Forty hex characters because that is
// what a revision id has always looked like here, and callers slice it for
// display.
func newRevisionID() (string, error) {
	var b [20]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("mint revision id: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

func (p *pgBackend) ctx() context.Context { return context.Background() }

// headRevision reads a flow's current revision id, "" when the flow has none.
func (p *pgBackend) headRevision(ctx context.Context, q rowQuerier, graphID string) (string, error) {
	var rev string
	err := q.QueryRow(ctx,
		`SELECT revision FROM flow_heads WHERE tenant=$1 AND workspace=$2 AND graph_id=$3`,
		p.tenant, p.workspace, graphID).Scan(&rev)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return rev, err
}

// rowQuerier is the sliver of *pgxpool.Pool and pgx.Tx this file shares, so a
// helper can run inside or outside a transaction.
type rowQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func (p *pgBackend) save(graph core.Graph, author string, coalesce bool) (string, error) {
	if err := core.ValidGraphID(graph.ID); err != nil {
		return "", err
	}
	content, err := json.Marshal(graph)
	if err != nil {
		return "", fmt.Errorf("marshal graph: %w", err)
	}
	ctx := p.ctx()
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	head, err := p.headRevision(ctx, tx, graph.ID)
	if err != nil {
		return "", err
	}

	var (
		headContent []byte
		headAuthor  string
		headMessage string
		headParent  string
		headAt      time.Time
	)
	if head != "" {
		err = tx.QueryRow(ctx,
			`SELECT content, author, message, parent, created_at FROM flow_revisions
			  WHERE tenant=$1 AND workspace=$2 AND graph_id=$3 AND revision=$4`,
			p.tenant, p.workspace, graph.ID, head).
			Scan(&headContent, &headAuthor, &headMessage, &headParent, &headAt)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return "", err
		}
	}

	// Unchanged content is a no-op, matching what git did with an empty commit:
	// re-saving identical content (the AI chat's "apply" after the agent
	// already saved through MCP) surfaces the existing revision rather than
	// stacking a duplicate.
	if head != "" && headContent != nil && sameJSON(headContent, content) {
		return head, tx.Commit(ctx)
	}

	// Amend a recent autosave by the same author, so an editing burst stays one
	// entry in the history.
	amend := coalesce && head != "" && headAuthor == author &&
		strings.HasPrefix(headMessage, "autosave:") &&
		time.Since(headAt) <= autosaveCoalesceWindow

	if amend {
		// The burst netted back to the pre-autosave content — add a step then
		// delete it, drag a wire then undo it. Drop the autosave entirely and
		// move the flow back to its parent, so the revert is what persists.
		// Keeping it would silently restore the change the user undid.
		if headParent != "" {
			var parentContent []byte
			if err := tx.QueryRow(ctx,
				`SELECT content FROM flow_revisions
				  WHERE tenant=$1 AND workspace=$2 AND graph_id=$3 AND revision=$4`,
				p.tenant, p.workspace, graph.ID, headParent).Scan(&parentContent); err == nil &&
				parentContent != nil && sameJSON(parentContent, content) {
				if err := p.dropRevision(ctx, tx, graph.ID, head, headParent); err != nil {
					return "", err
				}
				return headParent, tx.Commit(ctx)
			}
		}
		// Ordinary amend: replace the autosave's content in place. The revision
		// id is stable across this, which is why ids are not content-derived.
		if _, err := tx.Exec(ctx,
			`UPDATE flow_revisions SET content=$5, message=$6, created_at=now()
			  WHERE tenant=$1 AND workspace=$2 AND graph_id=$3 AND revision=$4`,
			p.tenant, p.workspace, graph.ID, head, content,
			autosaveMessage(graph.ID, author)); err != nil {
			return "", err
		}
		return head, tx.Commit(ctx)
	}

	rev, err := newRevisionID()
	if err != nil {
		return "", err
	}
	msg := explicitMessage(graph.ID, author)
	if coalesce {
		msg = autosaveMessage(graph.ID, author)
	}
	if err := p.insertRevision(ctx, tx, graph.ID, rev, head, author, msg, content); err != nil {
		return "", err
	}
	return rev, tx.Commit(ctx)
}

func (p *pgBackend) insertRevision(ctx context.Context, tx pgx.Tx, graphID, rev, parent, author, message string, content []byte) error {
	if _, err := tx.Exec(ctx,
		`INSERT INTO flow_revisions (tenant, workspace, graph_id, revision, parent, author, message, content)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		p.tenant, p.workspace, graphID, rev, parent, author, message, content); err != nil {
		return err
	}
	_, err := tx.Exec(ctx,
		`INSERT INTO flow_heads (tenant, workspace, graph_id, revision) VALUES ($1,$2,$3,$4)
		 ON CONFLICT (tenant, workspace, graph_id) DO UPDATE SET revision=EXCLUDED.revision, updated_at=now()`,
		p.tenant, p.workspace, graphID, rev)
	return err
}

// dropRevision removes a revision and moves the flow back to parent. Only used
// for the discarded-autosave case above, where nothing can be pointing at it
// yet; env pointers and labels naming it are cleared for safety.
func (p *pgBackend) dropRevision(ctx context.Context, tx pgx.Tx, graphID, rev, parent string) error {
	if _, err := tx.Exec(ctx,
		`DELETE FROM flow_envs WHERE tenant=$1 AND workspace=$2 AND graph_id=$3 AND revision=$4`,
		p.tenant, p.workspace, graphID, rev); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		`DELETE FROM flow_revisions WHERE tenant=$1 AND workspace=$2 AND graph_id=$3 AND revision=$4`,
		p.tenant, p.workspace, graphID, rev); err != nil {
		return err
	}
	_, err := tx.Exec(ctx,
		`UPDATE flow_heads SET revision=$4, updated_at=now()
		  WHERE tenant=$1 AND workspace=$2 AND graph_id=$3`,
		p.tenant, p.workspace, graphID, parent)
	return err
}

func (p *pgBackend) delete(graphID, author string) (string, error) {
	if graphID == "" {
		return "", errors.New("graphID required")
	}
	ctx := p.ctx()
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	head, err := p.headRevision(ctx, tx, graphID)
	if err != nil {
		return "", err
	}
	if head == "" {
		return "", tx.Commit(ctx) // never existed
	}
	var content []byte
	if err := tx.QueryRow(ctx,
		`SELECT content FROM flow_revisions WHERE tenant=$1 AND workspace=$2 AND graph_id=$3 AND revision=$4`,
		p.tenant, p.workspace, graphID, head).Scan(&content); err != nil {
		return "", err
	}
	if content == nil {
		// Already deleted. Same outcome as never having existed, which is what
		// lets the caller answer "deleted or already gone" identically.
		return "", tx.Commit(ctx)
	}
	rev, err := newRevisionID()
	if err != nil {
		return "", err
	}
	// A tombstone, not a purge: the flow's history is where a mistaken delete
	// is recovered from. Its environment pointers go, so it stops firing.
	if err := p.insertRevision(ctx, tx, graphID, rev, head, author,
		fmt.Sprintf("graph: delete %s [user:%s]", graphID, author), nil); err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx,
		`DELETE FROM flow_envs WHERE tenant=$1 AND workspace=$2 AND graph_id=$3`,
		p.tenant, p.workspace, graphID); err != nil {
		return "", err
	}
	return rev, tx.Commit(ctx)
}

func (p *pgBackend) load(graphID string) (core.Graph, error) {
	return p.loadAt("HEAD", graphID)
}

func (p *pgBackend) loadAt(ref, graphID string) (core.Graph, error) {
	ctx := p.ctx()
	rev, err := p.resolveGraph(graphID, ref)
	if err != nil {
		return core.Graph{}, err
	}
	if rev == "" {
		return core.Graph{}, fmt.Errorf("graph %q: %w", graphID, ErrGraphNotFound)
	}
	var content []byte
	err = p.pool.QueryRow(ctx,
		`SELECT content FROM flow_revisions WHERE tenant=$1 AND workspace=$2 AND graph_id=$3 AND revision=$4`,
		p.tenant, p.workspace, graphID, rev).Scan(&content)
	if errors.Is(err, pgx.ErrNoRows) {
		return core.Graph{}, fmt.Errorf("graph %q: %w", graphID, ErrGraphNotFound)
	}
	if err != nil {
		return core.Graph{}, err
	}
	if content == nil {
		return core.Graph{}, fmt.Errorf("graph %q: %w", graphID, ErrGraphNotFound)
	}
	var g core.Graph
	if err := json.Unmarshal(content, &g); err != nil {
		return core.Graph{}, fmt.Errorf("decode graph %q: %w", graphID, err)
	}
	return g, nil
}

func (p *pgBackend) listGraphs() ([]string, error) {
	rows, err := p.pool.Query(p.ctx(),
		`SELECT h.graph_id FROM flow_heads h
		   JOIN flow_revisions r
		     ON r.tenant=h.tenant AND r.workspace=h.workspace
		    AND r.graph_id=h.graph_id AND r.revision=h.revision
		  WHERE h.tenant=$1 AND h.workspace=$2 AND r.content IS NOT NULL
		  ORDER BY h.graph_id`,
		p.tenant, p.workspace)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func (p *pgBackend) history(graphID string, limit int) ([]Revision, error) {
	if graphID == "" {
		return nil, errors.New("graphID required")
	}
	if limit <= 0 {
		limit = 100
	}
	rows, err := p.pool.Query(p.ctx(),
		`SELECT revision, author, message, label, created_at FROM flow_revisions
		  WHERE tenant=$1 AND workspace=$2 AND graph_id=$3
		  ORDER BY seq DESC LIMIT $4`,
		p.tenant, p.workspace, graphID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	revs := make([]Revision, 0, limit)
	for rows.Next() {
		var r Revision
		if err := rows.Scan(&r.Commit, &r.Author, &r.Message, &r.Label, &r.When); err != nil {
			return nil, err
		}
		r.Autosave = strings.HasPrefix(r.Message, "autosave:")
		revs = append(revs, r)
	}
	return revs, rows.Err()
}

// head is the workspace's high-water revision sequence — a token that changes
// on any write to any flow in it. Opaque by contract; callers only compare it.
func (p *pgBackend) head() (string, error) {
	var seq *int64
	if err := p.pool.QueryRow(p.ctx(),
		`SELECT max(seq) FROM flow_revisions WHERE tenant=$1 AND workspace=$2`,
		p.tenant, p.workspace).Scan(&seq); err != nil {
		return "", err
	}
	if seq == nil {
		return "", nil
	}
	return fmt.Sprintf("ws-%d", *seq), nil
}

func (p *pgBackend) resolve(ref string) (string, error) {
	if ref == "" || ref == "HEAD" {
		return p.head()
	}
	return ref, nil
}

// resolveGraph turns a ref into one flow's revision id. Unlike git, where every
// flow shared a commit history and "HEAD" named a revision of all of them, a
// revision here belongs to exactly one flow — so resolving needs to know which.
func (p *pgBackend) resolveGraph(graphID, ref string) (string, error) {
	ctx := p.ctx()
	switch ref {
	case "", "HEAD":
		return p.headRevision(ctx, p.pool, graphID)
	}
	// An environment name (e.g. "published") resolves through its pointer;
	// anything else is taken as a revision id.
	var rev string
	err := p.pool.QueryRow(ctx,
		`SELECT revision FROM flow_envs WHERE tenant=$1 AND workspace=$2 AND graph_id=$3 AND env=$4`,
		p.tenant, p.workspace, graphID, ref).Scan(&rev)
	if err == nil {
		return rev, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	return ref, nil
}

func (p *pgBackend) envCommit(graphID, env string) (string, error) {
	var rev string
	err := p.pool.QueryRow(p.ctx(),
		`SELECT revision FROM flow_envs WHERE tenant=$1 AND workspace=$2 AND graph_id=$3 AND env=$4`,
		p.tenant, p.workspace, graphID, env).Scan(&rev)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return rev, err
}

func (p *pgBackend) setEnv(graphID, env, commit string) error {
	if env == "" {
		return errors.New("env required")
	}
	rev, err := p.resolveGraph(graphID, commit)
	if err != nil {
		return err
	}
	if rev == "" {
		return fmt.Errorf("could not resolve %q", commit)
	}
	_, err = p.pool.Exec(p.ctx(),
		`INSERT INTO flow_envs (tenant, workspace, graph_id, env, revision) VALUES ($1,$2,$3,$4,$5)
		 ON CONFLICT (tenant, workspace, graph_id, env) DO UPDATE SET revision=EXCLUDED.revision`,
		p.tenant, p.workspace, graphID, env, rev)
	return err
}

func (p *pgBackend) clearEnv(graphID, env string) error {
	if env == "" {
		return errors.New("env required")
	}
	_, err := p.pool.Exec(p.ctx(),
		`DELETE FROM flow_envs WHERE tenant=$1 AND workspace=$2 AND graph_id=$3 AND env=$4`,
		p.tenant, p.workspace, graphID, env)
	return err
}

func (p *pgBackend) setLabel(graphID, commit, label string) error {
	if graphID == "" {
		return errors.New("graphID required")
	}
	rev, err := p.resolveGraph(graphID, commit)
	if err != nil {
		return err
	}
	if rev == "" {
		return fmt.Errorf("could not resolve %q", commit)
	}
	_, err = p.pool.Exec(p.ctx(),
		`UPDATE flow_revisions SET label=$5
		  WHERE tenant=$1 AND workspace=$2 AND graph_id=$3 AND revision=$4`,
		p.tenant, p.workspace, graphID, rev, strings.TrimSpace(label))
	return err
}

func (p *pgBackend) label(graphID, commit string) (string, error) {
	if graphID == "" {
		return "", errors.New("graphID required")
	}
	rev, err := p.resolveGraph(graphID, commit)
	if err != nil {
		return "", err
	}
	var label string
	err = p.pool.QueryRow(p.ctx(),
		`SELECT label FROM flow_revisions WHERE tenant=$1 AND workspace=$2 AND graph_id=$3 AND revision=$4`,
		p.tenant, p.workspace, graphID, rev).Scan(&label)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return label, err
}

// refs has no meaning without a git repository. Empty rather than an error:
// the only callers are ones doing their own listing, and "no branches" is the
// honest answer.
func (p *pgBackend) refs(string) ([]string, error) { return nil, nil }

// mirror: a repository is synthesized from the revision log, when the caller
// has given synthesis somewhere to keep it. Without that, Store.Push turns the
// false into ErrMirrorUnsupported.
func (p *pgBackend) mirror() (gitMirrorer, bool) {
	if p.mirrorDir == "" {
		return nil, false
	}
	return &pgMirror{pg: p, dir: p.mirrorDir}, true
}

// sameJSON compares two marshalled graphs for semantic equality, so a re-save
// of identical content is recognised even if key order differs.
func sameJSON(a, b []byte) bool {
	var x, y any
	if err := json.Unmarshal(a, &x); err != nil {
		return false
	}
	if err := json.Unmarshal(b, &y); err != nil {
		return false
	}
	xb, err1 := json.Marshal(x)
	yb, err2 := json.Marshal(y)
	return err1 == nil && err2 == nil && string(xb) == string(yb)
}
