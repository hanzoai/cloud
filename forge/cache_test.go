package forge

// What these pin, in order of how badly getting them wrong ends:
//
//  1. TENANCY. The cache key carries the ACTOR. Keyed by org alone it would
//     serve one user the repository set of whoever asked first — a cross-user
//     read of the private repos Sudo exists to keep apart. That is the test
//     that must never be deleted.
//  2. That a deadline still reaches the forge THROUGH the cache. The bug this
//     file exists to fix was an unbounded wait, and a cache that swallowed
//     cancellation would reintroduce it one layer up.
//  3. That the cache actually spares the forge the call — the whole point.
//  4. That a failure is not remembered, and that a caller cannot corrupt what
//     the next caller reads.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// countingForge answers the repo list, counts how often it was asked, and can be
// held mid-answer. Counting is what makes "the second caller did not ask again"
// observable at all; holding is what makes a slow forge reproducible without a
// slow test.
type countingForge struct {
	*httptest.Server
	calls atomic.Int64
	// hold, when non-nil, blocks every answer until it is closed.
	hold chan struct{}
	// body is the JSON the repo list answers with; swapped to prove a refresh
	// actually re-read the forge rather than replaying what it had. count is the
	// X-Total-Count that goes with it.
	mu    sync.Mutex
	body  string
	count int
}

func newCounting(t *testing.T) *countingForge {
	t.Helper()
	f := &countingForge{body: `[{"name":"api","full_name":"acme/api"}]`, count: 1}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/repos") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		f.calls.Add(1)
		if f.hold != nil {
			select {
			case <-f.hold:
			case <-r.Context().Done():
				return
			}
		}
		f.mu.Lock()
		b, n := f.body, f.count
		f.mu.Unlock()
		// As the real forge does, so the concurrent walk sees the whole list in
		// the first page and does not go asking for a second.
		w.Header().Set("X-Total-Count", strconv.Itoa(n))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(b))
	}))
	t.Cleanup(f.Server.Close)
	return f
}

// setBody swaps the answer AND the count that describes it, so the walk still
// sees a self-consistent forge.
func (f *countingForge) setBody(s string, n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.body, f.count = s, n
}

func (f *countingForge) client(t *testing.T) *Client {
	t.Helper()
	c, err := New(f.URL, "machine-token")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

// clock installs a movable clock on c's read cache and returns the mover.
// Installed BEFORE any goroutine exists so the assignment cannot race the
// background refresh that later reads it.
func clock(c *Client) func(time.Duration) {
	var off atomic.Int64
	base := time.Now()
	now := func() time.Time { return base.Add(time.Duration(off.Load())) }
	c.repos.now = now
	return func(d time.Duration) { off.Store(int64(d)) }
}

// ── 1. tenancy: the key carries the actor ────────────────────────────────────

// THE test. Two actors ask for the same org and see legitimately different
// repositories, because the forge answers as the sudoed user. A cache keyed by
// org alone would hand the second caller the first caller's answer, which is a
// private-repo disclosure dressed as a cache hit — and it would look like a
// working board while doing it.
func TestReposCache_KeyedByActorSoOneUserNeverSeesAnothersRepositories(t *testing.T) {
	s := newStub(t)
	s.visible["alice"] = []string{"acme"}    // a member: sees acme's repos
	s.visible["bob"] = []string{"othercorp"} // not a member of acme: sees none of them
	s.repos["acme"] = []Repo{
		{Name: "api", FullName: "acme/api"},
		{Name: "secret", FullName: "acme/secret", Private: true},
	}
	c := s.client(t)

	alice, err := c.As("alice").Repos(t.Context(), "acme")
	if err != nil {
		t.Fatalf("alice Repos: %v", err)
	}
	if len(alice) != 2 {
		t.Fatalf("alice should see acme's 2 repos, got %d", len(alice))
	}

	// Bob asks for the same org, immediately, while alice's answer is warm.
	bob, err := c.As("bob").Repos(t.Context(), "acme")
	if err != nil {
		t.Fatalf("bob Repos: %v", err)
	}
	if len(bob) != 0 {
		t.Fatalf("CROSS-USER CACHE READ: bob saw %d of acme's repos through alice's cache entry: %+v", len(bob), bob)
	}
	for _, r := range bob {
		if r.Private {
			t.Fatalf("bob was served the private repo %q", r.FullName)
		}
	}
}

// ── 2. a deadline still reaches the forge through the cache ──────────────────

// The regression this whole change exists for, asserted at the layer that could
// silently undo it: a cache sitting between the caller and the transport must
// pass cancellation DOWN, or the wait it was added to shorten becomes unbounded
// again one layer up.
func TestReposCache_ADeadlineStillReachesTheForge(t *testing.T) {
	f := newCounting(t)
	f.hold = make(chan struct{}) // never released: this forge does not answer
	c := f.client(t).As("alice")

	ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := c.Repos(ctx, "acme")
	took := time.Since(start)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want DeadlineExceeded through the cache, got %v", err)
	}
	if took > 5*time.Second {
		t.Fatalf("the deadline did not bound the call: took %s", took)
	}
}

