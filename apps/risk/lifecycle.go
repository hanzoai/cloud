package risk

// lifecycle.go is what keeps scoring good over time: which fit decides, which
// one is on trial beside it, how both survive a rollout, and when a new one is
// estimated.
//
// THE ROLLOUT IS THE DESIGN CONSTRAINT, NOT AN AFTERTHOUGHT. cloud deploys with
// strategy Recreate at one replica (universe charts/app/values/hanzo/cloud.yaml).
// Every deploy is a hard all-endpoint window AND drops every in-memory forest,
// every threshold the appetite had computed, and every challenger's counters. So
// the invariant here is stated as a refusal rather than as a best effort: a
// tenant whose champion cannot be restored returns to WARMING and declines to
// score. Never a fresh model scoring from state it does not have — that model
// answers "unremarkable" to everything, which reads as a clean world and is
// indistinguishable from one.
//
// TWO SHAPES MEANS TWO STORES. anomaly.Store carries ONE geometry for every
// tenant it holds, and its Digest — the thing Restore checks — is a function of
// that geometry. So a champion and a challenger of different shapes cannot share
// a store, and the stable below holds one store per shape. It is bounded, and on
// overflow a fit is REFUSED rather than someone's champion evicted: refusing to
// estimate is visible, and silently unseating a live control is not.
//
// THE CHALLENGER SEES EXACTLY WHAT THE CHAMPION SAW. Both read the SAME velocity
// rings, in the same request, after the same single record() call — so the
// comparison is over one traffic stream and one feature vector. Scored from two
// separate streams the pair would be answering two different questions and the
// difference between them would be attributed to the model.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/luxfi/aml/pkg/anomaly"
	"github.com/luxfi/aml/pkg/velocity"
	"github.com/zap-proto/zip"
)

// ── the stable ──────────────────────────────────────────────────────────────

// stable holds the forests this process is serving, ONE STORE PER (TENANT,
// VERSION) PAIR, over the one velocity store every tenant's rings live in.
//
// THE KEY IS THE VERSION, NOT THE GEOMETRY. A version's learned state is its
// own: it was estimated on its own rows, it is kept under its own row in the
// tenant's file, and while it serves it learns from the stream on its own. Two
// versions that happen to share a geometry are still two models, so keying on
// the geometry makes them ONE — the champion and the challenger then advance the
// same counters, the trial compares a model against itself, and promoting a
// version that already shares the incumbent's shape changes nothing while
// reporting that it did. Keyed on the version that cannot be expressed.
//
// The empty version names the SHIPPED model: the geometry this process ships,
// which is what a tenant runs before it has promoted anything, what /v1/ml/train
// teaches and what /v1/ml/snapshot pins. It is a seat like any other, so there
// is no store in this package that is not one tenant's.
//
// ONE TENANT PER STORE, AND THAT IS THE WHOLE POINT. anomaly.Store evicts the
// least recently used TENANT when it fills (anomaly.evict), so a store shared by
// many tenants is a place where one tenant's traffic silently unseats another's
// champion: no error, no log, and the second tenant's control goes quiet while
// reading exactly like a quiet week. A store built with MaxOrgs 1 and keyed by
// the tenant cannot express that — the only key it will ever hold is the tenant
// it was made for, so a tenant can only ever displace itself.
type stable struct {
	// shape is the geometry this process ships — the one an unpromoted tenant
	// runs and the one a fit inherits when the caller names none.
	shape candidate
	vel   *velocity.Store
	// keep persists a seat that is about to be dropped, into the tenant's own
	// file. It is what makes the deployment-wide bound below safe rather than
	// merely bounded: without it, one tenant's traffic throws away another
	// tenant's learning since process start. Called OUTSIDE the lock, on the
	// request that caused the overflow, so the cost lands on whoever spent it.
	keep func(Tenant, string, *anomaly.Store)

	mu   sync.Mutex
	held map[seat]*resident
	// roles is what this process last read as a tenant's serving pair. It is a
	// CACHE of two rows and it exists because the alternative is two SQL reads
	// on a one-connection file on every decision. It is invalidated in process,
	// by the one function that moves a role — there is one replica, so there is
	// no second process to tell, and the manifest says why training and scoring
	// may not be split into two.
	roles map[Tenant]serving
	clock int64
}

// seat is one resident model: a tenant and the version whose state it holds.
// The empty version is the shipped model.
type seat struct {
	tenant Tenant
	fit    string
}

// resident is one seat's store plus the two facts the serving path needs about
// it without going back to the file.
type resident struct {
	store *anomaly.Store
	// shape is the geometry the store was built at. A sealed version's geometry
	// is immutable, so a mismatch is a bug rather than a state — and the answer
	// to it is to rebuild rather than to serve a version's state at a shape it
	// was not estimated under.
	shape string
	// read records that this process has already resolved this seat against the
	// tenant's file. It is EXACT rather than a guess: the seat's store is the
	// only place that state lives in this process and this process is the file's
	// only writer, so "there was nothing" stays true until the seat is dropped.
	// It is what makes hydrate() a map read instead of a snapshot per request.
	read bool
	used int64
}

// serving is the pair of versions a tenant currently runs, as this process last
// read them.
type serving struct {
	champion   ref
	challenger ref
}

// ref names a version and the geometry it needs, which is everything the serving
// path has to know without going back to the file.
type ref struct {
	id    string
	shape candidate
}

func (v serving) each(f func(ref)) {
	for _, r := range [2]ref{v.champion, v.challenger} {
		if r.id != "" {
			f(r)
		}
	}
}

// seatsPerTenant is how many models one tenant may hold resident. Three,
// because three serve: the shipped model, the champion it promoted, and the
// challenger on trial beside it. A fourth is a model nothing is deciding with —
// a version passing through a role change — so the tenant's own least recently
// used goes, which is a tenant degrading only itself and costs one file read.
const seatsPerTenant = 3

// stableSeats is the fleet-wide safety valve: how many resident models this
// process will hold across every tenant. At the measured 336 KB per tenant model
// that is about 172 MB.
//
// A DEPLOYMENT-WIDE BOUND IS A PLACE ONE TENANT SPENDS ANOTHER'S, so this one
// is arranged so that what it takes is recoverable and nothing is lost. Overflow
// drops the least recently used seat, and before it is dropped its learned state
// is KEPT in that tenant's own file; hydrate() then reloads it on that tenant's
// next request. Without the keep, a busy tenant's traffic would silently discard
// a quiet tenant's counters back to its last snapshot — a tenant degrading
// somebody else, which is the failure this fleet keeps finding and the one shape
// a shared bound may never have.
const stableSeats = 512

func newStable(shape candidate, vel *velocity.Store, keep func(Tenant, string, *anomaly.Store)) *stable {
	if keep == nil {
		keep = func(Tenant, string, *anomaly.Store) {}
	}
	return &stable{
		shape: shape, vel: vel, keep: keep,
		held:  map[seat]*resident{},
		roles: map[Tenant]serving{},
	}
}

// shapeOf reads a store's geometry back as a candidate — the same value the
// exhaustive search proposes and a fit records. Derived rather than restated, so
// the default shape has exactly one definition and it is anomaly's own.
func shapeOf(cfg anomaly.Config) candidate {
	return candidate{
		Trees: cfg.Trees, Depth: cfg.Depth, Window: cfg.Window,
		Blend: cfg.Blend, Review: cfg.Appetite.Review,
	}
}

func shapeKey(c candidate) string {
	return fmt.Sprintf("%d/%d/%d/%g/%g", c.Trees, c.Depth, c.Window, c.Blend, c.Review)
}

