package risk

// learn.go — THE MODEL PLANE. One model per organisation, learned from that
// organisation's own first-party data, and nothing else.
//
// WHERE THE TRAINING HAPPENS: in this process, on half-space trees
// (github.com/luxfi/aml/pkg/anomaly). Not a service call, not a CRD, not a GPU.
// That is a decision with reasons, and they are the reasons the moat is cheap:
//
//   - There is no training pass, no retained sample and no retraining job. The
//     geometry is built BEFORE any data arrives — a random dimension, a split at
//     the node midpoint — so the model IS a set of mass counters and learning is
//     an increment. A tenant's model is current the instant its last event lands.
//   - The geometry is PER TENANT, seeded from mix(seed, tenant). Two
//     organisations do not merely have different counters; they have different
//     trees. Probing one reveals nothing about where another's regions lie.
//   - EVERY piece of a tenant's state is the tenant's own: its trees, its
//     counters, its stated appetite AND its sliding aggregates (ring.go). Nothing
//     is shared, so nothing a tenant does can evict, move or read anything of
//     another's. The bounds are per tenant too, so a tenant at its own bound
//     degrades itself and no one else.
//   - It is bounded and durable: a measured ~336 KB of counters per tenant, with
//     [anomaly.Store.Snapshot] and [anomaly.Store.Restore] over the per-org
//     encrypted SQLite the rest of the fleet already uses, and the aggregates
//     rebuilt deterministically from the tenant's own record of what it taught.
//   - Attribution is a COUNTERFACTUAL on the model that raised the alert: move
//     one coordinate to its neutral value, rescore, and the drop IS that
//     feature's contribution. No second explainer model, and no gap between what
//     scored and what is explained.
//   - It cannot act alone. Evidence is capped at review by the engine's own
//     ceiling, weight is non-negative, and a refusal is COUNTED rather than
//     silent — because silence must never read as a clean result.
//
// WHAT IT LEARNS FROM: the organisation's own feature surface (feature.go), which
// is its own event surface rolled up — product events, captured failures, metered
// inference. Nothing from another tenant reaches a model. The only cross-org
// signal that exists at all is the aggregate baseline (baseline.go), and no model
// reads it: it is published for a human to compare against, not folded into a
// score.
//
// SHADOW IS THE DEFAULT, PER TENANT. A model that quietly went live and started
// refusing payments is the worst failure available here, so a new tenant's model
// scores, learns and records what it WOULD have alerted on, and contributes
// nothing, until its organisation states otherwise. GET /v1/ml/state reports the
// stated appetite beside the realised one, which is what makes the appetite a
// measured commitment rather than an intention.

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/luxfi/aml/pkg/anomaly"
	"github.com/luxfi/aml/pkg/types"
	"github.com/luxfi/aml/pkg/velocity"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// reportingThreshold is the value a transaction is judged "just under" against,
// in USD. Ten thousand is the cash-reporting limit the structuring features are
// defined around (Regulation (EU) 2024/1624 Art. 69(2); 31 CFR 1010.311). It is a
// STATED constant rather than a tuned one: the sub-threshold features measure
// distance from a published limit, and a limit chosen to make a detector look
// good is not a limit.
const reportingThreshold = 10_000.0

// warmWindow is how much of a tenant's own surface is folded in when its model
// first becomes resident. Thirty days is the longest window the feature inventory
// reads, so folding more would add nothing the rings can hold — it is the same
// [ringWindow], named from the fold's side.
const warmWindow = ringWindow

// observation is one thing that happened, in the vocabulary the model reads. It
// is deliberately NOT the wire shape: the wire is a contract with callers and
// this is a contract with the engine, and folding them into one type is how a
// caller ends up able to name a tenant.
type observation struct {
	// ID identifies the event. It selects the below-the-line review sample by
	// hash, so it must be the caller's own stable id and not a counter.
	ID string
	// Kind and Subject are whose behaviour this is. Kind is one of [kinds].
	Kind, Subject string
	// USD is the value moved, already converted. Zero where the event moves no
	// money, which leaves the value features blind rather than fabricating a
	// currency for an activity event.
	USD float64
	// Peer is the counterparty, and Device the device fingerprint. Each is an
	// aggregation AXIS: the pair axis is what makes "unfamiliar" a fact about a
	// relationship, and the device axis is what surfaces several nominally
	// unrelated subjects acting as one.
	Peer, Device string
	// At is when it happened.
	At time.Time
}

// tx renders an observation as the engine's transaction, under the QUALIFIED
// tenant key. The key is the model index, the aggregate key and the snapshot
// identity, so it is set HERE, once, from a value the caller could not supply.
//
// The subject is namespaced by its kind, so a person and an account that happen
// to share an identifier are two subjects and never one aggregate.
func (o observation) tx(t tenant) types.Transaction {
	return types.Transaction{
		ID:                o.ID,
		OrgID:             string(t),
		AccountID:         o.Kind + ":" + o.Subject,
		Counterparty:      o.Peer,
		DeviceFingerprint: o.Device,
		USD:               o.USD,
		Notional:          o.USD,
		Currency:          "USD",
		Timestamp:         o.At,
	}
}

// ── the plane ────────────────────────────────────────────────────────────────

// resident is one tenant's model: its own anomaly store (its own geometry, its
// own appetite, its own shadow switch) over ITS OWN aggregate rings.
//
// The rings are held here and not on the plane, and that placement is the whole
// tenant-isolation argument for this file. A ring set on the plane is one map
// with one cap that every tenant writes into, so the busiest organisation evicts
// the quietest one's keys and the quiet one's velocity features go blind with no
// error and no alert. Held here they have one owner, one bound and one lifetime.
type resident struct {
	mu   sync.Mutex
	key  tenant
	cfg  anomaly.Config
	mod  *anomaly.Store
	vel  *velocity.Store // THIS tenant's aggregates; see ring.go
	warm bool            // the tenant's own surface has been folded in
	// edge is the newest timestamp the rings carry. The rings only move forward
	// (see [placeable]), so this is what a backdated observation is measured
	// against before it is allowed to claim it happened now.
	edge time.Time
	// replayed is how many of this tenant's own recorded observations rebuilt its
	// rings when it became resident. Held here so the fold can quote it whenever
	// the fold actually starts, which is not always the request that planted it.
	replayed int
	// warmed is how far this tenant's own surface has ALREADY been folded into the
	// masses this model holds. It travels with the snapshot (one row, one fact) and
	// it is what stops a fold from being a re-fold.
	//
	// Without it, a model restored from its own snapshot and then warmed over the
	// same thirty days learns that history a second time — and with a resident
	// bound that a busy fleet hits routinely, "a second time" is once per eviction.
	// The masses no longer describe the traffic; they describe how often the tenant
	// was evicted, which is a number about us.
	warmed time.Time
	// advance serialises this tenant's two watermark-driven folds — the source
	// planes into its surface ([plane.roll]) and its surface into its model
	// ([plane.warm]). Each is idempotent under its own watermark, which is only
	// true one at a time: two concurrent rolls both read the mark, both insert and
	// both advance it, and a SummingMergeTree adds the two rows rather than
	// replacing one. It is a SEPARATE lock from mu because a fold runs for minutes
	// and mu is on every score.
	//
	// It is a CHANNEL and not a Mutex because the wait has to be cancellable: a
	// background fold can hold it for the whole warm deadline, and a request that
	// blocked on sync.Mutex for two minutes is a hung request whose caller is long
	// gone. See [resident.hold].
	advance chan struct{}
	touch   time.Time // for eviction order
}

