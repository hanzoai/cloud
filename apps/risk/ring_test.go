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
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/luxfi/aml/pkg/anomaly"
	"github.com/luxfi/aml/pkg/types"
	"github.com/luxfi/aml/pkg/velocity"

	"github.com/hanzoai/cloud"
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
		reflect.TypeOf(&rings{}):          "sliding aggregates",
		reflect.TypeOf(&anomaly.Store{}):  "a model",
	}
	// The resident must hold the WRAPPER, because the wrapper is where the bound
	// is counted; holding the bare store back would put the eviction out of sight
	// again.
	//
	// The MODEL is named by its seam and not by one family's store: a resident holds a
	// [detector], so the assertion is that per-tenant model state lives on the resident
	// whatever family it belongs to. A plane field of that type would be exactly as
	// shared as a plane field of the store type was, which is why the type above still
	// names the store — that is the field a regression would reintroduce.
	onResident := map[reflect.Type]bool{
		reflect.TypeOf(&rings{}):                true,
		reflect.TypeOf((*detector)(nil)).Elem(): true,
	}
	pt := reflect.TypeOf(plane{})
	for i := 0; i < pt.NumField(); i++ {
		f := pt.Field(i)
		if f.Type == reflect.TypeOf((*detector)(nil)).Elem() {
			t.Errorf("plane.%s holds a model for the whole process — one organisation's volume then "+
				"evicts another's, silently. Per-tenant state belongs on the resident.", f.Name)
		}
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
	for want := range onResident {
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

	quiet := ob(t, "a_1", kindAccount, "u_quiet", 250, at)
	if _, err := p.learn(a, quiet); err != nil {
		t.Fatalf("learn(A): %v", err)
	}
	before := velocityOf(t, p, a, quiet)
	if before == 0 {
		t.Fatal("organisation A's own event did not reach its aggregates — the test proves nothing")
	}

	// B fills its OWN bound several times over. Every one of these is ordinary use:
	// distinct subjects, one event each, well inside every limit the API states.
	flood := make([]observation, 0, ringKeyCeiling*3)
	for i := range ringKeyCeiling * 3 {
		flood = append(flood, ob(t, "b_"+strconv.Itoa(i), kindAccount, "u_"+strconv.Itoa(i), 10,
			at.Add(time.Duration(i)*time.Millisecond)))
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
	if held := residentRings(t, p, b).Keys(); held > ringKeyCeiling {
		t.Fatalf("organisation B holds %d keys against a per-tenant ceiling of %d — the bound is not being applied",
			held, ringKeyCeiling)
	}
	// And B knows it: a bound that binds must be readable, not inferred.
	if st := residentStrain(t, p, b); !st.Saturated || st.Forgotten == 0 {
		t.Fatalf("organisation B flooded %d subjects past a %d ceiling and its own state reports "+
			"saturated=%v forgotten=%d — a control that starts forgetting must say so",
			ringKeyCeiling*3, ringKeyCeiling, st.Saturated, st.Forgotten)
	}
}

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
	if err := first.close(context.Background()); err != nil {
		t.Fatalf("close: %v", err)
	}

	second := planeAt(t, dir)
	defer func() { _ = second.close(context.Background()) }()
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
	learnedBefore, _, _ := p.state(k)

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
	learnedAfter, _, _ := p.state(k)
	if learnedAfter.Learned != learnedBefore.Learned {
		t.Fatalf("an eviction unlearned %d events", learnedBefore.Learned-learnedAfter.Learned)
	}
	if _, _, evicted, _ := p.residents(); evicted == 0 {
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

	poison := ob(t, "poison", kindAccount, "u_evader", 1, now.AddDate(100, 0, 0))
	if _, err := p.learn(k, poison); err != nil {
		t.Fatalf("learn(poison): %v", err)
	}
	real := []observation{
		ob(t, "r_1", kindAccount, "u_evader", 9500, now.Add(-30*time.Minute)),
		ob(t, "r_2", kindAccount, "u_evader", 9600, now.Add(-20*time.Minute)),
		ob(t, "r_3", kindAccount, "u_evader", 9700, now.Add(-10*time.Minute)),
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
	shared := ob(t, "e_1", kindAccount, "u_1", 100, at)
	if _, err := p.learn(a, shared); err != nil {
		t.Fatalf("learn(A): %v", err)
	}
	for i := range 5 {
		one := ob(t, "e_"+strconv.Itoa(i), kindAccount, "u_1", 100, at.Add(time.Duration(i)*time.Second))
		if _, err := p.learn(b, one); err != nil {
			t.Fatalf("learn(B): %v", err)
		}
	}
	if got := velocityOf(t, p, a, shared); got != 1 {
		t.Fatalf("brand A's subject has a 24h count of %d after ONE event of its own — %d of another brand's "+
			"events reached it through the shared file", got, got-1)
	}
	// And the record itself: rebuilt from disk, each brand replays only its own.
	if _, _, n, err := p.rebuild(a); err != nil || n != 1 {
		t.Fatalf("brand A's record replays %d observation(s) (err %v), want 1", n, err)
	}
	if _, _, n, err := p.rebuild(b); err != nil || n != 5 {
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
	if _, _, n, err := p.rebuild(k); err != nil || n != len(evs) {
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
	return r.vel.vel
}

// residentStrain is what that tenant's own state reports about its aggregates.
func residentStrain(t *testing.T, p *plane, k tenant) strain {
	t.Helper()
	r, err := p.resident(k)
	if err != nil {
		t.Fatalf("resident(%q): %v", string(k), err)
	}
	r.vel.reconcile()
	return r.vel.strain()
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
	for i := 0; i < recordRows+2*maxBatch; i += maxBatch {
		batch := make([]observation, 0, maxBatch)
		for j := range maxBatch {
			n := i + j
			batch = append(batch, ob(t, "e_"+strconv.Itoa(n), kindAccount, "u_"+strconv.Itoa(n%64), 1,
				at.Add(time.Duration(n)*time.Millisecond)))
		}
		if _, err := p.learn(k, batch...); err != nil {
			t.Fatalf("learn: %v", err)
		}
	}
	held := recorded(t, p, k)
	if held > recordRows {
		t.Fatalf("this organisation's record holds %d observations against a stated bound of %d — "+
			"an unbounded record is one tenant filling the volume every tenant's shelf lives on", held, recordRows)
	}
	if held < recordRows/2 {
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
func recorded(t testing.TB, p *plane, k tenant) int {
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
	for i := range 30 {
		probe.hold(string(k), map[string]any{
			"subject_kind": kindAccount, "subject": "u_" + itoa(i%5),
			"bucket": surfaceAt(i + 1),
			"events": uint32(2), "spend_nano": int64(120_000_000),
		})
	}
	first := planeAt(t, dir)
	holdFolds(t, first)
	f := first.fold(context.Background(), k)
	if f.Folded == 0 {
		t.Fatalf("the first fold read nothing: %+v", f)
	}
	learned, _, err := first.state(k)
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	if err := first.close(context.Background()); err != nil { // writes the snapshot AND its watermark
		t.Fatalf("close: %v", err)
	}

	second := planeAt(t, dir)
	defer func() { _ = second.close(context.Background()) }()
	holdFolds(t, second)
	again := second.fold(context.Background(), k)
	if again.Folded != 0 {
		t.Fatalf("a restored model re-read %d buckets of history it had already folded — "+
			"its masses now count that history twice", again.Folded)
	}
	after, _, err := second.state(k)
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	if after.Learned != learned.Learned {
		t.Fatalf("the model learned %d events, was %d — the fold is not idempotent across a restart",
			after.Learned, learned.Learned)
	}
}

// TestRings_TheCeilingIsMeasuredNotAsserted: [shards] mirrors velocity's own
// sharding factor, and a mirror nobody checks is a guess. The store applies
// MaxKeys as MaxKeys/shards+1 PER SHARD, so the most it can hold is
// [ringKeyCeiling] — measure it.
//
// Mutation proof: set shards to 1 (or derive ringKeys without the shard
// discount) and the store holds more keys than the ceiling states, which is the
// per-tenant byte budget being exceeded.
func TestRings_TheCeilingIsMeasuredNotAsserted(t *testing.T) {
	vel := newRings()
	at := time.Now().UTC()
	for i := range ringKeyCeiling * 4 {
		vel.record(types.Transaction{
			ID: strconv.Itoa(i), OrgID: "b/o", AccountID: "account:u_" + strconv.Itoa(i),
			USD: 1, Timestamp: at,
		})
	}
	vel.reconcile()
	if held := vel.vel.Keys(); held > ringKeyCeiling {
		t.Fatalf("one tenant's rings hold %d keys against a published ceiling of %d — the per-tenant "+
			"byte budget of %d MiB is understated by %.1fx",
			held, ringKeyCeiling, residentRingBudget>>20, float64(held)/float64(ringKeyCeiling))
	}
	st := vel.strain()
	if !st.Saturated || st.Forgotten == 0 {
		t.Fatalf("%d subjects went into a %d ceiling and the aggregates report saturated=%v forgotten=%d — "+
			"a bound that binds must be readable, not inferred", ringKeyCeiling*4, ringKeyCeiling, st.Saturated, st.Forgotten)
	}
	if st.Bound != ringKeyCeiling {
		t.Fatalf("the reported bound is %d, want the real ceiling %d", st.Bound, ringKeyCeiling)
	}
}

// TestEvent_DefaultIdsDoNotCollideInOneSecond: a caller that sends no id of its
// own is ordinary traffic, and it was being silently dropped.
//
// The default id used to be minted from the kind, the subject and the STAMP —
// and the stamp is truncated to the second, so forty events for one subject
// inside one second were forty events with one id. The record deduplicates on
// (tenant, id), so thirty-nine never reached it; the rings are a projection of
// that record, so after every rollout that subject read as having acted ONCE.
// A detector going quiet on a schedule, for the most ordinary caller there is.
//
// Mutation proof: mint the default id from kind+subject+stamp again and the
// record below holds 1 row instead of 40.
func TestEvent_DefaultIdsDoNotCollideInOneSecond(t *testing.T) {
	probe.reset(true)
	dir := t.TempDir()
	k := key(t, brandA, orgA)
	const n = 40
	at := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)

	p := planeAt(t, dir)
	holdFolds(t, p)
	obs := make([]observation, 0, n)
	for range n {
		// The wire shape a caller sends when it has no id of its own, and every one
		// of them inside the SAME second.
		o, err := riskEvent{Kind: kindAccount, Subject: "u_1", Nano: 1_000_000_000, At: at.Format(time.RFC3339)}.observation(at)
		if err != nil {
			t.Fatalf("observation: %v", err)
		}
		obs = append(obs, o)
	}
	if _, err := p.learn(k, obs...); err != nil {
		t.Fatalf("learn: %v", err)
	}
	if held := recorded(t, p, k); held != n {
		t.Fatalf("%d events for one subject inside one second left %d row(s) on the record — "+
			"the rest are dropped, and the aggregates are a projection of this record", n, held)
	}
	if err := p.close(context.Background()); err != nil {
		t.Fatalf("close: %v", err)
	}

	// And the rollout: the replay must find all of them.
	second := planeAt(t, dir)
	defer func() { _ = second.close(context.Background()) }()
	holdFolds(t, second)
	if _, _, replayed, err := second.rebuild(k); err != nil || replayed != n {
		t.Fatalf("a rollout replayed %d of %d events (err %v) — that subject's velocity reads %d "+
			"after every deploy", replayed, n, err, replayed)
	}
}

// TestLearn_ARetriedBatchConvergesInMemoryToo: the record is idempotent on the
// caller's own event id, which is the property a client with a timeout needs.
// Convergence of the DURABLE half alone is not convergence: the rings and the
// masses are what a decision is made from, and a retry that skipped the rows and
// still moved them counts every event twice in exactly those numbers.
//
// Mutation proof: apply every observation in plane.learn regardless of what note
// reported, and Learned below doubles.
func TestLearn_ARetriedBatchConvergesInMemoryToo(t *testing.T) {
	probe.reset(true)
	p := newTestPlane(t)
	holdFolds(t, p)
	k := key(t, brandA, orgA)
	at := time.Now().UTC().Add(-time.Hour)
	batch := []observation{
		ob(t, "evt-1", kindAccount, "u_1", 100, at),
		ob(t, "evt-2", kindAccount, "u_1", 200, at.Add(time.Second)),
	}
	// THE COUNT IS THE ASSERTION, on both calls. The first learns the batch; the
	// second learns NOTHING, and says so — which is what makes the convergence
	// observable to the caller rather than only true inside the plane. It is also
	// what the call is metered at, so a retry is free.
	for i, want := range []int{len(batch), 0} { // the client timed out and sent it again
		learned, err := p.learn(k, batch...)
		if err != nil {
			t.Fatalf("learn %d: %v", i, err)
		}
		if learned != want {
			t.Fatalf("call %d of a %d-event batch reported learning from %d, want %d — a duplicate is inert",
				i, len(batch), learned, want)
		}
	}
	if held := recorded(t, p, k); held != len(batch) {
		t.Fatalf("the record holds %d rows after a retry of a %d-event batch", held, len(batch))
	}
	st, _, err := p.state(k)
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	if st.Learned != int64(len(batch)) {
		t.Fatalf("the model learned %d times from a retried %d-event batch — the record converged and "+
			"the masses did not, so the numbers a decision is made from are double the events",
			st.Learned, len(batch))
	}
	if got := velocityOf(t, p, k, batch[0]); got != len(batch) {
		t.Fatalf("the subject's 24h count is %d after a retried %d-event batch", got, len(batch))
	}
}

// ── what the aggregates already hold ─────────────────────────────────────────
//
// Everything above holds the aggregates as the MODEL's eight dimensions. These
// hold them as a RULE's two plain counts, which is a different reader with a
// different failure: a model reading blind is a refusal an operator can see, and
// a rule reading zero is a control that allowed.

// TestPrior_ReadsTheNarrowestWindowAndNotAWiderOne.
//
// The count bound is a BURST bound, so it has to be read over the finest window
// the aggregates keep. Read over the widest one, sixty events in a month would
// trip a bound written for sixty events in an hour, and the rule would review the
// organisation's ordinary customers instead of its fast ones.
//
// Mutation proof: take the widest window in [rings.pace] instead of the narrowest
// and this fails, because the events a fortnight back are counted.
func TestPrior_ReadsTheNarrowestWindowAndNotAWiderOne(t *testing.T) {
	probe.reset(true)
	p := newTestPlane(t)
	holdFolds(t, p)
	k := key(t, brandA, orgA)
	now := time.Now().UTC()

	// Three inside the burst window, three well outside it and inside the widest.
	far := []observation{
		ob(t, "far-1", kindAccount, "u_1", 10, now.Add(-20*24*time.Hour)),
		ob(t, "far-2", kindAccount, "u_1", 10, now.Add(-15*24*time.Hour)),
		ob(t, "far-3", kindAccount, "u_1", 10, now.Add(-10*24*time.Hour)),
	}
	near := []observation{
		ob(t, "near-1", kindAccount, "u_1", 10, now.Add(-4*time.Minute)),
		ob(t, "near-2", kindAccount, "u_1", 10, now.Add(-3*time.Minute)),
		ob(t, "near-3", kindAccount, "u_1", 10, now.Add(-2*time.Minute)),
	}
	// Oldest first, which is the order the rings only move forward in.
	if _, err := p.learn(k, append(far, near...)...); err != nil {
		t.Fatalf("learn: %v", err)
	}

	seen, err := p.prior(k, near[0])
	if err != nil {
		t.Fatalf("prior: %v", err)
	}
	if len(seen.Pace) == 0 {
		t.Fatal("the reading names no axis at all, so no bound over it can fire")
	}
	got := seen.Pace[0]
	if got.Axis != axisSubject {
		t.Fatalf("the first axis is %q, want %q — [anomaly.Keys] leads with the account", got.Axis, axisSubject)
	}
	if got.Events != len(near) {
		t.Errorf("the burst window counts %d of %d recent events, with %d older ones on the same "+
			"subject — the bound is written for one window and read over another",
			got.Events, len(near), len(far))
	}
	// And it says which window it was read over, so the bound and the reading
	// cannot be about two different spans.
	if got.Span != time.Hour {
		t.Errorf("the narrowest window is %s, and every bound in [onPace] is stated for the burst "+
			"window — a change here changes what those numbers mean", got.Span)
	}
	if want := nanoOfUSD(float64(10 * len(near))); got.Nano != want {
		t.Errorf("the burst window accrued %d nano, want %d", got.Nano, want)
	}
}

// TestPrior_CountsTheDistinctSubjectsSharingAnIdentifier.
//
// The fan-out is a count of SUBJECTS and never of events. Counted without
// DISTINCT it is a second and worse velocity rule: one busy account reaches the
// bound on its own device, and every ordinary customer is a farm.
//
// Mutation proof: drop DISTINCT from [sharedByDevice] and the busy subject alone
// reaches the bound; drop the LIMIT and the count runs past it.
func TestPrior_CountsTheDistinctSubjectsSharingAnIdentifier(t *testing.T) {
	probe.reset(true)
	p := newTestPlane(t)
	holdFolds(t, p)
	k := key(t, brandA, orgA)
	at := time.Now().UTC().Add(-time.Hour)

	// ONE subject, many events, one device — a busy customer and not a farm.
	busy := make([]observation, 0, fanSubjects*2)
	for i := range fanSubjects * 2 {
		busy = append(busy, ob(t, "busy_"+itoa(i), kindAccount, "u_busy", 1,
			at.Add(time.Duration(i)*time.Second), "", "d_one"))
	}
	if _, err := p.learn(k, busy...); err != nil {
		t.Fatalf("learn: %v", err)
	}
	seen, err := p.prior(k, busy[0])
	if err != nil {
		t.Fatalf("prior: %v", err)
	}
	if len(seen.Shared) != 1 || seen.Shared[0].Axis != axisDevice {
		t.Fatalf("the reading's links are %+v, want the one device the event carries", seen.Shared)
	}
	if seen.Shared[0].Subjects != 1 {
		t.Fatalf("%d events from ONE subject on one device read as %d subjects — the fan-out counts "+
			"subjects, and a busy customer is not a network", len(busy), seen.Shared[0].Subjects)
	}
	if d := onFan(seen); d.fired() {
		t.Errorf("a busy customer's own device was found shared: %+v", d)
	}

	// And now a real one: distinct subjects, one event apiece, the same device.
	farm := make([]observation, 0, 2*fanSubjects)
	for i := range 2 * fanSubjects {
		farm = append(farm, ob(t, "farm_"+itoa(i), kindAccount, "u_farm_"+itoa(i), 1,
			at.Add(time.Duration(i)*time.Second), "", "d_farm"))
	}
	if _, err := p.learn(k, farm...); err != nil {
		t.Fatalf("learn: %v", err)
	}
	seen, err = p.prior(k, farm[0])
	if err != nil {
		t.Fatalf("prior: %v", err)
	}
	// AT THE BOUND AND NOT PAST IT. Twice as many subjects share the device, and
	// the rule asks only whether the bound was reached: counting further is work
	// with no reader, and the LIMIT is what stops it.
	if seen.Shared[0].Subjects != fanSubjects {
		t.Fatalf("%d distinct subjects on one device read as %d, want the bound %d",
			len(farm), seen.Shared[0].Subjects, fanSubjects)
	}
	if d := onFan(seen); !d.fired() {
		t.Errorf("%d distinct subjects on one device determined nothing", len(farm))
	}
}

// TestPrior_ReadsNoIdentifierTheEventDoesNotCarry.
//
// An absent device is ABSENT and never the empty string. Counted as one, every
// anonymous event in the organisation pools into a single identifier that reaches
// any bound immediately — a rule that reviews the whole product because of the
// events that named nothing. It is [anomaly.Keys]' own rule, applied to the link
// identifiers it does not key.
//
// Mutation proof: drop the empty check in [plane.prior] and this fails.
func TestPrior_ReadsNoIdentifierTheEventDoesNotCarry(t *testing.T) {
	probe.reset(true)
	p := newTestPlane(t)
	holdFolds(t, p)
	k := key(t, brandA, orgA)
	at := time.Now().UTC().Add(-time.Hour)

	// Many subjects, none of them naming a device or a counterparty.
	batch := make([]observation, 0, fanSubjects*2)
	for i := range fanSubjects * 2 {
		batch = append(batch, ob(t, "anon_"+itoa(i), kindAccount, "u_anon_"+itoa(i), 1,
			at.Add(time.Duration(i)*time.Second)))
	}
	if _, err := p.learn(k, batch...); err != nil {
		t.Fatalf("learn: %v", err)
	}
	seen, err := p.prior(k, batch[0])
	if err != nil {
		t.Fatalf("prior: %v", err)
	}
	if len(seen.Shared) != 0 {
		t.Fatalf("an event naming no device and no counterparty produced %+v — every anonymous "+
			"event in the organisation would pool into one identifier", seen.Shared)
	}
	if d := onFan(seen); d.fired() {
		t.Errorf("the fan-out fired on identifiers nobody stated: %+v", d)
	}
}

// TestPrior_IsScopedToTheAskingTenant. The reading is a SECOND thing on the decide
// path that reads a tenant's own history, so it is a second thing that could read
// somebody else's. Both halves are held: the rings belong to one resident, and the
// record query carries the qualified tenant as its leading predicate.
//
// IT RUNS THE SHARED-FILE CROSSING, and that is what makes it a test rather than a
// tautology. Two ORGANISATIONS have two shelf FILES, so a query with no tenant
// predicate at all still cannot cross between them — a reader who only tried that
// pair would prove nothing about the predicate. Two BRANDS' identically named
// organisations share ONE file ([TestRecord_TwoBrandsShareAFileAndNotARecord]),
// and the qualified tenant is the only thing that tells their rows apart. So the
// farm is planted under the other BRAND, in the victim's own file.
//
// Mutation proof: drop `tenant = ?` from [sharedByDevice] and the other brand's
// farm is found from this brand's first event.
func TestPrior_IsScopedToTheAskingTenant(t *testing.T) {
	probe.reset(true)
	p := newTestPlane(t)
	holdFolds(t, p)
	victim, other := key(t, brandA, orgA), key(t, brandB, orgA) // SAME org slug, two brands
	if victim.org() != other.org() {
		t.Fatal("the two keys do not share an org slug — the test is not exercising the shared file")
	}
	at := time.Now().UTC().Add(-time.Hour)

	// One tenant runs a farm on a device, at burst speed and at real value.
	loud := make([]observation, 0, burstEvents)
	for i := range burstEvents {
		loud = append(loud, ob(t, "loud_"+itoa(i), kindAccount, "u_loud_"+itoa(i%fanSubjects), 500,
			at.Add(time.Duration(i)*time.Second), "", "d_shared"))
	}
	if _, err := p.learn(other, loud...); err != nil {
		t.Fatalf("learn: %v", err)
	}
	// The other tenant's ONE event names the same device and the same subject
	// spelling, in the same file. Nothing about the first may be found from it.
	quiet := ob(t, "quiet-1", kindAccount, "u_loud_0", 1, at, "", "d_shared")
	if _, err := p.learn(victim, quiet); err != nil {
		t.Fatalf("learn: %v", err)
	}
	seen, err := p.prior(victim, quiet)
	if err != nil {
		t.Fatalf("prior: %v", err)
	}
	for _, w := range seen.Pace {
		if w.Events > 1 {
			t.Errorf("axis %q reads %d events for a tenant that sent one — another tenant's "+
				"traffic is in its aggregates", w.Axis, w.Events)
		}
	}
	for _, s := range seen.Shared {
		if s.Subjects > 1 {
			t.Errorf("%q reads %d subjects for a tenant that has one — another tenant's record "+
				"answered its query", s.Axis, s.Subjects)
		}
	}
	if d := determine("US", 1, seen); d.fired() {
		t.Errorf("a tenant that sent ONE small event was determined %+v on another tenant's history", d)
	}
	// And the crossing is REAL for the tenant that owns it: the same query against
	// the other key finds the farm. Without this the assertions above could pass on
	// a query that finds nothing for anybody.
	loudSeen, err := p.prior(other, loud[0])
	if err != nil {
		t.Fatalf("prior(other): %v", err)
	}
	if d := onFan(loudSeen); !d.fired() {
		t.Fatalf("the farm is not found by the tenant that ran it (%+v) — the assertions above "+
			"prove nothing", loudSeen.Shared)
	}
}

// TestNanoOfUSD_SaturatesRatherThanWrapping. The aggregates accrue in float USD
// and every stated bound is written in int64 nano, so this conversion is on the
// path of every pace determination. One that wrapped would turn the largest
// accrual there is into a small — or negative — one, and the rule would read the
// worst event it will ever see as unremarkable.
func TestNanoOfUSD_SaturatesRatherThanWrapping(t *testing.T) {
	for _, tc := range []struct {
		usd  float64
		want int64
	}{
		{0, 0},
		{-1, 0},         // an aggregate cannot owe money, and zero is the honest reading
		{math.NaN(), 0}, // and NaN is neither greater nor less than zero
		{10_000, freezeNano},
		{50_000, reviewNano},
		{1e30, math.MaxInt64},        // past the ceiling: the largest bound there is
		{math.Inf(1), math.MaxInt64}, //
	} {
		if got := nanoOfUSD(tc.usd); got != tc.want {
			t.Errorf("nanoOfUSD(%v) = %d, want %d", tc.usd, got, tc.want)
		}
	}
	// The DIRECTION is what matters: a saturated value can only make a bound fire.
	d := onPace(reading{Pace: []paced{{Axis: axisSubject, Events: burstEvents, Nano: nanoOfUSD(1e30)}}})
	if d.Action != cloud.ActionRestrict {
		t.Errorf("a burst accruing past the int64 ceiling determined %q, want %q",
			d.Action, cloud.ActionRestrict)
	}
}
