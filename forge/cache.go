package forge

// cache.go is the read-through store the forge's slow list endpoints answer
// from.
//
// # Why one exists at all
//
// The package comment says every read is a read OF the forge, and that remains
// true: nothing here is written, nothing is mirrored into a table, and no entry
// outlives [repoStale]. What this adds is a bounded STALENESS WINDOW on a read,
// which is a different object from the drifting second copy that comment
// refuses — a copy drifts because it is updated independently, and this is only
// ever overwritten by the forge's own answer.
//
// It exists because /orgs/{org}/repos is expensive in proportion to the org.
// Measured on git.hanzo.ai: a 1-repo org answers in ~1s, and the 64-repo org
// takes 10-22s, because the forge computes permissions and statistics per
// repository. Every surface in this package begins with that call — the board
// list IS it, and the milestone rollup opens with it — so uncached, each page
// view pays the whole cost again, and the shipped symptom is a board that never
// finishes loading.
//
// # The key is the ACTOR, never the org alone
//
// The forge answers these endpoints as the SUDOED USER, which is the whole
// point of [Client.As]: two members of one org legitimately see different
// repositories, and a private repo is invisible to a member who cannot read it.
// So the cache key carries the actor. Keyed by org alone, the first caller's
// visible set would be served to the second — a cross-user read of precisely
// the private repositories Sudo exists to keep apart, and a tenancy bug rather
// than a performance one.
//
// # Stale-while-revalidate, and why not a plain TTL
//
// Under a plain TTL the first caller after each expiry pays the full 22s, so on
// a busy board someone waits every minute forever. Serving a stale entry while
// ONE background refresh runs behind it means only a genuinely cold cache ever
// blocks, and the answer is never older than [repoStale].
//
// The refresh runs on a context detached from the request that triggered it
// (the request's own context is cancelled the moment its response is written,
// which would kill the refresh it just started) but is still bounded, so a
// wedged forge cannot leak goroutines.

import (
	"context"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

const (
	// repoFresh is how long an entry is served without touching the forge.
	//
	// Minutes rather than seconds because of what a refresh COSTS and what it
	// buys. A repository list changes when someone creates or archives a repo —
	// weekly, against a board opened many times an hour — while re-reading it
	// for the 250-repo org is fifty requests to the estate's slowest endpoint.
	// At a one-minute window an open board would put that load on the forge
	// every minute, per actor, to learn nothing had changed, and this endpoint
	// has been measured degrading from 13s to 22s under load.
	//
	// Nothing a board renders is stale for it: issues are not cached at all, so
	// cards, columns and assignees are always the forge's current answer. What
	// waits out this window is only a repository CREATED in the last few minutes
	// that has no work on it yet — and one that does have work appears at once,
	// because the board list is built from issues.
	repoFresh = 5 * time.Minute

	// repoStale is how long a fresh-expired entry may still be SERVED while a
	// refresh runs behind it. Past this an entry is not an answer any more and
	// a caller waits for the forge.
	repoStale = 30 * time.Minute

	// refreshBudget bounds a background refresh. It is not tied to any request,
	// so without this a wedged forge would hold the goroutine indefinitely.
	refreshBudget = 60 * time.Second
)

// entry is one cached answer and when the forge gave it.
type entry[T any] struct {
	val  T
	born time.Time
}

// cache is a read-through, stale-while-revalidate store for one value type.
//
// The zero value is NOT usable; build it with [newCache]. It is safe for
// concurrent use, and concurrent misses on one key collapse into a single
// upstream call — which matters when that call costs 20 seconds and a board
// load fires two of them (the project list and the milestone rollup both open
// with the repository list).
type cache[T any] struct {
	mu    sync.Mutex
	items map[string]entry[T]
	group singleflight.Group

	// now is time.Now, replaced in tests so the fresh/stale windows can be
	// crossed without sleeping through them.
	now func() time.Time
}

func newCache[T any]() *cache[T] {
	return &cache[T]{items: map[string]entry[T]{}, now: time.Now}
}

// lookup returns the entry for key and its age.
func (c *cache[T]) lookup(key string) (T, time.Duration, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.items[key]
	if !ok {
		var zero T
		return zero, 0, false
	}
	return e.val, c.now().Sub(e.born), true
}

// store overwrites key and drops every entry past [repoStale], which is what
// bounds the map: an actor who signs in once and never returns does not hold a
// repository list for the life of the process.
func (c *cache[T]) store(key string, v T) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	c.items[key] = entry[T]{val: v, born: now}
	for k, e := range c.items {
		if now.Sub(e.born) > repoStale {
			delete(c.items, k)
		}
	}
}