// fold is how much of a tenant's OWN event surface has been folded into its
// model, and why that did not happen when it did not. An empty surface and an
// unreachable warehouse are different facts and a model must not report them as
// one.
type fold struct {
	Folded int
	Window time.Duration
	Gap    string
	// Rolled is how many windows of this tenant's SOURCE planes were folded into
	// its feature surface before the read. Zero with no gap means the surface was
	// already current, which is a different fact from the rollup never running.
	Rolled int
	// Replayed is how many of the tenant's own recorded observations rebuilt its
	// aggregates when it became resident. It is what says a rollout was a rebuild
	// rather than a blindness.
	Replayed int
}

// plane holds every resident model. It holds NO tenant data of its own: every
// counter, tree and ring belongs to exactly one [resident], keyed by tenant.
//
// ONE OWNER OF THE STATE. The model is in-process mutable state: if one process
// learned and another scored, the two would hold different counters and give two
// different answers to one question, with no error and no log. So the learn path
// and the score path are the same binary, and the app that owns them owns every
// /v1/ml leaf that touches them.
//
// THERE IS NO SHARED TENANT STORE HERE, and [TestPlane_HoldsNoSharedTenantState]
// keeps it that way by reflecting over these fields. It used to hold one
// process-wide velocity store with a process-wide cap, which meant the busiest
// organisation evicted the quietest one's keys — the quiet tenant's velocity
// features then read blind, it scored as unremarkable, and nothing said so.
type plane struct {
	mu  sync.Mutex
	res map[tenant]*resident
	// shelf is the per-org encrypted SQLite the snapshots, the recorded
	// observations, the rollup watermarks and the search runs live on. Isolation
	// there is PHYSICAL — a tenant is a file — so a query in one cannot reach
	// another's rows however the statement is written.
	shelf *cloud.OrgStore[*shelf]
	log   luxlog.Logger
	// now is the clock, so a test drives the plane without sleeping.
	now func() time.Time
	// folded records what each resident's warm managed, guarded by mu.
	folded map[tenant]fold
	// evicted counts tenants dropped to keep the resident bound, guarded by mu.
	// Eviction here is LOSSLESS — learned state is written down first and the
	// aggregates rebuild from the tenant's own record — but a bound being hit is a
	// capacity fact an operator must be able to see, so it is counted and reported
	// on the probe rather than being silent.
	evicted int64
	// running is the in-flight search per tenant, guarded by mu. ONE at a time:
	// an exhaustive grid over a tenant's whole history is real compute, and a
	// tenant that can start N of them has a denial of service it can request of
	// itself.
	running map[tenant]report
	// busy counts a tenant's in-flight scores and learns, guarded by mu. Every
	// call for one tenant serialises on that tenant's model lock, so without a
	// bound a tenant's own concurrency turns into an unbounded queue of goroutines
	// holding request state. The bound is PER TENANT for the same reason every
	// other bound here is: at it, that organisation is told to slow down and
	// nobody else notices.
	busy map[tenant]int
	// folds is the fold admission ticket, and it is a CONCURRENCY bound rather
	// than a state bound — the one shape of process-wide bound that is allowed
	// here, because nothing a tenant holds is evicted by another tenant taking a
	// ticket. A fold rolls up to four source planes and reads a window of the
	// warehouse, so N tenants arriving at once is N times that against one
	// warehouse; unbounded it is this app doing to the datastore exactly what the
	// shared ring store used to do to tenants.
	//
	// A tenant that finds every ticket taken is NOT marked folded, so its next
	// request tries again. Deferred and reported, never dropped and silent.
	folds chan struct{}
	// ctx bounds every background fold and search; close cancels it and waits.
	ctx  context.Context
	stop context.CancelFunc
	wg   sync.WaitGroup
}

// maxFolds is how many tenants may be folding at once. Each fold holds at most
// one ticket and finishes within [warmDeadline], and a tenant can only ever have
// one fold in flight, so the wait is bounded by other tenants' folds and not by
// any one tenant's behaviour.
const maxFolds = 8

// newPlane builds the model plane. It FAILS rather than degrades when the
// aggregate store does not keep a window the feature inventory reads — the
// alternative is a model that runs, alerts, and has been reading zero for a
// feature the whole time.
func newPlane(base cloud.Base) (*plane, error) {
	// Construct one store against a throwaway tenant ring set purely to validate
	// the pair. A configuration that cannot be built is a boot failure, not a
	// per-request surprise on some tenant's first event.
	if _, err := anomaly.New(defaultConfig(), newRings()); err != nil {
		return nil, fmt.Errorf("risk: model plane: %w", err)
	}
	ctx, stop := context.WithCancel(context.Background())
	p := &plane{
		res:     map[tenant]*resident{},
		shelf:   cloud.NewOrgStore(base.DataDir, "risk", openShelf, cloud.WithDurable(base.Durable), cloud.WithStoreLogger(base.Log)),
		log:     base.Log,
		now:     time.Now,
		folded:  map[tenant]fold{},
		running: map[tenant]report{},
		busy:    map[tenant]int{},
		folds:   make(chan struct{}, maxFolds),
		ctx:     ctx,
		stop:    stop,
	}
	// The network baseline is the ONLY cross-organisation surface, and it is a
	// scheduled job with no route: it lives here so it starts and stops with the
	// plane, and dies with the process rather than outliving it.
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		schedule(ctx, base.Log)
	}()
	return p, nil
}

// defaultConfig is the deployment's starting posture for every tenant.
//
// Shadow is TRUE. A model that has never been reviewed against a tenant's own
// traffic must not be able to change an outcome, and the way that is guaranteed
// is that turning it live is an explicit act by that tenant recorded on its own
// state.
func defaultConfig() anomaly.Config {
	return anomaly.Config{
		Appetite: anomaly.Appetite{Review: 0.01, Sample: 0.001},
		Shadow:   true,
	}
}

