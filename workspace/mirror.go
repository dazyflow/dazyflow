// SPDX-FileCopyrightText: 2026 Joachim Klahr
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

// Mirroring pushes this workspace's repository — the one that already holds
// every flow as graphs/<id>.json plus the environment tags — to a remote the
// customer owns. Because the store IS a git repository, "mirror my flows to
// my own git server" is a push, not an export format.
//
// The remote is a REPLICA, not a peer. There is no fetch/merge side: the
// daemon is the single writer, so a mirror push forces every ref and deletes
// the ones that no longer exist locally, and a commit made directly on the
// remote is overwritten by the next push. That contract is what makes the
// feature safe to run unattended — the alternative (reconciling two writers)
// collides with the editor's autosave and the amend/coalesce head, and is a
// different feature entirely.
//
// Forcing is not optional here, which is worth stating because it looks
// aggressive: SaveCoalescing AMENDS the previous autosave commit inside its
// window, so a workspace's history is legitimately rewritten during ordinary
// editing. A non-forced mirror would start rejecting pushes the first time a
// user typed two params half a minute apart.

// ErrUnrelatedRemote is returned when the remote holds a repository that
// shares no history with this workspace — none of its refs names an object we
// have. A forced mirror push would destroy it, and the realistic cause is a
// misconfiguration rather than an intention:
//
//   - a restored deployment whose data volume was lost, mirroring its fresh
//     empty workspace over the very backup it should have been restored FROM;
//   - a mirror URL pointing at the wrong repository (another org's, or an
//     unrelated project);
//   - a remote someone has been committing to directly, whose work the next
//     push would erase.
//
// Refusing turns all three from silent data loss into a message. The
// interactive path can override it — see PushOverwritingUnrelated — so a user
// who genuinely means to repoint a mirror still can, deliberately.
var ErrUnrelatedRemote = errors.New("the remote holds a repository that shares no history with this workspace")

// mirrorRemoteName names the ephemeral remote each push constructs. It is
// never written to the repository's config — see Push.
const mirrorRemoteName = "dazyflow-mirror"

// mirroredPrefixes are the ref namespaces a mirror carries. Tags matter as
// much as branches: PromoteToEnvironment records the published revision as
// refs/tags/graphs/<id>/<env>, so a mirror without tags would hold every
// flow but lose which revision is live.
var mirroredPrefixes = []string{"refs/heads/", "refs/tags/"}

// PushResult reports what one mirror push did. Changed is false for the
// common no-op case (nothing new since the last push), which callers
// surface as a successful mirror rather than an error — go-git signals it
// with NoErrAlreadyUpToDate, an error value that means success.
type PushResult struct {
	// Head is the local HEAD commit at push time — the revision the remote
	// now holds. Empty only for a repo with no commits.
	Head string
	// Changed reports whether the remote actually moved.
	Changed bool
	// Pushed and Deleted count the refs updated and removed on the remote.
	// Surfaced so the UI can say what a push did rather than only that it
	// succeeded.
	Pushed  int
	Deleted int
}

// Push mirrors the repository to remoteURL, authenticating with auth.
//
// The remote is addressed by URL rather than by a named remote in the repo's
// config: the config is the customer's own repository state (it travels to
// the mirror), and writing a remote into it would both leak the mirror
// target into every clone and make the daemon's push destination stateful.
// Constructing an ephemeral remote keeps the target a runtime argument.
//
// Callers must treat a returned error as "the mirror is stale", never as a
// failure of whatever triggered the push — a save or publish has already
// succeeded by the time we get here.
func (s *Store) Push(ctx context.Context, remoteURL string, auth transport.AuthMethod) (PushResult, error) {
	return s.push(ctx, remoteURL, auth, false)
}

// PushOverwritingUnrelated is Push with the shared-history check disabled: it
// will overwrite a remote holding an unrelated repository.
//
// Only ever call this for an action a human just confirmed. The automatic
// mirror path must use Push, so that a misconfigured or repurposed remote
// fails loudly instead of being erased by a background job nobody was
// watching.
func (s *Store) PushOverwritingUnrelated(ctx context.Context, remoteURL string, auth transport.AuthMethod) (PushResult, error) {
	return s.push(ctx, remoteURL, auth, true)
}