// at returns the store holding one tenant's model for one version, building it
// at that version's geometry on first ask. It never refuses for capacity: a
// caller asking has a version to serve, and refusing to house it would mean
// declining to score with a model the tenant promoted.
func (st *stable) at(t Tenant, fit string, c candidate) (*anomaly.Store, error) {
	st.mu.Lock()
	r, out, err := st.hold(t, fit, c)
	var store *anomaly.Store
	if r != nil {
		store = r.store
	}
	st.mu.Unlock()
	st.kept(out)
	if err != nil {
		return nil, err
	}
	return store, nil
}

// dropped is one seat the bound removed, on its way to the tenant's own file.
type dropped struct {
	seat
	store *anomaly.Store
}

// kept persists what the bound removed. Called with the lock RELEASED: it writes
// a file, and holding the stable's lock across a write would serialise every
// tenant's model lookup behind one tenant's disk.
func (st *stable) kept(out []dropped) {
	for _, d := range out {
		st.keep(d.tenant, d.fit, d.store)
	}
}

// owed returns one version's store and whether this process still owes a read of
// the tenant's file for it. It is the whole of hydrate's steady-state cost: one
// map lookup, no snapshot, no allocation.
func (st *stable) owed(t Tenant, fit string, c candidate) (*anomaly.Store, bool, error) {
	st.mu.Lock()
	r, out, err := st.hold(t, fit, c)
	// READ UNDER THE LOCK. The seat is shared and read is written by the one
	// function that resolves it, so answering from the struct after the unlock is
	// a race — and one whose losing side re-reads the tenant's file per request.
	var store *anomaly.Store
	var owed bool
	if r != nil {
		store, owed = r.store, !r.read
	}
	st.mu.Unlock()
	st.kept(out)
	if err != nil {
		return nil, false, err
	}
	return store, owed, nil
}

// read marks a seat resolved: this process has looked in the tenant's file for
// it and owes no further look until the seat is dropped. It is marked whatever
// the look FOUND — a version with no kept state is a fact, and re-reading the
// file per request to rediscover it is the cost hydrate exists to avoid.
func (st *stable) read(t Tenant, fit string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if r, ok := st.held[seat{tenant: t, fit: fit}]; ok {
		r.read = true
	}
}

// resident returns a version's store only if one is ALREADY held. Keeping state
// out of a store that never held it would write one version's counters under
// another version's name, so the snapshot path asks this and not at().
func (st *stable) resident(t Tenant, fit string) (*anomaly.Store, bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	r, ok := st.held[seat{tenant: t, fit: fit}]
	if !ok {
		return nil, false
	}
	st.clock++
	r.used = st.clock
	return r.store, true
}

// hold is the one place a seat is resolved or built. Caller holds st.mu, and
// takes the seats the bound dropped so they can be kept with it released.
func (st *stable) hold(t Tenant, fit string, c candidate) (*resident, []dropped, error) {
	k := seat{tenant: t, fit: fit}
	key := shapeKey(c)
	st.clock++
	if r, ok := st.held[k]; ok {
		if r.shape == key {
			r.used = st.clock
			return r, nil, nil
		}
		// A sealed version's geometry cannot move, so this is a defect and not a
		// state. Rebuilding is the fail-secure answer: state estimated under one
		// geometry restored into another is refused by the engine anyway, and
		// serving it under the wrong shape would not be. It is NOT kept: writing a
		// forest of the wrong geometry into the version's row would overwrite the
		// state a correct restore needs.
		delete(st.held, k)
	}
	s, err := anomaly.New(anomaly.Config{
		Trees: c.Trees, Depth: c.Depth, Window: c.Window, Blend: c.Blend,
		Appetite: anomaly.Appetite{Review: c.Review, Sample: 0.001},
		// SHADOW AT THE ENGINE, ALWAYS. What a model's action would have been is
		// decided by this package's own action ladder, never by the engine's. Two
		// gates in series: the engine cannot alert on its own and the tenant's
		// live/shadow switch still governs the outcome.
		Shadow: true,
		// ONE. The store is this tenant's, so the only key it can ever be asked
		// for is this tenant's, and there is no arrangement of other tenants'
		// traffic that reaches it.
		MaxOrgs: 1,
	}, st.vel)
	if err != nil {
		return nil, nil, err
	}
	r := &resident{store: s, shape: key, used: st.clock}
	st.held[k] = r
	return r, st.trim(t), nil
}

// trim enforces both bounds: the tenant's own, then the deployment's. Caller
// holds st.mu. It RETURNS what it removed rather than discarding it, because a
// dropped seat holds counters that are not in the tenant's file yet.
func (st *stable) trim(t Tenant) []dropped {
	var out []dropped
	for {
		var oldest seat
		var at int64
		n := 0
		for k, r := range st.held {
			if k.tenant != t {
				continue
			}
			n++
			if at == 0 || r.used < at {
				oldest, at = k, r.used
			}
		}
		if n <= seatsPerTenant {
			break
		}
		out = append(out, dropped{seat: oldest, store: st.held[oldest].store})
		delete(st.held, oldest)
	}
	for len(st.held) > stableSeats {
		var oldest seat
		var at int64
		for k, r := range st.held {
			if at == 0 || r.used < at {
				oldest, at = k, r.used
			}
		}
		out = append(out, dropped{seat: oldest, store: st.held[oldest].store})
		delete(st.held, oldest)
	}
	return out
}

// serving reads this process's cached view of which versions a tenant runs.
func (st *stable) serving(t Tenant) (serving, bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	v, ok := st.roles[t]
	return v, ok
}

func (st *stable) setServing(t Tenant, v serving) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.roles[t] = v
}

// forget drops a tenant's cached role pair. Called by the ONE function that
// moves a role, so the next request reads the file rather than this process's
// memory of it.
//
// It does NOT drop the seats. A seat holds one version's state and a role change
// moves which version serves, not what any version has learned — so dropping
// them would throw away the retired champion's counters, which is exactly what
// makes a rollback instant rather than a re-warm.
func (st *stable) forget(t Tenant) {
	st.mu.Lock()
	defer st.mu.Unlock()
	delete(st.roles, t)
}

// ── the bench: bounded, queued, cancellable estimation ──────────────────────

// bench is the estimation queue.
//
// AN ESTIMATION MUST NOT BE A CHEAP DENIAL OF SERVICE. It replays thousands of
// rows through a fresh forest — seconds to minutes of a shared pod's CPU — and
// the pod is a single replica. Four bounds, all of them here so none can be
// argued about at a call site: one in flight per tenant, a fleet-wide
// concurrency limit, a wall-clock ceiling, and a row cap on what is read. The
// fifth bound is money and lives at the op, where the caller's own ledger is
// debited before the work is queued.
type bench struct {
	// slots is the fleet-wide concurrency bound. Acquiring it IS the queue: a
	// worker blocks here until a slot frees or its context is cancelled.
	slots chan struct{}
	// probes is the same bound for measurement that runs IN a request — a drift
	// reading replays up to fitRows rows through a fresh forest, which is the
	// estimation's work at a smaller size. It is a separate pool because the two
	// wait differently: an estimation may queue for minutes and a request may
	// not, so a reading must never sit behind a training job.
	probes chan struct{}

	mu   sync.Mutex
	live map[Tenant]benchJob
	// probing is the tenant holding an in-request measurement. ONE PER TENANT is
	// what makes the fleet-wide pool safe: a tenant can occupy at most one of
	// probeSlots, so no arrangement of one tenant's traffic can close the door on
	// another's — the pool bounds the pod's CPU and the per-tenant rule bounds
	// who can spend it.
	probing map[Tenant]bool
}