// resident returns the tenant's model, rebuilding its aggregates and restoring
// its learned state from its own shelf on first touch, and evicting the least
// recently used tenant when the budget is full.
//
// RESTORE BEFORE LEARN, always. The geometry is a pure function of the seed the
// snapshot carries, so restoring after a single learn would replace trees the
// counters were accumulated in — and a model whose masses were learned in one
// geometry and scored in another is wrong in a way nothing reports.
//
// The AGGREGATES are rebuilt here too, from the tenant's own recorded
// observations (ring.go) and deterministically. That is what makes a rollout — or
// an eviction — a rebuild rather than a blindness: without it every deploy of a
// single-replica binary returns every tenant's velocity features to reading zero.
func (p *plane) resident(t tenant) (*resident, error) {
	if !t.qualified() {
		return nil, fmt.Errorf("risk: unqualified tenant key %q", string(t))
	}
	p.mu.Lock()
	if r, held := p.res[t]; held {
		r.touch = p.now()
		p.foldSoon(r)
		p.mu.Unlock()
		return r, nil
	}
	p.mu.Unlock()

	vel, edge, replayed, err := p.rings(t)
	if err != nil {
		// Empty rings are HONEST: every velocity feature then reads blind and the
		// model reports it. What must not happen is a tenant being handed anything
		// other than its own aggregates, so the failure degrades this tenant only.
		p.log.Warn("aggregates could not be rebuilt; this tenant's velocity features start blind",
			"tenant", string(t), "err", err)
	}
	r, err := p.plant(t, defaultConfig(), vel)
	if err != nil {
		return nil, err
	}
	r.edge, r.replayed = edge, replayed
	// Learned state, and the appetite the tenant last stated, both come off its
	// own shelf. A failure to read is logged and the tenant starts warming —
	// never silently treated as "nothing learned yet", which is the same state
	// and a very different fact.
	if snap, cfg, warmed, err := p.load(t); err != nil {
		p.log.Warn("model state unavailable; tenant starts warming", "tenant", string(t), "err", err)
	} else {
		if cfg != nil {
			r.cfg.Appetite, r.cfg.Shadow = cfg.Appetite, cfg.Shadow
			mod, err := anomaly.New(r.cfg, r.vel)
			if err != nil {
				// The stated appetite could not be rebuilt. Keep the DEFAULT posture,
				// which is shadow — refusing to honour a policy is survivable, quietly
				// running live because a policy failed to load is not — and say so.
				p.log.Warn("stated appetite could not be restored; the tenant keeps the default shadow posture",
					"tenant", string(t), "err", err)
				r.cfg = defaultConfig()
			} else {
				r.mod = mod
			}
		}
		if snap != nil {
			if err := p.install(r, *snap); err != nil {
				p.log.Warn("model state rejected; tenant starts warming", "tenant", string(t), "err", err)
			} else {
				// The fold watermark comes back WITH the state it describes, and only
				// with it: a rejected snapshot is a model that has read nothing, so it
				// re-reads its history rather than trusting a mark for masses it no
				// longer holds.
				r.warmed = warmed
			}
		}
	}

	p.mu.Lock()
	if held, ok := p.res[t]; ok { // another caller won the race; theirs is the one
		held.touch = p.now()
		p.mu.Unlock()
		return held, nil
	}
	gone := p.evict()
	r.touch = p.now()
	p.res[t] = r
	p.foldSoon(r)
	p.mu.Unlock()

	// The evicted tenant's state is written OUTSIDE the lock. Holding the plane's
	// lock across a disk write would make every other tenant's first request wait
	// on one tenant's eviction, which is the shape of an outage rather than a
	// slowdown.
	if gone != nil {
		if err := p.save(gone); err != nil {
			p.log.Warn("evicted a model without saving it; that tenant rebuilds from its own record",
				"tenant", string(gone.key), "err", err)
		}
	}
	return r, nil
}

// foldSoon starts this tenant's fold if it has not been started and a ticket is
// free. Called with p.mu held, from EVERY path that touches a resident.
//
// THE MOAT, APPLIED WITHOUT BEING ASKED FOR. The moment a tenant's model becomes
// resident it folds in that tenant's OWN event surface, in the background and
// bounded, so a model is warm from first-party history rather than from whatever
// happens to arrive next. It is best-effort by construction — the warehouse is
// not on the scoring path — and what it managed is reported on the model state.
//
// The sentinel is written BEFORE the goroutine starts: a caller polling the state
// while the fold runs is told it is running rather than that it has not begun,
// and a resident can only ever fold once.
//
// A tenant that finds no ticket free writes NO sentinel and is retried on its next
// touch — which is why this is called from the cache-hit path too. The alternative,
// starting a goroutine per residency regardless, is unbounded fan-out at the
// warehouse: a fold rolls up to four source planes and reads a window, so a
// thousand tenants arriving after a rollout is a thousand of those at once.
func (p *plane) foldSoon(r *resident) {
	t := r.key
	if _, attempted := p.folded[t]; attempted {
		return
	}
	select {
	case p.folds <- struct{}{}:
	default:
		return // every ticket is taken; the next touch tries again
	}
	replayed := r.replayed
	p.folded[t] = fold{Window: warmWindow, Replayed: replayed, Gap: "folding this organisation's own surface in"}
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		defer func() { <-p.folds }()
		ctx, cancel := context.WithTimeout(p.ctx, warmDeadline)
		defer cancel()
		f := p.fold(ctx, t)
		f.Replayed = replayed
		p.mu.Lock()
		// Only if this is still the resident the fold ran for. A tenant evicted
		// mid-fold and touched again has a NEW model, and writing this report against
		// it would say "already folded" about a fold that happened to a model that is
		// gone — after which the new one never folds and nothing says so.
		if p.res[t] == r {
			p.folded[t] = f
		}
		p.mu.Unlock()
	}()
}

// fold brings a tenant's feature surface up to date from its own source planes
// and then folds that surface into its model. It is the whole moat in one
// function and in this order, because a warm that reads a surface nobody rolled
// up reads nothing.
func (p *plane) fold(ctx context.Context, t tenant) fold {
	f := fold{Window: warmWindow}
	rolled, err := p.roll(ctx, t)
	f.Rolled = rolled
	if err != nil {
		// A surface that could not be brought current is still worth folding: the
		// windows that DID land are real. The gap is carried either way, because a
		// stale surface and a complete one are different facts.
		f.Gap = err.Error()
	}
	n, err := p.warm(ctx, t)
	f.Folded = n
	if err != nil {
		f.Gap = err.Error()
	}
	return f
}

// warmDeadline bounds one fold. A warehouse that is slow must not hold a model
// warming forever with nothing said about it.
const warmDeadline = 2 * time.Minute

// surface reports what the tenant's fold managed.
func (p *plane) surface(t tenant) fold {
	p.mu.Lock()
	defer p.mu.Unlock()
	f, ok := p.folded[t]
	if !ok {
		// Not started. Either nothing has touched this tenant yet, or every fold
		// ticket was taken when it did and the next touch will try again — both are
		// "not folded", and reporting a zero fold with no gap would be the silence
		// this app exists to refuse.
		return fold{Window: warmWindow, Gap: "the organisation's own surface has not been folded in yet"}
	}
	return f
}