// do answers key from the cache, calling fn only when it has to.
//
//	age < repoFresh   the cached answer, and the forge is not touched
//	age < repoStale   the cached answer NOW, and one refresh runs behind it
//	otherwise         fn, and the caller waits for it
//
// Only a successful fn is stored: caching a failure would turn one bad minute
// on the forge into a bad minute for every caller, and an error here is
// reported rather than remembered.
func (c *cache[T]) do(ctx context.Context, key string, fn func(context.Context) (T, error)) (T, error) {
	if v, age, ok := c.lookup(key); ok {
		if age < repoFresh {
			return v, nil
		}
		if age < repoStale {
			c.refresh(ctx, key, fn)
			return v, nil
		}
	}
	return c.fetch(ctx, key, fn)
}

// warm answers only from what is already held, and starts ONE background fill
// when nothing is. The caller gets an immediate answer or an immediate no, and
// never a wait — which is the point: it is for values a caller would rather do
// without than block on.
func (c *cache[T]) warm(ctx context.Context, key string, fn func(context.Context) (T, error)) (T, bool) {
	if v, age, ok := c.lookup(key); ok && age < repoStale {
		if age >= repoFresh {
			c.refresh(ctx, key, fn)
		}
		return v, true
	}
	c.refresh(ctx, key, fn)
	var zero T
	return zero, false
}

// fetch calls fn once on behalf of every caller currently missing on key.
//
// The callers that queue behind the leader are BY CONSTRUCTION the same actor —
// the key carries the actor — so no caller can be made to wait on, or inherit
// the failure of, a request made as somebody else.
func (c *cache[T]) fetch(ctx context.Context, key string, fn func(context.Context) (T, error)) (T, error) {
	v, err, _ := c.group.Do(key, func() (any, error) {
		// Re-check under the flight: a caller that queued while the leader was
		// storing must read that answer rather than start a second call for it.
		if v, age, ok := c.lookup(key); ok && age < repoFresh {
			return v, nil
		}
		got, err := fn(ctx)
		if err != nil {
			return nil, err
		}
		c.store(key, got)
		return got, nil
	})
	if err != nil {
		var zero T
		return zero, err
	}
	return v.(T), nil
}

// refresh renews key behind the caller, at most one flight at a time.
//
// The context is DETACHED from the request's: that context is cancelled as soon
// as the response is written, so a refresh inheriting it would be killed by the
// very request it was started for and the entry would never be renewed. Values
// (a trace, a request id) are kept; only the cancellation is dropped, and a
// deadline is put back so the goroutine cannot outlive a wedged forge.
func (c *cache[T]) refresh(ctx context.Context, key string, fn func(context.Context) (T, error)) {
	go func() {
		bg, cancel := context.WithTimeout(context.WithoutCancel(ctx), refreshBudget)
		defer cancel()
		//nolint:errcheck // a refresh failure leaves the served entry in place;
		// the next caller past repoStale surfaces the error itself.
		_, _, _ = c.group.Do(key, func() (any, error) {
			got, err := fn(bg)
			if err != nil {
				return nil, err
			}
			c.store(key, got)
			return got, nil
		})
	}()
}