// benchJob is the one piece of heavy work a tenant has on the bench. It carries
// the KIND as well as the identifier because two kinds queue here — an
// estimation and an exhaustive search — and a caller asking "is a version being
// estimated" must not be answered with a search.
//
// The kind is the BILLED unit's own word ("fit", "search"), so the queue and the
// ledger name the same thing.
type benchJob struct {
	kind   string
	id     string
	cancel context.CancelFunc
}

// The two kinds of work the bench carries. They are the BILLED units' own words,
// so the queue, the ledger and the op all name one thing.
const (
	kindFit    = "fit"
	kindSearch = "search"
)

// benchSlots is how many estimations may run at once across every tenant. Two:
// the pod also serves the decision path, and a decision that waits behind a
// training job is an authorisation that timed out.
const benchSlots = 2

// probeSlots is how many in-request measurements may run at once across every
// tenant. Two, for the same reason and against the same single replica; with one
// per tenant it takes two distinct tenants to fill it.
const probeSlots = 2

// benchDeadline bounds one estimation end to end, queue wait included. Ten
// minutes is the same ceiling the exhaustive search takes, for the same work.
const benchDeadline = 10 * time.Minute

func newBench() *bench {
	return &bench{
		slots:   make(chan struct{}, benchSlots),
		probes:  make(chan struct{}, probeSlots),
		live:    map[Tenant]benchJob{},
		probing: map[Tenant]bool{},
	}
}

// start queues one job for a tenant. A second one for the same tenant is
// REFUSED rather than queued, whatever its kind: a tenant looping either op
// would otherwise hold an unbounded queue of work nobody will read the result
// of, and one tenant's own two kinds competing for the deployment's two slots is
// the same denial with extra steps.
func (b *bench) start(t Tenant, kind, id string, run func(context.Context)) error {
	b.mu.Lock()
	if j, ok := b.live[t]; ok {
		b.mu.Unlock()
		return zip.Errorf(409, "this tenant already has %s %s on the bench", j.kind, j.id)
	}
	ctx, cancel := context.WithTimeout(context.Background(), benchDeadline)
	b.live[t] = benchJob{kind: kind, id: id, cancel: cancel}
	b.mu.Unlock()

	go func() {
		defer func() {
			cancel()
			b.mu.Lock()
			if j, ok := b.live[t]; ok && j.id == id {
				delete(b.live, t)
			}
			b.mu.Unlock()
		}()
		select {
		case b.slots <- struct{}{}:
			defer func() { <-b.slots }()
		case <-ctx.Done():
			run(ctx) // cancelled while queued: the runner records why
			return
		}
		run(ctx)
	}()
	return nil
}

// stop cancels a tenant's in-flight job. It names the identifier so a cancel
// racing a completion cannot take down the next one.
func (b *bench) stop(t Tenant, id string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	j, ok := b.live[t]
	if !ok || j.id != id {
		return false
	}
	j.cancel()
	return true
}

// running names a tenant's in-flight job OF ONE KIND. Asked for a kind rather
// than for whatever is there, because "which version is being estimated" and
// "is a search running" are two questions and one answer to both would report a
// search as a version.
func (b *bench) running(t Tenant, kind string) (string, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	j, ok := b.live[t]
	if !ok || j.kind != kind {
		return "", false
	}
	return j.id, true
}

// probe admits one in-request measurement and returns the release. A second one
// for the same tenant is REFUSED rather than queued, and the fleet-wide slot is
// WAITED for on the caller's own context — so a tenant looping the op spends
// only its own one slot and only its own deadline, and a caller that gave up is
// not still holding a core.
func (b *bench) probe(ctx context.Context, t Tenant) (func(), error) {
	b.mu.Lock()
	if b.probing[t] {
		b.mu.Unlock()
		return nil, zip.Errorf(409, "a measurement is already running for this tenant")
	}
	b.probing[t] = true
	b.mu.Unlock()

	release := func() {
		b.mu.Lock()
		delete(b.probing, t)
		b.mu.Unlock()
	}
	select {
	case b.probes <- struct{}{}:
		return func() { <-b.probes; release() }, nil
	case <-ctx.Done():
		release()
		return nil, zip.Errorf(503, "the measurement queue did not clear inside this request's deadline")
	}
}

// ── serving: which store decides, and which one is on trial ─────────────────

// shipped is the tenant's OWN model at the geometry this process ships: the one
// it runs before it has promoted a version, the one /v1/ml/train teaches and
// /v1/ml/snapshot pins, and the fallback the rules still read a score from.
//
// It loads that tenant's kept state the first time this process asks, and never
// again until the seat is dropped — so an op that reaches for it never reads a
// model with no memory, and reaching for it costs one map lookup.
func shipped(s *stateService, t Tenant) (*anomaly.Store, error) {
	store, owed, err := s.State.stable.owed(t, "", s.State.shape)
	if err != nil {
		return nil, err
	}
	if !owed {
		return store, nil
	}
	settled, err := loadModel(s.State.shelf, store, t)
	if err != nil {
		s.Log.Warn("risk: a tenant's learned state could not be restored; it starts warming",
			"tenant", t.String(), "err", err)
	}
	// Marked only when the answer is final. A tenant with no kept state must not
	// read its file per request to rediscover that — this process is the file's
	// only writer — but a file that could not be read this once must be asked
	// again, or one busy moment silences the control until the next rollout.
	if settled {
		s.State.stable.read(t, "")
	}
	return store, nil
}

// champion returns the store whose score decides for a tenant, and the fit id
// that names it.
//
// A tenant with no promoted fit runs its shipped model. That is not a fallback
// to something weaker — it is the same model the decision plane has always run,
// named.
//
// It reads the role pair hydrate() cached and not the file. Two SQL reads on a
// one-connection store, per decision, to answer a question that changes when a
// person promotes something, is a cost paid for nothing.
func champion(s *stateService, t Tenant) (*anomaly.Store, string) {
	v, ok := s.State.stable.serving(t)
	if !ok || v.champion.id == "" {
		store, err := shipped(s, t)
		if err != nil {
			s.Log.Error("risk: the shipped geometry cannot be housed; this tenant is not scoring",
				"tenant", t.String(), "err", err)
			return nil, ""
		}
		return store, ""
	}
	store, err := s.State.stable.at(t, v.champion.id, v.champion.shape)
	if err != nil {
		// The geometry cannot be housed. Falling back to the shipped model here
		// would silently score with a model the tenant did not promote, so the
		// honest answer is the promoted shape's own absence — which the caller
		// sees as warming, and which the log says out loud once.
		s.Log.Error("risk: the champion fit's geometry cannot be housed; this tenant is not scoring",
			"tenant", t.String(), "fit", v.champion.id, "err", err)
		return nil, v.champion.id
	}
	return store, v.champion.id
}

// challenge scores the challenger on the decision the champion just made.
//
// It runs AFTER the record plane has the decision and contributes nothing to it.
// The rings were advanced once, by decide(), and are not touched again — so the
// challenger reads the identical feature vector the champion read, which is the
// only arrangement under which the two numbers are comparable.
//
// Errors are logged and swallowed. A trial that fails must not fail the
// authorisation it was riding along with; a challenger that cannot be scored is
// a gap in the trial, not a gap in the control.
func challenge(s *stateService, sc scope, db *sql.DB, o observation, out outcome, championFit string) {
	v, ok := s.State.stable.serving(sc.tenant)
	if !ok || v.challenger.id == "" {
		return
	}
	store, err := s.State.stable.at(sc.tenant, v.challenger.id, v.challenger.shape)
	if err != nil {
		s.Log.Warn("risk: the challenger's geometry cannot be housed; the trial is not running",
			"tenant", sc.tenant.String(), "fit", v.challenger.id, "err", err)
		return
	}
	tx, ent := txOf(sc.tenant, o)
	a := store.Inspect(tx, ent)
	// A challenger's store is Shadow at the engine, so a.Alert is always false
	// there by construction. What is recorded is what it WOULD have alerted on,
	// computed with the engine's own strictly-above rule.
	alert := a.Scored && a.Score > a.Cut
	// Assess advances the challenger's own counters on the same stream the
	// champion learned from. A challenger that only inspected would never warm,
	// and a trial whose model never warms proves the incumbent right by default.
	_, _ = store.Assess(tx, ent)
	if err := putChallenge(db, challengeRow{
		Decision: out.id, At: o.at, Champion: championFit, Incumbent: out.score,
		Fit: v.challenger.id, Score: a.Score, Cut: a.Cut, Alert: alert, Scored: a.Scored,
	}); err != nil {
		s.Log.Warn("risk: a challenger's score was not recorded", "fit", v.challenger.id, "err", err)
	}
}

