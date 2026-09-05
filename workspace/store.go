// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package workspace stores a tenant's flows, with full revision history.
//
// Every save records a revision carrying the author's identity, and an
// environment (published, staging) is a pointer at a frozen revision. Two
// backends implement that:
//
//   - git, one repository per workspace on local disk. History, diff and audit
//     come for free from a format the customer already owns and can clone.
//   - Postgres, a revision log shared by every replica. The same semantics
//     without a working tree, so flow authoring is safe on more than one dzd.
//
// Store is the façade over both. Callers hold a *Store and never see which
// backend answers.
package workspace

import (
	"context"
	"errors"
	"time"

	"github.com/go-git/go-git/v5/plumbing/transport"

	"github.com/dazyflow/dazyflow/core"
)

// backend is the storage half of a workspace: revisions of graphs, the
// environment pointers into them, and the labels attached to them.
//
// Locking is the backend's own business. The git backend serializes per
// directory (two Stores over one working tree share a single `.git/index`);
// the Postgres backend leaves it to the database.
type backend interface {
	save(graph core.Graph, author string, coalesce bool) (string, error)
	delete(graphID, author string) (string, error)

	load(graphID string) (core.Graph, error)
	loadAt(ref, graphID string) (core.Graph, error)
	listGraphs() ([]string, error)
	// listAtHead reads every flow at head, each with its pointer for env, in
	// one pass. The flow list is what needs it: done a flow at a time it is
	// three round trips per flow on Postgres, and on git it re-resolves the
	// same HEAD, commit and tree once per flow.
	listAtHead(env string) ([]FlowAtHead, error)
	history(graphID string, limit int) ([]Revision, error)

	// head is a token that changes whenever anything in the workspace does.
	// Callers memoize whole-workspace views against it; its content is opaque.
	head() (string, error)
	resolve(ref string) (string, error)
	// resolveGraph is resolve scoped to one flow. Git needed no such thing —
	// every flow shared one commit history, so a revision named all of them —
	// but a Postgres revision belongs to exactly one flow.
	resolveGraph(graphID, ref string) (string, error)

	envCommit(graphID, env string) (string, error)
	setEnv(graphID, env, commit string) error
	clearEnv(graphID, env string) error

	setLabel(graphID, commit, label string) error
	label(graphID, commit string) (string, error)

	// refs surfaces the underlying branch/tag names. Empty on a backend with
	// no such concept; the API exists for callers doing their own git listing.
	refs(prefix string) ([]string, error)

	// mirror returns the git-mirroring half of this backend, and false when
	// the backend cannot mirror. See Store.Push.
	mirror() (gitMirrorer, bool)
}

// gitMirrorer is the push half of a backend that has a real git repository
// behind it.
type gitMirrorer interface {
	push(ctx context.Context, remoteURL string, auth transport.AuthMethod, allowUnrelated bool) (PushResult, error)
}

// FlowAtHead is one flow as the flow list needs it: its current content and
// whether — and at which revision — it is published.
type FlowAtHead struct {
	ID        string
	Graph     core.Graph
	EnvCommit string
}

// Store is a workspace: the flows of one (tenant, workspace) pair and their
// history. Safe for concurrent use.
type Store struct {
	b backend
}

// OpenFS opens (or initializes) a git-backed Store rooted at dir on the local
// filesystem. If dir is empty an in-memory Store is created — useful for tests
// and ephemeral workspaces.
func OpenFS(dir string) (*Store, error) {
	b, err := openBackend(dir)
	if err != nil {
		return nil, err
	}
	return &Store{b: b}, nil
}

// Save records a new revision of graph and returns its id.
func (s *Store) Save(graph core.Graph, author string) (string, error) {
	return s.b.save(graph, author, false)
}

// SaveCoalescing is the editor-autosave variant: a save that immediately
// follows a recent autosave of the same graph by the same author replaces it
// instead of stacking a new revision, so a continuous editing session stays one
// entry in the history. See autosaveCoalesceWindow.
func (s *Store) SaveCoalescing(graph core.Graph, author string) (string, error) {
	return s.b.save(graph, author, true)
}

// Delete removes a flow and records the removal. Returns the resulting revision
// id. Idempotent in the "already gone" sense: a missing flow returns
// (commit="", nil) so the caller can surface "deleted or already gone" as one
// outcome.
func (s *Store) Delete(graphID, author string) (string, error) {
	return s.b.delete(graphID, author)
}

// Load reads a flow at the workspace's current revision. Returns
// ErrGraphNotFound when the flow does not exist; any other error means the
// store could not be read and says nothing about whether the flow exists.
func (s *Store) Load(id string) (core.Graph, error) { return s.b.load(id) }

// LoadAt reads a flow as of ref (an environment pointer, "HEAD", or a raw
// revision id).
func (s *Store) LoadAt(ref, id string) (core.Graph, error) { return s.b.loadAt(ref, id) }

// ListGraphs returns the IDs of every flow currently in the workspace.
func (s *Store) ListGraphs() ([]string, error) { return s.b.listGraphs() }

// History returns the revisions that touched a flow, newest first, capped at
// limit (limit <= 0 applies a default). Restoring a revision is an ordinary
// Save of that revision's content — a new entry at the top — so history is
// never rewritten.
func (s *Store) History(id string, limit int) ([]Revision, error) { return s.b.history(id, limit) }

// Head is a token that changes whenever anything in the workspace does. A cheap
// cache key for callers that memoize views derived from the whole flow set
// (e.g. the drop-suggestion adjacency): when it is unchanged, nothing those
// views depend on has changed either. Its content is opaque.
func (s *Store) Head() (string, error) { return s.b.head() }