// plant builds a fresh resident with its own random geometry seed. A seed drawn
// per tenant per process is geometry an outsider cannot predict and therefore
// cannot probe for a region to hide activity in; a snapshot carries its own seed,
// so reproducing a past score never needs this one.
func (p *plane) plant(t tenant, cfg anomaly.Config, vel *velocity.Store) (*resident, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return nil, fmt.Errorf("risk: seed: %w", err)
	}
	cfg.Seed = binary.LittleEndian.Uint64(b[:])
	cfg.MaxOrgs = 1 // one store, one tenant: the store IS the tenant boundary here
	mod, err := anomaly.New(cfg, vel)
	if err != nil {
		return nil, fmt.Errorf("risk: model for %q: %w", string(t), err)
	}
	return &resident{key: t, cfg: cfg, mod: mod, vel: vel, advance: make(chan struct{}, 1)}, nil
}

// hold takes this tenant's fold ticket, or gives up when the caller does.
//
// The wait is CANCELLABLE, which a mutex is not: the other holder may be a
// background fold with two minutes of warehouse work in front of it, and a
// request that waits that long has outlived the client that asked. A caller
// refused here has lost nothing — both folds are watermark-driven, so whatever
// the holder is doing is the work this call would have done.
func (r *resident) hold(ctx context.Context) (func(), error) {
	select {
	case r.advance <- struct{}{}:
		return func() { <-r.advance }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// install restores learned state into a resident, REFUSING a snapshot that names
// another tenant.
//
// The engine checks the shape, the version and the mass invariant; it does not
// check WHOSE state this is, because in its own deployment the caller is the
// tenant boundary. Here the caller is a request, so the check belongs here: a
// snapshot naming another organisation is that organisation's learned behaviour,
// and installing it would put one tenant's activity inside another's model.
func (p *plane) install(r *resident, snap anomaly.Snapshot) error {
	if tenant(snap.OrgID) != r.key {
		return zip.ErrForbidden("this snapshot names another organisation's model")
	}
	if err := r.mod.Restore(snap); err != nil {
		return zip.ErrBadRequest(err.Error())
	}
	return nil
}

// evict drops the least recently used resident and hands it back for the caller
// to write down. Called with p.mu held; the WRITE is deliberately not, because a
// disk write under the plane's lock stalls every other tenant.
//
// EVICTION IS LOSSLESS HERE, which is the only reason a process-wide resident
// bound is allowed to exist at all. The learned state is snapshotted to the
// tenant's own shelf before the resident is dropped, and the aggregates are a
// projection of the tenant's own durable record, so a tenant evicted by another
// tenant's arrival pays a rebuild on its next request and loses nothing. It is
// counted too: a bound being hit is a capacity fact, and the version of this
// mechanism that dropped state silently is exactly the defect this app exists to
// avoid.
func (p *plane) evict() *resident {
	if len(p.res) < maxResident {
		return nil
	}
	var oldest tenant
	var at time.Time
	for k, r := range p.res {
		if at.IsZero() || r.touch.Before(at) {
			oldest, at = k, r.touch
		}
	}
	if oldest == "" {
		return nil
	}
	r := p.res[oldest]
	delete(p.res, oldest)
	delete(p.folded, oldest)
	p.evicted++
	p.log.Info("model evicted to hold the resident bound; it rebuilds from its own record on next use",
		"tenant", string(oldest), "resident", maxResident, "evicted", p.evicted)
	return r
}

// maxInFlight bounds how many scores and learns ONE tenant may have in the plane
// at once. Every one of them serialises on that tenant's model lock, so an
// unbounded count is that organisation's own concurrency turned into a queue of
// goroutines each holding a request's state.
const maxInFlight = 32

// enter takes one of a tenant's in-flight slots, or refuses. Pair it with leave.
//
// The refusal is 429 and it names the tenant's own bound, because the honest
// answer to "you have too many requests open" is to say so — a request quietly
// queued behind thirty others is a timeout with extra steps.
func (p *plane) enter(t tenant) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.busy[t] >= maxInFlight {
		return zip.Errorf(429, "this organisation already has %d model calls in flight", maxInFlight)
	}
	p.busy[t]++
	return nil
}

func (p *plane) leave(t tenant) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if n := p.busy[t] - 1; n > 0 {
		p.busy[t] = n
	} else {
		delete(p.busy, t)
	}
}

// ── the three verbs ──────────────────────────────────────────────────────────

