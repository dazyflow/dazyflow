// Package workspace stores graphs in a Git repository owned by the
// customer. Every save commits with the author's identity in the message
// and trailers, giving teams full history/audit/diff without bespoke
// tooling. Environments (staging, production) are modeled as Git tags
// pointing at frozen graph revisions.
package workspace

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
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

	"git.sr.ht/~klahr/hazyflow/core"
)

// Store wraps a Git working tree. Graphs live under graphs/<id>.json;
// environments under refs/tags/graphs/<id>/<env>.
//
// mu serializes every repo access. go-git's repository/storer types are
// not safe for concurrent use, and the scheduler's rescan reads
// (ListGraphs/Load) run on tickers concurrently with the HTTP gateway's
// Save path — so an unguarded Store data-races on the storage map. It's a
// full Mutex rather than an RWMutex on purpose: the filesystem backend's
// object LRU cache is mutated *during reads*, so even two concurrent
// readers would race on it. Graph save/load/list are infrequent next to
// job execution, so full mutual exclusion per (tenant,workspace) store is
// a fine trade for correctness.
type Store struct {
	mu   sync.Mutex
	repo *git.Repository
	fs   billy.Filesystem
}

// OpenFS opens (or initializes) a Store rooted at dir on the local
// filesystem. If dir is empty an in-memory Store is created — useful for
// tests and ephemeral workspaces.
func OpenFS(dir string) (*Store, error) {
	if dir == "" {
		return openMemory()
	}
	return openDisk(dir)
}

func openMemory() (*Store, error) {
	fs := memfs.New()
	storer := memory.NewStorage()
	repo, err := git.Init(storer, fs)
	if err != nil {
		return nil, fmt.Errorf("init memory repo: %w", err)
	}
	return &Store{repo: repo, fs: fs}, nil
}

func openDisk(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir %q: %w", dir, err)
	}
	wt := osfs.New(dir)
	gitDir, err := wt.Chroot(".git")
	if err != nil {
		return nil, err
	}
	storer := filesystem.NewStorage(gitDir, cache.NewObjectLRUDefault())

	repo, err := openOrInit(storer, wt)
	if err != nil {
		return nil, err
	}
	return &Store{repo: repo, fs: wt}, nil
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
	// Seed an initial commit with a .gitkeep so HEAD resolves to a
	// real tree from the moment the store opens. Without this,
	// ListGraphs walks a HEAD whose hash has no reachable object and
	// go-git nil-derefs inside the filesystem object store.
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
			Name:  "hazyflow",
			Email: "hazyflow@local",
			When:  time.Now(),
		},
	}); err != nil {
		return nil, fmt.Errorf("seed commit: %w", err)
	}
	return repo, nil
}

// autosaveCoalesceWindow bounds how long a run of editor autosaves of the
// same graph by the same author collapses into a single commit. Each
// coalescing autosave amends the previous one as long as it landed within
// this window, so a continuous editing session is one commit and every
// pause longer than the window starts a fresh one. Explicit saves never
// coalesce, so the user can still drop intentional checkpoints.
const autosaveCoalesceWindow = 90 * time.Second

// autosaveMessage / explicitMessage are the commit subjects. They differ so
// a later autosave can recognise (and amend) a previous autosave commit
// without ever amending an explicit checkpoint.
func autosaveMessage(graphID, author string) string {
	return fmt.Sprintf("autosave: update %s [user:%s]", graphID, author)
}
func explicitMessage(graphID, author string) string {
	return fmt.Sprintf("graph: update %s [user:%s]", graphID, author)
}

