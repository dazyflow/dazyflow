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
	"time"

	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-billy/v5/memfs"
	"github.com/go-git/go-billy/v5/osfs"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/cache"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/storage"
	"github.com/go-git/go-git/v5/storage/filesystem"
	"github.com/go-git/go-git/v5/storage/memory"

	"git.sr.ht/~klahr/hazy-flow/core"
)

// Store wraps a Git working tree. Graphs live under graphs/<id>.json;
// environments under refs/tags/graphs/<id>/<env>.
type Store struct {
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
			Name:  "hazy-flow",
			Email: "hazy-flow@local",
			When:  time.Now(),
		},
	}); err != nil {
		return nil, fmt.Errorf("seed commit: %w", err)
	}
	return repo, nil
}

// Save writes graph to graphs/<id>.json and commits the change as the
// given author. Returns the new commit hash.
func (s *Store) Save(graph core.Graph, author string) (string, error) {
	if graph.ID == "" {
		return "", errors.New("graph.ID required")
	}
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

	msg := fmt.Sprintf("graph: update %s [user:%s]", graph.ID, author)
	hash, err := wt.Commit(msg, &git.CommitOptions{
		Author: &object.Signature{
			Name:  author,
			Email: author,
			When:  time.Now(),
		},
		AllowEmptyCommits: false,
	})
	if err != nil {
		return "", fmt.Errorf("commit: %w", err)
	}
	return hash.String(), nil
}

// Load reads graphs/<id>.json from HEAD.
func (s *Store) Load(id string) (core.Graph, error) {
	head, err := s.repo.Head()
	if err != nil {
		return core.Graph{}, fmt.Errorf("head: %w", err)
	}
	return s.loadAt(head.Hash(), id)
}

// LoadAt reads graphs/<id>.json from the commit identified by ref (a
// branch, tag, or hex hash).
func (s *Store) LoadAt(ref, id string) (core.Graph, error) {
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

// ListGraphs returns the IDs of every graph currently committed at HEAD.
func (s *Store) ListGraphs() ([]string, error) {
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

func graphPath(id string) string         { return "graphs/" + id + ".json" }
func envTag(graphID, env string) string  { return "graphs/" + graphID + "/" + env }

// Branches/Tags surface the underlying refs for callers that want to do
// their own listing/diff.
func (s *Store) Branches() ([]string, error) { return listRefs(s.repo, "refs/heads/") }
func (s *Store) Tags() ([]string, error)     { return listRefs(s.repo, "refs/tags/") }

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

