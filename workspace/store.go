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
	"encoding/json"
	"errors"
	"time"

	"github.com/go-git/go-git/v5/plumbing/transport"

	"github.com/dazyflow/dazyflow/core"
)

type backend interface {
	save(graph core.Graph, author string, coalesce bool) (string, error)
	delete(graphID, author string) (string, error)

	load(graphID string) (core.Graph, error)
	loadAt(ref, graphID string) (core.Graph, error)
	listGraphs() ([]string, error)
	listAtHead(env string, headersOnly bool) ([]FlowAtHead, error)
	history(graphID string, limit int) ([]Revision, error)

	head() (string, error)
	resolve(ref string) (string, error)
	resolveGraph(graphID, ref string) (string, error)

	envCommit(graphID, env string) (string, error)
	setEnv(graphID, env, commit string) error
	clearEnv(graphID, env string) error

	setLabel(graphID, commit, label string) error
	label(graphID, commit string) (string, error)

	refs(prefix string) ([]string, error)

	// mirror returns the git-mirroring half of this backend, and false when
	// the backend cannot mirror. See Store.Push.
	mirror() (gitMirrorer, bool)
}

type gitMirrorer interface {
	push(ctx context.Context, remoteURL string, auth transport.AuthMethod, allowUnrelated bool) (PushResult, error)
}

type FlowAtHead struct {
	ID        string
	Graph     core.Graph
	EnvCommit string
}

// FlowHeader is what ListHeadersAtHead returns: the same three fields as
// FlowAtHead, but its Graph came through core.UnmarshalGraphHeader, so the
// params of every non-trigger step are nil whether or not the stored flow has
// any.
//
// It is a distinct type rather than a flag on FlowAtHead so that the elision
// is visible where the value is used. These graphs reach AuthorizeGraphView,
// whose Visibility and Owner decide who may see a flow, and a value that is
// "a flow, except sometimes missing part of itself" should not be able to
// stand in silently for one that is whole.
type FlowHeader struct {
	ID        string
	Graph     core.Graph
	EnvCommit string
}

type Store struct {
	b backend
}

func OpenFS(dir string) (*Store, error) {
	b, err := openBackend(dir)
	if err != nil {
		return nil, err
	}
	return &Store{b: b}, nil
}

func (s *Store) Save(graph core.Graph, author string) (string, error) {
	return s.b.save(graph, author, false)
}

func (s *Store) SaveCoalescing(graph core.Graph, author string) (string, error) {
	return s.b.save(graph, author, true)
}

func (s *Store) Delete(graphID, author string) (string, error) {
	return s.b.delete(graphID, author)
}

func (s *Store) Load(id string) (core.Graph, error) { return s.b.load(id) }

func (s *Store) LoadAt(ref, id string) (core.Graph, error) { return s.b.loadAt(ref, id) }

func (s *Store) ListGraphs() ([]string, error) { return s.b.listGraphs() }

// History returns the revisions that touched a flow, newest first, capped at
// limit (limit <= 0 applies a default). Restoring a revision is an ordinary
// Save of that revision's content — a new entry at the top — so history is
// never rewritten.
func (s *Store) History(id string, limit int) ([]Revision, error) { return s.b.history(id, limit) }

func (s *Store) Head() (string, error) { return s.b.head() }

func (s *Store) Resolve(ref string) (string, error) { return s.b.resolve(ref) }

func (s *Store) ResolveFor(graphID, ref string) (string, error) {
	return s.b.resolveGraph(graphID, ref)
}

func (s *Store) PromoteToEnvironment(graphID, env, commit string) error {
	return s.b.setEnv(graphID, env, commit)
}

func (s *Store) ClearEnvironment(graphID, env string) error {
	return s.b.clearEnv(graphID, env)
}

func (s *Store) ListAtHead(env string) ([]FlowAtHead, error) { return s.b.listAtHead(env, false) }

// ListHeadersAtHead is ListAtHead for the LIST views: the same flows with the
// same env pointers, but each decoded by core.UnmarshalGraphHeader, so the
// params of ordinary steps are never built.
//
// It is the read the flow list, the schedules list and the drop-suggestion
// miner want. Decoding those params is ~78% of a full list read, and on the
// git backend the read runs under the mutex that serializes the entire
// workspace — so it is not one caller's latency but the workspace's read
// throughput. See core.UnmarshalGraphHeader for what is kept and why.
//
// USE THE FULL ListAtHead to run, edit or validate a flow: an elided step has
// Params == nil, which is indistinguishable from a step that has none.
func (s *Store) ListHeadersAtHead(env string) ([]FlowHeader, error) {
	flows, err := s.b.listAtHead(env, true)
	if err != nil {
		return nil, err
	}
	out := make([]FlowHeader, len(flows))
	for i, f := range flows {
		out[i] = FlowHeader(f)
	}
	return out, nil
}

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

func (s *Store) SetRevisionLabel(graphID, commit, label string) error {
	return s.b.setLabel(graphID, commit, label)
}

func (s *Store) RevisionLabel(graphID, commit string) (string, error) {
	return s.b.label(graphID, commit)
}

func (s *Store) Branches() ([]string, error) { return s.b.refs("refs/heads/") }
func (s *Store) Tags() ([]string, error)     { return s.b.refs("refs/tags/") }

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

type Revision struct {
	Commit   string    `json:"commit"`
	Author   string    `json:"author"`
	Message  string    `json:"message"`
	When     time.Time `json:"when"`
	Autosave bool      `json:"autosave"`
	Label    string    `json:"label,omitempty"`
}

func (s *Store) git() *gitBackend {
	b, _ := s.b.(*gitBackend)
	return b
}

// decodeFlow decodes one stored flow document, either whole or as a header.
// Both backends call it so the two reads cannot decode differently — the
// conformance suite asserts a header list matches the full list with ordinary
// params dropped, and that only holds if there is one definition of each.
func decodeFlow(data []byte, headersOnly bool) (core.Graph, error) {
	if headersOnly {
		return core.UnmarshalGraphHeader(data)
	}
	var g core.Graph
	if err := json.Unmarshal(data, &g); err != nil {
		return core.Graph{}, err
	}
	return g, nil
}