// challengeRow is one side-by-side comparison, appended to its own table and
// never onto the decision. The decision is the RECORD of what was decided;
// amending it with what something else would have decided would make the record
// mutable, and a record that can be rewritten after the fact is not one.
type challengeRow struct {
	Decision  string
	At        time.Time
	Champion  string
	Incumbent float64
	Fit       string
	Score     float64
	Cut       float64
	Alert     bool
	Scored    bool
}

func putChallenge(db *sql.DB, r challengeRow) error {
	_, err := db.Exec(`INSERT INTO challenge (decision, at, champion, incumbent, fit, score, cut, alert, scored)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(decision) DO NOTHING`,
		r.Decision, stamp(r.At), r.Champion, r.Incumbent, r.Fit, r.Score, r.Cut, r.Alert, r.Scored)
	return err
}

// trialDepth is how many of a trial's comparisons are kept. It is the tally's own
// ceiling, and that is the whole argument: nothing reads further back, so a row
// older than this is a row that can only ever be storage.
//
// A trial writes one row PER DECISION for as long as it runs. On a tenant
// authorising a million payments a day that is a million rows a day into a
// SQLite file on a pod with one disk, for a table nothing reads past the most
// recent thousand. Unbounded growth in a per-tenant store is a tenant filling its
// own disk — the only shape of that failure this design accepts, and still not
// one worth accepting when the reader is already bounded.
const trialDepth = 5000

// pruneChunk is how many rows one DELETE removes. The statement holds the
// tenant's ONE connection for its whole run (cloud.OrgDB sets MaxOpenConns(1)),
// so a prune that is not chunked is a decision path that is not answering.
const pruneChunk = 2000

// pruneBudget is how many rows ONE TENANT's prune may remove per tick, across
// every trial on its file. At the tick's five minutes it clears several times
// the arrival rate of the busiest tenant this plane has measured, so a backlog
// drains over a few ticks instead of in one that blocks the tenant for the
// length of the backlog.
const pruneBudget = 20000

// pruneTrials drops each trial's rows past trialDepth, oldest first.
//
// It runs on the tick and not on the insert: a DELETE per decision is a write on
// the authorisation path, paid a million times to remove rows a bounded reader
// was never going to see.
//
// IT IS LINEAR IN WHAT IT DELETES AND IT NEVER HOLDS THE CONNECTION LONG. The
// obvious statement — delete every row with trialDepth newer siblings — is a
// correlated count per row, which is quadratic: measured at 4.4 s over 20k rows
// and 21.3 s over 80k, on the single connection every decision for that tenant
// is queued behind. Here the cut is found ONCE per trial as the timestamp of the
// trialDepth-th newest row, straight off the (fit, at DESC) index, and the rows
// below it are removed in bounded chunks under the caller's deadline. Ties on
// the cut are kept rather than half-deleted, so the bound is trialDepth plus
// however many rows share one instant.
func pruneTrials(ctx context.Context, db *sql.DB) error {
	fits, err := trialFits(ctx, db)
	if err != nil {
		return err
	}
	// ONE budget for the tenant, not one per trial. Spent per trial it is not a
	// bound at all: a tenant with eight trials on file would remove eight times
	// what the constant says, on the single connection its decisions queue
	// behind, and the number would say something the code does not do.
	budget := pruneBudget
	for _, fit := range fits {
		if budget <= 0 {
			return nil
		}
		var cut string
		err := db.QueryRowContext(ctx,
			`SELECT at FROM challenge WHERE fit = ? ORDER BY at DESC LIMIT 1 OFFSET ?`,
			fit, trialDepth-1).Scan(&cut)
		if errors.Is(err, sql.ErrNoRows) {
			continue // this trial is inside the bound
		}
		if err != nil {
			return err
		}
		for budget > 0 {
			chunk := min(pruneChunk, budget)
			res, err := db.ExecContext(ctx, `DELETE FROM challenge WHERE decision IN (
				SELECT decision FROM challenge WHERE fit = ? AND at < ? ORDER BY at ASC LIMIT ?)`,
				fit, cut, chunk)
			if err != nil {
				return err
			}
			n, err := res.RowsAffected()
			if err != nil {
				return err
			}
			if n == 0 {
				break
			}
			budget -= int(n)
		}
	}
	return nil
}

