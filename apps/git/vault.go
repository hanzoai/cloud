package git

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/hanzoai/cloud/apps/git/objects"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/namespace"
)

// vault.go — where a repository's history is.
//
// A repository with no link has its history here, and here is the org's objects store
// (apps/git/objects): rows of hanzo-sql, read and written by any replica. A linked
// repository's directory is a cache of its host (reclaim.go) and is not in the store.
//
// A repository made before the store existed has a bare directory and no rows. The first
// use of it brings it in: every object and ref is copied in one transaction, the copy is
// checked against the directory (the same objects, the same refs, every object hashing to
// its name), and the directory is made read-only, its bytes untouched, so the old copy is
// a rollback and not a second writer. A repository that cannot be brought in, because the
// directory is damaged or the copy does not match, stays what it was and is served from
// the directory by the git CLI, as it always was; it is tried again after retryAdopt.
type vault struct {
	// sweeping is the background bringing-in (run); Shutdown waits for it. It is not
	// the service's arriving group, which a test and a deploy wait on while the
	// service lives.
	sweeping sync.WaitGroup
	mu       sync.Mutex
	adopting map[string]*sync.Mutex
	failed   map[string]time.Time
	// audited makes the directory audit (audit.go) run once a process.
	audited sync.Once
}

func newVault() *vault {
	return &vault{adopting: map[string]*sync.Mutex{}, failed: map[string]time.Time{}}
}

// retryAdopt is how long a repository that failed to come in waits before it is tried
// again: a copy of a large repository is minutes of work to fail at.
const retryAdopt = 10 * time.Minute

// objectsFor is the org's objects store, opened once.
func objectsFor(s *cloud.Service[state], org string) (*objects.Store, error) {
	if !orgRE.MatchString(org) {
		return nil, fmt.Errorf("git: org %q is not an org name", org)
	}
	ns, err := principal.OrgNamespace(org, "")
	if err != nil {
		return nil, err
	}
	return s.State.objects.For(ns)
}

// home is the objects store holding r's history, or nil when r's history is a directory
// (a repository that could not be brought in) or, for a linked repository, not fetched yet. A
// repository with a bare directory and no rows is brought in first.
func (v *vault) home(ctx context.Context, s *cloud.Service[state], r Repo) (*objects.Store, error) {
	objs, err := objectsFor(s, r.Org)
	if err != nil {
		return nil, err
	}
	ok, err := objs.Has(ctx, r.ID)
	if err != nil {
		return nil, err
	}
	if ok {
		return objs, nil
	}
	if r.Origin != "" {
		// A linked repository is filled from its host when it is first read (reading).
		return nil, nil
	}
	if !s.State.storage.exists(r.Org, r.Project, r.Name) {
		return nil, nil
	}
	return v.adopt(ctx, s, objs, r)
}

// init makes the empty repository r names: rows in the store for one whose history is
// here, a bare directory for the cache of a linked one.
func (v *vault) init(ctx context.Context, s *cloud.Service[state], r Repo) error {
	objs, err := objectsFor(s, r.Org)
	if err != nil {
		return err
	}
	if err := objs.Create(ctx, r.ID, cmp.Or(r.DefaultBranch, defaultBranchName)); err != nil && !errors.Is(err, objects.ErrExists) {
		return err
	}
	return nil
}

// remove deletes everything of r: its rows and whatever directory it had.
func (v *vault) remove(ctx context.Context, s *cloud.Service[state], r Repo) error {
	objs, err := objectsFor(s, r.Org)
	if err != nil {
		return err
	}
	return errors.Join(objs.Remove(ctx, r.ID), s.State.storage.remove(r.Org, r.Project, r.Name))
}

// size is the bytes r occupies: its rows' stored bytes, or its directory's.
func (v *vault) size(ctx context.Context, s *cloud.Service[state], org, project, name string) (int64, error) {
	store, err := storeFor(s, org)
	if err != nil {
		return 0, err
	}
	r, err := store.Get(ctx, org, project, name)
	if err != nil {
		return 0, err
	}
	objs, err := v.home(ctx, s, r)
	if err != nil {
		return 0, err
	}
	if objs != nil {
		return objs.Size(ctx, r.ID)
	}
	return s.State.storage.sizeBytes(org, project, name)
}

