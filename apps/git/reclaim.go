package git

import (
	"context"
	"fmt"
	"github.com/hanzoai/cloud/internal/environ"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hanzoai/cloud"
)

// reclaim.go — the bare repos are a CACHE, and a cache has a bound.
//
// This store had none. Every repo a mirror ever brought in stayed for the life
// of the volume, so its size tracked how much had EVER been imported rather than
// how much was being served — 201.7G of it, measured, against 11G for every
// other thing on the same claim. That is what pinned the deployment to one
// replica: a claim that large is ReadWriteOnce, RWO attaches to one node, and
// the chart refuses a second pod rather than watch it hang on Multi-Attach.
//
// # A copy with an ORIGIN can be made again. That is the whole rule.
//
// Releasing a repo is safe exactly when its bytes can be fetched back, and the
// only thing that can say so is a source that has actually answered. So
// [Repo.Origin] is written by a mirror that SUCCEEDED (mirror.go), and a repo
// without one is PINNED — never a candidate, however cold, however large.
//
// The failure direction is the safe one in both halves: an unrecorded origin
// costs disk, and disk is recoverable. Releasing the only copy of something is
// not. Every repo that predates the column is therefore pinned, which is correct
// — the bytes on disk carry no remote (measured: `[core] bare = true` and
// nothing else), so nothing here can infer where they came from, and guessing is
// how you delete a customer's repository.
//
// # IDLE IS THE SAFETY CRITERION, SIZE IS THE TARGET
//
// The same split [cloud.OrgStore]'s reclaim makes, for the same reason: a caller
// holds a bare directory path across a multi-GB clone that outlives the call
// that handed it over. So a repo is a candidate only after [repoIdle] untouched
// AND with no live reader inside it, and among candidates the COLDEST goes
// first. Evicting the largest would free the most per pass and is the version
// that deletes a repository somebody is actively cloning.
//
// When the bound is exceeded and nothing is releasable, that is SAID and the
// store stays over. An over-target store is visible and recoverable; a repo
// deleted out from under its only copy is neither.

// repoIdle is how long a repo must go untouched before reclaim may release it.
// It is the safety half of the bound, not a tuning knob: [materialize] hands
// back a directory path its caller streams a pack out of long after the call
// returns, so anything more recent than this may still be in use. Five minutes
// is far longer than any request that holds one, and is the same figure
// cloud.StoreIdleAfter uses for the same hazard.
const repoIdle = 5 * time.Minute

// cacheEnv holds the store's ceiling in BYTES. Unset or 0 means unbounded, which
// is what every existing deployment gets until an operator picks a number — this
// mechanism must not start deleting repositories because a binary rolled.
//
// Bytes and not a repo count, because the constraint is a volume: 3,160 repos
// ranged from 36KB to 10.4GB when measured, so a count bounds nothing.
const cacheEnv = "GIT_CACHE_BYTES"

