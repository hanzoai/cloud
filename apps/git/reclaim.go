package git

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hanzoai/cloud/apps/git/objects"
	"github.com/hanzoai/cloud/sdk"
)

// reclaim.go — a linked repository lives in the objects store, like any other, and this
// file decides when it is current.
//
// # A repository with a LINK is the store's copy of its host
//
// A linked repository's history is on its host (link.go). Its copy here is rows in the
// org's objects store and packs in the bucket, filled from the host when it is missing
// (syncFromHost, linkstore.go), brought up to date when the host was last asked more than
// [linkFresh] ago, and served from the store by the same code that serves a repository
// whose history is here: a replica keeps nothing on its disk, and any replica serves any
// repository. Nothing here is bounded or released: the store is the bucket.
//
// What a replica does remember, for [linkFresh], is when it last asked the host, so a
// burst of reads is one question to the host and not one each; and a push that went up
// through here, or that the host reported, makes the next read ask again ([cache.stale]).

// linkFresh is how long a linked repository's copy answers before its host is asked
// again: a push to the host is read here at most this long after, and at once after a
// push that went up through here.
const linkFresh = 30 * time.Second

// held is what a replica knows of one linked repository: which link its last question
// was about and when the host last answered it.
type held struct {
	origin  string
	fetched time.Time
}

// cache is the process's memory of when it last asked each host.
type cache struct {
	mu    sync.Mutex
	repos map[string]*held
	// ttl is how long an answer holds (linkFresh). A field rather than a const read at
	// the call site: the policy is a value, so a test states it instead of waiting.
	ttl time.Duration
	// fetching holds one channel per repository being brought up to date, so N callers
	// who miss at the same instant cost ONE question to the host and one transaction.
	fetching map[string]chan struct{}
	// fetches counts questions that reached a host, so a test can tell a read the cache
	// answered from one its host did.
	fetches atomic.Int64
}

func newCache() *cache {
	return &cache{repos: map[string]*held{}, fetching: map[string]chan struct{}{}, ttl: linkFresh}
}

// stale makes a repo's next read ask its host: a push went up through here, or the host
// reported one, so the copy is behind.
func (c *cache) stale(org, project, name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if h, ok := c.repos[org+"/"+project+"/"+name]; ok {
		h.fetched = time.Time{}
	}
}

// fresh makes r's copy answer for its link: filled from the host when it holds nothing,
// brought up to date when the host last answered more than c.ttl ago, and left alone
// otherwise. N callers at once cost ONE question; the rest wait and then read the store.
// It reports whether it reached the host.
//
// A host that fails while a copy is here leaves that copy answering, and is asked again
// after c.ttl; with no copy here the read fails, and nothing half-fetched is left for
// the next one (the fetch is one transaction).
func (c *cache) fresh(ctx context.Context, s *cloud.Service[state], r Repo, k *link) (bool, error) {
	key := r.Org + "/" + r.Project + "/" + r.Name
	c.mu.Lock()
	h, ok := c.repos[key]
	if ok && h.origin == r.Origin && time.Since(h.fetched) < c.ttl {
		c.mu.Unlock()
		return false, nil
	}
	if ch, ok := c.fetching[key]; ok {
		c.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			return false, ctx.Err()
		}
		c.mu.Lock()
		h, ok := c.repos[key]
		current := ok && h.origin == r.Origin && !h.fetched.IsZero()
		c.mu.Unlock()
		if current {
			return false, nil
		}
		return false, fmt.Errorf("git: %s/%s was not fetched from %s", r.Org, r.Name, host(k.CloneURL))
	}
	ch := make(chan struct{})
	c.fetching[key] = ch
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.fetching, key)
		c.mu.Unlock()
		close(ch)
	}()

	here := holdsHistory(ctx, s, r)
	cred, err := s.State.links.cred(ctx, r.Org, k)
	if err == nil {
		c.fetches.Add(1)
		err = syncFromHost(ctx, s, r, k, cred)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	h, ok = c.repos[key]
	if !ok {
		h = &held{}
		c.repos[key] = h
	}
	h.origin = r.Origin
	switch {
	case err == nil:
		h.fetched = time.Now()
		s.Log.Info("git.cache fetched", "org", r.Org, "project", r.Project, "repo", r.Name, "link", r.Origin)
		return true, nil
	case here:
		h.fetched = time.Now()
		s.Log.Warn("git.cache fetch failed; the copy here answers", "org", r.Org, "repo", r.Name, "link", r.Origin, "err", err)
		return false, nil
	}
	delete(c.repos, key)
	return false, err
}

// holdsHistory is whether the store already holds refs for r.
func holdsHistory(ctx context.Context, s *cloud.Service[state], r Repo) bool {
	objs, err := objectsFor(s, r.Org)
	if err != nil {
		return false
	}
	if ok, err := objs.Has(ctx, r.ID); err != nil || !ok {
		return false
	}
	return hasHistory(objs.Repo(ctx, r.ID))
}

// materialize makes r readable and answers the bare directory to read it from, which is
// "" for every repository the store holds. A linked repository is read only by a caller
// its host lets read it (link.go), from the store, brought up to date first when it is
// missing or stale. The returned func ends the read and must be called.
//
// It is the ONE entry point from "this repo exists" (the row) to "its objects are
// readable", and every path that reads objects goes through it. Only a repository that
// could not be brought into the store from a directory (vault.go) has a directory.
func materialize(ctx context.Context, s *cloud.Service[state], r Repo) (string, func(), error) {
	if k, ok := linkOf(r); ok {
		if err := s.State.links.see(ctx, r.Org, k); err != nil {
			return "", nil, err
		}
		if _, err := s.State.cache.fresh(ctx, s, r, k); err != nil {
			return "", nil, err
		}
		return "", func() {}, nil
	}
	objs, err := s.State.vault.home(ctx, s, r)
	if err != nil {
		return "", nil, err
	}
	if objs != nil {
		return "", func() {}, nil
	}
	if !s.State.storage.exists(r.Org, r.Project, r.Name) {
		return "", nil, fmt.Errorf("git: %s/%s has no objects here and no link to fetch them from: %w",
			r.Org, r.Name, ErrNoRepository)
	}
	return s.State.storage.absRepoPath(r.Org, r.Project, r.Name), func() {}, nil
}

// open is r's objects ready to read, and the func that lets them go
// ([materialize]).
func open(ctx context.Context, s *cloud.Service[state], r Repo) (Repository, func(), error) {
	_, done, err := materialize(ctx, s, r)
	if err != nil {
		return nil, nil, err
	}
	repo, err := openRepository(ctx, s, r)
	if err != nil {
		done()
		return nil, nil, err
	}
	return repo, done, nil
}

// reach opens (org, project, name) by its row ([open]) as the caller on ctx: a
// reader that has a repository's coordinates and not its row, as an event does.
func reach(s *cloud.Service[state], ctx context.Context, org, project, name string) (Repository, func(), error) {
	store, err := storeFor(s, org)
	if err != nil {
		return nil, nil, err
	}
	r, err := store.Get(ctx, org, project, name)
	if err != nil {
		return nil, nil, err
	}
	return open(ctx, s, r)
}

// reading is the store r is read from, made ready as the caller on ctx: a linked
// repository is first brought up to date from its host ([materialize]). It answers nil
// for a repository that is still a directory.
func (v *vault) reading(ctx context.Context, s *cloud.Service[state], r Repo) (*objects.Store, error) {
	if _, ok := linkOf(r); ok {
		_, done, err := materialize(ctx, s, r)
		if err != nil {
			return nil, err
		}
		done()
	}
	return v.home(ctx, s, r)
}
