// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package workspace

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-billy/v5/memfs"
	"github.com/go-git/go-billy/v5/osfs"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/cache"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/storer"
	"github.com/go-git/go-git/v5/storage"
	"github.com/go-git/go-git/v5/storage/filesystem"
	"github.com/go-git/go-git/v5/storage/memory"

	"github.com/dazyflow/dazyflow/core"
)

// A working tree the customer owns, so every change is a commit they can read
// with ordinary git tooling. Flows live at graphs/<id>.json.
type gitBackend struct {
	mu   *sync.Mutex
	repo *git.Repository
	fs   billy.Filesystem
	dir  string
}

// One mutex per absolute workspace DIRECTORY, not per store: two stores opened
// on the same path must serialize against each other, or concurrent commits
// corrupt the index.
var dirLocks sync.Map // absolute dir → *sync.Mutex

func dirMutex(dir string) *sync.Mutex {
	if v, ok := dirLocks.Load(dir); ok {
		return v.(*sync.Mutex)
	}
	v, _ := dirLocks.LoadOrStore(dir, new(sync.Mutex))
	return v.(*sync.Mutex)
}

// Bounds one repository's decompressed-object cache.
const objectCacheBytes = 2 * 1024 * 1024

func openBackend(dir string) (*gitBackend, error) {
	if dir == "" {
		return openMemory()
	}
	return openDisk(dir)
}

func openMemory() (*gitBackend, error) {
	fs := memfs.New()
	storer := memory.NewStorage()
	repo, err := git.Init(storer, fs)
	if err != nil {
		return nil, fmt.Errorf("init memory repo: %w", err)
	}
	return &gitBackend{mu: new(sync.Mutex), repo: repo, fs: fs}, nil
}

func openDisk(dir string) (*gitBackend, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir %q: %w", dir, err)
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("resolve %q: %w", dir, err)
	}
	wt := osfs.New(dir)
	gitDir, err := wt.Chroot(".git")
	if err != nil {
		return nil, err
	}
	storer := filesystem.NewStorage(gitDir, cache.NewObjectLRU(objectCacheBytes))

	repo, err := openOrInit(storer, wt)
	if err != nil {
		return nil, err
	}
	return &gitBackend{mu: dirMutex(filepath.Clean(abs)), repo: repo, fs: wt, dir: abs}, nil
}

func openOrInit(storer storage.Storer, wt billy.Filesystem) (*git.Repository, error) {
	repo, err := git.Open(storer, wt)
	if err == nil {
		return repo, nil
	}
	if !errors.Is(err, git.ErrRepositoryNotExists) {
		return nil, fmt.Errorf("open repo: %w", err)
	}
	repo, err = git.Init(storer, wt)
	if err != nil {
		return nil, fmt.Errorf("init repo: %w", err)
	}
	tree, err := repo.Worktree()
	if err != nil {
		return nil, fmt.Errorf("seed worktree: %w", err)
	}
	f, err := wt.Create(".gitkeep")
	if err != nil {
		return nil, fmt.Errorf("seed gitkeep: %w", err)
	}
	if err := f.Close(); err != nil {
		return nil, fmt.Errorf("seed gitkeep close: %w", err)
	}
	if _, err := tree.Add(".gitkeep"); err != nil {
		return nil, fmt.Errorf("seed add: %w", err)
	}
	if _, err := tree.Commit("init", &git.CommitOptions{
		Author: &object.Signature{
			Name:  "dazyflow",
			Email: "dazyflow@local",
			When:  time.Now(),
		},
	}); err != nil {
		return nil, fmt.Errorf("seed commit: %w", err)
	}
	return repo, nil
}

// Consecutive autosaves inside the window amend one commit.
const autosaveCoalesceWindow = 90 * time.Second

func autosaveMessage(graphID, author string) string {
	return fmt.Sprintf("autosave: update %s [user:%s]", graphID, author)
}
func explicitMessage(graphID, author string) string {
	return fmt.Sprintf("graph: update %s [user:%s]", graphID, author)
}

