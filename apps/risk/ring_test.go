package risk

// ring_test.go — the sliding aggregates, which are where this app's tenant
// boundary was actually broken.
//
// The model's boundary (learn_test.go) was already held by three tests. The
// AGGREGATES were not, and they are eight of the nine dimensions the model reads:
// one process-wide store with a process-wide cap meant the busiest organisation
// evicted the quietest one's keys, and the victim then scored as unremarkable
// with no error and no alert. Every test here fails if that store comes back.
//
// Four properties, four tests:
//
//   - the plane holds no tenant state of its own —
//     [TestPlane_HoldsNoSharedTenantState];
//   - one organisation's volume cannot evict another's aggregates —
//     [TestRings_OneOrganisationCannotEvictAnother];
//   - a rollout rebuilds them EXACTLY rather than blinding everyone —
//     [TestRings_SurviveARolloutExactly];
//   - a timestamp cannot be used to blind a subject —
//     [TestRings_AFutureStampCannotBlindASubject].

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/luxfi/aml/pkg/anomaly"
	"github.com/luxfi/aml/pkg/velocity"
)

// TestPlane_HoldsNoSharedTenantState is the STRUCTURAL half: the plane may hold
// per-tenant maps keyed BY tenant and nothing else that carries tenant data.
//
// It is a reflection test rather than a paragraph because the defect it refutes
// is a single field. `vel *velocity.Store` on the plane is one store every tenant
// writes into with one cap over all of them, and no behavioural test of one
// tenant would ever notice.
//
// Mutation proof: add `vel *velocity.Store` back to plane and this fails.
func TestPlane_HoldsNoSharedTenantState(t *testing.T) {
	// Types that carry one organisation's data. On the plane they would be shared;
	// on a resident they are that resident's own.
	tenantState := map[reflect.Type]string{
		reflect.TypeOf(&velocity.Store{}): "sliding aggregates",
		reflect.TypeOf(&anomaly.Store{}):  "a model",
	}
	pt := reflect.TypeOf(plane{})
	for i := 0; i < pt.NumField(); i++ {
		f := pt.Field(i)
		if what, shared := tenantState[f.Type]; shared {
			t.Errorf("plane.%s holds %s for the whole process — one organisation's volume then evicts another's, "+
				"silently. Per-tenant state belongs on the resident.", f.Name, what)
		}
		if f.Type.Kind() == reflect.Map && f.Type.Key() != reflect.TypeOf(tenant("")) {
			t.Errorf("plane.%s is a map keyed by %s — every map on the plane must be keyed BY TENANT, "+
				"or it is a place two organisations share.", f.Name, f.Type.Key())
		}
	}
	// And the resident does hold them, so the test above is about PLACEMENT and not
	// about the fields having been deleted.
	rt := reflect.TypeOf(resident{})
	for want := range tenantState {
		var found bool
		for i := 0; i < rt.NumField(); i++ {
			if rt.Field(i).Type == want {
				found = true
			}
		}
		if !found {
			t.Errorf("no resident field holds %s — the state has to live somewhere, and per tenant is where", want)
		}
	}
}