// trialFits lists the trials with rows in this tenant's file. There are at most
// a handful — one per version that has ever been on trial — and the alternative
// is a statement that has to reason about every row to find them.
func trialFits(ctx context.Context, db *sql.DB) ([]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT DISTINCT fit FROM challenge`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var fit string
		if err := rows.Scan(&fit); err != nil {
			return nil, err
		}
		out = append(out, fit)
	}
	return out, rows.Err()
}

// tally is the comparison read back: how the two sides answered the same
// stream. It reports AGREEMENT and the two alert shares rather than declaring a
// winner, because a winner needs judged rows and this table has none — the
// judgements arrive later, on the decisions, and the fit's own metrics are where
// they are measured.
//
// (`trial` would have been the word, and learn.go already holds it for one point
// of the exhaustive search's grid. One word, one meaning.)
type tally struct {
	Fit       string
	Champion  string
	Rows      int
	Scored    int
	Alerted   int
	Incumbent int
	Agreed    int
}

func readTally(db *sql.DB, fit string, limit int) (tally, error) {
	if limit <= 0 || limit > trialDepth {
		limit = 1000
	}
	rows, err := db.Query(`SELECT champion, incumbent, score, cut, alert, scored
		FROM challenge WHERE fit = ? ORDER BY at DESC LIMIT ?`, fit, limit)
	if err != nil {
		return tally{}, err
	}
	defer func() { _ = rows.Close() }()
	out := tally{Fit: fit}
	for rows.Next() {
		var champ string
		var incumbent, score, cut float64
		var alert, scored bool
		if err := rows.Scan(&champ, &incumbent, &score, &cut, &alert, &scored); err != nil {
			return tally{}, err
		}
		out.Rows++
		out.Champion = champ
		if !scored {
			continue
		}
		out.Scored++
		if alert {
			out.Alerted++
		}
		// The incumbent's own cut is not on this row, so its alert is read off
		// the score the decision recorded against the challenger's threshold —
		// which is the comparison an operator wants anyway: same line, two models.
		inc := incumbent > cut
		if inc {
			out.Incumbent++
		}
		if inc == alert {
			out.Agreed++
		}
	}
	return out, rows.Err()
}

// ── hydration: surviving the rollout ────────────────────────────────────────

// fitKey names the row a fit's learned state is kept under in the tenant's own
// model table. One row per fit, because a fit IS the state plus the shape that
// produced it and a second row would be a second answer to one question.
func fitKey(id string) string { return "fit/" + id }

// keepFit snapshots a fit's learned state into the tenant's file.
func keepFit(db *sql.DB, store *anomaly.Store, t Tenant, id string) error {
	snap, ok := store.Snapshot(t.String())
	if !ok {
		return nil // nothing learned under this geometry; nothing to keep
	}
	body, err := json.Marshal(snap)
	if err != nil {
		return err
	}
	return putModel(db, fitKey(id), body)
}

// takeFit restores a fit's learned state.
//
// The snapshot's tenant must be the tenant asking. The engine checks it too;
// checking here as well means a restore of one tenant's file into another's
// request is refused at the boundary that knows who asked, which is this one.
//
// It reports SETTLED on the same rule loadModel does: absent, corrupt, foreign
// or shape-mismatched state is an answer, and a file that could not be read is a
// retry.
func takeFit(db *sql.DB, store *anomaly.Store, t Tenant, id string) (bool, error) {
	body, err := getModel(db, fitKey(id))
	if err != nil {
		return errors.Is(err, errNoState), err
	}
	var snap anomaly.Snapshot
	if err := json.Unmarshal(body, &snap); err != nil {
		return true, err
	}
	if snap.OrgID != t.String() {
		return true, fmt.Errorf("risk: snapshot belongs to another tenant")
	}
	return true, store.Restore(snap)
}

// hydrate makes this process's memory of a tenant match what that tenant's own
// file says it is running. It is the whole answer to Recreate-at-one-replica,
// and it is ONE mechanism rather than two.
//
// IT RUNS ON EVERY REQUEST, NOT ONLY THE FIRST. A once-per-process restore is
// correct exactly until something drops the state — and something does: the
// stable evicts the least recently used seat when it fills. Restored once, an
// evicted tenant never reloads, so its champion comes back empty and scores from
// nothing for the rest of the process's life, which reads as a clean world and is
// indistinguishable from one. Reloading on demand makes an eviction cost one file
// read instead of a control that went quiet.
//
// AND RUNNING PER REQUEST MEANS IT MUST COST A MAP READ. Asking a store whether
// it holds a tenant by taking a snapshot of it costs a deep copy of the whole
// forest and the model's write lock — measured at 209 KB and 51.6 µs per call,
// serialised against the very Assess the request is about. So residency is a
// fact THIS package owns: a seat records that its file has been read, the record
// is exact because this process is that file's only writer, and it dies with the
// seat. Steady state is one map lookup per serving version and nothing else.
//
// A champion that will not restore is left UNRESTORED and said out loud. The
// store then holds nothing for this tenant, the engine reports warming, and the
// decision plane declines to score — which is the same thing it already does on
// eviction. Matching that behaviour is deliberate: two policies for "the model
// has no memory" would eventually disagree.
func hydrate(s *stateService, t Tenant, db *sql.DB) {
	v, known := s.State.stable.serving(t)
	if !known {
		v = readServing(s, db)
		s.State.stable.setServing(t, v)
	}
	v.each(func(r ref) {
		store, owed, err := s.State.stable.owed(t, r.id, r.shape)
		if err != nil {
			s.Log.Error("risk: a promoted fit's geometry cannot be housed",
				"tenant", t.String(), "fit", r.id, "err", err)
			return
		}
		if !owed {
			return
		}
		settled, err := takeFit(db, store, t, r.id)
		if err != nil {
			s.Log.Error("risk: a promoted fit's learned state could not be loaded; this tenant is warming",
				"tenant", t.String(), "fit", r.id, "err", err)
		}
		if settled {
			s.State.stable.read(t, r.id)
		}
	})
	// The shipped model, only when it is the one that decides. A tenant running a
	// promoted champion reaches its shipped model through the ops that own it,
	// and standing a second forest up per request for a model nothing is asking
	// about is 336 KB and a file read spent on nobody's question.
	if v.champion.id == "" {
		if _, err := shipped(s, t); err != nil {
			s.Log.Error("risk: the shipped geometry cannot be housed",
				"tenant", t.String(), "err", err)
		}
	}
}

// readServing reads which versions a tenant runs, from the tenant's own file.
// Only a version that was actually estimated can serve, so a queued or refused
// row in a role — which the index permits and a crash mid-fit produces — names
// no store and is skipped rather than housed empty.
func readServing(s *stateService, db *sql.DB) serving {
	var v serving
	for _, e := range []struct {
		role string
		into *ref
	}{{roleChampion, &v.champion}, {roleChallenger, &v.challenger}} {
		r, ok, err := fitInRole(db, e.role)
		if err != nil {
			s.Log.Warn("risk: a tenant's serving version could not be read", "role", e.role, "err", err)
			continue
		}
		if !ok || r.Status != fitReady {
			continue
		}
		*e.into = ref{id: r.ID, shape: r.Shape}
	}
	return v
}

// keepAll snapshots every RESIDENT model this tenant has, the shipped one
// included. Called on shutdown, and on every role change that stands a fit down
// — so a retired champion keeps the memory that makes rolling back to it
// instant.
//
// It reads the ROLES from the file rather than from the cache, because it is
// called at the moments the cache is about to be or has just been invalidated,
// and because a retired version must be kept too — that is what makes a rollback
// instant rather than a re-warm, and retired is not a role the serving cache
// carries.
//
// A version with no seat is SKIPPED and not built. Building one would keep an
// empty forest under that version's name, overwriting whatever the file held for
// it — the state a rollback is going to want.
func keepAll(s *stateService, t Tenant, db *sql.DB) error {
	if store, held := s.State.stable.resident(t, ""); held {
		if err := saveModel(s.State.shelf, store, t); err != nil {
			return err
		}
	}
	for _, role := range []string{roleChampion, roleChallenger, roleRetired} {
		r, ok, err := fitInRole(db, role)
		if err != nil || !ok || r.Status != fitReady {
			continue
		}
		store, held := s.State.stable.resident(t, r.ID)
		if !held {
			continue
		}
		if err := keepFit(db, store, t, r.ID); err != nil {
			return err
		}
	}
	return nil
}

// ── the schedule ────────────────────────────────────────────────────────────

// scheduleKey is the setting the retrain policy lives under, in the tenant's own
// settings table — the same place the live/shadow switch lives, because both are
// governed policy and there is one place a tenant's policy is kept.
const scheduleKey = "fit.schedule"

// schedule is when a tenant's model is re-estimated without anyone asking.
//
// BOTH TRIGGERS ARE ONE PREDICATE. "Scheduled" is Every elapsing and
// "event-driven" is Labels being reached; they are read together by due(), so
// there is one place that decides and no way for the two to disagree about
// whether a retrain is owed.
//
// AND NEITHER TRIGGER PROMOTES. A scheduled fit lands as a candidate or, if the
// tenant asked, as a challenger — never as the champion. A model that took over
// the decision path because a timer fired is a control nobody decided to run,
// and the transition record would name a clock as its author.
type schedule struct {
	// Every is hours between re-estimations. Zero is off, which is the default:
	// automatic retraining is something a tenant turns on.
	Every int `json:"every"`
	// Labels is how many NEW matured judgements must have arrived since the last
	// fit before one is owed. It is the event trigger, and it counts MATURED
	// rows — a burst of fresh disputes is precisely the moment not to retrain,
	// because their horizon has not run and the set they would form is the tail
	// of an argument rather than a conclusion.
	Labels int `json:"labels"`
	// Horizon, Window and Rows are the estimation's own bounds, so an automatic
	// fit is shaped by the same policy an explicit one takes.
	Horizon int `json:"horizon"`
	Window  int `json:"window"`
	Rows    int `json:"rows"`
	// Role is where an automatic fit lands: candidate or challenger. Champion is
	// refused at the setter, not merely unused here.
	Role string `json:"role"`
}

func (s schedule) withDefaults() schedule {
	if s.Horizon <= 0 {
		s.Horizon = defaultHorizon
	}
	if s.Window <= 0 {
		s.Window = defaultWindow
	}
	if s.Rows <= 0 || s.Rows > fitRows {
		s.Rows = fitRows
	}
	if s.Role != roleChallenger {
		s.Role = roleCandidate
	}
	return s
}

func getSchedule(db *sql.DB) (schedule, error) {
	var body string
	err := db.QueryRow(`SELECT value FROM setting WHERE key = ?`, scheduleKey).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		return schedule{}.withDefaults(), nil
	}
	if err != nil {
		return schedule{}, err
	}
	var s schedule
	if err := json.Unmarshal([]byte(body), &s); err != nil {
		return schedule{}, err
	}
	return s.withDefaults(), nil
}

func putSchedule(db *sql.DB, s schedule) error {
	body, err := json.Marshal(s)
	if err != nil {
		return err
	}
	_, err = db.Exec(`INSERT INTO setting (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, scheduleKey, string(body))
	return err
}