func (s *gitBackend) headIsRecentAutosave(graphID, author string) bool {
	ref, err := s.repo.Head()
	if err != nil {
		return false
	}
	c, err := s.repo.CommitObject(ref.Hash())
	if err != nil {
		return false
	}
	if c.Message != autosaveMessage(graphID, author) {
		return false
	}
	return time.Since(c.Author.When) <= autosaveCoalesceWindow
}

func (s *gitBackend) save(graph core.Graph, author string, coalesce bool) (string, error) {
	// The id becomes a path here and a git ref name at publish, so validate both.
	if err := core.ValidGraphID(graph.ID); err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	wt, err := s.repo.Worktree()
	if err != nil {
		return "", err
	}
	relPath := graphPath(graph.ID)

	data, err := json.MarshalIndent(graph, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal graph: %w", err)
	}

	if err := s.fs.MkdirAll(path.Dir(relPath), 0o755); err != nil {
		return "", err
	}
	f, err := s.fs.Create(relPath)
	if err != nil {
		return "", err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}

	if _, err := wt.Add(relPath); err != nil {
		return "", fmt.Errorf("git add: %w", err)
	}

	amend := coalesce && s.headIsRecentAutosave(graph.ID, author)
	msg := explicitMessage(graph.ID, author)
	if coalesce {
		msg = autosaveMessage(graph.ID, author)
	}
	hash, err := wt.Commit(msg, &git.CommitOptions{
		Author: &object.Signature{
			Name:  author,
			Email: author,
			When:  time.Now(),
		},
		AllowEmptyCommits: false,
		Amend:             amend,
	})
	if err != nil {
		if errors.Is(err, git.ErrEmptyCommit) {
			// The staged tree equals its parent's, so there is nothing to commit. Callers
			// treat it as success: an autosave of an unchanged flow is not an error.
			if amend {
				return s.dropAmendedHead()
			}
			head, herr := s.repo.Head()
			if herr != nil {
				return "", fmt.Errorf("commit: %w (and head lookup: %v)", err, herr)
			}
			return head.Hash().String(), nil
		}
		return "", fmt.Errorf("commit: %w", err)
	}
	return hash.String(), nil
}

// Rewinds to HEAD's first parent, which is how an amend is undone.
func (s *gitBackend) dropAmendedHead() (string, error) {
	head, err := s.repo.Head()
	if err != nil {
		return "", fmt.Errorf("amend-empty: head lookup: %w", err)
	}
	c, err := s.repo.CommitObject(head.Hash())
	if err != nil {
		return "", fmt.Errorf("amend-empty: head commit: %w", err)
	}
	// Amending the root commit has nothing to rewind to.
	if len(c.ParentHashes) == 0 {
		return head.Hash().String(), nil
	}
	parent := c.ParentHashes[0]
	if err := s.repo.Storer.SetReference(plumbing.NewHashReference(head.Name(), parent)); err != nil {
		return "", fmt.Errorf("amend-empty: rewind to parent: %w", err)
	}
	return parent.String(), nil
}