// TestRings_OneOrganisationCannotEvictAnother is the BEHAVIOURAL half, and it is
// the cheap cross-tenant denial of service stated as an experiment: organisation
// B does nothing but ordinary business at volume, and organisation A's velocity
// must be exactly what it was.
//
// Mutation proof: give the plane one shared velocity.Store again and A's
// observation drops to zero — B's subjects evict A's, least-recently-updated
// first, and A's is the oldest.
func TestRings_OneOrganisationCannotEvictAnother(t *testing.T) {
	probe.reset(true)
	p := newTestPlane(t)
	a, b := key(t, brandA, orgA), key(t, brandA, orgB)
	at := time.Now().UTC().Add(-time.Hour)

	quiet := observation{ID: "a_1", Kind: kindAccount, Subject: "u_quiet", USD: 250, At: at}
	if _, err := p.learn(a, quiet); err != nil {
		t.Fatalf("learn(A): %v", err)
	}
	before := velocityOf(t, p, a, quiet)
	if before == 0 {
		t.Fatal("organisation A's own event did not reach its aggregates — the test proves nothing")
	}

	// B fills its OWN bound several times over. Every one of these is ordinary use:
	// distinct subjects, one event each, well inside every limit the API states.
	flood := make([]observation, 0, residentKeys*3)
	for i := 0; i < residentKeys*3; i++ {
		flood = append(flood, observation{
			ID: "b_" + strconv.Itoa(i), Kind: kindAccount,
			Subject: "u_" + strconv.Itoa(i), USD: 10,
			At: at.Add(time.Duration(i) * time.Millisecond),
		})
	}
	for i := 0; i < len(flood); i += maxBatch {
		end := min(i+maxBatch, len(flood))
		if _, err := p.learn(b, flood[i:end]...); err != nil {
			t.Fatalf("learn(B): %v", err)
		}
	}

	if after := velocityOf(t, p, a, quiet); after != before {
		t.Fatalf("organisation A's 24h count went %d -> %d while ANOTHER organisation was busy. "+
			"A shared aggregate store makes one tenant's ordinary volume a silent denial of service against every other.",
			before, after)
	}
	// And B really did hit its own bound, so the experiment was the experiment.
	if held := residentRings(t, p, b).Keys(); held > residentKeys+shards {
		t.Fatalf("organisation B holds %d keys against a per-tenant bound of %d — the bound is not being applied",
			held, residentKeys)
	}
}

// shards is velocity's own sharding factor. The bound is applied per shard
// (MaxKeys/shards+1), so the effective ceiling is the stated one plus at most one
// key per shard — stated here so the assertion above measures the real bound
// rather than an idealised one.
const shards = 64

// TestRings_SurviveARolloutExactly: the binary deploys one replica at a time with
// the old pod stopped first, so anything held only in memory is gone on every
// rollout. For eight of the nine model dimensions that means reading zero — the
// same silent quiet this app exists to prevent, arriving on a schedule.
//
// The aggregates are therefore a projection of the tenant's own durable record,
// and the property is EXACTNESS: same record, same replay order, same numbers.
//
// Mutation proof: stop writing the record in plane.note, or stop replaying it in
// plane.rings, and the counts come back zero.
func TestRings_SurviveARolloutExactly(t *testing.T) {
	probe.reset(true)
	dir := t.TempDir()
	k := key(t, brandA, orgA)
	evs := stream(400, time.Now().UTC().Add(-4*time.Hour))

	first := planeAt(t, dir)
	teach(t, first, k, evs)
	before := velocitySnapshot(t, first, k, evs)
	if err := first.close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	second := planeAt(t, dir)
	defer func() { _ = second.close() }()
	after := velocitySnapshot(t, second, k, evs)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("a rollout changed this organisation's aggregates.\nbefore: %v\nafter:  %v\n"+
			"Every velocity feature then reads a different number than the model was trained against.", before, after)
	}
	var total int
	for _, n := range after {
		total += n
	}
	if total == 0 {
		t.Fatal("both sides are zero — the comparison passes and proves nothing")
	}
}

// TestRings_SurviveAnEviction: the resident bound is a process-wide bound, which
// is only acceptable because hitting it costs a REBUILD and never a loss. An
// evicted tenant's model state is written down and its aggregates come back off
// its own record.
//
// Mutation proof: drop the p.save(gone) call in resident, or the replay in
// plane.rings, and the returning tenant is blind.
func TestRings_SurviveAnEviction(t *testing.T) {
	probe.reset(true)
	p := newTestPlane(t)
	k := key(t, brandA, orgA)
	evs := stream(300, time.Now().UTC().Add(-3*time.Hour))
	teach(t, p, k, evs)
	before := velocitySnapshot(t, p, k, evs)
	learnedBefore, _ := p.state(k)

	// Evict exactly the way the bound does: write the state down, then drop it.
	p.mu.Lock()
	gone := p.res[k]
	delete(p.res, k)
	delete(p.folded, k)
	p.evicted++
	p.mu.Unlock()
	if err := p.save(gone); err != nil {
		t.Fatalf("save on eviction: %v", err)
	}

	after := velocitySnapshot(t, p, k, evs)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("an eviction changed this organisation's aggregates.\nbefore: %v\nafter:  %v", before, after)
	}
	learnedAfter, _ := p.state(k)
	if learnedAfter.Learned != learnedBefore.Learned {
		t.Fatalf("an eviction unlearned %d events", learnedBefore.Learned-learnedAfter.Learned)
	}
	if _, evicted := p.residents(); evicted == 0 {
		t.Fatal("the probe reports no evictions — a bound being hit must be visible to an operator")
	}
}

