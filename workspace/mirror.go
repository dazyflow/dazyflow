// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package workspace

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport"
)

// Pushes the repository that already holds every flow, so nothing is re-serialized.

// The remote holds a repository sharing no history with this one, so pushing
// would overwrite somebody else's work. Refused unless explicitly overridden.
var ErrUnrelatedRemote = errors.New("the remote holds a repository that shares no history with this workspace")

const mirrorRemoteName = "dazyflow-mirror"

// Tags matter as much as branches: publish state lives in a tag.
var mirroredPrefixes = []string{"refs/heads/", "refs/tags/"}

// Changed is false when the remote was already up to date.
type PushResult struct {
	Head    string
	Changed bool
	Pushed  int
	Deleted int
}

// Refuses a remote with unrelated history; see PushOverwritingUnrelated.
func (s *gitBackend) Push(ctx context.Context, remoteURL string, auth transport.AuthMethod) (PushResult, error) {
	return s.push(ctx, remoteURL, auth, false)
}

// Push with the shared-history check disabled: it WILL overwrite the remote.
func (s *gitBackend) PushOverwritingUnrelated(ctx context.Context, remoteURL string, auth transport.AuthMethod) (PushResult, error) {
	return s.push(ctx, remoteURL, auth, true)
}

func (s *gitBackend) push(ctx context.Context, remoteURL string, auth transport.AuthMethod, allowUnrelated bool) (PushResult, error) {
	if strings.TrimSpace(remoteURL) == "" {
		return PushResult{}, errors.New("remote URL required")
	}
	// Under the store lock: go-git's repository object is not safe for concurrent use.
	s.mu.Lock()
	defer s.mu.Unlock()

	var res PushResult
	if head, err := s.repo.Head(); err == nil {
		res.Head = head.Hash().String()
	}

	remote := git.NewRemote(s.repo.Storer, &config.RemoteConfig{
		Name: mirrorRemoteName,
		URLs: []string{remoteURL},
	})

	local, err := s.mirroredRefs()
	if err != nil {
		return res, fmt.Errorf("list local refs: %w", err)
	}
	remoteHas, remoteHashes, err := listRemoteMirroredRefs(ctx, remote, auth)
	if err != nil {
		return res, err
	}

	// BEFORE anything is sent: every refspec below is a force-push.
	if !allowUnrelated && len(remoteHashes) > 0 && !s.sharesHistory(remoteHashes) {
		return res, fmt.Errorf("%w (%d ref(s) on the remote, none of them known here) — check the URL, or overwrite it deliberately if this really is the repository you want to replace",
			ErrUnrelatedRemote, len(remoteHashes))
	}

	// Explicit per-ref refspecs rather than a wildcard, so a ref deleted locally is
	// deleted on the remote instead of lingering there for ever.
	specs := make([]config.RefSpec, 0, len(local)+len(remoteHas))
	for _, name := range local {
		specs = append(specs, config.RefSpec("+"+name+":"+name))
	}
	deleted := 0
	for _, name := range remoteHas {
		if _, ok := local.has(name); ok {
			continue
		}
		// A leading colon with an empty source deletes the remote ref.
		specs = append(specs, config.RefSpec(":"+name))
		deleted++
	}
	if len(specs) == 0 {
		return res, nil
	}

	err = remote.PushContext(ctx, &git.PushOptions{
		RemoteName: mirrorRemoteName,
		RemoteURL:  remoteURL,
		RefSpecs:   specs,
		Auth:       auth,
		Force:      false,
		Prune:      false,
	})
	switch {
	case err == nil:
		res.Changed = true
		res.Pushed = len(local)
		res.Deleted = deleted
		return res, nil
	case errors.Is(err, git.NoErrAlreadyUpToDate):
		return res, nil
	default:
		return res, fmt.Errorf("push to mirror: %w", err)
	}
}

type refSet []string

func (r refSet) has(name string) (int, bool) {
	for i, n := range r {
		if n == name {
			return i, true
		}
	}
	return 0, false
}

func (s *gitBackend) mirroredRefs() (refSet, error) {
	iter, err := s.repo.References()
	if err != nil {
		return nil, err
	}
	var out refSet
	err = iter.ForEach(func(ref *plumbing.Reference) error {
		if ref.Type() != plumbing.HashReference {
			return nil
		}
		if mirrored(ref.Name().String()) {
			out = append(out, ref.Name().String())
		}
		return nil
	})
	sort.Strings(out)
	return out, err
}

func listRemoteMirroredRefs(ctx context.Context, remote *git.Remote, auth transport.AuthMethod) (refSet, []plumbing.Hash, error) {
	refs, err := remote.ListContext(ctx, &git.ListOptions{Auth: auth})
	if errors.Is(err, transport.ErrEmptyRemoteRepository) {
		// A brand-new empty remote has nothing to overwrite.
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("list mirror refs: %w", err)
	}
	var out refSet
	var hashes []plumbing.Hash
	for _, ref := range refs {
		if ref.Type() != plumbing.HashReference {
			continue
		}
		if mirrored(ref.Name().String()) {
			out = append(out, ref.Name().String())
			hashes = append(hashes, ref.Hash())
		}
	}
	sort.Strings(out)
	return out, hashes, nil
}

// Any remote ref target present in this repository proves shared history.
func (s *gitBackend) sharesHistory(remoteHashes []plumbing.Hash) bool {
	for _, h := range remoteHashes {
		if h.IsZero() {
			continue
		}
		if s.repo.Storer.HasEncodedObject(h) == nil {
			return true
		}
	}
	return false
}

func mirrored(name string) bool {
	for _, p := range mirroredPrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}