// adopt brings r's directory into the store, once, however many requests ask at once.
// It answers the store when r is in it and nil when r stays a directory.
func (v *vault) adopt(ctx context.Context, s *cloud.Service[state], objs *objects.Store, r Repo) (*objects.Store, error) {
	key := r.Org + "/" + r.Project + "/" + r.Name
	v.mu.Lock()
	if at, ok := v.failed[key]; ok && time.Since(at) < retryAdopt {
		v.mu.Unlock()
		return nil, nil
	}
	lock, ok := v.adopting[key]
	if !ok {
		lock = &sync.Mutex{}
		v.adopting[key] = lock
	}
	v.mu.Unlock()
	lock.Lock()
	defer lock.Unlock()
	defer func() {
		v.mu.Lock()
		delete(v.adopting, key)
		v.mu.Unlock()
	}()

	if ok, err := objs.Has(ctx, r.ID); err != nil {
		return nil, err
	} else if ok {
		return objs, nil
	}
	// The copy is not the caller's to abandon: a client that hangs up after a minute of
	// a ten-minute copy has not asked for it to stop.
	bg := context.WithoutCancel(ctx)
	stop := warm(bg, s, r.Org)
	defer stop()
	started := time.Now()
	if err := copyDir(bg, s, objs, r); err != nil {
		s.Log.Warn("git.vault adopt failed; the directory keeps serving", "org", r.Org, "project", r.Project, "repo", r.Name, "err", err)
		v.mu.Lock()
		v.failed[key] = time.Now()
		v.mu.Unlock()
		return nil, nil
	}
	if err := s.State.storage.freeze(r.Org, r.Project, r.Name); err != nil {
		s.Log.Warn("git.vault adopt: directory left writable", "org", r.Org, "repo", r.Name, "err", err)
	}
	s.Log.Info("git.vault adopted", "org", r.Org, "project", r.Project, "repo", r.Name, "took", time.Since(started).Round(time.Millisecond).String())
	return objs, nil
}

// warm touches the org's objects store every minute until stop is called, so a long
// copy or a long clone is not a store reclaim may think idle (cloud.StoreIdleAfter).
func warm(ctx context.Context, s *cloud.Service[state], org string) (stop func()) {
	ctx, cancel := context.WithCancel(ctx)
	go func() {
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				_, _ = objectsFor(s, org)
			}
		}
	}()
	return cancel
}

// copyDir copies r's directory into the store in one transaction and checks the copy
// against it before the transaction commits: every object is named by its content (a
// damaged object hashes to something else and fails the copy), the store holds as many
// objects as the directory, every ref is where it was, and every branch can be read to
// the root. Nothing is kept unless all of it holds.
func copyDir(ctx context.Context, s *cloud.Service[state], objs *objects.Store, r Repo) error {
	src, err := s.State.storage.storer(r.Org, r.Project, r.Name)
	if err != nil {
		return err
	}
	tx, err := objs.Make(ctx, r.ID, cmp.Or(r.DefaultBranch, defaultBranchName))
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	it, err := src.IterEncodedObjects(plumbing.AnyObject)
	if err != nil {
		return fmt.Errorf("list objects: %w", err)
	}
	distinct := map[plumbing.Hash]struct{}{}
	err = it.ForEach(func(o plumbing.EncodedObject) error {
		h, err := tx.SetEncodedObject(o)
		if err != nil {
			return fmt.Errorf("object %s: %w", o.Hash(), err)
		}
		if h != o.Hash() {
			return fmt.Errorf("object %s hashes to %s", o.Hash(), h)
		}
		distinct[h] = struct{}{}
		return nil
	})
	if err != nil {
		return err
	}

	refs, err := src.IterReferences()
	if err != nil {
		return fmt.Errorf("list refs: %w", err)
	}
	var want []*plumbing.Reference
	tips := map[plumbing.ReferenceName]plumbing.Hash{}
	if err := refs.ForEach(func(ref *plumbing.Reference) error {
		want = append(want, ref)
		if ref.Type() == plumbing.HashReference {
			tips[ref.Name()] = ref.Hash()
		}
		return nil
	}); err != nil {
		return err
	}
	for _, ref := range want {
		if err := tx.SetReference(ref); err != nil {
			return fmt.Errorf("ref %s: %w", ref.Name(), err)
		}
	}
	if shallow, err := src.Shallow(); err == nil && len(shallow) > 0 {
		if err := tx.SetShallow(shallow); err != nil {
			return err
		}
	}

	if n, err := tx.Count(); err != nil {
		return err
	} else if n != int64(len(distinct)) {
		return fmt.Errorf("the store holds %d objects and the directory %d", n, len(distinct))
	}
	for _, ref := range want {
		got, err := tx.Reference(ref.Name())
		if err != nil || got.Type() != ref.Type() || got.Hash() != ref.Hash() || got.Target() != ref.Target() {
			return fmt.Errorf("ref %s is %v after the copy and was %v", ref.Name(), got, ref)
		}
	}
	// HEAD is symbolic and may point at a branch nobody has pushed yet, and an unborn HEAD
	// is no tip; every other ref must lead to objects the copy holds.
	if n, err := tx.CountLooseRefs(); err != nil {
		return err
	} else if n != len(want) {
		return fmt.Errorf("the store holds %d refs and the directory %d", n, len(want))
	}
	if err := tx.Connected(tips); err != nil {
		return err
	}
	return tx.Commit()
}

