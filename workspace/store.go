// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// A tenant's flows with full revision history, backed by a git repository the
// customer owns and can read with ordinary git tooling.
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

// The list projection: enough to render a row without decoding every param.
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

// Only the revisions that touched THIS flow's own path.
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

// One tree walk with params left undecoded — the flow list is the most repeated
// read in the product.
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

func (s *Store) PublishedCommit(id string) (string, error) {
	return s.b.envCommit(id, PublishedEnv)
}

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

func (s *Store) Push(ctx context.Context, remoteURL string, auth transport.AuthMethod) (PushResult, error) {
	m, ok := s.b.mirror()
	if !ok {
		return PushResult{}, ErrMirrorUnsupported
	}
	return m.push(ctx, remoteURL, auth, false)
}

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