// Only the commits that touched this flow's own path.
func (s *gitBackend) history(id string, limit int) ([]Revision, error) {
	if id == "" {
		return nil, errors.New("graphID required")
	}
	if limit <= 0 {
		limit = 100
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rel := graphPath(id)
	iter, err := s.repo.Log(&git.LogOptions{FileName: &rel, Order: git.LogOrderCommitterTime})
	if err != nil {
		return nil, fmt.Errorf("log %s: %w", rel, err)
	}
	defer iter.Close()
	revs := make([]Revision, 0, limit)
	err = iter.ForEach(func(c *object.Commit) error {
		if len(revs) >= limit {
			return storer.ErrStop
		}
		revs = append(revs, Revision{
			Commit:   c.Hash.String(),
			Author:   c.Author.Name,
			Message:  c.Message,
			When:     c.Author.When,
			Autosave: strings.HasPrefix(c.Message, "autosave:"),
			Label:    s.revisionLabel(id, c.Hash.String()),
		})
		return nil
	})
	if err != nil && !errors.Is(err, storer.ErrStop) {
		return nil, fmt.Errorf("walk history: %w", err)
	}
	return revs, nil
}

func (s *gitBackend) delete(graphID, author string) (string, error) {
	if graphID == "" {
		return "", errors.New("graphID required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	wt, err := s.repo.Worktree()
	if err != nil {
		return "", err
	}
	relPath := graphPath(graphID)
	if _, statErr := s.fs.Stat(relPath); statErr != nil {
		return "", nil
	}
	if err := s.fs.Remove(relPath); err != nil {
		return "", fmt.Errorf("remove %s: %w", relPath, err)
	}
	if _, err := wt.Add(relPath); err != nil {
		return "", fmt.Errorf("git add (removal): %w", err)
	}
	msg := fmt.Sprintf("graph: delete %s [user:%s]", graphID, author)
	hash, err := wt.Commit(msg, &git.CommitOptions{
		Author: &object.Signature{
			Name:  author,
			Email: author,
			When:  time.Now(),
		},
		AllowEmptyCommits: false,
	})
	if err != nil {
		if errors.Is(err, git.ErrEmptyCommit) {
			head, herr := s.repo.Head()
			if herr != nil {
				return "", fmt.Errorf("commit: %w (and head lookup: %v)", err, herr)
			}
			return head.Hash().String(), nil
		}
		return "", fmt.Errorf("commit: %w", err)
	}
	return hash.String(), nil
}

// The commit resolves but carries no such flow.
var ErrGraphNotFound = errors.New("graph not found")

func (s *gitBackend) load(id string) (core.Graph, error) {
	data, err := s.readFlowAtHead(id)
	if err != nil {
		return core.Graph{}, err
	}
	return decodeGraphBytes(data, graphPath(id))
}

// The locked half: decoding happens outside the lock, being the larger cost.
func (s *gitBackend) readFlowAtHead(id string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	head, err := s.repo.Head()
	if err != nil {
		// An unborn HEAD is a repo with no commits, not an error.
		if errors.Is(err, plumbing.ErrReferenceNotFound) {
			return nil, fmt.Errorf("graph %q: %w", id, ErrGraphNotFound)
		}
		return nil, fmt.Errorf("head: %w", err)
	}
	return s.readAtHash(head.Hash(), id)
}

func (s *gitBackend) loadAt(ref, id string) (core.Graph, error) {
	data, err := s.readFlowAt(ref, id)
	if err != nil {
		return core.Graph{}, err
	}
	return decodeGraphBytes(data, graphPath(id))
}

func (s *gitBackend) readFlowAt(ref, id string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	hash, err := s.resolveHash(ref)
	if err != nil {
		return nil, err
	}
	return s.readAtHash(hash, id)
}

func (s *gitBackend) readAtHash(hash plumbing.Hash, id string) ([]byte, error) {
	commit, err := s.repo.CommitObject(hash)
	if err != nil {
		return nil, fmt.Errorf("commit %s: %w", hash, err)
	}
	tree, err := commit.Tree()
	if err != nil {
		return nil, err
	}
	file, err := tree.File(graphPath(id))
	if err != nil {
		// "Never saved" and "store is broken" must not read the same to a caller.
		if errors.Is(err, object.ErrFileNotFound) ||
			errors.Is(err, object.ErrDirectoryNotFound) ||
			errors.Is(err, object.ErrEntryNotFound) {
			return nil, fmt.Errorf("graph %q at %s: %w", id, hash, ErrGraphNotFound)
		}
		return nil, fmt.Errorf("graph %q at %s: %w", id, hash, err)
	}
	return readBlob(file)
}

func decodeGraphBytes(data []byte, name string) (core.Graph, error) {
	var g core.Graph
	if err := json.Unmarshal(data, &g); err != nil {
		return core.Graph{}, fmt.Errorf("parse %s: %w", name, err)
	}
	return g, nil
}

func (s *gitBackend) setEnv(graphID, env, commit string) error {
	if env == "" {
		return errors.New("env required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	hash, err := s.resolveHash(commit)
	if err != nil {
		return err
	}
	name := plumbing.NewTagReferenceName(envTag(graphID, env))
	ref := plumbing.NewHashReference(name, hash)
	if err := s.repo.Storer.SetReference(ref); err != nil {
		return fmt.Errorf("set tag: %w", err)
	}
	return nil
}

func (s *gitBackend) clearEnv(graphID, env string) error {
	if env == "" {
		return errors.New("env required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	name := plumbing.NewTagReferenceName(envTag(graphID, env))
	if err := s.repo.Storer.RemoveReference(name); err != nil &&
		!errors.Is(err, plumbing.ErrReferenceNotFound) {
		return fmt.Errorf("remove tag: %w", err)
	}
	return nil
}

const PublishedEnv = "published"

func (s *gitBackend) envCommit(id, env string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.envCommitLocked(id, env)
}

func (s *gitBackend) envCommitLocked(id, env string) (string, error) {
	name := plumbing.NewTagReferenceName(envTag(id, env))
	ref, err := s.repo.Reference(name, true)
	if err != nil {
		if errors.Is(err, plumbing.ErrReferenceNotFound) {
			return "", nil
		}
		return "", err
	}
	return ref.Hash().String(), nil
}

// One tree walk rather than a load per flow, and the published tags are read in
// the same pass.
func (s *gitBackend) listAtHead(env string, headersOnly bool) ([]FlowAtHead, error) {
	raw, envAt, err := s.readAtHead(env)
	if err != nil || raw == nil {
		return nil, err
	}
	var out []FlowAtHead
	for _, f := range raw {
		g, err := decodeFlow(f.data, headersOnly)
		if err != nil {
			continue
		}
		out = append(out, FlowAtHead{ID: f.id, Graph: g, EnvCommit: envAt[f.id]})
	}
	return out, nil
}

type rawFlow struct {
	id   string
	data []byte
}

func (s *gitBackend) readAtHead(env string) ([]rawFlow, map[string]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	head, err := s.repo.Head()
	if errors.Is(err, plumbing.ErrReferenceNotFound) {
		return nil, nil, nil // no commits yet: an empty workspace, not a fault
	}
	if err != nil {
		return nil, nil, err
	}
	commit, err := s.repo.CommitObject(head.Hash())
	if err != nil {
		return nil, nil, err
	}
	tree, err := commit.Tree()
	if err != nil {
		return nil, nil, err
	}
	envAt := map[string]string{}
	if env != "" {
		refs, err := s.repo.References()
		if err != nil {
			return nil, nil, err
		}
		suffix := "/" + env
		if err := refs.ForEach(func(r *plumbing.Reference) error {
			name := strings.TrimPrefix(string(r.Name()), "refs/tags/graphs/")
			if name == string(r.Name()) || !strings.HasSuffix(name, suffix) {
				return nil
			}
			id := strings.TrimSuffix(name, suffix)
			if id == "" || strings.Contains(id, "/") {
				return nil
			}
			envAt[id] = r.Hash().String()
			return nil
		}); err != nil {
			return nil, nil, err
		}
	}
	out := []rawFlow{}
	err = tree.Files().ForEach(func(f *object.File) error {
		dir, base := path.Split(f.Name)
		if dir != "graphs/" || !strings.HasSuffix(base, ".json") {
			return nil
		}
		data, err := readBlob(f)
		if err != nil {
			return err
		}
		out = append(out, rawFlow{id: strings.TrimSuffix(base, ".json"), data: data})
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return out, envAt, nil
}

var ErrNotPublished = errors.New("flow is not published")

func (s *gitBackend) listGraphs() ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	head, err := s.repo.Head()
	if errors.Is(err, plumbing.ErrReferenceNotFound) {
		// A repo with no commits and a missing directory both read as empty.
		if err := s.rootPresent(); err != nil {
			return nil, err
		}
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	commit, err := s.repo.CommitObject(head.Hash())
	if err != nil {
		return nil, err
	}
	tree, err := commit.Tree()
	if err != nil {
		return nil, err
	}
	var ids []string
	err = tree.Files().ForEach(func(f *object.File) error {
		if dir, base := path.Split(f.Name); dir == "graphs/" && strings.HasSuffix(base, ".json") {
			ids = append(ids, strings.TrimSuffix(base, ".json"))
		}
		return nil
	})
	return ids, err
}

func (s *gitBackend) rootPresent() error {
	if s.dir == "" {
		return nil
	}
	if _, err := os.Stat(s.dir); err != nil {
		return fmt.Errorf("workspace %s: %w", s.dir, err)
	}
	return nil
}

func (s *gitBackend) head() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	head, err := s.repo.Head()
	if errors.Is(err, plumbing.ErrReferenceNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return head.Hash().String(), nil
}

func (s *gitBackend) resolve(ref string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h, err := s.resolveHash(ref)
	if err != nil {
		return "", err
	}
	return h.String(), nil
}

func (s *gitBackend) resolveGraph(_, ref string) (string, error) { return s.resolve(ref) }

func (s *gitBackend) resolveHash(ref string) (plumbing.Hash, error) {
	if h, err := s.repo.ResolveRevision(plumbing.Revision(ref)); err == nil {
		return *h, nil
	}
	if len(ref) == 40 {
		return plumbing.NewHash(ref), nil
	}
	return plumbing.ZeroHash, fmt.Errorf("could not resolve %q", ref)
}

func graphPath(id string) string        { return "graphs/" + id + ".json" }
func envTag(graphID, env string) string { return "graphs/" + graphID + "/" + env }

// An annotated tag, so the label travels with a push.
func labelTag(graphID, commit string) string {
	return "graphs/" + graphID + "/labels/" + commit
}

func (s *gitBackend) setLabel(graphID, commit, label string) error {
	if graphID == "" {
		return errors.New("graphID required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	hash, err := s.resolveHash(commit)
	if err != nil {
		return err
	}
	name := plumbing.NewTagReferenceName(labelTag(graphID, hash.String()))
	if err := s.repo.Storer.RemoveReference(name); err != nil &&
		!errors.Is(err, plumbing.ErrReferenceNotFound) {
		return fmt.Errorf("clear label: %w", err)
	}
	if strings.TrimSpace(label) == "" {
		return nil // empty label = clear
	}
	_, err = s.repo.CreateTag(labelTag(graphID, hash.String()), hash, &git.CreateTagOptions{
		Message: label,
		Tagger: &object.Signature{
			Name:  "dazyflow",
			Email: "dazyflow@local",
			When:  time.Now(),
		},
	})
	if err != nil {
		return fmt.Errorf("create label tag: %w", err)
	}
	return nil
}

func (s *gitBackend) label(graphID, commit string) (string, error) {
	if graphID == "" {
		return "", errors.New("graphID required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	hash, err := s.resolveHash(commit)
	if err != nil {
		return "", err
	}
	return s.revisionLabel(graphID, hash.String()), nil
}

func (s *gitBackend) revisionLabel(graphID, commit string) string {
	name := plumbing.NewTagReferenceName(labelTag(graphID, commit))
	ref, err := s.repo.Reference(name, false)
	if err != nil {
		return ""
	}
	tag, err := s.repo.TagObject(ref.Hash())
	if err != nil {
		return ""
	}
	return strings.TrimSpace(tag.Message)
}

func (s *gitBackend) refs(prefix string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return listRefs(s.repo, prefix)
}

func (s *gitBackend) mirror() (gitMirrorer, bool) { return s, true }

func listRefs(repo *git.Repository, prefix string) ([]string, error) {
	refs, err := repo.References()
	if err != nil {
		return nil, err
	}
	var out []string
	err = refs.ForEach(func(r *plumbing.Reference) error {
		name := string(r.Name())
		if strings.HasPrefix(name, prefix) {
			out = append(out, strings.TrimPrefix(name, prefix))
		}
		return nil
	})
	return out, err
}

// Reads into a blob-sized buffer, so a large flow is one allocation.
func readBlob(f *object.File) (data []byte, err error) {
	r, err := f.Reader()
	if err != nil {
		return nil, err
	}
	defer func() {
		if cerr := r.Close(); err == nil {
			err = cerr
		}
	}()
	data = make([]byte, f.Size)
	if _, err = io.ReadFull(r, data); err != nil {
		return nil, err
	}
	return data, nil
}