// due answers whether a re-estimation is owed. One predicate, both triggers, and
// it says WHY so the answer is readable rather than merely true.
//
// A tenant with no new matured judgement since its last fit is SKIPPED, not
// retrained: re-estimating the same rows produces the same artefact with a new
// identifier, which fills the registry with fits nobody can tell apart and
// resets nothing.
func due(s schedule, last time.Time, sinceLast int, now time.Time) (bool, string) {
	if s.Every <= 0 {
		return false, ""
	}
	if !last.IsZero() && now.Sub(last) < time.Duration(s.Every)*time.Hour {
		return false, ""
	}
	if s.Labels > 0 && sinceLast < s.Labels {
		return false, ""
	}
	if last.IsZero() {
		return true, "no fit has been estimated for this tenant yet"
	}
	if s.Labels > 0 {
		return true, fmt.Sprintf("%d new matured judgements since the last fit", sinceLast)
	}
	return true, fmt.Sprintf("%s since the last fit", now.Sub(last).Round(time.Hour))
}

// maturedSince counts judgements on rows that have aged past the horizon and
// were judged after a given instant. It is the event trigger's arithmetic and
// the only reason the trigger is honest: counting raw label arrivals would fire
// on a burst of disputes whose horizon has not run.
func maturedSince(db *sql.DB, horizon int, since time.Time, now time.Time) (int, error) {
	mature := stamp(now.AddDate(0, 0, -horizon))
	var n int
	err := db.QueryRow(`SELECT COUNT(*) FROM decision
		WHERE label != '' AND label != 'unknown' AND at <= ? AND label_at > ?`,
		mature, stamp(since)).Scan(&n)
	return n, err
}

// lastFit is when this tenant last had a fit estimated, and its identifier.
func lastFit(db *sql.DB) (time.Time, string, error) {
	var at, id string
	err := db.QueryRow(`SELECT at, id FROM fit ORDER BY at DESC LIMIT 1`).Scan(&at, &id)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, "", nil
	}
	if err != nil {
		return time.Time{}, "", err
	}
	return unstamp(at), id, nil
}

// ── the tick ────────────────────────────────────────────────────────────────

// lifecycleEvery is how often the resident tenants are examined for an owed
// re-estimation and for drift. Five minutes: fine enough that an hourly schedule
// means the hour it says, coarse enough that the walk is invisible next to the
// decision path.
const lifecycleEvery = 5 * time.Minute

// tickBudget bounds ONE walk of the resident tenants, end to end. It is under
// lifecycleEvery so two walks can never overlap, and it is the outer bound the
// per-tenant one nests inside: without it a walk is as long as the sum of its
// tenants, which on a busy deployment is longer than the interval that starts
// the next one.
const tickBudget = 4 * time.Minute

// tenantBudget bounds one tenant's examination inside the walk. It is what stops
// one tenant's backlog from consuming the walk and leaving every tenant after it
// in the map unexamined — a tenant may only ever degrade itself.
const tenantBudget = 45 * time.Second

// lifecycle is the app's second background loop. It walks the tenants THIS
// PROCESS currently holds open, which is exactly the set with recent traffic —
// a tenant with none has no new matured judgement to trigger on and nothing to
// drift, and opening every org's file to discover that is a fleet-wide walk that
// grows with the customer list.
func lifecycle(s *stateService) {
	for {
		time.Sleep(lifecycleEvery)
		tick(s, time.Now())
	}
}

func tick(s *stateService, now time.Time) {
	walk, stop := context.WithTimeout(context.Background(), tickBudget)
	defer stop()
	for _, t := range s.State.shelf.tenants() {
		if walk.Err() != nil {
			s.Log.Warn("risk: the model walk ran out of budget; the remaining tenants are examined on the next tick")
			return
		}
		db, err := s.State.shelf.open(t)
		if err != nil {
			continue
		}
		ctx, done := context.WithTimeout(walk, tenantBudget)
		err = watch(ctx, s, t, db, now)
		done()
		if err != nil {
			s.Log.Warn("risk: a tenant's model could not be examined", "tenant", t.String(), "err", err)
		}
	}
}

// watch is one tenant's examination: is a re-estimation owed, and has the
// champion drifted. Every statement it runs takes the caller's context, so the
// walk's budget is a real bound and not a comment — the tenant's file has ONE
// connection and every decision for that tenant queues behind whatever this
// holds.
func watch(ctx context.Context, s *stateService, t Tenant, db *sql.DB, now time.Time) error {
	sched, err := getSchedule(db)
	if err != nil {
		return err
	}
	last, _, err := lastFit(db)
	if err != nil {
		return err
	}
	n, err := maturedSince(db, sched.Horizon, last, now)
	if err != nil {
		return err
	}
	if err := pruneTrials(ctx, db); err != nil {
		s.Log.Warn("risk: a tenant's trial rows were not pruned", "tenant", t.String(), "err", err)
	}
	if ok, why := due(sched, last, n, now); ok {
		if _, running := s.State.bench.running(t, kindFit); !running {
			// The scheduler's scope: the tenant's own org, no project and no
			// validated project claim — a background path has no principal to bind
			// one to, so only the org- and service-scoped caps enforce. It still
			// pays: enqueue gates and meters whoever asks, and a timer asking is
			// still this tenant asking.
			sc := scope{tenant: t, org: t.org()}
			if _, err := enqueue(ctx, s, sc, db, "schedule", sched.Role, mlFitIn{
				Algo: algoForest, Horizon: sched.Horizon, Window: sched.Window, Rows: sched.Rows,
				Note: why,
			}, now); err != nil {
				s.Log.Warn("risk: a scheduled re-estimation was not queued", "tenant", t.String(), "err", err)
			}
		}
	}
	if err := alarm(ctx, s, t, db, now); err != nil {
		return err
	}
	// The background paths write records too — a queued version, a raised drift
	// alarm, a pruned trial — and no request is going to ship them. There is no
	// caller here to refuse, so an unacked ship is said out loud and retried on
	// the next tick, which is the same five minutes this walk already runs on.
	if acked, err := s.State.shelf.ship(ctx, t, db); err != nil || !acked {
		s.Log.Warn("risk: a tenant's background records were not shipped; the next tick retries",
			"tenant", t.String(), "acked", acked, "err", err)
	}
	return nil
}

// ── enqueue: the one path a fit is created by ───────────────────────────────