// TestRings_AFutureStampCannotBlindASubject is the evasion, stated as an attack.
//
// The aggregates track a leading edge. One event stamped far ahead moves it
// there, and from then on every real event for that subject is older than every
// window, folded to the edge, and reads back as nothing — so the subject's
// velocity features go neutral and its behaviour is invisible. One request, no
// error, permanent.
//
// Two refusals hold it, and this exercises the inner one: [placeable] keeps an
// unbelievable stamp out of the rings whatever door it came through. The wire
// door's 400 is the outer one ([TestEvent_RefusesAnUnbelievableTimestamp]).
//
// Mutation proof: delete the skew check in placeable and the real events below
// stop counting.
func TestRings_AFutureStampCannotBlindASubject(t *testing.T) {
	probe.reset(true)
	p := newTestPlane(t)
	k := key(t, brandA, orgA)
	now := time.Now().UTC()

	poison := observation{ID: "poison", Kind: kindAccount, Subject: "u_evader", USD: 1, At: now.AddDate(100, 0, 0)}
	if _, err := p.learn(k, poison); err != nil {
		t.Fatalf("learn(poison): %v", err)
	}
	real := []observation{
		{ID: "r_1", Kind: kindAccount, Subject: "u_evader", USD: 9500, At: now.Add(-30 * time.Minute)},
		{ID: "r_2", Kind: kindAccount, Subject: "u_evader", USD: 9600, At: now.Add(-20 * time.Minute)},
		{ID: "r_3", Kind: kindAccount, Subject: "u_evader", USD: 9700, At: now.Add(-10 * time.Minute)},
	}
	if _, err := p.learn(k, real...); err != nil {
		t.Fatalf("learn(real): %v", err)
	}
	if got := velocityOf(t, p, k, real[0]); got != len(real) {
		t.Fatalf("the subject's 24h count is %d after %d real events — a single future timestamp "+
			"pushed the aggregates' leading edge out and blinded every velocity feature for it", got, len(real))
	}
}

// TestRecord_TwoBrandsShareAFileAndNotARecord: the durable record lives on the
// per-org store, and that store is named for the BARE org slug — so `acme` on one
// brand and `acme` on another are one file. The qualified tenant is what tells
// their rows apart, and it is the leading term of the primary key and of every
// statement that touches the table.
//
// This is the crossing the record introduces, so it is the crossing it is tested
// for. Mutation proof: make the observation table's primary key `id` alone, or
// drop `tenant = ?` from the replay, and one brand's events arrive in the other's
// aggregates.
func TestRecord_TwoBrandsShareAFileAndNotARecord(t *testing.T) {
	probe.reset(true)
	p := newTestPlane(t)
	a, b := key(t, brandA, orgA), key(t, brandB, orgA) // SAME org slug, two brands
	if a.org() != b.org() {
		t.Fatal("the two keys do not share an org slug — the test is not exercising the shared file")
	}
	at := time.Now().UTC().Add(-time.Hour)
	// The SAME event id under both brands: an id-keyed record would collapse them.
	shared := observation{ID: "e_1", Kind: kindAccount, Subject: "u_1", USD: 100, At: at}
	if _, err := p.learn(a, shared); err != nil {
		t.Fatalf("learn(A): %v", err)
	}
	for i := 0; i < 5; i++ {
		one := observation{ID: "e_1", Kind: kindAccount, Subject: "u_1", USD: 100, At: at.Add(time.Duration(i) * time.Second)}
		one.ID = "e_" + strconv.Itoa(i)
		if _, err := p.learn(b, one); err != nil {
			t.Fatalf("learn(B): %v", err)
		}
	}
	if got := velocityOf(t, p, a, shared); got != 1 {
		t.Fatalf("brand A's subject has a 24h count of %d after ONE event of its own — %d of another brand's "+
			"events reached it through the shared file", got, got-1)
	}
	// And the record itself: rebuilt from disk, each brand replays only its own.
	if _, _, n, err := p.rings(a); err != nil || n != 1 {
		t.Fatalf("brand A's record replays %d observation(s) (err %v), want 1", n, err)
	}
	if _, _, n, err := p.rings(b); err != nil || n != 5 {
		t.Fatalf("brand B's record replays %d observation(s) (err %v), want 5", n, err)
	}
}

