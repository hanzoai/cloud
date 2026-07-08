package git

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	billy "github.com/go-git/go-billy/v5"
	"github.com/go-git/go-billy/v5/osfs"
	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/cache"
	"github.com/go-git/go-git/v5/plumbing/storer"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/plumbing/transport/server"
	"github.com/go-git/go-git/v5/storage/filesystem"
)

// storage is the billy-backed home for every tenant's bare repositories. Each
// repo lives at rootDir/<org>/<project>/<name>.git as a bare go-git repo on a
// go-billy filesystem; the same billy FS is what the go-git server transport
// (upload-pack / receive-pack) reads and writes, so clone AND push operate
// directly on tenant storage with no shell-out to the `git` binary.
//
// It is backed by osfs, not hanzoai/vfs: vfs.FS (Create/Open(ctx)/Lookup over
// *Inode/*File) does not implement the go-billy Filesystem surface go-git's
// dotgit layer requires (Chroot, Root, TempFile, Rename, Symlink/Readlink,
// Lstat, MkdirAll, plus billy.File Lock/Truncate/Seek). osfs is pointed at a
// data-dir root an operator can back with a FUSE-mounted VFS or an S3-backed volume.
type storage struct {
	rootDir string
	fs      billy.Filesystem // osfs rooted at rootDir; the billy home for all repos
}

func newStorage(rootDir string) (*storage, error) {
	if rootDir == "" {
		return nil, fmt.Errorf("git storage: empty rootDir")
	}
	if err := os.MkdirAll(rootDir, 0o755); err != nil {
		return nil, fmt.Errorf("git storage: mkdir %q: %w", rootDir, err)
	}
	return &storage{rootDir: rootDir, fs: osfs.New(rootDir)}, nil
}

// repoRel is the storage-relative billy path for a repo: "<org>/<project>/<name>.git".
// org, project and name are all validated identifiers (idRE / no path separators),
// so the join can never traverse out of the storage root.
func repoRel(org, project, name string) string {
	return filepath.ToSlash(filepath.Join(org, projectDir(project), name+".git"))
}

// projectDir maps an (optional) project sub-scope to a path segment. An empty
// project (org-level repo) uses a fixed "_" segment so org-level and
// project-scoped repos never collide and the tree stays two-deep-then-repo.
func projectDir(project string) string {
	if project == "" {
		return "_"
	}
	return project
}

// dotGitFS returns the billy filesystem chrooted at a repo's bare .git home.
func (s *storage) dotGitFS(org, project, name string) (billy.Filesystem, error) {
	return s.fs.Chroot(repoRel(org, project, name))
}

// storer builds a go-git filesystem storer for a repo — the object/ref backend
// the server transport sessions AND the repository init/open read and write.
// Returns the concrete *filesystem.Storage (it satisfies both the narrow
// storer.Storer the loader wants and the full storage.Storer that
// git.Init/Open require, which the interface value would erase).
func (s *storage) storer(org, project, name string) (*filesystem.Storage, error) {
	dot, err := s.dotGitFS(org, project, name)
	if err != nil {
		return nil, fmt.Errorf("chroot repo: %w", err)
	}
	return filesystem.NewStorage(dot, cache.NewObjectLRUDefault()), nil
}

// initBare creates an empty bare repository at the repo's storage path with
// HEAD pointing at defaultBranch. Callers guard on the metadata row for
// idempotency; here a pre-existing repo returns go-git's
// ErrRepositoryAlreadyExists.
func (s *storage) initBare(org, project, name, defaultBranch string) error {
	st, err := s.storer(org, project, name)
	if err != nil {
		return err
	}
	if defaultBranch == "" {
		defaultBranch = defaultBranchName
	}
	// worktree nil → bare repo. DefaultBranch makes the symbolic HEAD point at
	// the requested branch so the first push to it makes it the repo default,
	// matching `git init --bare --initial-branch`.
	_, err = gogit.InitWithOptions(st, nil, gogit.InitOptions{
		DefaultBranch: plumbing.NewBranchReferenceName(defaultBranch),
	})
	if err != nil {
		return fmt.Errorf("git init: %w", err)
	}
	return nil
}

// exists reports whether a bare repo is present at the storage path.
func (s *storage) exists(org, project, name string) bool {
	dot, err := s.dotGitFS(org, project, name)
	if err != nil {
		return false
	}
	if _, err := dot.Stat("HEAD"); err == nil {
		return true
	}
	// A fresh go-git bare repo may not have a plain HEAD file yet depending on
	// backend; fall back to the config marker.
	if _, err := dot.Stat("config"); err == nil {
		return true
	}
	return false
}

// remove deletes a repo's entire storage subtree (purge on DELETE). Best-effort
// on the OS path; metadata is the source of truth for existence.
func (s *storage) remove(org, project, name string) error {
	abs := filepath.Join(s.rootDir, filepath.FromSlash(repoRel(org, project, name)))
	if err := os.RemoveAll(abs); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove repo storage: %w", err)
	}
	return nil
}

// sizeBytes sums the on-disk size of a repo's storage subtree — the real,
// meterable storage number recorded on create and after each push.
func (s *storage) sizeBytes(org, project, name string) (int64, error) {
	abs := filepath.Join(s.rootDir, filepath.FromSlash(repoRel(org, project, name)))
	var total int64
	err := filepath.WalkDir(abs, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("size walk: %w", err)
	}
	return total, nil
}

// transport builds a go-git server transport whose loader resolves the endpoint
// path to the repo's billy-backed storer. The smart-HTTP handlers derive their
// upload-pack / receive-pack sessions from this. Each request builds its own
// transport (sessions are cheap; the storer is the durable part).
func (s *storage) transport(org, project, name string) transport.Transport {
	st, err := s.storer(org, project, name)
	loader := staticLoader{storer: st, err: err}
	return server.NewServer(loader)
}

// staticLoader is a server.Loader that always returns the one storer we built
// for the request's repo, regardless of the endpoint path. The URL routing is
// already done by the HTTP layer (org/repo path params → this storer), so the
// loader does not re-parse the endpoint.
type staticLoader struct {
	storer storer.Storer
	err    error
}

func (l staticLoader) Load(*transport.Endpoint) (storer.Storer, error) {
	if l.err != nil {
		return nil, l.err
	}
	return l.storer, nil
}
