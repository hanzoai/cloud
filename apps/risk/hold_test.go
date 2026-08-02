package risk

// hold_test.go carries the proofs for the five findings that held this branch.
// Each one was written FIRST, run against the code that had the defect, and
// watched go red; the fix is what turns it green, and reintroducing the defect
// turns it red again.
//
// They live together rather than beside their subject because they are one
// story — three of the five are the SAME two defect classes reappearing one
// layer down:
//
//	class A   a bound on the COUNT of caller-sized values is not a bound.
//	          Bound the bytes, or cap the value at the wire door so that
//	          count x cap IS the byte bound.
//	class B   one store shared by every tenant with a global cap is a
//	          cross-tenant evictor. Per-tenant state, per-tenant bounds.
//	loud      a bound that binds, a model that disarms, a counter that is
//	          dropped: every one of them is a NAMED state an operator and the
//	          tenant can read. A control that switches off quietly is worse
//	          than no control.

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
	"unsafe"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// ── 1. the loud half of the per-tenant bound ────────────────────────────────

// TestStrainedIsTrueTheMomentACounterIsDropped is the whole design resting on
// one boolean.
//
// THE DEFECT: strained() read `vel.Keys() >= maxKeys()`, but the store evicts
// PER SHARD at MaxKeys/shardCount+1, and shard skew starts dropping keys long
// before the total reaches the bound. A tenant's own counters went missing
// while its decisions reported `strained: false` — a rule written as
// `velocity.ip.1h.count >= 5` stops firing on an evicted key and the decision
// reads clean. That is the silently-disarmed control, reproduced inside a single
// tenant.
//
// THE TEST: record distinct subjects one at a time and, at every step, require
// that the tenant is told the moment its live cardinality stops tracking what it
// recorded.
func TestStrainedIsTrueTheMomentACounterIsDropped(t *testing.T) {
	t.Setenv(envVelBytes, "1048576") // the floor, so the bound is reachable in a test
	_, s := wireApp(t)

	tn := Tenant("hanzo/acme")
	r := resOf(t, s, tn)
	at := time.Now()

	for i := 0; i < maxKeys()+16; i++ {
		r.record(observation{at: at, kind: "account", subject: fmt.Sprintf("s-%d", i), amount: 1_000_000_000})
		vel, _, _ := r.arms()
		if vel.keys() < i+1 && !r.strained() {
			t.Fatalf("after %d distinct subjects this tenant holds %d counters — %d of its own are gone — and strained is false",
				i+1, vel.keys(), i+1-vel.keys())
		}
	}
	if !r.strained() {
		t.Fatal("the tenant is past its own cardinality bound and strained is still false")
	}
}

// TestNothingIsEvictedInsideATenantsAggregates is the structural half.
//
// The engine's own eviction is LRU inside a shard, and it is invisible: no
// counter, no callback, nothing a caller can read. So this app does not use it.
// The store is built with a cardinality the app's own gate can never reach, and
// the gate is what refuses — loudly, and exactly. This test pins that the store
// really does hold every key the gate admitted, which is what makes
// `Keys() == admitted` true and therefore what makes `strained` exact.
func TestNothingIsEvictedInsideATenantsAggregates(t *testing.T) {
	t.Setenv(envVelBytes, "1048576")
	tn := Tenant("hanzo/acme")
	vel := aggregates()
	at := time.Now()
	n := maxKeys()
	for i := 0; i < n; i++ {
		vel.record(tn, observation{at: at, kind: "account", subject: fmt.Sprintf("s-%d", i), amount: 1_000_000_000}, nil)
	}
	// ASKED OF THE ENGINE, not of the ledger. `keys()` counts what the GATE
	// admitted and cannot see an eviction, so asserting on it would be true
	// whether or not the store dropped anything — the exact shape of test that
	// let the original defect through.
	if got := vel.stored(); got != n {
		t.Fatalf("the engine holds %d of the %d keys the gate admitted — its own per-shard eviction (MaxKeys/%d+1) is reachable under this app's gate, so a dropped counter is invisible again", got, n, velShards)
	}
	if got := vel.keys(); got != n {
		t.Fatalf("the ledger holds %d of the %d keys it admitted", got, n)
	}
	if got := vel.missed(); got != 0 {
		t.Fatalf("the gate refused %d keys inside its own bound", got)
	}
	// And the very next one is refused BY THE GATE, loudly, rather than
	// disappearing inside a shard.
	vel.record(tn, observation{at: at, kind: "account", subject: "one-too-many", amount: 1_000_000_000}, nil)
	if vel.keys() != n || vel.missed() != 1 || !vel.strained() {
		t.Fatalf("past the bound: keys=%d missed=%d strained=%v — want %d, 1, true",
			vel.keys(), vel.missed(), vel.strained(), n)
	}
}

