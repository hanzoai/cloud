package cloud

// orgdb_bound_test.go — THE OPEN BOUND IS THE DEPLOYMENT'S, AND SAYING IT IS OVER
// IS ONE LINE.
//
// Production ran 379 orgs against a bound of 256, and the admin directory reads
// every org's wallet every five minutes — exactly StoreIdleAfter. Reclaim takes only
// a store idle that long, so each pass evicted the ledgers it was about to read
// again: 371 fenced ships and re-hydrates in one minute, every five minutes. Right
// after a restart nothing is idle yet, so the warning fired on every For — hundreds
// of identical lines naming a constant nobody could change.

import (
	"bytes"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/sdk/principal"
	luxlog "github.com/luxfi/log"
	"golang.org/x/sync/errgroup"
)

// TestOpenBoundIsTheDeploymentsOwn: CLOUD_OPEN_STORES reaches every OrgStore, the
// default stands when it is unset, and a value that is not a positive integer
// refuses boot rather than quietly running on the bound it was set to leave.
func TestOpenBoundIsTheDeploymentsOwn(t *testing.T) {
	base := func(open string) *Config {
		return &Config{Brand: "hanzo", Domain: "api.hanzo.ai", DataDir: "/var/lib/cloud", OpenStores: open}
	}
	for _, c := range []struct {
		raw  string
		want int
		ok   bool
	}{
		{"", MaxOpenPerStore, true},
		{"1024", 1024, true},
		{" 2048 ", 2048, true},
		{"0", 0, false},
		{"-5", 0, false},
		{"1k", 0, false},
	} {
		got, err := base(c.raw).Stores()
		if (err == nil) != c.ok || got != c.want {
			t.Errorf("Stores(%q) = %d, %v; want %d, ok=%v", c.raw, got, err, c.want, c.ok)
		}
		if verr := base(c.raw).Validate(); (verr == nil) != c.ok {
			t.Errorf("Validate with CLOUD_OPEN_STORES=%q: %v, want ok=%v", c.raw, verr, c.ok)
		}
	}

	t.Setenv("CLOUD_OPEN_STORES", "1024")
	if got := NewOrgStore(Base{DataDir: t.TempDir()}, "widget", openRows).maxOpen; got != 1024 {
		t.Errorf("OrgStore runs at %d, want the deployment's 1024", got)
	}
	t.Setenv("CLOUD_OPEN_STORES", "")
	if got := NewOrgStore(Base{DataDir: t.TempDir()}, "widget", openRows).maxOpen; got != MaxOpenPerStore {
		t.Errorf("OrgStore with no stated bound runs at %d, want MaxOpenPerStore %d", got, MaxOpenPerStore)
	}
}

// TestASweepInsideTheBoundEvictsNothing is the production shape: one sweep over
// every org, then the next sweep after every store has gone idle. Over more orgs than
// the default bound, a store sized from the deployment holds the whole set across
// both passes — and the default, at the same sweep, thrashes, which is what the
// deployment was paying for.
func TestASweepInsideTheBoundEvictsNothing(t *testing.T) {
	orgs := MaxOpenPerStore + 44
	const idle = 50 * time.Millisecond
	// A sweep asks for every org, sixteen at a time: opening a store and releasing one
	// each wait on the disk, and one after another that wait is the whole test. What
	// is counted does not turn on the order — a set inside the bound is never over it,
	// and one past it is over it by the same number however the asks interleave.
	sweep := func(store *OrgStore[*sql.DB]) {
		var g errgroup.Group
		g.SetLimit(16)
		for i := range orgs {
			g.Go(func() error {
				_, err := store.For(principal.MustOrgNamespace(fmt.Sprintf("org%04d", i), ""))
				return err
			})
		}
		if err := g.Wait(); err != nil {
			t.Fatal(err)
		}
	}
	run := func(bound string) int64 {
		t.Setenv("CLOUD_OPEN_STORES", bound)
		store := NewOrgStore(Base{DataDir: t.TempDir()}, "widget", openRows)
		store.idleAfter = idle
		t.Cleanup(func() { _ = store.CloseAll() })
		sweep(store)
		time.Sleep(2 * idle) // every store is now past the deadline
		sweep(store)
		return store.evictions.Load()
	}

	if got := run(fmt.Sprint(2 * MaxOpenPerStore)); got != 0 {
		t.Fatalf("%d evictions sweeping %d orgs at a bound of %d: a set inside the bound must stay open across passes", got, orgs, 2*MaxOpenPerStore)
	}
	if got := run(""); got < int64(orgs-MaxOpenPerStore) {
		t.Fatalf("only %d evictions sweeping %d orgs at the default bound %d — the thrash this test exists to contrast is not reproduced", got, orgs, MaxOpenPerStore)
	} else {
		t.Logf("default bound: %d evictions over two sweeps of %d orgs", got, orgs)
	}
}

// TestOverTheBoundIsSaidOncePerEpisode: reclaim runs on every For, so a warning
// there is a warning per call unless it remembers it already spoke. It says the
// fact on entering the state, says it has recovered on leaving, and says it again
// on the next entry — two episodes, two warnings, however many calls each spans.
func TestOverTheBoundIsSaidOncePerEpisode(t *testing.T) {
	var buf bytes.Buffer
	store := NewOrgStore(Base{DataDir: t.TempDir(), Log: luxlog.New("test").Output(&buf)}, "widget", openRows)
	store.maxOpen = 2
	store.idleAfter = time.Hour // nothing ages out: every store is live
	t.Cleanup(func() { _ = store.CloseAll() })

	open := func(prefix string, n int) {
		for i := range n {
			if _, err := store.For(principal.MustOrgNamespace(fmt.Sprintf("%s%02d", prefix, i), "")); err != nil {
				t.Fatal(err)
			}
		}
	}
	count := func(msg string) int { return strings.Count(buf.String(), msg) }
	const over, back = "org stores over the open bound", "org stores back within the open bound"

	open("live", 10) // eight calls over the bound
	if got := count(over); got != 1 {
		t.Fatalf("said %d times over eight calls in one episode, want once:\n%s", got, buf.String())
	}

	// Everything ages out; the next call reclaims down to the bound and says so.
	store.mu.Lock()
	store.idleAfter = time.Millisecond
	store.mu.Unlock()
	time.Sleep(5 * time.Millisecond)
	open("live", 1)
	if got := count(back); got != 1 {
		t.Fatalf("recovery said %d times, want once:\n%s", got, buf.String())
	}

	store.mu.Lock()
	store.idleAfter = time.Hour
	store.mu.Unlock()
	open("next", 5) // a second episode
	if got := count(over); got != 2 {
		t.Fatalf("said %d times across two episodes, want twice:\n%s", got, buf.String())
	}
}