// enqueue writes the registry row and hands the estimation to the bench.
//
// It is the ONE path — the typed op and the scheduler both come through here —
// because the two differ in exactly one thing (who asked) and everything else
// about creating a fit is the same act. Two paths would be two places for the
// bounds to be applied, and one of them would eventually be the weaker. That
// included MONEY until this: the op gated and metered and the scheduler did
// not, so a tenant that set an hourly schedule bought unlimited estimations
// competing for the same two fleet-wide slots as the ones somebody paid for.
// The fifth bound now lives here with the other four.
//
// It RETURNS the identifier it minted. Reading it back off the bench instead
// would be a race the caller cannot win: an estimation that finishes — or is
// refused, or is cancelled — before the op looks leaves the bench empty, and the
// op would then answer 404 for a fit it had just created successfully.
func enqueue(ctx context.Context, s *stateService, sc scope, db *sql.DB, by, land string, in mlFitIn, now time.Time) (string, error) {
	t := sc.tenant
	algo := strings.TrimSpace(in.Algo)
	if algo == "" {
		algo = algoForest
	}
	if algo != algoForest {
		return "", zip.Errorf(422,
			"%q is not an estimator this plane can run; the one it can is %q", algo, algoForest)
	}
	shape := s.State.shape
	if in.Shape != nil {
		shape = candidate(*in.Shape)
	}
	if err := checkShape(shape); err != nil {
		return "", err
	}
	// The ledger, before anything is written or queued. Whoever asked — a person
	// or this tenant's own schedule — pays for the CPU on their own org.
	if err := s.State.bill.Gate(ctx, sc.org, sc.project, sc.validate, "fit", fitCents); err != nil {
		return "", zip.Errorf(402, "%s", err.Error())
	}

	horizon := in.Horizon
	if horizon <= 0 {
		horizon = defaultHorizon
	}
	window := in.Window
	if window <= 0 {
		window = defaultWindow
	}
	rows := in.Rows
	if rows <= 0 || rows > fitRows {
		rows = fitRows
	}
	version, err := nextVersion(db, sourceHistory)
	if err != nil {
		return "", err
	}

	id := newID("fit")
	to := now
	from := now.AddDate(0, 0, -window)
	r := fitRow{
		ID: id, At: now, By: by, Algo: algo, Shape: shape, Role: roleCandidate, Status: fitQueued,
		Source: fitSource{Name: sourceHistory, Version: version, From: from, To: to, Horizon: horizon},
	}
	if err := putFit(db, r); err != nil {
		return "", err
	}
	if err := s.State.bench.start(t, kindFit, id, func(ctx context.Context) {
		run(ctx, s, t, db, r, rows, land)
	}); err != nil {
		_ = markFit(db, id, fitRefused, err.Error())
		return "", err
	}
	// Metered on the same ledger the gate above checked, after the work is
	// admitted rather than after it finishes: what was bought is a slot on the
	// bench, and a caller who cancels has still spent one.
	s.State.bill.Meter(sc.org, sc.project, "fit", fitCents, sc.request, sc.clientIP)
	return id, nil
}

// checkShape holds a caller-supplied geometry to the grid the exhaustive search
// already searches. It is a CLOSED range and not a validation of taste: 400
// trees at depth 20 is 400 * 2^21 nodes, which is not a model, it is an
// allocation, and it is reachable by anyone with a key.
func checkShape(c candidate) error {
	switch {
	case c.Trees < 5 || c.Trees > 64:
		return zip.Errorf(422, "trees must be between 5 and 64")
	case c.Depth < 4 || c.Depth > 12:
		return zip.Errorf(422, "depth must be between 4 and 12")
	case c.Window < 32 || c.Window > 4096:
		return zip.Errorf(422, "window must be between 32 and 4096")
	case c.Blend <= 0 || c.Blend > 1:
		return zip.Errorf(422, "blend must be in (0, 1]")
	case c.Review <= 0 || c.Review > 0.5:
		return zip.Errorf(422, "review appetite must be in (0, 0.5]")
	}
	return nil
}

// run is the estimation, off the request path.
//
// Every exit writes the row. A fit that stops without saying why is worse than
// one that failed: the queue looks busy, the registry looks patient, and nobody
// learns that the model stopped being re-estimated three weeks ago.
func run(ctx context.Context, s *stateService, t Tenant, db *sql.DB, r fitRow, rows int, land string) {
	if err := ctx.Err(); err != nil {
		_ = markFit(db, r.ID, fitCancelled, "cancelled before it began")
		return
	}
	if err := markFit(db, r.ID, fitFitting, ""); err != nil {
		s.Log.Error("risk: a fit could not be marked as running", "fit", r.ID, "err", err)
		return
	}

	hist, err := replayHistory(db, rows)
	if err != nil {
		_ = markFit(db, r.ID, fitRefused, err.Error())
		return
	}
	admitted := matured(hist, r.Source.From, r.Source.To, r.Source.Horizon, time.Now())
	if len(admitted) < minFitRows {
		_ = markFit(db, r.ID, fitRefused, fmt.Sprintf(
			"%d rows matured past the %d-day horizon in this window; %d is the floor below which a measurement "+
				"is indistinguishable from noise", len(admitted), r.Source.Horizon, minFitRows))
		return
	}
	train, test, cut := split(admitted, trainShare)
	// A group-temporal split over a window dominated by a few subjects can put
	// nearly everything on one side. That is the grouping doing exactly what it
	// must — a subject may not straddle the cut — and it is an unusable
	// measurement, so it is REFUSED rather than reported as a fit measured on
	// three rows.
	if held := int(float64(len(admitted)) * minHeld); len(train) == 0 || len(test) < held {
		_ = markFit(db, r.ID, fitRefused, fmt.Sprintf(
			"the temporal cut held out %d of %d rows because too few subjects straddle it; a measurement needs "+
				"at least %d, and a subject may not appear on both sides", len(test), len(admitted), held))
		return
	}

	inv := anomaly.Inventory()
	dims := make([]string, 0, len(inv))
	for i := range inv {
		dims = append(dims, inv[i].Name)
	}
	src := r.Source
	src.Cut, src.Rows, src.Train, src.Test = cut, len(admitted), len(train), len(test)
	src.Dims, src.Inventory = dims, s.State.digest
	for _, row := range test {
		switch disposition(row.label) {
		case "productive":
			src.Judged++
			src.Productive++
		case "unproductive":
			src.Judged++
			src.Unproductive++
		}
	}
	src.Digest = sourceDigest(train, test, dims)

	seed, err := newSeed()
	if err != nil {
		_ = markFit(db, r.ID, fitRefused, err.Error())
		return
	}
	snap, metrics, profile, err := estimate(ctx, t, r.Shape, seed, train, test)
	if err != nil {
		status := fitRefused
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			status = fitCancelled
		}
		_ = markFit(db, r.ID, status, err.Error())
		return
	}

	// The learned state lands BEFORE the row is sealed. Sealed first, a crash
	// between the two leaves a fit the registry calls ready and the store cannot
	// serve — which is exactly the state hydrate() has to refuse, discovered a
	// rollout later instead of now.
	body, err := json.Marshal(snap)
	if err != nil {
		_ = markFit(db, r.ID, fitRefused, err.Error())
		return
	}
	if err := putModel(db, fitKey(r.ID), body); err != nil {
		_ = markFit(db, r.ID, fitRefused, err.Error())
		return
	}
	if err := sealFit(db, r.ID, src, metrics, profile, fitDigest(r.Algo, r.Shape, src)); err != nil {
		if !errors.Is(err, errNotFitting) {
			s.Log.Error("risk: a completed fit could not be sealed", "fit", r.ID, "err", err)
		}
		return
	}

	if land == roleChallenger {
		sealed, err := getFit(db, r.ID)
		if err == nil {
			if _, err := setRole(db, sealed, roleChallenger, r.By, "the schedule enrols its fits as challengers", time.Now()); err != nil {
				s.Log.Warn("risk: a scheduled fit was not enrolled as challenger", "fit", r.ID, "err", err)
			}
			// The serving pair moved without a request doing it, so this process's
			// memory of who is on trial is stale until it is dropped. A cache the
			// background loop can leave stale is a challenger that never scores.
			s.State.stable.forget(t)
		}
	}
	s.Log.Info("risk: a fit was estimated",
		"tenant", t.String(), "fit", r.ID, "rows", src.Rows, "judged", src.Judged, "digest", fitDigest(r.Algo, r.Shape, src))
}