// ── 2. the bound is on bytes, not on a count of caller-sized values ─────────

// TestThePublishedCeilingHoldsForTheLongestValueTheDoorAccepts is class A.
//
// THE DEFECT: every bound in this design was a COUNT — 1,387 keys, 1,024 agency
// answers, 20,000 list entries — over values the CALLER sizes. The published
// 8 MiB per-tenant ceiling was measured against a six-byte identifier; a 64 KiB
// subject id put ONE tenant at 100 MiB, thirteen times the number an operator
// was reading.
//
// THE TEST: measure a key whose value is the LONGEST the wire door will accept,
// and fail if the published per-key figure under-states it. `fmt.Sprintf("s-%d")`
// is not a worst case and a ceiling proven against it proves nothing.
func TestThePublishedCeilingHoldsForTheLongestValueTheDoorAccepts(t *testing.T) {
	tn := Tenant(strings.Repeat("o", textMax/2) + "/" + strings.Repeat("g", textMax/2-1))
	value := strings.Repeat("v", textMax)
	n := maxKeys()

	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	vel := aggregates()
	at := time.Now()
	for i := 0; i < n; i++ {
		vel.record(tn, observation{at: at, kind: "account", subject: fmt.Sprintf("%d%s", i, value[:textMax-8]), amount: 1_000_000_000}, nil)
	}
	runtime.GC()
	runtime.ReadMemStats(&after)
	runtime.KeepAlive(vel)

	if vel.keys() != n {
		t.Fatalf("the gate admitted %d of %d keys", vel.keys(), n)
	}
	measured := int(after.HeapAlloc-before.HeapAlloc) / n
	if measured > bytesPerKey() {
		t.Fatalf("a worst-case key measures %d B against a published ceiling of %d B — the per-tenant budget buys %d keys it cannot hold",
			measured, bytesPerKey(), maxKeys())
	}
	if held := n * measured; held > velBytes() {
		t.Fatalf("a full tenant measures %d B against a published per-tenant budget of %d B", held, velBytes())
	}
	t.Logf("worst-case key: measured %d B, published %d B; %d keys per tenant, %d B measured against a %d B budget",
		measured, bytesPerKey(), n, n*measured, velBytes())
}

// TestTheWireDoorRefusesAValueTheBoundCannotPrice is the other half of class A:
// the cap has to be enforced where the value ARRIVES, or the arithmetic above is
// about a value the app never sees.
//
// It is one middleware over both groups rather than a rule per field, because a
// rule per field is a rule the next field will not have.
func TestTheWireDoorRefusesAValueTheBoundCannotPrice(t *testing.T) {
	app, _ := wireApp(t)
	long := strings.Repeat("x", textMax+1)

	for _, c := range []struct {
		what   string
		method string
		path   string
		body   string
	}{
		{"a subject id", http.MethodPost, "/v1/risk/decide", `{"subject":{"kind":"account","id":"` + long + `"},"stage":"payment"}`},
		{"a signal value", http.MethodPost, "/v1/risk/decide", `{"subject":{"kind":"account","id":"a"},"signals":{"ip":"` + long + `"}}`},
		{"an agent reference", http.MethodPost, "/v1/risk/decide", `{"subject":{"kind":"account","id":"a"},"actor":{"agent":"` + long + `"}}`},
		{"a list entry", http.MethodPost, "/v1/risk/lists/deny-ip/entries", `{"values":["` + long + `"]}`},
		{"a rule expression", http.MethodPost, "/v1/risk/rules", `{"id":"r","when":"` + long + `","action":"decline"}`},
		{"a path segment", http.MethodDelete, "/v1/risk/lists/deny-ip/entries/" + long, ""},
		{"a scored observation", http.MethodPost, "/v1/risk/score", `{"observation":{"subject":{"kind":"account","id":"` + long + `"}}}`},
	} {
		code, body := req(t, app, c.method, c.path, "acme", "u_acme", c.body)
		if code != http.StatusBadRequest {
			t.Errorf("%s of %d bytes answered %d, want 400 — a value the ceiling cannot price reached the store: %s",
				c.what, len(long), code, string(body))
		}
	}

	// And the door does not refuse what the app is for.
	code, body := req(t, app, http.MethodPost, "/v1/risk/decide", "acme", "u_acme",
		`{"subject":{"kind":"account","id":"a-1"},"stage":"payment","amount":{"nano":1000000000,"currency":"USD"}}`)
	if code != http.StatusOK {
		t.Fatalf("an ordinary decision answered %d: %s", code, string(body))
	}
}

