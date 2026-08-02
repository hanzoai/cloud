package risk

// ship_test.go is the durability contract: a record this plane acknowledged is
// in the org's durable object, not merely on a pod's disk.
//
// The plane opened per-tenant SQLite through cloud.OrgDB directly — the only app
// in the fleet that did — so nothing hydrated a file on open, nothing elected a
// writer, and nothing ever shipped. cloud is Recreate at one replica: an
// ungraceful termination lost every acknowledged decision since the volume was
// last intact, and the next pod would hydrate an older snapshot over it.

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
)

// TestTheRecordPlaneWalksTheFleetsOneDoor pins the structural half: this app
// opens per-org files through cloud.OrgStore, which is where hydrate, the
// writer election and the fenced ship live. A hand-rolled map over cloud.OrgDB
// compiles, serves, and is not durable.
func TestTheRecordPlaneWalksTheFleetsOneDoor(t *testing.T) {
	ents, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	var doors int
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		body, err := os.ReadFile(filepath.Clean(e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		doors += strings.Count(string(body), "cloud.NewOrgStore")
		if n := strings.Count(string(body), "cloud.OrgDB("); n > 0 {
			t.Errorf("%s opens a per-org file through cloud.OrgDB directly (%d times). That skips the "+
				"hydrate, the writer election and the fenced ship — every sibling plane goes through "+
				"cloud.OrgStore", e.Name(), n)
		}
	}
	if doors != 1 {
		t.Fatalf("this app builds %d per-org stores, want exactly 1 — two doors are two answers to "+
			"'which file does this tenant write'", doors)
	}
}

// TestARecordIsShippedBeforeItIsAcknowledged is ship-before-ack itself: the row
// is in the file when the ship runs, and the ship has finished before the
// caller is told anything.
func TestARecordIsShippedBeforeItIsAcknowledged(t *testing.T) {
	app, s := wireApp(t)
	tn, _ := qualify("hanzo", "acme")

	var ships atomic.Int64
	var sawRow atomic.Bool
	var done atomic.Bool
	s.State.shelf.pier.ship = func(got Tenant) (bool, error) {
		if got != tn {
			t.Errorf("shipped %q, want %q", got, tn)
		}
		ships.Add(1)
		db, err := s.State.shelf.open(got)
		if err != nil {
			return false, err
		}
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM decision`).Scan(&n); err != nil {
			return false, err
		}
		if n > 0 {
			sawRow.Store(true)
		}
		done.Store(true)
		return true, nil
	}

	code, body := req(t, app, http.MethodPost, "/v1/risk/decide", "acme", "u_acme",
		`{"stage":"payment","subject":{"kind":"account","id":"a-1"}}`)
	if code != http.StatusOK {
		t.Fatalf("decide = %d %s", code, body)
	}
	if ships.Load() == 0 {
		t.Fatal("the decision was acknowledged without the tenant's file being shipped — the record " +
			"lives only on this pod's disk, and a Recreate rollout that is not graceful loses it")
	}
	if !sawRow.Load() {
		t.Fatal("the ship ran before the decision was written, so what it shipped does not contain the " +
			"record it acknowledged")
	}
	if !done.Load() {
		t.Fatal("the response was produced before the ship finished")
	}
}

// TestAnUnackedShipIsNotAnAcknowledgement is the fail-secure half. A ship the
// fenced store did not admit means this replica is not (or is no longer) the
// org's writer: the row is in a local file the next hydrate overwrites.
// Answering 200 there is precisely the defect.
func TestAnUnackedShipIsNotAnAcknowledgement(t *testing.T) {
	for _, tc := range []struct {
		name  string
		acked bool
		err   error
	}{
		{"deposed", false, nil},
		{"store unreachable", false, errors.New("object store timed out")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app, s := wireApp(t)
			s.State.shelf.pier.ship = func(Tenant) (bool, error) { return tc.acked, tc.err }

			code, body := req(t, app, http.MethodPost, "/v1/risk/decide", "acme", "u_acme",
				`{"stage":"payment","subject":{"kind":"account","id":"a-1"}}`)
			if code != http.StatusServiceUnavailable {
				t.Fatalf("decide = %d %s, want 503 — a record that did not reach the durable object was "+
					"reported to the caller as recorded", code, body)
			}
		})
	}
}

// TestAReadIsNotAShip is the affordability half. Ship-before-ack is installed on
// the group and therefore runs for every request; a request that wrote nothing
// must not ship a file.
func TestAReadIsNotAShip(t *testing.T) {
	app, s := wireApp(t)
	var ships atomic.Int64
	s.State.shelf.pier.ship = func(Tenant) (bool, error) { ships.Add(1); return true, nil }

	// One write, to reach the shipped watermark.
	if code, body := req(t, app, http.MethodPost, "/v1/risk/decide", "acme", "u_acme",
		`{"stage":"payment","subject":{"kind":"account","id":"a-1"}}`); code != http.StatusOK {
		t.Fatalf("decide = %d %s", code, body)
	}
	after := ships.Load()
	for i := 0; i < 20; i++ {
		if code, body := req(t, app, http.MethodGet, "/v1/risk/decisions", "acme", "u_acme", ""); code != http.StatusOK {
			t.Fatalf("decisions = %d %s", code, body)
		}
	}
	if got := ships.Load() - after; got != 0 {
		t.Fatalf("twenty reads shipped the file %d times; a request that wrote nothing must cost a "+
			"counter read and nothing else", got)
	}
}

// TestConcurrentWritesShipOnce is the reason ship-before-ack is affordable on a
// path that answers authorisations: the ships COALESCE. Fifty concurrent
// decisions must not be fifty whole-file uploads.
func TestConcurrentWritesShipOnce(t *testing.T) {
	app, s := wireApp(t)
	var ships atomic.Int64
	s.State.shelf.pier.ship = func(Tenant) (bool, error) {
		ships.Add(1)
		time.Sleep(20 * time.Millisecond)
		return true, nil
	}

	const callers = 50
	var wg sync.WaitGroup
	codes := make([]int, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			codes[i], _ = req(t, app, http.MethodPost, "/v1/risk/decide", "acme", "u_acme",
				`{"stage":"payment","subject":{"kind":"account","id":"a-1"}}`)
		}(i)
	}
	wg.Wait()
	for i, c := range codes {
		if c != http.StatusOK {
			t.Fatalf("caller %d = %d", i, c)
		}
	}
	// Serialised, fifty callers would be fifty ships. Coalesced they are a
	// handful: one in flight, one queued behind it, repeated.
	if got := ships.Load(); got > callers/2 {
		t.Fatalf("%d callers cost %d ships; a whole-file ship per decision is a decision path that "+
			"cannot serve", callers, got)
	}
	if ships.Load() == 0 {
		t.Fatal("nothing shipped at all")
	}
}

// TestAShipAlreadyInFlightDoesNotAcknowledge is the correctness of the
// coalescing, stated at the pier where it lives.
//
// A ship that is ALREADY RUNNING snapshotted the file before this caller's write
// committed, so releasing the caller on it acknowledges a record that is not in
// the object — the coalescing would then be a way to lose exactly the writes it
// was meant to make cheap. Every caller waits for the ship AFTER the one in
// flight.
func TestAShipAlreadyInFlightDoesNotAcknowledge(t *testing.T) {
	tn, _ := qualify("hanzo", "acme")
	hold := make(chan struct{})
	var ships atomic.Int64
	p := newPier(func(Tenant) (bool, error) {
		if ships.Add(1) == 1 {
			<-hold // the first ship is still running when the second caller arrives
		}
		return true, nil
	})

	// Caller one starts ship one and blocks in it.
	first := make(chan struct{})
	go func() {
		_, _ = p.ack(context.Background(), tn)
		close(first)
	}()
	waitFor(t, func() bool { return ships.Load() == 1 }, "the first ship to start")

	// Caller two arrives while ship one is in flight. Its write is NOT in that
	// snapshot, so it must not be released by it.
	second := make(chan struct{})
	go func() {
		_, _ = p.ack(context.Background(), tn)
		close(second)
	}()
	select {
	case <-second:
		t.Fatal("a caller was acknowledged by a ship that began before its write committed — the row it " +
			"was told is durable is not in the object")
	case <-time.After(50 * time.Millisecond):
	}

	close(hold)
	<-first
	select {
	case <-second:
	case <-time.After(5 * time.Second):
		t.Fatal("the second caller was never released")
	}
	if got := ships.Load(); got != 2 {
		t.Fatalf("%d ships, want exactly 2 — one for each caller's own write", got)
	}
}

// TestAWaiterGivesUpOnItsOwnDeadline: a ship that hangs must not hold a request
// goroutine past the caller's context.
func TestAWaiterGivesUpOnItsOwnDeadline(t *testing.T) {
	tn, _ := qualify("hanzo", "acme")
	hold := make(chan struct{})
	t.Cleanup(func() { close(hold) })
	p := newPier(func(Tenant) (bool, error) { <-hold; return true, nil })

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	acked, err := p.ack(ctx, tn)
	if acked {
		t.Fatal("a caller that gave up was told its record is durable")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want the caller's own deadline", err)
	}
}

// TestTheBackgroundWalkShipsWhatItWrote: the tick queues versions, raises drift
// alarms and prunes trials with no request in sight. Nothing else is going to
// ship those.
func TestTheBackgroundWalkShipsWhatItWrote(t *testing.T) {
	_, s := wireAt(t, t.TempDir())
	tn, _ := qualify("hanzo", "acme")
	db, err := s.State.shelf.open(tn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	var ships atomic.Int64
	s.State.shelf.pier.ship = func(Tenant) (bool, error) { ships.Add(1); return true, nil }

	fill(t, db, "chall", trialDepth+pruneChunk)
	if err := watch(context.Background(), s, tn, db, time.Now()); err != nil {
		t.Fatalf("watch: %v", err)
	}
	if ships.Load() == 0 {
		t.Fatal("the background walk wrote to a tenant's file and never shipped it; those rows are lost " +
			"on an ungraceful termination and no request will ever ship them")
	}
}

// TestShutdownShipsTheFinalState: the snapshot teardown writes goes to a local
// disk the next pod may not have.
func TestShutdownShipsTheFinalState(t *testing.T) {
	_, s := wireAt(t, t.TempDir())
	tn, _ := qualify("hanzo", "acme")
	db, err := s.State.shelf.open(tn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO setting (key, value) VALUES ('probe', 'x')`); err != nil {
		t.Fatalf("write: %v", err)
	}
	var ships atomic.Int64
	s.State.shelf.pier.ship = func(Tenant) (bool, error) { ships.Add(1); return true, nil }
	if err := teardown(context.Background(), s); err != nil {
		t.Fatalf("teardown: %v", err)
	}
	if ships.Load() == 0 {
		t.Fatal("shutdown kept every model into local files and shipped none of them")
	}
}

// TestAnUnshippedShutdownIsAFailure: teardown reporting success while a tenant's
// file never reached the object would make the drain look clean.
func TestAnUnshippedShutdownIsAFailure(t *testing.T) {
	_, s := wireAt(t, t.TempDir())
	tn, _ := qualify("hanzo", "acme")
	db, err := s.State.shelf.open(tn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO setting (key, value) VALUES ('probe', 'x')`); err != nil {
		t.Fatalf("write: %v", err)
	}
	s.State.shelf.pier.ship = func(Tenant) (bool, error) { return false, nil }
	if err := teardown(context.Background(), s); err == nil {
		t.Fatal("teardown reported a clean drain while a tenant's records never reached the durable object")
	}
}

func waitFor(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// TestBothPrefixesShip: the surface is two groups — /v1/risk and /v1/ml — and
// the lifecycle leaves register a THIRD group object on the second prefix. A
// middleware installed on one of them and not reaching the others would leave a
// whole plane's records local-only, silently, and every test above would still
// pass.
func TestBothPrefixesShip(t *testing.T) {
	for _, tc := range []struct{ name, method, path, body string }{
		{"decide", http.MethodPost, "/v1/risk/decide", `{"stage":"payment","subject":{"kind":"account","id":"a-1"}}`},
		{"rule", http.MethodPost, "/v1/risk/rules", `{"name":"r","stage":"payment","action":"review","when":[{"fact":"amount.usd","op":">","value":"1"}]}`},
		{"train", http.MethodPost, "/v1/ml/train", `{"limit":1}`},
		{"schedule", http.MethodPut, "/v1/ml/schedule", `{"every":24}`},
		{"appetite", http.MethodPut, "/v1/ml/state/appetite", `{"review":0.02}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app, s := wireApp(t)
			s.State.shelf.pier.ship = func(Tenant) (bool, error) { return false, nil }
			code, body := req(t, app, tc.method, tc.path, "acme", "u_acme", tc.body)
			if code != http.StatusServiceUnavailable {
				t.Fatalf("%s %s = %d %s, want 503 — this leaf acknowledged a write the durable store "+
					"never admitted, so ship-before-ack does not reach this prefix",
					tc.method, tc.path, code, body)
			}
		})
	}
}