// Resolve turns a ref ("HEAD", an environment pointer, or a raw revision id)
// into a revision id. Used by callers that need to record the exact revision a
// ref pointed at — e.g. which one a label was attached to.
func (s *Store) Resolve(ref string) (string, error) { return s.b.resolve(ref) }

// ResolveFor is Resolve scoped to one flow: it returns the revision of graphID
// that ref names. Prefer it wherever the flow is known — it is the only form
// that means anything on a backend where a revision belongs to one flow.
func (s *Store) ResolveFor(graphID, ref string) (string, error) {
	return s.b.resolveGraph(graphID, ref)
}

// PromoteToEnvironment points env (e.g. "published") at commit. Environment
// pointers are intentionally movable, so this replaces any previous target.
func (s *Store) PromoteToEnvironment(graphID, env, commit string) error {
	return s.b.setEnv(graphID, env, commit)
}

// ClearEnvironment removes the environment pointer, reverting the flow to
// having no revision pinned for that env. Unpublishing a flow clears
// PublishedEnv, which takes it fully offline: the scheduler treats it as not
// live (PublishedCommit returns "") and the webhook, hosted-form and
// provider-event endpoints reject it (LoadPublished returns ErrNotPublished).
// Idempotent.
func (s *Store) ClearEnvironment(graphID, env string) error {
	return s.b.clearEnv(graphID, env)
}

// ListAtHead reads every flow in the workspace at head, each with its pointer
// for env (use PublishedEnv for the published revision). One pass over the
// workspace instead of a load and an env lookup per flow.
func (s *Store) ListAtHead(env string) ([]FlowAtHead, error) { return s.b.listAtHead(env) }

// PublishedCommit returns the revision a flow is published at, or "" when it
// has never been published.
func (s *Store) PublishedCommit(id string) (string, error) {
	return s.b.envCommit(id, PublishedEnv)
}

// LoadPublished reads a flow at its published revision. This is the version
// automatic triggers run — a published flow fires its last published revision,
// never whatever half-finished draft is current, which is what makes the
// editor's autosave safe on a live flow. The manual-run, sample and
// test-trigger paths deliberately keep using Load so an author can try edits
// before publishing them.
//
// An unpublished flow returns ErrNotPublished and fires NOTHING.
func (s *Store) LoadPublished(id string) (core.Graph, error) {
	commit, err := s.b.envCommit(id, PublishedEnv)
	if err != nil {
		return core.Graph{}, err
	}
	if commit == "" {
		return core.Graph{}, ErrNotPublished
	}
	return s.b.loadAt(commit, id)
}

// SetRevisionLabel attaches a human label to a specific revision. Labels are
// keyed by revision, not by environment: republishing an older one (rollback)
// brings back the label it was given, and the version-history panel shows each
// revision's name. Re-labeling replaces the previous label; an empty label
// clears it.
func (s *Store) SetRevisionLabel(graphID, commit, label string) error {
	return s.b.setLabel(graphID, commit, label)
}

// RevisionLabel returns the human label attached to graphID@commit, or "" when
// the revision is unlabeled.
func (s *Store) RevisionLabel(graphID, commit string) (string, error) {
	return s.b.label(graphID, commit)
}

// Branches and Tags surface the underlying refs for callers that want to do
// their own listing/diff. Empty on a backend with no such concept.
func (s *Store) Branches() ([]string, error) { return s.b.refs("refs/heads/") }
func (s *Store) Tags() ([]string, error)     { return s.b.refs("refs/tags/") }

// ErrMirrorUnsupported is returned by Push on a backend with no git repository
// behind it. The mirror is an export of the customer's own history, so a
// deployment that wants one keeps its flows in git.
var ErrMirrorUnsupported = errors.New("this workspace is not git-backed, so it cannot be mirrored to a git remote")

// Push mirrors the workspace to a git remote.
//
// Callers must treat a returned error as "the mirror is stale", never as a
// failure of whatever triggered the push — a save or publish has already
// succeeded by the time we get here.
func (s *Store) Push(ctx context.Context, remoteURL string, auth transport.AuthMethod) (PushResult, error) {
	m, ok := s.b.mirror()
	if !ok {
		return PushResult{}, ErrMirrorUnsupported
	}
	return m.push(ctx, remoteURL, auth, false)
}

// PushOverwritingUnrelated is Push with the shared-history check disabled: it
// will overwrite a remote holding an unrelated repository.
//
// Only ever call this for an action a human just confirmed. The automatic
// mirror path must use Push, so that a misconfigured or repurposed remote fails
// loudly instead of being erased by a background job nobody was watching.
func (s *Store) PushOverwritingUnrelated(ctx context.Context, remoteURL string, auth transport.AuthMethod) (PushResult, error) {
	m, ok := s.b.mirror()
	if !ok {
		return PushResult{}, ErrMirrorUnsupported
	}
	return m.push(ctx, remoteURL, auth, true)
}

// Revision is one entry in a flow's history.
type Revision struct {
	Commit   string    `json:"commit"`
	Author   string    `json:"author"`
	Message  string    `json:"message"`
	When     time.Time `json:"when"`
	Autosave bool      `json:"autosave"`
	// Label is the optional human name a publish gave this revision (e.g.
	// "Black Friday config"), empty when unlabeled. Keyed to the revision, so
	// rollback to an older one brings its label back.
	Label string `json:"label,omitempty"`
}

// git returns the git backend behind this Store, for the package's own
// white-box tests. Nil when the Store is not git-backed.
func (s *Store) git() *gitBackend {
	b, _ := s.b.(*gitBackend)
	return b
}