func (s *Store) push(ctx context.Context, remoteURL string, auth transport.AuthMethod, allowUnrelated bool) (PushResult, error) {
	if strings.TrimSpace(remoteURL) == "" {
		return PushResult{}, errors.New("remote URL required")
	}
	// The whole push happens under the store lock. go-git's repository and
	// storer are not safe for concurrent use, and the scheduler's rescan
	// reads run concurrently with this — see the Store doc comment. A push is
	// network-bound, so it does block saves to the same workspace for its
	// duration; that is why the caller (the daemon's mirror queue) coalesces
	// and runs it off the request path.
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

	// Shared-history check, BEFORE anything is sent. Every refspec below
	// either force-overwrites or deletes a remote ref, so this is the last
	// point at which a wrong remote can be distinguished from a right one.
	//
	// "Related" is deliberately generous: ONE remote ref naming an object we
	// hold is enough. That keeps the normal cases quiet — an ordinary push is
	// a fast-forward, and an autosave amend leaves the amended-away commit in
	// our object store, so the remote's tip is still an object we have — while
	// a wholly foreign repository, which shares nothing, is caught.
	if !allowUnrelated && len(remoteHashes) > 0 && !s.sharesHistory(remoteHashes) {
		return res, fmt.Errorf("%w (%d ref(s) on the remote, none of them known here) — check the URL, or overwrite it deliberately if this really is the repository you want to replace",
			ErrUnrelatedRemote, len(remoteHashes))
	}

	// Explicit per-ref refspecs rather than a "+refs/heads/*:refs/heads/*"
	// wildcard with PushOptions.Prune. That combination is broken in go-git:
	// Force rewrites each spec to prepend '+', and Prune then calls
	// RefSpec.Reverse(), which moves the '+' onto the destination side
	// ("refs/heads/*:+refs/heads/*"). Its Dst() therefore yields
	// "+refs/heads/master", which matches no real local ref, so prune
	// concludes every remote branch is stale and emits a deletion for it —
	// the remote's current branch included, which the server rejects. Naming
	// each ref sidesteps the reversal entirely and makes the two intents
	// (update these, delete those) separately reviewable.
	specs := make([]config.RefSpec, 0, len(local)+len(remoteHas))
	for _, name := range local {
		specs = append(specs, config.RefSpec("+"+name+":"+name))
	}
	deleted := 0
	for _, name := range remoteHas {
		if _, ok := local.has(name); ok {
			continue
		}
		// Leading colon with an empty source = delete this remote ref. This
		// is what keeps an unpublished flow from being advertised as live on
		// the mirror forever: unpublishing removes the published tag
		// locally (ClearEnvironment), and nothing else would propagate that.
		specs = append(specs, config.RefSpec(":"+name))
		deleted++
	}
	if len(specs) == 0 {
		// Nothing local and nothing to clean up — an empty workspace. Not an
		// error; there is simply nothing to mirror yet.
		return res, nil
	}

	err = remote.PushContext(ctx, &git.PushOptions{
		RemoteName: mirrorRemoteName,
		RemoteURL:  remoteURL,
		RefSpecs:   specs,
		Auth:       auth,
		// Force stays off: every update spec already carries its own '+', so
		// setting it would only re-trigger the rewrite described above.
		Force: false,
		Prune: false,
	})
	switch {
	case err == nil:
		res.Changed = true
		res.Pushed = len(local)
		res.Deleted = deleted
		return res, nil
	case errors.Is(err, git.NoErrAlreadyUpToDate):
		// Success: the remote already matches. Reported as unchanged so the
		// UI can say "up to date" instead of implying a transfer happened.
		return res, nil
	default:
		return res, fmt.Errorf("push to mirror: %w", err)
	}
}

// refSet is the set of ref names being mirrored, kept ordered so the
// generated refspec list (and therefore any log line describing a push) is
// stable across runs.
type refSet []string

func (r refSet) has(name string) (int, bool) {
	for i, n := range r {
		if n == name {
			return i, true
		}
	}
	return 0, false
}

// mirroredRefs lists this repo's branch and tag refs. Caller holds s.mu.
// Symbolic refs (HEAD) are skipped: a mirror pushes the refs HEAD points
// through, and pushing HEAD itself is neither needed nor meaningful here.
func (s *Store) mirroredRefs() (refSet, error) {
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

// listRemoteMirroredRefs asks the remote what it currently holds, so the
// push can name the refs that need deleting. An empty remote is the normal
// first-run state, not a failure.
func listRemoteMirroredRefs(ctx context.Context, remote *git.Remote, auth transport.AuthMethod) (refSet, []plumbing.Hash, error) {
	refs, err := remote.ListContext(ctx, &git.ListOptions{Auth: auth})
	if errors.Is(err, transport.ErrEmptyRemoteRepository) {
		// A brand-new empty repository: nothing to overwrite, nothing to
		// compare against. This is the expected state of a first push, so it
		// must NOT read as an unrelated remote.
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

// sharesHistory reports whether any of the remote's ref targets is an object
// this repository holds — the test for "these are the same repository".
//
// It asks the object store directly rather than walking commits: a mirror
// remote's tip is normally either our HEAD or an ancestor of it, and both are
// present locally. Walking would cost the whole history to answer a question
// one lookup settles.
func (s *Store) sharesHistory(remoteHashes []plumbing.Hash) bool {
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