// trainShare is how much of the admitted window the model learns from. Seven
// tenths: enough left over that the held-out measurement is a measurement, and
// the split is temporal so the tenth that decides is the most recent behaviour.
const trainShare = 0.7

// minFitRows is the floor below which a fit is refused. Below a few hundred rows
// the held-out split holds tens, and a separation measured on tens is a number
// with no interval around it that anyone would report.
const minFitRows = 200

// minHeld is the smallest share of the window the held-out side must carry. It
// exists because the split groups by SUBJECT after cutting by time, so a window
// where a handful of accounts do everything can legitimately land almost whole
// on one side.
const minHeld = 0.1

// ── the schedule's contract ─────────────────────────────────────────────────

// mlScheduleIn sets when this tenant's model is re-estimated without anyone
// asking.
type mlScheduleIn struct {
	// Every is hours between re-estimations. Zero turns automatic estimation
	// off, which is the default: a model that starts re-estimating itself
	// because a default said so is a control nobody chose to run.
	Every int `json:"every"`
	// Labels is how many NEW MATURED judgements must have arrived since the last
	// version before another is owed. It counts matured rows, never raw label
	// arrivals: a burst of fresh disputes is exactly the moment not to
	// re-estimate, because their horizon has not run and the set they would form
	// is the tail of an argument rather than a conclusion.
	Labels int `json:"labels,omitempty"`
	// Horizon is how many days a row must have aged before an automatic
	// estimation may admit it. Default 120, the far side of the card dispute
	// windows.
	Horizon int `json:"horizon,omitempty"`
	// Window is how many days of this tenant's own history each automatic
	// estimation reads. Default 365.
	Window int `json:"window,omitempty"`
	// Rows caps how many recorded observations an automatic estimation replays,
	// 1..5000. It is the same bound an explicit request takes, because it is the
	// same work on the same shared pod.
	Rows int `json:"rows,omitempty"`
	// Role is where an automatic version lands: candidate, or challenger to put
	// it straight on trial. Champion is REFUSED — a model that took over the
	// decision path because a timer fired is a change whose author is a clock.
	Role string `json:"role,omitempty"`
}

// mlScheduleOut is the policy plus what it is currently waiting for, so "why has
// nothing been re-estimated" is answerable without reading two more endpoints.
type mlScheduleOut struct {
	// Every is hours between automatic re-estimations, zero when they are off.
	Every int `json:"every"`
	// Labels is how many new matured judgements must arrive before another
	// version is owed, zero when only the clock triggers.
	Labels int `json:"labels"`
	// Horizon is how many days a row must have aged to be admitted.
	Horizon int `json:"horizon"`
	// Window is how many days of history each automatic estimation reads.
	Window int `json:"window"`
	// Rows caps how many observations each automatic estimation replays.
	Rows int `json:"rows"`
	// Role is where an automatic version lands: candidate, or challenger to put
	// it straight on trial. Never champion.
	Role string `json:"role"`
	// Last is when a version was last estimated, empty when none ever has been.
	Last string `json:"last,omitempty"`
	// Matured is how many new matured judgements have arrived since then — the
	// number the Labels trigger is waiting on.
	Matured int `json:"matured"`
	// Due reports whether a re-estimation is owed right now.
	Due bool `json:"due"`
	// Says is why it is owed, or what the trigger is still waiting for. It is
	// here so "why has nothing been re-estimated" is answerable without reading
	// two more endpoints and doing the arithmetic by hand.
	Says string `json:"says,omitempty"`
	// Estimating names the version being fitted, if one is.
	Estimating string `json:"estimating,omitempty"`
}

// Schedule reads when this tenant's model is re-estimated, and what the
// automatic trigger is currently waiting for.
func (o ops) schedule(ctx context.Context, _ *mlNoInput) (*mlScheduleOut, error) {
	sc, db, err := tenantState(ctx, o.s)
	if err != nil {
		return nil, err
	}
	return o.scheduleOut(sc, db, time.Now())
}

// SetSchedule sets when this tenant's model is re-estimated.
//
// A schedule may enrol its versions as candidates or as challengers and NEVER as
// champions: the trigger decides that a new estimation is owed, and a person
// decides which model gets to answer a customer.
//
// Example: {"every": 168, "labels": 50, "role": "challenger"}
func (o ops) setSchedule(ctx context.Context, in *mlScheduleIn) (*mlScheduleOut, error) {
	sc, db, err := tenantState(ctx, o.s)
	if err != nil {
		return nil, err
	}
	switch strings.TrimSpace(in.Role) {
	case "", roleCandidate, roleChallenger:
	case roleChampion:
		return nil, zip.Errorf(422,
			"a schedule may not promote; a version reaching the decision path is a decision somebody makes and is recorded with its reason")
	default:
		return nil, zip.Errorf(422, "role must be candidate or challenger")
	}
	if in.Every < 0 || in.Every > 24*365 {
		return nil, zip.Errorf(422, "every must be between 0 (off) and 8760 hours")
	}
	if in.Labels < 0 {
		return nil, zip.Errorf(422, "labels must not be negative")
	}
	if in.Horizon < 0 || in.Horizon > 3650 {
		return nil, zip.Errorf(422, "horizon must be between 0 and 3650 days")
	}
	if in.Window < 0 || in.Window > 3650 {
		return nil, zip.Errorf(422, "window must be between 0 and 3650 days")
	}
	if in.Rows < 0 || in.Rows > fitRows {
		return nil, zip.Errorf(422, "rows must be between 0 and %d", fitRows)
	}
	s := schedule{
		Every: in.Every, Labels: in.Labels, Horizon: in.Horizon,
		Window: in.Window, Rows: in.Rows, Role: strings.TrimSpace(in.Role),
	}.withDefaults()
	if err := putSchedule(db, s); err != nil {
		return nil, err
	}
	return o.scheduleOut(sc, db, time.Now())
}

func (o ops) scheduleOut(sc scope, db *sql.DB, now time.Time) (*mlScheduleOut, error) {
	s, err := getSchedule(db)
	if err != nil {
		return nil, err
	}
	last, _, err := lastFit(db)
	if err != nil {
		return nil, err
	}
	n, err := maturedSince(db, s.Horizon, last, now)
	if err != nil {
		return nil, err
	}
	ok, says := due(s, last, n, now)
	out := &mlScheduleOut{
		Every: s.Every, Labels: s.Labels, Horizon: s.Horizon, Window: s.Window,
		Rows: s.Rows, Role: s.Role, Matured: n, Due: ok, Says: says,
	}
	if !last.IsZero() {
		out.Last = stamp(last)
	}
	if says == "" {
		switch {
		case s.Every <= 0:
			out.Says = "automatic re-estimation is off"
		case s.Labels > 0 && n < s.Labels:
			out.Says = fmt.Sprintf("waiting for %d more matured judgements", s.Labels-n)
		default:
			out.Says = fmt.Sprintf("next due in %s",
				(time.Duration(s.Every)*time.Hour - now.Sub(last)).Round(time.Minute))
		}
	}
	if id, running := o.s.State.bench.running(sc.tenant, kindFit); running {
		out.Estimating = id
	}
	return out, nil
}