// A forge that never answers must not be REMEMBERED as an empty board either.
func TestReposCache_AFailureIsNotRemembered(t *testing.T) {
	f := newCounting(t)
	f.hold = make(chan struct{})
	c := f.client(t).As("alice")

	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	if _, err := c.Repos(ctx, "acme"); err == nil {
		t.Fatal("want an error from a forge that does not answer")
	}
	// Now the forge works. The next caller must get the real list, not a cached
	// failure and not a cached empty set.
	close(f.hold)
	got, err := c.Repos(t.Context(), "acme")
	if err != nil {
		t.Fatalf("Repos after recovery: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("a failure was remembered: got %d repos, want 1", len(got))
	}
}

// ── 3. the cache actually spares the forge the call ──────────────────────────

func TestReposCache_SecondCallerDoesNotAskTheForgeAgain(t *testing.T) {
	f := newCounting(t)
	c := f.client(t).As("alice")

	for i := range 5 {
		if _, err := c.Repos(t.Context(), "acme"); err != nil {
			t.Fatalf("Repos %d: %v", i, err)
		}
	}
	if n := f.calls.Load(); n != 1 {
		t.Fatalf("5 board loads asked the forge %d times, want 1", n)
	}
}

// Twenty concurrent cold callers are ONE upstream call. This is what stops a
// 20-second endpoint from being asked twenty times at once when a deployment
// restarts and every open tab revalidates together.
func TestReposCache_ConcurrentMissesCollapseIntoOneCall(t *testing.T) {
	f := newCounting(t)
	f.hold = make(chan struct{})
	c := f.client(t).As("alice")

	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for range 20 {
		wg.Go(func() {
			_, err := c.Repos(t.Context(), "acme")
			errs <- err
		})
	}
	// Let them all arrive and queue behind the one in flight.
	time.Sleep(100 * time.Millisecond)
	close(f.hold)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent Repos: %v", err)
		}
	}
	if n := f.calls.Load(); n != 1 {
		t.Fatalf("20 concurrent callers made %d forge calls, want 1", n)
	}
}