// score judges one observation WITHOUT learning from it, moving any counter, or
// touching the aggregate store. It is how a candidate is tried against a
// tenant's own behaviour before anything depends on the answer.
func (p *plane) score(t tenant, o observation) (anomaly.Assessment, error) {
	r, err := p.resident(t)
	if err != nil {
		return anomaly.Assessment{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.mod.Inspect(o.tx(t), types.Entity{OrgID: string(t)}), nil
}

// learn records observations into the tenant's own aggregates and lets its model
// learn from them, returning the verdict score would have given on each
// afterwards.
//
// It takes a BATCH because durability is per batch: the whole batch is written to
// the tenant's own record in one transaction BEFORE anything moves in memory, so
// a learn that cannot be written down is refused rather than answered from state
// the next rollout will silently undo. A batch is also one acquisition of the
// tenant's lock instead of N.
//
// The order within an event is deliberate and it is the engine's: RECORD FIRST,
// then assess. The numbers an alert quotes are then the same ones an investigator
// sees when they look at the subject, and every baseline in the feature set has
// this event removed from it arithmetically, so nothing is measured against
// itself.
func (p *plane) learn(t tenant, obs ...observation) ([]anomaly.Assessment, error) {
	if len(obs) == 0 {
		return nil, nil
	}
	r, err := p.resident(t)
	if err != nil {
		return nil, err
	}
	// DURABLE FIRST. The aggregates are a projection of this record; writing the
	// counters and not the record is how a deploy blinds a tenant.
	if err := p.note(t, obs); err != nil {
		return nil, err
	}
	now := p.now().UTC()
	out := make([]anomaly.Assessment, 0, len(obs))
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, o := range obs {
		tx := o.tx(t)
		// THE RINGS ONLY MOVE FORWARD. An observation the aggregates cannot hold at
		// its own bucket would be folded to the leading edge — counted as having
		// happened NOW — so it is kept out of them. The model still learns from it.
		if placeable(o.At, r.edge, now) {
			record(r.vel, tx)
			if o.At.After(r.edge) {
				r.edge = o.At
			}
		}
		// TWO PASSES, IN THIS ORDER, and the order is the whole point.
		//
		// Assess is the learning pass: it moves the counters the governance report is
		// computed from, and it answers only the engine's rule-hit shape — alert or
		// not — because that is all the decision plane needs. The FULL verdict (the
		// score, the threshold in force, every coordinate, the counterfactual
		// attribution) is what a caller of this surface wants, and Inspect is the only
		// way to get it.
		//
		// Inspect runs FIRST so both passes read the SAME model state: the aggregates
		// already include this event, and the score is computed before the masses
		// move. Run the other way round the reported verdict would describe a model
		// that had already learned from the event it is judging — a different model
		// from the one whose counters the report is built on.
		a := r.mod.Inspect(tx, types.Entity{OrgID: string(t)})
		r.mod.Assess(tx, types.Entity{OrgID: string(t)})
		out = append(out, a)
	}
	return out, nil
}

// state reports the tenant's model: what it has learned, the threshold in force,
// the stated appetite beside the realised one, every refusal by reason, and every
// feature that took its neutral value for want of data.
func (p *plane) state(t tenant) (anomaly.State, error) {
	r, err := p.resident(t)
	if err != nil {
		return anomaly.State{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.mod.State(string(t)), nil
}

// appetite restates the share of the stream the tenant's model may send for
// examination, and whether it is live.
//
// The appetite is a property of the Config, and the Config is fixed at
// construction — so the change is made the only honest way: snapshot the learned
// state, build the model the tenant asked for, restore into it. The digest covers
// the model's SHAPE (the inventory and the geometry parameters) and not the
// appetite, so the restore is exact and nothing is unlearned by a policy change.
func (p *plane) appetite(t tenant, review, sample float64, live bool) (anomaly.State, error) {
	r, err := p.resident(t)
	if err != nil {
		return anomaly.State{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	cfg := r.cfg
	cfg.Appetite.Review, cfg.Appetite.Sample, cfg.Shadow = review, sample, !live
	next, err := anomaly.New(cfg, r.vel)
	if err != nil {
		return anomaly.State{}, fmt.Errorf("risk: appetite: %w", err)
	}
	if snap, held := r.mod.Snapshot(string(t)); held {
		if err := next.Restore(snap); err != nil {
			return anomaly.State{}, fmt.Errorf("risk: appetite: carry learned state: %w", err)
		}
	}
	r.cfg, r.mod = cfg, next
	if err := p.persist(r); err != nil {
		return anomaly.State{}, err
	}
	return r.mod.State(string(t)), nil
}

// pin returns a copy of the tenant's learned state, and writes it to the
// tenant's own shelf in the same act — a pin nobody can read back later is not a
// pin.
func (p *plane) pin(t tenant) (anomaly.Snapshot, bool, error) {
	r, err := p.resident(t)
	if err != nil {
		return anomaly.Snapshot{}, false, err
	}
	r.mu.Lock()
	snap, held := r.mod.Snapshot(string(t))
	cfg, warmed := r.cfg, r.warmed
	r.mu.Unlock()
	if !held {
		return anomaly.Snapshot{}, false, nil
	}
	if err := p.write(t, snap, cfg, warmed); err != nil {
		return anomaly.Snapshot{}, false, err
	}
	return snap, true, nil
}

// adopt installs pinned state into the tenant's model and persists it, refusing a
// snapshot that names another organisation.
func (p *plane) adopt(t tenant, snap anomaly.Snapshot) (anomaly.State, error) {
	r, err := p.resident(t)
	if err != nil {
		return anomaly.State{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := p.install(r, snap); err != nil {
		return anomaly.State{}, err
	}
	if err := p.persist(r); err != nil {
		return anomaly.State{}, err
	}
	return r.mod.State(string(t)), nil
}

// ── warming from the tenant's own surface ────────────────────────────────────

// warm folds the tenant's own feature surface into its aggregates and its model.
// It is the ONLY path by which the warehouse reaches the model, and it is
// explicitly not the scoring path: a decision that needed analytics up would be a
// decision plane that fails when analytics does.
//
// It reports how much it folded, so "the model is warming" can be told apart from
// "the surface is empty" — which is the difference between a control that is
// coming up and a control that will never come up.
func (p *plane) warm(ctx context.Context, t tenant) (int, error) {
	r, err := p.resident(t)
	if err != nil {
		return 0, err
	}
	// ONE AT A TIME for this tenant. The watermark below makes a second fold a
	// no-op, and that is only true if the second one reads the mark after the first
	// has written it. A tenant evicted mid-fold and touched again is exactly how
	// two arrive at once.
	release, err := r.hold(ctx)
	if err != nil {
		return 0, err
	}
	defer release()
	end := p.now().UTC()
	// FROM WHERE THIS MODEL LEFT OFF, never from the window's start again. The
	// masses restored with the snapshot already contain everything folded before
	// r.warmed; reading it a second time teaches the model the same history twice,
	// once per eviction. The window still caps the lookback, so a model that has
	// been away longer than the surface keeps reads what the surface has.
	from := end.Add(-warmWindow)
	r.mu.Lock()
	warmed := r.warmed
	r.mu.Unlock()
	if warmed.After(from) {
		from = warmed
	}
	back := end.Sub(from)
	rs, err := rows(ctx, t, query{start: from, end: end})
	if err != nil {
		return 0, err
	}
	obs := replayable(rs, end, back)
	if len(obs) == 0 {
		// Nothing new to fold is still a fold that happened: the mark moves, so the
		// next one does not re-read this window either.
		r.mu.Lock()
		r.warm, r.warmed = true, end
		r.mu.Unlock()
		return 0, nil
	}
	// The tenant's lock is taken and released PER OBSERVATION, not held across the
	// whole fold: a fold is bounded by rows and by a deadline, but two minutes of
	// held lock is two minutes in which that organisation's own live requests
	// queue. The forward-only rule below is evaluated under the lock each time, so
	// interleaving with live traffic is correct rather than merely tolerated.
	for i, o := range obs {
		if err := ctx.Err(); err != nil {
			return i, err
		}
		tx := o.tx(t)
		r.mu.Lock()
		// Same forward-only rule as the live path. A tenant whose aggregates were
		// already rebuilt from its own record must not have them rewound by a
		// month-old bucket landing at the leading edge; the model learns from the
		// history either way.
		if placeable(o.At, r.edge, end) {
			record(r.vel, tx)
			if o.At.After(r.edge) {
				r.edge = o.At
			}
		}
		r.mod.Assess(tx, types.Entity{OrgID: string(t)})
		r.mu.Unlock()
	}
	r.mu.Lock()
	r.warm, r.warmed = true, end
	r.mu.Unlock()
	return len(obs), nil
}

// replayable reduces a window of the feature surface to the observations a model
// can learn from, oldest first, DROPPING anything outside the window it was read
// for.
//
// The bound is not belt-and-braces. A bucket stamped in the future pins the
// aggregates' leading edge there, after which every real event is older than
// every window and reads as if it never happened — one row, and that subject's
// velocity features are blind for good. The surface is written from an ingest
// door that takes a caller's timestamp, so the value is checked HERE, where it is
// used, and not only where it was written.
//
// One bucket becomes ONE observation carrying the bucket's activity, not N
// synthetic events: the rings aggregate anyway, and inventing N timestamps inside
// a five-minute bucket would manufacture a burst that never happened. Value comes
// from metered spend where the plane carries it and is ZERO where it does not,
// which leaves the value features blind rather than fabricating a currency for an
// activity event.
func replayable(rs []row, now time.Time, back time.Duration) []observation {
	rs = ordered(rs)
	out := make([]observation, 0, len(rs))
	for _, r := range rs {
		if r.Subject == "" || r.Bucket.IsZero() {
			continue
		}
		if within(r.Bucket, now, back) != nil {
			continue
		}
		out = append(out, observation{
			ID:      r.Kind + ":" + r.Subject + "@" + strconv.FormatInt(r.Bucket.Unix(), 10),
			Kind:    r.Kind,
			Subject: r.Subject,
			USD:     r.Value["spend"] / 1e9, // nano-USD on the wire, USD in the model
			At:      r.Bucket,
		})
	}
	return out
}

// ── exhaustive search ────────────────────────────────────────────────────────

// topology is one candidate shape of the detector. The grid below is the search
// space; a run tries every point in it against the tenant's OWN history.
type topology struct {
	Trees  int     `json:"trees"`
	Depth  int     `json:"depth"`
	Window int     `json:"window"`
	Blend  float64 `json:"blend"`
	Review float64 `json:"review"`
}

// trial is what one topology did over the tenant's history.
type trial struct {
	Topology topology `json:"topology"`
	// Learned, Scored and Alerted are the run's own counts.
	Learned int64 `json:"learned"`
	Scored  int64 `json:"scored"`
	Alerted int64 `json:"alerted"`
	// Stated and Realised are the appetite and what it actually produced. Their
	// distance is the whole point of the search.
	Stated   float64 `json:"stated"`
	Realised float64 `json:"realised"`
	// Warm is whether the candidate learned enough to have an opinion at all, and
	// Saturated whether the appetite could not be honoured by any threshold — the
	// one state that must never be mistaken for quiet.
	Warm      bool `json:"warm"`
	Saturated bool `json:"saturated"`
	// Blind counts, per feature, how often it took its neutral value.
	Blind map[string]int64 `json:"blind"`
	// Curve is the realised alert rate over successive tenths of the history —
	// the learning curve, which is what says whether a candidate settled or is
	// still moving.
	Curve []float64 `json:"curve"`
	// Fit ranks the candidate: the smaller the better. See [score].
	Fit float64 `json:"fit"`
}

// maxTrials bounds a run. An exhaustive search over an unbounded grid is a denial
// of service a tenant can request of itself.
const maxTrials = 64

// maxHistory bounds how much of a tenant's surface one run replays.
const maxHistory = 20_000

// grid is the search space, as a closed set of axes. Every value is a package
// constant: a caller chooses HOW MANY points to try, never WHICH, so no request
// can steer a run into a shape nobody has reasoned about.
var grid = struct {
	Trees  []int
	Depth  []int
	Window []int
	Blend  []float64
	Review []float64
}{
	Trees:  []int{15, 25, 40},
	Depth:  []int{6, 8, 10},
	Window: []int{128, 256, 512},
	Blend:  []float64{0.15, 0.25, 0.5},
	Review: []float64{0.005, 0.01, 0.02},
}

// candidates enumerates the grid, bounded. The order is stable so two runs over
// the same history rank the same way.
func candidates() []topology {
	out := make([]topology, 0, maxTrials)
	for _, tr := range grid.Trees {
		for _, d := range grid.Depth {
			for _, w := range grid.Window {
				for _, b := range grid.Blend {
					for _, rv := range grid.Review {
						if len(out) == maxTrials {
							return out
						}
						out = append(out, topology{Trees: tr, Depth: d, Window: w, Blend: b, Review: rv})
					}
				}
			}
		}
	}
	return out
}

// report is one completed search: every trial, and the winner.
type report struct {
	ID      string    `json:"id"`
	Tenant  string    `json:"-"`
	Started time.Time `json:"started"`
	Ended   time.Time `json:"ended"`
	Events  int       `json:"events"`
	Trials  []trial   `json:"trials"`
	Winner  *trial    `json:"winner,omitempty"`
	// Refusal says why a run proves nothing, when it does. An empty history is
	// REFUSED rather than reported as zero alerts, because "no alerts" is exactly
	// what a quiet model looks like and choosing a topology on the strength of an
	// empty replay is the failure a sandbox exists to prevent.
	Refusal string `json:"refusal,omitempty"`
}

// searchDeadline bounds one run end to end.
const searchDeadline = 10 * time.Minute

// begin accepts a search: it reads the tenant's own history NOW (so a caller
// learns immediately whether there is anything to replay) and runs the grid in
// the background, writing the result to the tenant's own shelf.
//
// ONE RUN PER TENANT. A second while one is in flight is a conflict, not a queue:
// the point of the search is to answer one question about one history, and two
// answers racing to the same shelf row is not two answers.
//
// admit is called ONCE, with the measured size of the run, after the history is
// read and before any candidate is tried. That is where the caller's ledger is
// checked, because it is the first moment the cost of the run is a number rather
// than a guess; a refusal there aborts before the first tree is planted.
func (p *plane) begin(ctx context.Context, t tenant, lookback time.Duration, admit func(events int) error) (report, error) {
	if _, err := p.resident(t); err != nil {
		return report{}, err
	}
	p.mu.Lock()
	if held, running := p.running[t]; running {
		p.mu.Unlock()
		return report{}, zip.ErrConflict("a search is already running for this organisation: " + held.ID)
	}
	p.mu.Unlock()

	// A SURFACE READ ROLLS FIRST. The fold that brought this tenant's surface
	// current runs in the background, so a search that only waited for residency
	// would race it and answer "your history is empty" on the very first use —
	// which is exactly the answer a search must never give wrongly. The roll is
	// idempotent under the watermark and aligned to [featureBucket], so a surface
	// already current issues no statement at all.
	if _, err := p.roll(ctx, t); err != nil {
		p.log.Warn("the surface could not be brought current before the search; it replays what has landed",
			"tenant", string(t), "err", err)
	}
	end := p.now().UTC()
	rs, err := rows(ctx, t, query{start: end.Add(-lookback), end: end, limit: maxHistory})
	if err != nil {
		return report{}, err
	}
	hist := replayable(rs, end, lookback)
	if len(hist) == 0 {
		return report{}, zip.ErrNotFound("this organisation's history over the window is empty, so a replay would prove nothing")
	}
	if len(hist) > maxHistory {
		hist = hist[len(hist)-maxHistory:]
	}
	if admit != nil {
		if err := admit(len(hist)); err != nil {
			return report{}, err
		}
	}

	id := runID()
	pending := report{ID: id, Tenant: string(t), Started: end, Events: len(hist)}
	p.mu.Lock()
	p.running[t] = pending
	p.mu.Unlock()

	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		runCtx, cancel := context.WithTimeout(p.ctx, searchDeadline)
		defer cancel()
		// The slot is released whatever happens. A run that panicked and left the
		// slot held would refuse every later search for that organisation with a
		// conflict naming a run that is not running.
		defer func() {
			p.mu.Lock()
			delete(p.running, t)
			p.mu.Unlock()
		}()
		rep := p.search(runCtx, t, id, hist)
		if err := p.keep(t, rep); err != nil {
			p.log.Warn("search finished but could not be saved", "tenant", string(t), "run", id, "err", err)
		}
	}()
	return pending, nil
}

// pending reports an in-flight run for the tenant, so a poll between accepting
// and finishing answers "still going" rather than 404.
func (p *plane) pending(t tenant, id string) (report, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	r, running := p.running[t]
	return r, running && r.ID == id
}

// runID mints an unguessable run identifier. It is scoped to a tenant's own shelf
// besides, so this is bookkeeping and not a capability — but a predictable id in
// a URL is a habit worth not having.
func runID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "srch_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return "srch_" + hex.EncodeToString(b[:])
}

// search replays the tenant's OWN history through every candidate topology and
// ranks them. It is structurally DRY: each candidate gets its own aggregate store
// and its own model, neither of which is the live one, and nothing here writes to
// the feature surface, the live rings or the live model.
func (p *plane) search(ctx context.Context, t tenant, id string, hist []observation) report {
	rep := report{ID: id, Tenant: string(t), Started: p.now().UTC(), Events: len(hist)}
	if len(hist) == 0 {
		rep.Ended = p.now().UTC()
		rep.Refusal = "the tenant's history is empty, so a replay proves nothing"
		return rep
	}
	if len(hist) > maxHistory {
		hist = hist[len(hist)-maxHistory:]
		rep.Events = len(hist)
	}
	for _, c := range candidates() {
		if err := ctx.Err(); err != nil {
			rep.Refusal = "the run was cancelled before every candidate was tried"
			break
		}
		tr, err := replay(t, c, hist)
		if err != nil {
			continue // a shape the engine refuses to build is not a candidate
		}
		rep.Trials = append(rep.Trials, tr)
	}
	sort.SliceStable(rep.Trials, func(i, j int) bool { return rep.Trials[i].Fit < rep.Trials[j].Fit })
	if len(rep.Trials) > 0 {
		w := rep.Trials[0]
		rep.Winner = &w
	}
	rep.Ended = p.now().UTC()
	return rep
}

// replay runs ONE candidate over the history in its own sandbox.
func replay(t tenant, c topology, hist []observation) (trial, error) {
	if len(hist) == 0 {
		return trial{}, errors.New("risk: a candidate cannot be tried over an empty history")
	}
	// A candidate's aggregates are one tenant's, bounded exactly like a live
	// tenant's, and thrown away with the trial. [newRings] is the only constructor
	// in this package, so a sandbox cannot be given a bigger — or a shared — one.
	vel := newRings()
	cfg := anomaly.Config{
		Trees: c.Trees, Depth: c.Depth, Window: c.Window, Blend: c.Blend,
		Appetite: anomaly.Appetite{Review: c.Review, Sample: 0},
		MaxOrgs:  1,
		Seed:     1, // fixed: a comparison between candidates must not also vary the geometry
	}
	mod, err := anomaly.New(cfg, vel)
	if err != nil {
		return trial{}, err
	}
	const bands = 10
	step := len(hist) / bands
	if step == 0 {
		step = len(hist)
	}
	curve := make([]float64, 0, bands)
	var lastScored, lastAlerted int64
	var edge time.Time
	// The sandbox replays HISTORY, so "now" for the forward-only rule is the end of
	// that history and not the wall clock — otherwise a replay of last month would
	// place nothing and every candidate would read blind for the same reason.
	now := hist[len(hist)-1].At
	for i, o := range hist {
		tx := o.tx(t)
		if placeable(o.At, edge, now) {
			record(vel, tx)
			if o.At.After(edge) {
				edge = o.At
			}
		}
		mod.Assess(tx, types.Entity{OrgID: string(t)})
		if (i+1)%step == 0 && len(curve) < bands {
			st := mod.State(string(t))
			if d := st.Scored - lastScored; d > 0 {
				curve = append(curve, float64(st.Alerted-lastAlerted)/float64(d))
			} else {
				curve = append(curve, 0)
			}
			lastScored, lastAlerted = st.Scored, st.Alerted
		}
	}
	st := mod.State(string(t))
	tr := trial{
		Topology: c, Learned: st.Learned, Scored: st.Scored, Alerted: st.Alerted,
		Stated: c.Review, Realised: st.Realised, Warm: st.Warm,
		Saturated: st.Saturated, Blind: st.Blind, Curve: curve,
	}
	tr.Fit = fit(tr)
	return tr, nil
}

// fit ranks a candidate, smaller being better. It is a stated formula rather than
// a tuned one, and every term is a property a reviewer can argue with:
//
//   - the distance between the appetite STATED and the one REALISED, relative to
//     the stated share, because an appetite that is not honoured is not a
//     governed alert level;
//   - a flat penalty for never reaching warm, because a model that cannot form an
//     opinion over the tenant's whole history is not a candidate;
//   - a flat penalty for saturation, because a saturated model alerts on nothing
//     and reads exactly like a quiet one;
//   - the share of the coordinate space that was blind, because a feature that
//     never had data is a feature the topology is not actually using.
func fit(t trial) float64 {
	f := 0.0
	if t.Stated > 0 {
		d := t.Realised - t.Stated
		if d < 0 {
			d = -d
		}
		f += d / t.Stated
	}
	if !t.Warm {
		f += 10
	}
	if t.Saturated {
		f += 10
	}
	if t.Scored > 0 {
		var blind int64
		for _, n := range t.Blind {
			blind += n
		}
		f += float64(blind) / float64(t.Scored*int64(len(t.Blind)+1))
	}
	return f
}

// ── the shelf: snapshots and search runs, on the tenant's own file ───────────

// shelf is one organisation's durable model state. Isolation is PHYSICAL: the
// file IS the tenant. The tenant key is stored in the row as well, so a restore
// that somehow reached the wrong file is still refused rather than applied.
type shelf struct {
	db *sql.DB
}

func openShelf(db *sql.DB) (*shelf, error) {
	s := &shelf{db: db}
	for _, stmt := range []string{
		// warmed is HOW FAR this organisation's own surface has been folded into the
		// model, and it lives in the SNAPSHOT'S OWN ROW on purpose: the two facts are
		// one fact. A state that came back and a fold that already happened must be
		// restored together or not at all — write the watermark somewhere else and a
		// tenant whose snapshot failed to save returns with an empty model that
		// believes it has already read its history.
		`CREATE TABLE IF NOT EXISTS model (
			tenant   TEXT PRIMARY KEY,
			snapshot BLOB NOT NULL,
			config   BLOB NOT NULL,
			warmed   INTEGER NOT NULL,
			at       INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS search (
			id     TEXT PRIMARY KEY,
			tenant TEXT NOT NULL,
			body   BLOB NOT NULL,
			at     INTEGER NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS search_tenant ON search(tenant, at)`,
		// The tenant's own record of what it taught, which its aggregates are a
		// projection of (ring.go), and how far each source plane has been folded into
		// its feature surface (feature.go).
		observationDDL,
		observationIndexDDL,
		rolledDDL,
	} {
		if _, err := db.Exec(stmt); err != nil {
			return nil, fmt.Errorf("risk: shelf: %w", err)
		}
	}
	return s, nil
}

func (s *shelf) Close() error { return s.db.Close() }

// for returns the tenant's shelf. The org half of the key names the file (the
// on-disk layout is the fleet's, and it is keyed on the org slug); the qualified
// key is what every row inside carries.
func (p *plane) for_(t tenant) (*shelf, error) {
	if !t.qualified() {
		return nil, fmt.Errorf("risk: unqualified tenant key %q", string(t))
	}
	// cloud.OrgNamespace is the fleet's ONE door from a validated org to the name
	// of the database its records live in, and the org half of this key came from
	// the validated principal through [qualify]. The brand half is NOT in the
	// namespace: a shelf file is the fleet's per-org file, so two brands' identically
	// named organisations share one — which is exactly why the qualified key is the
	// leading term of every row and every statement inside it
	// ([TestRecord_TwoBrandsShareAFileAndNotARecord]).
	ns, err := cloud.OrgNamespace(t.org(), "")
	if err != nil {
		return nil, fmt.Errorf("risk: %w", err)
	}
	return p.shelf.For(ns)
}

// save writes a resident's learned state, its stated appetite and how far its
// own surface has been folded in, to its own shelf.
func (p *plane) save(r *resident) error {
	r.mu.Lock()
	snap, held := r.mod.Snapshot(string(r.key))
	cfg, warmed := r.cfg, r.warmed
	r.mu.Unlock()
	if !held {
		return nil // nothing learned yet; there is no state to lose
	}
	return p.write(r.key, snap, cfg, warmed)
}

// persist writes a resident whose lock the caller already holds.
func (p *plane) persist(r *resident) error {
	snap, held := r.mod.Snapshot(string(r.key))
	if !held {
		return nil
	}
	return p.write(r.key, snap, r.cfg, r.warmed)
}

func (p *plane) write(t tenant, snap anomaly.Snapshot, cfg anomaly.Config, warmed time.Time) error {
	sh, err := p.for_(t)
	if err != nil {
		return err
	}
	body, err := json.Marshal(snap)
	if err != nil {
		return fmt.Errorf("risk: encode snapshot: %w", err)
	}
	// The seed is withheld from Config's JSON by the engine, so it is carried by
	// the snapshot alone — which is correct: the geometry belongs to the state, not
	// to the policy.
	policy, err := json.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("risk: encode config: %w", err)
	}
	_, err = sh.db.Exec(
		`INSERT INTO model (tenant, snapshot, config, warmed, at) VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(tenant) DO UPDATE SET snapshot = excluded.snapshot, config = excluded.config,
		     warmed = excluded.warmed, at = excluded.at`,
		string(t), body, policy, warmed.UTC().Unix(), time.Now().UTC().Unix())
	if err != nil {
		return fmt.Errorf("risk: save model state: %w", err)
	}
	return nil
}

// load reads a tenant's learned state, its stated appetite and how far its own
// surface has already been folded in. A missing row is not an error: a tenant
// that has never learned has nothing to restore, and it has not been warmed
// either — the two come back together or not at all.
func (p *plane) load(t tenant) (*anomaly.Snapshot, *anomaly.Config, time.Time, error) {
	sh, err := p.for_(t)
	if err != nil {
		return nil, nil, time.Time{}, err
	}
	var body, policy []byte
	var warmed int64
	// The tenant predicate is redundant with the file and kept anyway: it is the
	// leading bound term, so the statement is right even if the path were wrong.
	err = sh.db.QueryRow(`SELECT snapshot, config, warmed FROM model WHERE tenant = ?`, string(t)).
		Scan(&body, &policy, &warmed)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil, nil, time.Time{}, nil
	case err != nil:
		return nil, nil, time.Time{}, fmt.Errorf("risk: load model state: %w", err)
	}
	var snap anomaly.Snapshot
	if err := json.Unmarshal(body, &snap); err != nil {
		return nil, nil, time.Time{}, fmt.Errorf("risk: decode snapshot: %w", err)
	}
	var cfg anomaly.Config
	if err := json.Unmarshal(policy, &cfg); err != nil {
		return nil, nil, time.Time{}, fmt.Errorf("risk: decode config: %w", err)
	}
	return &snap, &cfg, time.Unix(warmed, 0).UTC(), nil
}

// keep writes a completed search run to the tenant's own shelf.
func (p *plane) keep(t tenant, rep report) error {
	sh, err := p.for_(t)
	if err != nil {
		return err
	}
	body, err := json.Marshal(rep)
	if err != nil {
		return fmt.Errorf("risk: encode search: %w", err)
	}
	_, err = sh.db.Exec(
		`INSERT INTO search (id, tenant, body, at) VALUES (?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET body = excluded.body, at = excluded.at`,
		rep.ID, string(t), body, time.Now().UTC().Unix())
	if err != nil {
		return fmt.Errorf("risk: save search: %w", err)
	}
	return nil
}

// run reads back one search run. The tenant is the LEADING bound predicate, so a
// run id another organisation minted is simply not there — the same answer an
// unknown id gives, which is what keeps the read from being a probe oracle.
func (p *plane) run(t tenant, id string) (*report, error) {
	sh, err := p.for_(t)
	if err != nil {
		return nil, err
	}
	var body []byte
	err = sh.db.QueryRow(`SELECT body FROM search WHERE tenant = ? AND id = ?`, string(t), id).Scan(&body)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil, nil
	case err != nil:
		return nil, fmt.Errorf("risk: load search: %w", err)
	}
	var rep report
	if err := json.Unmarshal(body, &rep); err != nil {
		return nil, fmt.Errorf("risk: decode search: %w", err)
	}
	return &rep, nil
}

// ── shutdown ─────────────────────────────────────────────────────────────────

// close snapshots EVERY resident model and shuts the shelves.
//
// This binary is deployed one replica at a time with the old pod stopped before
// the new one starts, so every rollout drops every warming model and the
// threshold it had computed. Without this, a deploy silently returns every tenant
// to warming — and a warming model refuses to score, which reads as "clean" to
// anything that does not check the refusal.
func (p *plane) close() error {
	p.stop()
	p.wg.Wait()
	p.mu.Lock()
	all := make([]*resident, 0, len(p.res))
	for _, r := range p.res {
		all = append(all, r)
	}
	p.res = map[tenant]*resident{}
	p.mu.Unlock()

	var first error
	for _, r := range all {
		if err := p.save(r); err != nil && first == nil {
			first = err
		}
	}
	if err := p.shelf.CloseAll(); err != nil && first == nil {
		first = err
	}
	return first
}

// dataDirWritable reports whether the shelf's root can be written, which is the
// one thing the health probe can check without touching a tenant's file.
func dataDirWritable(dir string) error {
	if dir == "" {
		return errors.New("no data directory configured")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return nil
}