func cacheBytes() int64 {
	v := environ.Or(cacheEnv, "")
	if v == "" {
		return 0
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// held is one materialized repo: what it cost, where it came from, when it was
// last handed out, and how many callers are inside it right now.
type held struct {
	org, project, name string
	origin             string
	size               int64
	used               time.Time
	// readers is the number of callers currently streaming from this directory.
	// It is not derivable from `used`: a clone of a 10GB repo is one touch and
	// then twenty minutes of reading, so idle time alone would call it cold and
	// delete the directory mid-pack.
	readers int
}

func (h *held) key() string { return h.org + "/" + h.project + "/" + h.name }

// cache is the process's view of what it has materialized. It bounds what THIS
// process brings in, which is the growth; repos already on the volume that
// nothing asks for are not tracked, are never released here, and are the
// operator's move — see the migration note in LLM.md.
type cache struct {
	mu    sync.Mutex
	repos map[string]*held
	bytes int64
	// bound is the ceiling in bytes; 0 disables reclaim entirely, which is what
	// every deployment gets until an operator picks a number. idle is how long a
	// repo must go untouched to become a candidate. Fields rather than a const
	// read at the call site, for the reason cloud.OrgStore makes them fields: the
	// policy is a value, so a test states it instead of waiting five minutes for
	// one.
	bound int64
	idle  time.Duration
	// fetching holds one channel per repo being fetched back, so N callers who
	// miss at the same instant cost ONE fetch. Without it the second caller runs
	// `git init --bare` on a directory the first just made — one error — and,
	// past that, a second `git fetch` into the same objects directory.
	fetching map[string]chan struct{}
	// evictions counts repos released, so a test can tell "the bound held
	// because nothing needed releasing" from "the bound held because releasing
	// works" — two very different green runs.
	evictions atomic.Int64
	// pinned counts passes that ended over the bound with nothing releasable.
	// It is the number that says "this deployment is holding repositories it
	// cannot re-fetch", which is an operator fact and not a failure.
	pinned atomic.Int64
}

func newCache() *cache {
	return &cache{
		repos:    map[string]*held{},
		fetching: map[string]chan struct{}{},
		bound:    cacheBytes(),
		idle:     repoIdle,
	}
}

// enter records that a repo is being served and takes a reader on it, so
// reclaim cannot release the directory while the caller streams from it. The
// returned func gives the reader back and must be called.
func (c *cache) enter(org, project, name, origin string, size int64) func() {
	c.mu.Lock()
	defer c.mu.Unlock()
	k := org + "/" + project + "/" + name
	h, ok := c.repos[k]
	if !ok {
		h = &held{org: org, project: project, name: name}
		c.repos[k] = h
	}
	// The row is re-read on every serve, so origin and size follow it rather
	// than going stale at whatever they were when the repo was first touched.
	h.origin = origin
	c.bytes += size - h.size
	h.size = size
	h.used = time.Now()
	h.readers++
	return func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		h.readers--
		h.used = time.Now()
	}
}

// diverged drops the origin from a repo's LIVE entry, so a release cannot be
// decided on a copy of the row that was read before the write landed.
//
// The row is the durable fact and this is its in-process projection; they move
// together or they do not move. Read the other way — leaving reclaim to re-read
// the row for each candidate — that is a database read per repo under the lock,
// on the path every serve runs.
func (c *cache) diverged(org, project, name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if h, ok := c.repos[org+"/"+project+"/"+name]; ok {
		h.origin = ""
	}
}

// candidate picks the coldest releasable repo, or nil when there is none.
// Held by c.mu.
func (c *cache) candidate(now time.Time) *held {
	var cold *held
	for _, h := range c.repos {
		switch {
		case h.origin == "": // pinned: nothing can fetch these bytes back
		case h.readers > 0: // somebody is inside it
		case now.Sub(h.used) < c.idle: // may still be in a caller's hands
		case cold == nil || h.used.Before(cold.used):
			cold = h
		}
	}
	return cold
}

// reclaim releases repos, coldest first, while the store is over the bound.
// It runs after every serve, where in the ordinary case it is a comparison and
// a return.
//
// Removing the DIRECTORY is the whole eviction — there is no handle to close and
// no lease to hand back, because a bare repo is bytes and the metadata row that
// describes it stays. That row is what makes the repo still EXIST afterwards:
// [materialize] finds no directory, reads the origin off the row and fetches it
// again, so a released repo is slow on its next use and never missing.
func (c *cache) reclaim(ctx context.Context, s *cloud.Service[state]) {
	bound := c.bound
	if bound <= 0 {
		return
	}
	for {
		c.mu.Lock()
		if c.bytes <= bound {
			c.mu.Unlock()
			return
		}
		h := c.candidate(time.Now())
		if h == nil {
			over := c.bytes
			c.mu.Unlock()
			c.pinned.Add(1)
			// Say it, and stay over. The alternative is releasing a repo that
			// nothing can fetch back, which trades a recoverable condition for
			// an unrecoverable one.
			s.Log.Warn("git.cache over bound with nothing releasable",
				"bytes", over, "bound", bound, "repos", len(c.repos))
			return
		}
		// Out of the map FIRST, still under the lock, so a serve arriving in the
		// interval takes the miss path and re-fetches rather than reading a
		// directory that is being deleted underneath it.
		delete(c.repos, h.key())
		c.bytes -= h.size
		c.mu.Unlock()

		if err := s.State.storage.remove(h.org, h.project, h.name); err != nil {
			s.Log.Warn("git.cache release", "org", h.org, "repo", h.name, "err", err)
			continue
		}
		c.evictions.Add(1)
		s.Log.Info("git.cache released", "org", h.org, "project", h.project, "repo", h.name,
			"bytes", h.size, "origin", h.origin)
		if ctx.Err() != nil {
			return
		}
	}
}

// refetch brings a released repo back from its origin, ONCE however many callers
// miss at the same moment — the rest wait and then ask the DISK whether it
// worked, rather than having an error handed between them.
//
// A repo with no directory and no origin is one whose objects are simply not
// here, and that is an error rather than a fresh empty repository: provisioning
// one answers a clone with no HEAD, which reads to every client as corruption.
func (c *cache) refetch(ctx context.Context, s *cloud.Service[state], r Repo) error {
	key := r.Org + "/" + r.Project + "/" + r.Name
	c.mu.Lock()
	if ch, ok := c.fetching[key]; ok {
		c.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			return ctx.Err()
		}
		if s.State.storage.exists(r.Org, r.Project, r.Name) {
			return nil
		}
		return fmt.Errorf("git: %s/%s was not fetched back", r.Org, r.Name)
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

	if r.Origin == "" {
		return fmt.Errorf("git: %s/%s has no objects and no origin to fetch them from", r.Org, r.Name)
	}
	if err := s.State.storage.initBare(r.Org, r.Project, r.Name, r.DefaultBranch); err != nil {
		return fmt.Errorf("git: re-init %s/%s: %w", r.Org, r.Name, err)
	}
	if err := s.State.storage.mirrorInto(ctx, r.Org, r.Project, r.Name, r.Origin, gitCred{}); err != nil {
		// Leave nothing half-fetched: the next caller must take the miss path
		// again rather than serve a partial repository.
		_ = s.State.storage.remove(r.Org, r.Project, r.Name)
		return fmt.Errorf("git: refetch %s/%s: %w", r.Org, r.Name, err)
	}
	s.Log.Info("git.cache refetched", "org", r.Org, "project", r.Project, "repo", r.Name, "origin", r.Origin)
	return nil
}

// materialize returns the on-disk bare directory for a repo, fetching it back
// from its origin when reclaim has released it, and takes a reader so the
// directory cannot be released while the caller uses it. The returned func gives
// the reader back and runs reclaim; it must be called.
//
// It is the ONE entry point from "this repo exists" (the metadata row) to "its
// objects are here" (the directory). Before the bound those were the same
// statement; now they are not, and every path that shells out to git against
// absRepoPath goes through here to make them agree again.
func materialize(ctx context.Context, s *cloud.Service[state], r Repo) (string, func(), error) {
	dir := s.State.storage.absRepoPath(r.Org, r.Project, r.Name)
	if !s.State.storage.exists(r.Org, r.Project, r.Name) {
		if err := s.State.cache.refetch(ctx, s, r); err != nil {
			return "", nil, err
		}
		if n, err := s.State.storage.sizeBytes(r.Org, r.Project, r.Name); err == nil {
			r.SizeBytes = n
		}
	}
	done := s.State.cache.enter(r.Org, r.Project, r.Name, r.Origin, r.SizeBytes)
	return dir, func() {
		done()
		s.State.cache.reclaim(context.WithoutCancel(ctx), s)
	}, nil
}

// materializeAt is materialize for a caller that holds a coordinate rather than
// a row — every pack endpoint, which resolves (org, project, repo) from the request
// and its principal. The row read is the same one the endpoint would need anyway to
// know the repo exists at all.
func materializeAt(ctx context.Context, s *cloud.Service[state], org, project, name string) (string, func(), error) {
	store, err := storeFor(s, org)
	if err != nil {
		return "", nil, err
	}
	r, err := store.Get(ctx, org, project, name)
	if err != nil {
		return "", nil, err
	}
	return materialize(ctx, s, r)
}