// Past the fresh window the entry is still SERVED — immediately — and refreshed
// behind the caller. Under a plain TTL this caller would instead pay the full
// cold-path wait, which is the difference between one slow load after a restart
// and one slow load every minute forever.
func TestReposCache_StaleIsServedAtOnceAndRefreshedBehind(t *testing.T) {
	f := newCounting(t)
	c := f.client(t).As("alice")
	move := clock(c)

	if _, err := c.Repos(t.Context(), "acme"); err != nil {
		t.Fatalf("warm: %v", err)
	}
	// The forge now holds a DIFFERENT answer, so a served-stale read and a
	// refreshed read are distinguishable.
	f.setBody(`[{"name":"api","full_name":"acme/api"},{"name":"web","full_name":"acme/web"}]`, 2)
	move(repoFresh + time.Second)

	got, err := c.Repos(t.Context(), "acme")
	if err != nil {
		t.Fatalf("stale read: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("a stale read should answer from the cache at once, got %d repos", len(got))
	}

	// The refresh runs behind it; wait for the forge to be asked a second time.
	deadline := time.Now().Add(5 * time.Second)
	for f.calls.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if n := f.calls.Load(); n != 2 {
		t.Fatalf("the stale read did not trigger a refresh: %d forge calls, want 2", n)
	}
	// And the refreshed answer is what the next caller gets.
	got, err = c.Repos(t.Context(), "acme")
	if err != nil {
		t.Fatalf("post-refresh read: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("the refresh did not replace the entry: got %d repos, want 2", len(got))
	}
}

// Past the stale window an entry is no longer an answer, and the caller waits
// for the forge rather than being served something arbitrarily old.
func TestReposCache_PastTheStaleWindowTheCallerWaitsForTheForge(t *testing.T) {
	f := newCounting(t)
	c := f.client(t).As("alice")
	move := clock(c)

	if _, err := c.Repos(t.Context(), "acme"); err != nil {
		t.Fatalf("warm: %v", err)
	}
	f.setBody(`[{"name":"api","full_name":"acme/api"},{"name":"web","full_name":"acme/web"}]`, 2)
	move(repoStale + time.Second)

	got, err := c.Repos(t.Context(), "acme")
	if err != nil {
		t.Fatalf("cold read: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("past the stale window the caller must get the forge's current answer, got %d repos", len(got))
	}
}

// ── 4. one caller cannot corrupt what the next one reads ─────────────────────

func TestReposCache_ACallerCannotMutateTheCachedList(t *testing.T) {
	f := newCounting(t)
	c := f.client(t).As("alice")

	first, err := c.Repos(t.Context(), "acme")
	if err != nil {
		t.Fatalf("Repos: %v", err)
	}
	first[0].Name = "clobbered"
	first[0].Archived = true

	second, err := c.Repos(t.Context(), "acme")
	if err != nil {
		t.Fatalf("Repos again: %v", err)
	}
	if second[0].Name != "api" || second[0].Archived {
		t.Fatalf("one caller rewrote the cached entry: %+v", second[0])
	}
}

// ── the credential rotates; the warm list must not be thrown away ────────────

func TestReuse_KeepsTheWarmListAcrossACredentialRotation(t *testing.T) {
	f := newCounting(t)
	old := f.client(t)
	if _, err := old.As("alice").Repos(t.Context(), "acme"); err != nil {
		t.Fatalf("warm: %v", err)
	}

	// The token is re-read from KMS and a new client built around it.
	fresh := f.client(t)
	fresh.Reuse(old)
	if _, err := fresh.As("alice").Repos(t.Context(), "acme"); err != nil {
		t.Fatalf("after rotation: %v", err)
	}
	if n := f.calls.Load(); n != 1 {
		t.Fatalf("rotating the credential threw away the cache: %d forge calls, want 1", n)
	}

	// Without Reuse it is genuinely a new cache — otherwise the test above would
	// pass for the wrong reason (a package-level cache shared by every client,
	// which would be a cross-DEPLOYMENT leak).
	lone := f.client(t)
	if _, err := lone.As("alice").Repos(t.Context(), "acme"); err != nil {
		t.Fatalf("lone client: %v", err)
	}
	if n := f.calls.Load(); n != 2 {
		t.Fatalf("a client built without Reuse shared another's cache: %d forge calls, want 2", n)
	}
}

// An unscoped client refuses BEFORE it can take a slot in the cache, so a
// missing actor can never be cached as an answer under an empty key.
func TestReposCache_UnscopedClientRefusesWithoutCaching(t *testing.T) {
	f := newCounting(t)
	c := f.client(t) // no As()

	if _, err := c.Repos(t.Context(), "acme"); !errors.Is(err, ErrNoActor) {
		t.Fatalf("want ErrNoActor, got %v", err)
	}
	if n := f.calls.Load(); n != 0 {
		t.Fatalf("an unscoped client reached the forge %d times", n)
	}
}

// ── the repository walk is concurrent ────────────────────────────────────────

// The performance fix, pinned as a PROPERTY rather than a stopwatch reading: the
// pages of one repository list must be in flight AT THE SAME TIME. A serial walk
// of the same org is ~7x slower against the real forge, and nothing else in the
// test suite would notice it had regressed.
//
// The stub answers only when it has seen every page it expects, so a serial walk
// deadlocks and fails on the test's own timeout while a concurrent one passes.
func TestListRepos_PagesAreFetchedConcurrently(t *testing.T) {
	const total = 64
	wantPages := (total + repoPage - 1) / repoPage

	var (
		mu       sync.Mutex
		inFlight int
		peak     int
	)
	arrived := make(chan struct{}, wantPages)
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pg, _ := strconv.Atoi(r.URL.Query().Get("page"))
		mu.Lock()
		inFlight++
		peak = max(peak, inFlight)
		mu.Unlock()

		if pg > 1 {
			// Page 1 must answer immediately — it is what carries the count the
			// walk pages off. Every later page waits until they have all arrived,
			// which only happens if they were issued together.
			arrived <- struct{}{}
			select {
			case <-release:
			case <-r.Context().Done():
			}
		}
		mu.Lock()
		inFlight--
		mu.Unlock()

		body := []Repo{}
		start := (pg - 1) * repoPage
		for i := start; i < min(start+repoPage, total); i++ {
			body = append(body, Repo{Name: fmt.Sprintf("r%d", i), FullName: fmt.Sprintf("acme/r%d", i)})
		}
		w.Header().Set("X-Total-Count", strconv.Itoa(total))
		writeJSON(w, body)
	}))
	t.Cleanup(srv.Close)

	c, err := New(srv.URL, "machine-token")
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	done := make(chan []Repo, 1)
	errc := make(chan error, 1)
	go func() {
		got, err := c.As("alice").Repos(t.Context(), "acme")
		if err != nil {
			errc <- err
			return
		}
		done <- got
	}()

	// Wait for every page past the first to be in flight simultaneously. A
	// serial walk never gets here, because page 2 is still blocked when page 3
	// would have been issued.
	deadline := time.After(15 * time.Second)
	for range wantPages - 1 {
		select {
		case <-arrived:
		case err := <-errc:
			t.Fatalf("Repos: %v", err)
		case <-deadline:
			mu.Lock()
			n := peak
			mu.Unlock()
			t.Fatalf("the repository walk is SERIAL: only %d page(s) were ever in flight at once, want %d", n, wantPages-1)
		}
	}
	close(release)

	select {
	case got := <-done:
		if len(got) != total {
			t.Fatalf("got %d repos, want %d — a concurrent walk must still return the whole list", len(got), total)
		}
		// Page order, not completion order: two identical reads must not differ.
		for i, r := range got {
			if want := fmt.Sprintf("r%d", i); r.Name != want {
				t.Fatalf("repo %d is %q, want %q — pages were concatenated out of order", i, r.Name, want)
			}
		}
	case err := <-errc:
		t.Fatalf("Repos: %v", err)
	case <-time.After(15 * time.Second):
		t.Fatal("the walk did not finish after its pages were released")
	}

	mu.Lock()
	defer mu.Unlock()
	if peak < 2 {
		t.Fatalf("peak concurrency was %d: the pages were not in flight together", peak)
	}
}