// ── 3. a rollout does not run the backlog it is tearing down ────────────────

// TestAStoppedRunnerDoesNotRunItsBacklog.
//
// THE DEFECT: stop() cancelled only the claims that had a cancel func — a job
// still in the BACKLOG has none, because execute() installs it — and then closed
// the channel. The workers drained the remainder, found each claim still there,
// installed a FRESH budget and ran the whole thing. Worst case stop() blocks for
// backlog/workers x searchBudget while SIGTERM's grace period is ~40s, so the
// pod is killed before teardown snapshots anything: every tenant reverts to its
// last snapshot or to `warming`. Ship-blocker 6, fleet-wide, armed by any
// authenticated tenant queueing searches before a deploy.
func TestAStoppedRunnerDoesNotRunItsBacklog(t *testing.T) {
	_, s := wireApp(t)
	db := dbOf(t, resOf(t, s, Tenant("hanzo/acme")))

	r := newRunner(luxlog.New("risktest"))
	var ran int64
	var mu sync.Mutex
	const queued = searchQueue
	for i := 0; i < queued; i++ {
		id := fmt.Sprintf("search_%d", i)
		if err := putSearch(db, id, searchRunning, []byte(`{}`)); err != nil {
			t.Fatalf("seeding the row: %v", err)
		}
		err := r.start(job{
			t: Tenant(fmt.Sprintf("hanzo/t%d", i)), id: id, db: db,
			load: func() ([]observation, error) {
				mu.Lock()
				ran++
				mu.Unlock()
				time.Sleep(50 * time.Millisecond)
				return []observation{{at: time.Now(), kind: "account", subject: "s"}}, nil
			},
		})
		if err != nil {
			t.Fatalf("queueing %d: %v", i, err)
		}
	}

	start := time.Now()
	r.stop()
	took := time.Since(start)

	if took > 10*time.Second {
		t.Fatalf("stop() took %s — a rollout's grace period is ~40s and teardown snapshots every model AFTER this returns", took)
	}
	for i := 0; i < queued; i++ {
		status, _, err := getSearch(db, fmt.Sprintf("search_%d", i))
		if err != nil {
			t.Fatalf("reading row %d: %v", i, err)
		}
		if status == searchDone {
			t.Fatalf("search_%d ran to completion during shutdown — the backlog is work a rollout still does", i)
		}
		if status == searchRunning {
			t.Fatalf("search_%d was left `running` by a process that is gone", i)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if ran > searchWorkers {
		t.Fatalf("%d queued searches read their inputs while the node was shutting down", ran)
	}
}

// TestASearchIsBoundedBeforeItReadsHistory. The expensive part ran BEFORE the
// bound: replayHistory materialised up to 5,000 rows into observations and only
// then asked whether this tenant already had a run going. Thirty concurrent
// calls each paid the full read and 29 answered 409 — none of them metered, and
// nothing limited how many held 5,000 observations at once.
//
// The inputs are now the WORKER's to load, so a refusal costs a map lookup and
// at most searchWorkers loads exist at any instant.
func TestASearchIsBoundedBeforeItReadsHistory(t *testing.T) {
	r := idle()
	tn := Tenant("hanzo/acme")
	var loads int64
	load := func() ([]observation, error) {
		loads++
		return nil, nil
	}
	if err := r.start(job{t: tn, id: "search_1", load: load}); err != nil {
		t.Fatalf("the first search was refused: %v", err)
	}
	for i := 0; i < 30; i++ {
		if err := r.start(job{t: tn, id: fmt.Sprintf("search_r%d", i), load: load}); err == nil {
			t.Fatal("a second concurrent search for one tenant was accepted")
		}
	}
	if loads != 0 {
		t.Fatalf("%d refused searches read this tenant's history first — the expensive half runs before the bound", loads)
	}
}

// ── 4. an operating point, not a ceiling of 48 ──────────────────────────────

// TestANewTenantIsAdmittedWhileTheNodeHasMemory.
//
// THE DEFECT: tenantMax defaulted to 48 and priced every tenant at its WORST
// case — the full 8 MiB aggregate budget — whether it held two entities or two
// thousand. The 49th concurrently-active org was refused the entire risk surface
// with 503, and reclaim only freed a cell after six hours of that tenant's own
// silence, so on a one-replica pod the refusal stood for most of a day. Raising
// the knob broke the ceiling it was computed from: 4,096 x 10.4 MiB = 42 GiB.
//
// It is class A again, at the process layer: a bound on the COUNT of things
// whose size the tenant decides. The bound is now BYTES, and a tenant is priced
// at what it actually holds.
func TestANewTenantIsAdmittedWhileTheNodeHasMemory(t *testing.T) {
	_, s := wireApp(t)
	const orgs = 200
	for i := 0; i < orgs; i++ {
		tn := Tenant(fmt.Sprintf("hanzo/org-%d", i))
		r, err := s.State.res.of(tn, s.Log)
		if err != nil {
			held, _, _, _ := s.State.res.count()
			t.Fatalf("org %d of %d was refused the whole risk surface with %d resident and %d B of %d B in use: %v",
				i+1, orgs, held, s.State.res.bytes(), memBytes(), err)
		}
		// Each one is a real tenant doing real work, not an empty cell.
		r.record(observation{at: time.Now(), kind: "account", subject: "a-1", amount: 1_000_000_000})
	}
	held, _, _, _ := s.State.res.count()
	if held != orgs {
		t.Fatalf("%d tenants resident, want %d", held, orgs)
	}
	t.Logf("%d active tenants resident in %d B of a %d B node budget", held, s.State.res.bytes(), memBytes())
}

// TestAFullNodeReclaimsBeforeItRefuses. The refusal is still there — a node with
// no memory and nothing to reclaim must say so rather than be OOM-killed with
// every tenant on board — but it is the LAST answer, not the first. A tenant
// that has been silent past the retire floor is reclaimed to make room, which
// costs it its rings (published as `since`) and nothing durable.
func TestAFullNodeReclaimsBeforeItRefuses(t *testing.T) {
	t.Setenv(envMemory, fmt.Sprint(2*cellBytes)) // room for two cells
	_, s := wireApp(t)

	a, b := Tenant("hanzo/acme"), Tenant("hanzo/beta")
	ra := resOf(t, s, a)
	resOf(t, s, b)

	// Both are hot: nothing may be taken from either, so the newcomer is refused.
	if _, err := s.State.res.of(Tenant("hanzo/gamma"), s.Log); err == nil {
		t.Fatal("a third tenant was admitted onto a full node whose incumbents are both active")
	}

	// A goes quiet past the retire floor. Now the node can make room without
	// taking anything from a tenant that is using it.
	ra.mu.Lock()
	ra.touched = time.Now().Add(-2 * idleFloor)
	ra.mu.Unlock()

	if _, err := s.State.res.of(Tenant("hanzo/gamma"), s.Log); err != nil {
		t.Fatalf("a newcomer was refused while a tenant silent for %s held a cell: %v", 2*idleFloor, err)
	}
	if _, _, reclaimed, _ := s.State.res.count(); reclaimed != 1 {
		t.Fatalf("reclaimed = %d, want 1 — a cell taken under pressure has to be counted where an operator reads it", reclaimed)
	}
}

// ── 5. a cell is never closed under a request that is holding it ────────────

// TestACellIsNotClosedUnderAnotherRequest.
//
// THE DEFECT: `of` called abandon() when open() or arm() failed, and abandon
// released the cell it found in the map — which, for a concurrent first request
// on the same org, is the cell the OTHER request is already using. release()
// did `_ = r.db.Close(); r.db = nil`, so the winner's next query dereferenced a
// nil *sql.DB. There is no recover() in the request path and cloud runs ONE
// replica, so a transient SQLITE_BUSY on a cold tenant is a total outage for
// every tenant on the pod.
//
// THE SHAPE THAT CANNOT EXPRESS IT: a cell is built COMPLETE and only then
// published. The only cell `of` can release is one it built and nobody has ever
// seen, so there is no abandon() to get wrong.
func TestACellIsNotClosedUnderAnotherRequest(t *testing.T) {
	_, s := wireApp(t)
	tn := Tenant("hanzo/acme")

	var wg sync.WaitGroup
	cells := make([]*resident, 8)
	for i := range cells {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r, err := s.State.res.of(tn, s.Log)
			if err != nil {
				t.Errorf("concurrent admission %d: %v", i, err)
				return
			}
			cells[i] = r
		}(i)
	}
	wg.Wait()

	// Every racer holds a usable handle onto the SAME cell — one tenant, one
	// cell, one file.
	for i, r := range cells {
		if r == nil {
			continue
		}
		if r != cells[0] {
			t.Fatalf("racer %d holds a different cell for the same tenant — two writers on one file", i)
		}
		if _, _, err := getSearch(dbOf(t, r), "search_absent"); err == nil {
			t.Fatalf("racer %d: reading an absent row succeeded", i)
		} else if strings.Contains(err.Error(), "closed") || strings.Contains(err.Error(), "nil") {
			t.Fatalf("racer %d holds a closed or nil handle: %v", i, err)
		}
	}
}

// TestAPanicInAWorkerIsNotAnOutage. cloud runs one replica: an unrecovered panic
// in a background goroutine takes every tenant down. The worker owns the panic,
// writes it onto the run's own row, and keeps serving.
func TestAPanicInAWorkerIsNotAnOutage(t *testing.T) {
	_, s := wireApp(t)
	db := dbOf(t, resOf(t, s, Tenant("hanzo/acme")))

	r := newRunner(luxlog.New("risktest"))
	defer r.stop()
	if err := putSearch(db, "search_boom", searchRunning, []byte(`{}`)); err != nil {
		t.Fatalf("seeding: %v", err)
	}
	err := r.start(job{
		t: Tenant("hanzo/acme"), id: "search_boom", db: db,
		load: func() ([]observation, error) { panic("the input plane exploded") },
	})
	if err != nil {
		t.Fatalf("queueing: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		status, body, err := getSearch(db, "search_boom")
		if err != nil {
			t.Fatalf("reading the row: %v", err)
		}
		if status != searchRunning {
			if status != searchRefused {
				t.Fatalf("a panicking search left status %q, want %q", status, searchRefused)
			}
			var rep searchReport
			if err := json.Unmarshal(body, &rep); err != nil {
				t.Fatalf("decoding the report: %v", err)
			}
			if rep.Refusal == "" {
				t.Fatal("the row carries no reason, so nobody can tell a crash from a cancel")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the row is still `running` — the worker died and took its claim with it")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// And the pool still serves.
	if err := putSearch(db, "search_after", searchRunning, []byte(`{}`)); err != nil {
		t.Fatalf("seeding: %v", err)
	}
	if err := r.start(job{
		t: Tenant("hanzo/beta"), id: "search_after", db: db,
		load: func() ([]observation, error) { return nil, nil },
	}); err != nil {
		t.Fatalf("the pool stopped serving after a panic: %v", err)
	}
}

// ── 6. using this plane is not governing it ─────────────────────────────────

// TestGoverningThisTenantNeedsItsOwnAdmin.
//
// FOUND WHILE FIXING THE FIVE ABOVE, and reported: there was no role, scope or
// admin predicate anywhere in the app, so any principal carrying the org claim
// could turn the org's fraud plane off. PUT /v1/risk/mode {"mode":"shadow"}
// makes every rule observe and nothing act; DELETE a rule deletes a detection;
// a blanket suppression mutes one; an allow-list entry is a bypass; appetite
// decides how much of the stream the model may look at. A leaked low-privilege
// customer key reached all of them, and the customer would find out from a
// chargeback.
//
// One predicate, one place (governState), and the negative and positive halves
// are both here: a member is refused and an admin of the SAME org is not, or the
// gate is either absent or a wall.
func TestGoverningThisTenantNeedsItsOwnAdmin(t *testing.T) {
	app, _ := wireApp(t)

	for _, c := range []struct {
		what   string
		method string
		path   string
		body   string
		admin  int
	}{
		{"taking the tenant to shadow", http.MethodPut, "/v1/risk/mode", `{"mode":"shadow"}`, http.StatusOK},
		{"retiring a rule", http.MethodDelete, "/v1/risk/rules/signup-burst-ip", "", http.StatusOK},
		{"blanket-muting a rule", http.MethodPost, "/v1/risk/suppressions", `{"rule":"payment-card-testing","reason":"x"}`, http.StatusCreated},
		{"adding an allow-list bypass", http.MethodPost, "/v1/risk/lists/ip-allow/entries", `{"values":["1.2.3.4"]}`, http.StatusOK},
		{"narrowing what the model looks at", http.MethodPut, "/v1/risk/state/appetite", `{"review":0.001,"sample":0.001}`, http.StatusOK},
		{"overwriting the learned state", http.MethodPost, "/v1/risk/snapshot", "", http.StatusCreated},
	} {
		if code, body := req(t, app, c.method, c.path, "acme", "u_member", c.body); code != http.StatusForbidden {
			t.Errorf("%s as an ordinary member of the org answered %d, want 403 — a leaked customer key turns this tenant's fraud plane off: %s",
				c.what, code, string(body))
		}
		if code, body := reqAdmin(t, app, c.method, c.path, "acme", "u_admin", c.body); code != c.admin {
			t.Errorf("%s as an admin OF THIS ORG answered %d, want %d — the gate is a wall, not a scope: %s",
				c.what, code, c.admin, string(body))
		}
	}

	// And USING the plane is untouched: an ordinary member still scores, reads
	// and labels. A gate that also stopped the product would be a different bug.
	for _, c := range []struct {
		what   string
		method string
		path   string
		body   string
		want   int
	}{
		{"deciding", http.MethodPost, "/v1/risk/decide", `{"stage":"payment","subject":{"kind":"account","id":"a-1"}}`, http.StatusOK},
		{"scoring", http.MethodPost, "/v1/risk/score", `{"observation":{"subject":{"kind":"account","id":"a-1"}}}`, http.StatusOK},
		{"reading the rules", http.MethodGet, "/v1/risk/rules", "", http.StatusOK},
		{"reading the model state", http.MethodGet, "/v1/risk/state", "", http.StatusOK},
	} {
		if code, body := req(t, app, c.method, c.path, "acme", "u_member", c.body); code != c.want {
			t.Errorf("%s as an ordinary member answered %d, want %d: %s", c.what, code, c.want, string(body))
		}
	}
}

// ── 7. the same class, on the volume ────────────────────────────────────────

// TestTheDecisionLogIsARingAndSaysHowFarBack.
//
// REPORTED, NOT FIXED IN THE LAST CUT: there was no DELETE, no TTL and no prune
// on any durable plane. Every decide wrote a row forever, and every tenant's
// SQLite file lives on the pod's ONE volume — so one authenticated org filling
// the disk is "one org quiets another" moved from RAM to disk, with every other
// tenant's writes failing behind it.
//
// The log is bounded in BYTES like everything else, and it is a RING rather than
// a refusal: refusing a governance write costs a rule the tenant can retry,
// refusing a DECISION costs the authorization it asked for. Dropping the oldest
// is only honest if the window is published, so the page carries it.
func TestTheDecisionLogIsARingAndSaysHowFarBack(t *testing.T) {
	_, s := wireApp(t)
	tn := Tenant("hanzo/acme")
	db := dbOf(t, resOf(t, s, tn))

	// Well past the retention, written straight at the store so the test is
	// about the ring and not about the decide path's speed.
	over := recordCap() + 2*pruneEvery
	at := time.Now().Add(-time.Duration(over) * time.Second)
	for i := 0; i < over; i++ {
		err := putDecision(db, observation{
			id: fmt.Sprintf("dec_%06d", i), at: at.Add(time.Duration(i) * time.Second),
			stage: StagePayment, kind: "account", subject: "a-1",
		}, outcome{id: fmt.Sprintf("dec_%06d", i), action: ActionAllow}, "digest", "", at, false)
		if err != nil {
			t.Fatalf("writing %d: %v", i, err)
		}
	}
	if err := prune(db); err != nil {
		t.Fatalf("prune: %v", err)
	}

	held, oldest, err := retention(db)
	if err != nil {
		t.Fatalf("retention: %v", err)
	}
	if held > recordCap() {
		t.Fatalf("the log holds %d decisions against a retention of %d — one tenant's write volume is every tenant's disk", held, recordCap())
	}
	if held != recordCap() {
		t.Fatalf("the log holds %d, want exactly %d — the ring dropped more than it had to", held, recordCap())
	}
	if bytes := held * recordMax; bytes > recordBudget {
		t.Fatalf("the retained log may reach %d B against a published budget of %d B", bytes, recordBudget)
	}
	if oldest == "" {
		t.Fatal("the log does not say how far back it goes, so a period that was never retained reads as a period with nothing in it")
	}
	// The NEWEST survived and the OLDEST went: a ring that dropped the wrong end
	// would pass every count assertion above.
	rows, err := decisionsPage(db, "", "", "", "", 1)
	if err != nil || len(rows) != 1 {
		t.Fatalf("reading the newest: %v %d", err, len(rows))
	}
	if rows[0].ID != fmt.Sprintf("dec_%06d", over-1) {
		t.Fatalf("the newest decision is %s, want dec_%06d — the ring dropped the wrong end", rows[0].ID, over-1)
	}
	if _, _, _, _, err := decisionDetail(db, "dec_000000"); err == nil {
		t.Fatal("the oldest decision is still there, so nothing was pruned")
	}
}

// ── 6. a value that outlives the request must own its bytes ─────────────────

// TestADetachedEventDoesNotAliasTheRequest is a defect RED DID NOT REPORT, found
// by running the suite under -race: two DATA RACES, both between
// analytics.PublishEvents marshalling a risk event and fasthttp parsing the NEXT
// request on the same connection.
//
// THE DEFECT: emit() built the event from strings the caller handed it and then
// detached a goroutine to publish it. fasthttp owns the byte buffers behind
// header values and the path, and REUSES them for the next request — so
// `by(sc)` (X-User-Id), `sc.org` (X-Org-Id) and every path parameter (in.ID,
// in.Name) were strings pointing into memory the server was about to overwrite.
// The published event therefore carried whatever the NEXT request wrote there,
// which on a shared pod is another tenant's user id under this tenant's
// DistinctID. Not a crash — a silent, wrong analytics record, and unbounded
// undefined behaviour under the race.
//
// It is the same shape as class A and class B one layer further down: a value
// whose LIFETIME is the request, complected with a consumer whose lifetime is
// not. The fix is that the detach point takes ownership, once, for everyone.
//
// THE TEST models exactly what fasthttp does — it hands emit a string aliasing a
// buffer and then overwrites the buffer — so it is deterministic rather than a
// race the scheduler has to be persuaded into.
func TestADetachedEventDoesNotAliasTheRequest(t *testing.T) {
	buf := []byte("user-alpha")
	aliased := unsafe.String(unsafe.SliceData(buf), len(buf))

	kept := detach(aliased, "risk.rule.retired", map[string]any{
		"by": aliased, "rule": aliased, "count": 3,
	})

	// fasthttp reuses the buffer for the next request on this connection.
	copy(buf, "user-BETA!")

	if kept.DistinctID != "user-alpha" {
		t.Errorf("the detached event's tenant is %q after the buffer was reused, want %q — the event aliases request memory", kept.DistinctID, "user-alpha")
	}
	for _, k := range []string{"by", "rule"} {
		if got := kept.Properties[k]; got != "user-alpha" {
			t.Errorf("the detached event's %q is %q after the buffer was reused, want %q — the event aliases request memory", k, got, "user-alpha")
		}
	}
	if got := kept.Properties["count"]; got != 3 {
		t.Errorf("the detached event's non-text property is %v, want 3 — taking ownership must not change the value", got)
	}
	// The caller's own map must not be the one that was detached: a caller that
	// reuses or mutates its map after emit returns would otherwise mutate an
	// event already in flight.
	props := map[string]any{"by": "u1"}
	ev := detach("acme", "risk.mode", props)
	props["by"] = "u2"
	if ev.Properties["by"] != "u1" {
		t.Error("the detached event shares the caller's map, so a caller that reuses it rewrites an event already in flight")
	}
}

// TestThereIsOneDetachAndItTakesOwnership pins the structure rather than the
// instance. Cloning inside emit only holds while emit is the ONLY place this
// package hands request-derived text to something that outlives the request; a
// second `go analytics.Publish...` written next year would reopen the defect
// with the fix still sitting in the file. So: exactly one publish site, and it
// is reached through detach().
func TestThereIsOneDetachAndItTakesOwnership(t *testing.T) {
	sites := callsIn(t, "analytics", "PublishEvents")
	total := 0
	for file, n := range sites {
		total += n
		if file != "store.go" {
			t.Errorf("%s publishes onto the bus %d time(s); the only publish site is emit() in store.go, which detaches an owned copy first", file, n)
		}
	}
	if total != 1 {
		t.Errorf("analytics.PublishEvents is called %d time(s) in this package, want exactly 1 — a second detach is a second chance to hand a goroutine memory the server is about to overwrite", total)
	}
}

// TestARolloutDoesNotHandAnInFlightRequestAClosedFile is the OTHER half of
// ship-blocker 5, and it is the half that is reachable on every single rollout.
//
// abandon() is gone and a cell is built complete before it is published, so no
// request can have its file closed by a racing admission. But `residency.close`
// — what teardown runs on SIGTERM — retires EVERY cell immediately, with no idle
// requirement, and cloud deploys Recreate at ONE replica. A request that has
// already resolved its tenant and is between two queries then reads
// `r.handle == nil`, and `(*sql.DB)(nil).QueryRow` locks a nil mutex: the same
// nil dereference, arrived at from shutdown instead of from a lost race, on a
// path every deploy takes.
//
// THE SHAPE THAT CANNOT EXPRESS IT: an op cannot obtain a file handle without
// obtaining an error alongside it. tenantState is the ONE door every op passes,
// so resolving the handle THERE — once, checked — is what makes a nil handle
// unrepresentable downstream rather than a rule 27 call sites have to remember.
func TestARolloutDoesNotHandAnInFlightRequestAClosedFile(t *testing.T) {
	_, s := wireApp(t)
	tn := Tenant("hanzo/acme")

	// A request that has resolved its tenant and is about to read its file.
	res, err := s.State.res.of(tn, s.Log)
	if err != nil {
		t.Fatalf("resolving the tenant: %v", err)
	}

	// SIGTERM. teardown snapshots every model and closes every file.
	s.State.res.close(s.Log)

	// The in-flight request now goes to read. It must be REFUSED, not handed a
	// handle it will dereference.
	db, err := res.file()
	if err == nil {
		t.Fatal("a cell closed by teardown still hands out a file handle; the next query nil-dereferences and takes every tenant on the pod with it")
	}
	if db != nil {
		t.Fatalf("the refusal came with a handle anyway (%v)", db)
	}
	var he *zip.HTTPError
	if !errors.As(err, &he) || he.Status != http.StatusServiceUnavailable {
		t.Errorf("a request that lost its cell to a rollout answers %v, want a 503 it can retry against the next pod", err)
	}
}