// BenchmarkKeepOnARead is what ship-before-ack costs a request that wrote
// nothing, which is most of them. It has to be a counter read and a map lookup
// or the middleware is a tax on the decision path.
func BenchmarkKeepOnARead(b *testing.B) {
	dir := b.TempDir()
	s, err := build(cloud.Deps{Logger: luxlog.New("riskbench"), DataDir: dir, Brand: "hanzo"})
	if err != nil {
		b.Fatalf("build: %v", err)
	}
	defer s.State.shelf.close()
	tn, _ := qualify("hanzo", "acme")
	db, err := s.State.shelf.open(tn)
	if err != nil {
		b.Fatalf("open: %v", err)
	}
	if _, err := s.State.shelf.ship(context.Background(), tn, db); err != nil {
		b.Fatalf("ship: %v", err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := s.State.shelf.ship(context.Background(), tn, db); err != nil {
			b.Fatalf("ship: %v", err)
		}
	}
}

// BenchmarkOneRuleRead is the reference the number above is read against: ONE of
// the several statements /v1/risk/decide already runs per request. The ship
// check has to be small NEXT TO THE OP, not small in the abstract.
func BenchmarkOneRuleRead(b *testing.B) {
	dir := b.TempDir()
	s, err := build(cloud.Deps{Logger: luxlog.New("riskbench"), DataDir: dir, Brand: "hanzo"})
	if err != nil {
		b.Fatalf("build: %v", err)
	}
	defer s.State.shelf.close()
	tn, _ := qualify("hanzo", "acme")
	db, err := s.State.shelf.open(tn)
	if err != nil {
		b.Fatalf("open: %v", err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := loadRules(db); err != nil {
			b.Fatalf("loadRules: %v", err)
		}
	}
}