// headIsRecentAutosave reports whether HEAD is an autosave commit for this
// exact (graph, author) that landed within the coalesce window — i.e. the
// next autosave should amend it rather than stack a new commit. Caller holds
// s.mu.
func (s *Store) headIsRecentAutosave(graphID, author string) bool {
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

// Save writes the graph and commits a new revision. Use this for explicit
// saves — it always produces its own commit (an intentional checkpoint).
func (s *Store) Save(graph core.Graph, author string) (string, error) {
	return s.save(graph, author, false)
}

// SaveCoalescing is the editor-autosave variant: a save that immediately
// follows a recent autosave of the same graph by the same author amends it
// instead of stacking a new commit, so a continuous editing session stays
// one commit in the history. See autosaveCoalesceWindow.
func (s *Store) SaveCoalescing(graph core.Graph, author string) (string, error) {
	return s.save(graph, author, true)
}

func (s *Store) save(graph core.Graph, author string, coalesce bool) (string, error) {
	if graph.ID == "" {
		return "", errors.New("graph.ID required")
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

	// Coalesce a run of autosaves into one commit by amending the previous
	// autosave when it's recent and for the same graph+author.
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
		// Re-saving identical content is a no-op, not a failure —
		// callers (especially the AI chat's "apply" step after the
		// agent already saved through MCP) hit this and shouldn't
		// see an error. Surface the existing HEAD as the commit.
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

// Revision is one entry in a graph's commit history.
type Revision struct {
	Commit   string    `json:"commit"`
	Author   string    `json:"author"`
	Message  string    `json:"message"`
	When     time.Time `json:"when"`
	Autosave bool      `json:"autosave"`
}

// History returns the commits that touched graphs/<id>.json, newest first,
// capped at limit (limit <= 0 applies a default). It's the backing data for
// the editor's version-history panel; restoring a revision is a normal Save
// of that revision's content (a new commit at the top), so history is never
// rewritten.
func (s *Store) History(id string, limit int) ([]Revision, error) {
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
		})
		return nil
	})
	if err != nil && !errors.Is(err, storer.ErrStop) {
		return nil, fmt.Errorf("walk history: %w", err)
	}
	return revs, nil
}