// TestRecord_IsIdempotentOnTheCallersOwnEventID: a client that retries a batch
// after a timeout must not double its own history. The record is keyed on the
// caller's own stable event id, so a replay of the same batch converges.
func TestRecord_IsIdempotentOnTheCallersOwnEventID(t *testing.T) {
	probe.reset(true)
	p := newTestPlane(t)
	k := key(t, brandA, orgA)
	evs := stream(20, time.Now().UTC().Add(-2*time.Hour))
	teach(t, p, k, evs)
	teach(t, p, k, evs) // the retry
	if _, _, n, err := p.rings(k); err != nil || n != len(evs) {
		t.Fatalf("a retried batch left %d observations on the record (err %v), want %d", n, err, len(evs))
	}
}

// ── reading the aggregates ───────────────────────────────────────────────────

// residentRings reaches one tenant's own aggregates. It is a test helper and not
// an accessor on purpose: nothing in the package needs to reach another tenant's
// rings, so there is no method that could.
func residentRings(t *testing.T, p *plane, k tenant) *velocity.Store {
	t.Helper()
	r, err := p.resident(k)
	if err != nil {
		t.Fatalf("resident(%q): %v", string(k), err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.vel
}

// velocityOf is the 24h count for an observation's own account axis — the number
// eight of the nine model dimensions are computed from.
func velocityOf(t *testing.T, p *plane, k tenant, o observation) int {
	t.Helper()
	vel := residentRings(t, p, k)
	for _, obs := range vel.Observe(anomaly.Keys(o.tx(k))[0]) {
		if obs.Window == "24h" {
			return obs.Count
		}
	}
	t.Fatal("the aggregates keep no 24h window — the inventory reads one")
	return 0
}

// velocitySnapshot is every window of every axis the events touch, keyed so two
// processes can be compared field by field.
func velocitySnapshot(t *testing.T, p *plane, k tenant, evs []observation) map[string]int {
	t.Helper()
	vel := residentRings(t, p, k)
	out := map[string]int{}
	for _, o := range evs {
		for _, key := range anomaly.Keys(o.tx(k)) {
			for _, obs := range vel.Observe(key) {
				out[key.Kind+"/"+key.Value+"/"+obs.Window] = obs.Count
			}
		}
	}
	return out
}

// TestRings_HaveOneConstructor is the STRUCTURAL guarantee behind every test
// above: `newRings` applies the per-tenant bound, so if it is the only place the
// package builds a ring set then a shared or unbounded one cannot be written
// here at all. The reflection test refutes the field; this refutes the call.
//
// Mutation proof: call velocity.New anywhere but ring.go and this names the file.
func TestRings_HaveOneConstructor(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	var offenders []string
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") || name == "ring.go" {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if strings.Contains(string(src), "velocity.New(") {
			offenders = append(offenders, name)
		}
	}
	if len(offenders) > 0 {
		t.Fatalf("%s builds a ring set directly — every set must come from newRings, which is what applies "+
			"the per-tenant bound. A second constructor is where an unbounded or shared store comes back.",
			strings.Join(offenders, ", "))
	}
	if src, err := os.ReadFile("ring.go"); err != nil || !strings.Contains(string(src), "velocity.New(") {
		t.Fatal("ring.go does not build a ring set — the assertion above is vacuous")
	}
}

// TestRecord_IsBoundedPerTenant: the durable record is what makes the aggregates
// survive a rollout, and every tenant's shelf shares one volume — so a record
// bounded only by AGE is a tenant able to fill the disk another tenant's model is
// stored on. It is bounded by count too, at exactly what a rebuild will ever
// replay, so rows past the bound are rows kept for nothing.
//
// Mutation proof: delete the count prune in plane.note and the record keeps every
// event forever (within the window).
func TestRecord_IsBoundedPerTenant(t *testing.T) {
	probe.reset(true)
	p := newTestPlane(t)
	k := key(t, brandA, orgA)
	at := time.Now().UTC().Add(-2 * time.Hour)
	// Comfortably past the bound, in batches the API itself allows.
	for i := 0; i < ringRows+2*maxBatch; i += maxBatch {
		batch := make([]observation, 0, maxBatch)
		for j := 0; j < maxBatch; j++ {
			n := i + j
			batch = append(batch, observation{
				ID: "e_" + strconv.Itoa(n), Kind: kindAccount,
				Subject: "u_" + strconv.Itoa(n%64), USD: 1,
				At: at.Add(time.Duration(n) * time.Millisecond),
			})
		}
		if _, err := p.learn(k, batch...); err != nil {
			t.Fatalf("learn: %v", err)
		}
	}
	held := recorded(t, p, k)
	if held > ringRows {
		t.Fatalf("this organisation's record holds %d observations against a stated bound of %d — "+
			"an unbounded record is one tenant filling the volume every tenant's shelf lives on", held, ringRows)
	}
	if held < ringRows/2 {
		t.Fatalf("the record holds only %d observations — the prune is throwing away history the rebuild reads", held)
	}
	// What survives is the RECENT end: a bound that kept the oldest rows would
	// rebuild aggregates for events no window covers.
	var oldest int64
	sh, err := p.for_(k)
	if err != nil {
		t.Fatalf("shelf: %v", err)
	}
	if err := sh.db.QueryRow(`SELECT min(at) FROM observation WHERE tenant = ?`, string(k)).Scan(&oldest); err != nil {
		t.Fatalf("read the record: %v", err)
	}
	if time.Unix(oldest, 0).UTC().Before(at) {
		t.Fatalf("the surviving rows start at %s, before the first event at %s", time.Unix(oldest, 0).UTC(), at)
	}
}

// recorded is how many observations a tenant's own record holds.
func recorded(t *testing.T, p *plane, k tenant) int {
	t.Helper()
	sh, err := p.for_(k)
	if err != nil {
		t.Fatalf("shelf: %v", err)
	}
	var n int
	if err := sh.db.QueryRow(`SELECT count(*) FROM observation WHERE tenant = ?`, string(k)).Scan(&n); err != nil {
		t.Fatalf("count the record: %v", err)
	}
	return n
}

// TestWarm_FoldsAHistoryOnce: the fold reads a tenant's own surface into its
// model, and the resident bound means a busy fleet evicts and re-plants tenants
// routinely. A fold that starts from the window's edge every time therefore
// teaches the SAME history once per eviction, and the model's masses stop
// describing the traffic and start describing how often we evicted it.
//
// The watermark travels in the snapshot's own row, so "the state came back" and
// "the history was already read" are one fact, restored together.
//
// Mutation proof: drop the r.warmed check in plane.warm and the second fold reads
// the same buckets again.
func TestWarm_FoldsAHistoryOnce(t *testing.T) {
	probe.reset(true)
	dir := t.TempDir()
	k := key(t, brandA, orgA)
	now := time.Now().UTC()
	for i := 0; i < 30; i++ {
		probe.hold(string(k), map[string]any{
			"subject_kind": kindAccount, "subject": "u_" + itoa(i%5),
			"bucket": now.Add(-time.Duration(i+1) * 10 * time.Minute),
			"events": uint32(2), "spend_nano": int64(120_000_000),
		})
	}
	first := planeAt(t, dir)
	holdFolds(t, first)
	f := first.fold(context.Background(), k)
	if f.Folded == 0 {
		t.Fatalf("the first fold read nothing: %+v", f)
	}
	learned, err := first.state(k)
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	if err := first.close(); err != nil { // writes the snapshot AND its watermark
		t.Fatalf("close: %v", err)
	}

	second := planeAt(t, dir)
	defer func() { _ = second.close() }()
	holdFolds(t, second)
	again := second.fold(context.Background(), k)
	if again.Folded != 0 {
		t.Fatalf("a restored model re-read %d buckets of history it had already folded — "+
			"its masses now count that history twice", again.Folded)
	}
	after, err := second.state(k)
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	if after.Learned != learned.Learned {
		t.Fatalf("the model learned %d events, was %d — the fold is not idempotent across a restart",
			after.Learned, learned.Learned)
	}
}