// freeze makes a repository's directory read-only, files and folders, and leaves every
// byte as it was. A directory a pod still mounted from before the store would otherwise
// take a push the store never sees.
func (s *storage) freeze(org, project, name string) error {
	return filepath.WalkDir(s.absRepoPath(org, project, name), func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		mode := os.FileMode(0o444)
		if d.IsDir() {
			mode = 0o555
		}
		return os.Chmod(path, mode)
	})
}

// thaw undoes freeze for a directory about to be deleted: a folder cannot lose its
// children while it is read-only.
func (s *storage) thaw(org, project, name string) {
	_ = filepath.WalkDir(s.absRepoPath(org, project, name), func(path string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			_ = os.Chmod(path, 0o755)
		}
		return nil
	})
}

// headBranch is the branch HEAD names, short: the repository's default branch as git
// reads it, or "" when HEAD names none.
func headBranch(repo *objects.Repo) string {
	ref, err := repo.Reference(plumbing.HEAD)
	if err != nil || ref.Type() != plumbing.SymbolicReference {
		return ""
	}
	return ref.Target().Short()
}

// tipsOf is every branch of repo and the commit it is at.
func tipsOf(repo *objects.Repo) map[string]string {
	refs, err := repo.References()
	if err != nil {
		return nil
	}
	tips := map[string]string{}
	for _, ref := range refs {
		if ref.Type() == plumbing.HashReference && ref.Name().IsBranch() {
			tips[ref.Name().Short()] = ref.Hash().String()
		}
	}
	return tips
}

// run brings in the directories this host still holds, smallest first, a minute after
// start and then every retryAdopt, so a repository nobody asks for is not left on a disk
// a replica is about to lose. Opening one is adopting it (home).
//
// It runs where the store is hanzo-sql, which is the deployment that is about to lose its
// volume. Without the DSN the store is a file beside the directories, and copying every
// repository of an org into a second file on the same volume is a cost nobody asked for:
// there a directory is brought in when it is first used, as it is everywhere.
func (v *vault) run(s *cloud.Service[state]) {
	if locus.DSN() == "" {
		return
	}
	timer := time.NewTimer(time.Minute)
	defer timer.Stop()
	for {
		select {
		case <-s.State.life.Done():
			return
		case <-timer.C:
		}
		v.sweep(s)
		v.auditOnce(s)
		timer.Reset(retryAdopt)
	}
}

// maintainEvery is how often the packs of the stores this replica owns are compacted and
// the blobs nothing needs are collected. A push adds a pack, so a repository that is
// pushed to all day is merged back to one a few times a day, not on every push.
const maintainEvery = 10 * time.Minute

// maintain compacts and collects each org's objects store, one org at a time, on the
// replica that owns it (cloud.OrgStore.Owned: a lease, so two replicas do not repeat
// each other's work, and one that dies is taken over). Compaction is safe to run
// anywhere at any time; the lease only saves the work.
func (v *vault) maintain(s *cloud.Service[state]) {
	timer := time.NewTimer(2 * time.Minute)
	defer timer.Stop()
	for {
		select {
		case <-s.State.life.Done():
			return
		case <-timer.C:
		}
		ctx := s.State.life
		_ = s.State.objects.Each(func(ns namespace.Namespace, st *objects.Store, err error) {
			if err != nil || ctx.Err() != nil || !s.State.objects.Owned(ns) {
				return
			}
			compacted, collected, err := st.Maintain(ctx)
			if err != nil {
				s.Log.Warn("git.objects maintenance failed", "ns", ns.ID(), "err", err)
				return
			}
			if compacted+collected > 0 {
				s.Log.Info("git.objects maintained", "ns", ns.ID(), "compacted", compacted, "collected", collected)
			}
		})
		timer.Reset(maintainEvery)
	}
}

func (v *vault) sweep(s *cloud.Service[state]) {
	ctx := s.State.life
	_ = s.State.stores.Each(func(ns namespace.Namespace, st *Store, err error) {
		if err != nil || ns.ID() == "" || ctx.Err() != nil {
			return
		}
		rows, err := st.ListOrg(ctx, ns.ID())
		if err != nil {
			return
		}
		sort.Slice(rows, func(i, j int) bool { return rows[i].SizeBytes < rows[j].SizeBytes })
		for _, r := range rows {
			if ctx.Err() != nil {
				return
			}
			if r.Origin == "" && s.State.storage.exists(r.Org, r.Project, r.Name) {
				_, _ = v.home(ctx, s, r)
			}
		}
	})
}