// Delete removes graphs/<id>.json from the worktree and commits the
// removal. Returns the resulting commit hash on success. Idempotent
// in the "file doesn't exist" sense: a missing path returns
// (commit="", nil) so the caller can surface "deleted or already
// gone" as the same outcome.
func (s *Store) Delete(graphID, author string) (string, error) {
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
		// Not present in the worktree. Treat as already-deleted —
		// keeps the caller's semantics simple (HTTP 204 whether the
		// resource was there or not, matching REST conventions).
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

// Load reads graphs/<id>.json from HEAD.
func (s *Store) Load(id string) (core.Graph, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	head, err := s.repo.Head()
	if err != nil {
		return core.Graph{}, fmt.Errorf("head: %w", err)
	}
	return s.loadAt(head.Hash(), id)
}

// LoadAt reads graphs/<id>.json from the commit identified by ref (a
// branch, tag, or hex hash).
func (s *Store) LoadAt(ref, id string) (core.Graph, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	hash, err := s.resolve(ref)
	if err != nil {
		return core.Graph{}, err
	}
	return s.loadAt(hash, id)
}

func (s *Store) loadAt(hash plumbing.Hash, id string) (core.Graph, error) {
	commit, err := s.repo.CommitObject(hash)
	if err != nil {
		return core.Graph{}, fmt.Errorf("commit %s: %w", hash, err)
	}
	tree, err := commit.Tree()
	if err != nil {
		return core.Graph{}, err
	}
	file, err := tree.File(graphPath(id))
	if err != nil {
		return core.Graph{}, fmt.Errorf("graph %q at %s: %w", id, hash, err)
	}
	contents, err := file.Contents()
	if err != nil {
		return core.Graph{}, err
	}
	var g core.Graph
	if err := json.Unmarshal([]byte(contents), &g); err != nil {
		return core.Graph{}, fmt.Errorf("parse %s: %w", file.Name, err)
	}
	return g, nil
}

// PromoteToEnvironment moves the environment tag (refs/tags/graphs/<id>/<env>)
// to the supplied commit. Common envs: staging, production.
func (s *Store) PromoteToEnvironment(graphID, env, commit string) error {
	if env == "" {
		return errors.New("env required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	hash, err := s.resolve(commit)
	if err != nil {
		return err
	}
	name := plumbing.NewTagReferenceName(envTag(graphID, env))
	// Force update — env tags are intentionally movable.
	ref := plumbing.NewHashReference(name, hash)
	if err := s.repo.Storer.SetReference(ref); err != nil {
		return fmt.Errorf("set tag: %w", err)
	}
	return nil
}

// ClearEnvironment removes the environment tag (refs/tags/graphs/<id>/<env>),
// reverting the flow to having no revision pinned for that env. Unpublishing a
// flow clears the PublishedEnv tag: the scheduler then treats the flow as
// "not live" (PublishedCommit returns ""), while webhook/event endpoints fall
// back to HEAD via LoadPublishedOrHead. Idempotent — clearing an env that was
// never set is a no-op, not an error.
func (s *Store) ClearEnvironment(graphID, env string) error {
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

// PublishedEnv is the environment name for the "live" published version
// of a flow — the revision automatic triggers (cron/poll/webhook) run.
// Publishing moves this tag to a commit via PromoteToEnvironment; rollback
// re-publishes an older commit. HEAD remains the editable draft.
const PublishedEnv = "published"

// PublishedCommit returns the commit hash the flow's published tag points
// at, or "" when the flow has never been published. A never-published
// flow is not an error — the caller falls back to HEAD (today's
// behaviour) so introducing publish doesn't silently stop existing flows
// from firing.
func (s *Store) PublishedCommit(id string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.publishedCommit(id)
}

func (s *Store) publishedCommit(id string) (string, error) {
	name := plumbing.NewTagReferenceName(envTag(id, PublishedEnv))
	ref, err := s.repo.Reference(name, true)
	if err != nil {
		if errors.Is(err, plumbing.ErrReferenceNotFound) {
			return "", nil
		}
		return "", err
	}
	return ref.Hash().String(), nil
}

// LoadPublishedOrHead reads the flow at its published tag, falling back to
// HEAD when the flow has never been published. This is the version
// automatic triggers run: a published flow fires its last published
// revision, not whatever half-finished draft is at HEAD. The manual-run,
// sample, and test-trigger paths deliberately keep using Load (HEAD) so an
// author can test edits before publishing them live.
func (s *Store) LoadPublishedOrHead(id string) (core.Graph, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	commit, err := s.publishedCommit(id)
	if err != nil {
		return core.Graph{}, err
	}
	if commit == "" {
		head, err := s.repo.Head()
		if err != nil {
			return core.Graph{}, fmt.Errorf("head: %w", err)
		}
		return s.loadAt(head.Hash(), id)
	}
	return s.loadAt(plumbing.NewHash(commit), id)
}

// ListGraphs returns the IDs of every graph currently committed at HEAD.
func (s *Store) ListGraphs() ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	head, err := s.repo.Head()
	if errors.Is(err, plumbing.ErrReferenceNotFound) {
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

// Head returns the current HEAD commit hash as a hex string, or "" when
// the repo has no commits yet. A cheap cache key for callers that
// memoize views derived from the whole graph set (e.g. the drop-suggestion
// adjacency) — when HEAD is unchanged, nothing the derived view depends on
// has changed either.
func (s *Store) Head() (string, error) {
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

func (s *Store) resolve(ref string) (plumbing.Hash, error) {
	if h, err := s.repo.ResolveRevision(plumbing.Revision(ref)); err == nil {
		return *h, nil
	}
	// Treat ref as raw hash.
	if len(ref) == 40 {
		return plumbing.NewHash(ref), nil
	}
	return plumbing.ZeroHash, fmt.Errorf("could not resolve %q", ref)
}

func graphPath(id string) string        { return "graphs/" + id + ".json" }
func envTag(graphID, env string) string { return "graphs/" + graphID + "/" + env }

// Branches/Tags surface the underlying refs for callers that want to do
// their own listing/diff.
func (s *Store) Branches() ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return listRefs(s.repo, "refs/heads/")
}
func (s *Store) Tags() ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return listRefs(s.repo, "refs/tags/")
}

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
