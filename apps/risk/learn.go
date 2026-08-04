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
// nothing, until its organisation states otherwise. GET /v1/risk/state reports the
// stated appetite beside the realised one, which is what makes the appetite a
// measured commitment rather than an intention.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/luxfi/aml/pkg/anomaly"
	"github.com/luxfi/aml/pkg/types"

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
//
// Its fields are unexported and it has ONE constructor, [observe], because every
// ceiling in this package — the ring budget, the record budget, the process
// ceiling — is a COUNT multiplied by the size of one of these. A second way to
// build one is a second place the size is unbounded.
type observation struct {
	// id identifies the event. It selects the below-the-line review sample by
	// hash, so it must be the caller's own stable id and not a counter.
	id string
	// kind and subject are whose behaviour this is. kind is one of [kinds].
	kind, subject string
	// usd is the value moved, already converted. Zero where the event moves no
	// money, which leaves the value features blind rather than fabricating a
	// currency for an activity event.
	usd float64
	// peer is the counterparty, and device the device fingerprint. Each is an
	// aggregation AXIS: the pair axis is what makes "unfamiliar" a fact about a
	// relationship, and the device axis is what surfaces several nominally
	// unrelated subjects acting as one.
	peer, device string
	// at is when it happened.
	at time.Time
}

// actor is WHOSE behaviour an observation is, on every axis the model aggregates
// over. It is the caller-chosen half of an observation, which is exactly the half
// that has to be bounded.
type actor struct {
	// Kind namespaces Subject, so a person and an account sharing an identifier
	// stay two subjects. It is one of [kinds] and is therefore already bounded by
	// a closed set.
	Kind string
	// Subject is the identifier on that kind.
	Subject string
	// Peer is the counterparty and Device the device fingerprint, each an
	// aggregation axis of its own.
	Peer, Device string
}

// observe is the ONE constructor of an [observation] and the only place
// [maxField] is applied.
//
// There is no other way to build one — [TestObservation_HasOneConstructor] walks
// the package's own syntax tree and fails on a second composite literal — because
// a bound applied at some of the doors is a bound at none of them: the live wire,
// the replay from the tenant's own record and the fold from its own feature
// surface all reach the same rings and the same disk.
//
// It REFUSES rather than truncating. Two subjects differing only past the cut
// would silently become one set of aggregates, which is a wrong answer wearing a
// right one's clothes.
//
// The stamp is truncated to ONE SECOND, the resolution the durable record carries,
// so a rebuild from that record is identical to the live rings rather than merely
// close to them.
func observe(id string, a actor, usd float64, at time.Time) (observation, error) {
	if !known(a.Kind) {
		return observation{}, zip.ErrBadRequest("'kind' must be one of " + strings.Join(kinds, ", "))
	}
	if a.Subject == "" {
		return observation{}, zip.ErrBadRequest("'subject' is required — an event that names nobody has nobody to be unusual for")
	}
	for _, f := range []struct{ name, value string }{
		{"id", id}, {"subject", a.Subject}, {"peer", a.Peer}, {"device", a.Device},
	} {
		if len(f.value) > maxField {
			return observation{}, zip.Errorf(413, "'%s' is %d bytes — at most %d, because every ceiling this plane publishes is a count of these",
				f.name, len(f.value), maxField)
		}
	}
	return observation{
		id: id, kind: a.Kind, subject: a.Subject,
		usd: usd, peer: a.Peer, device: a.Device,
		at: at.UTC().Truncate(time.Second),
	}, nil
}

// bucketID names the one observation a surface bucket becomes. It is a DIGEST and
// not the parts concatenated, for two reasons that are both bounds: the parts
// together can exceed [maxField] while every one of them is legal on its own, and
// a digest is the same length whatever the subject is. Deterministic, so the same
// bucket folded again after an eviction is the same event and lands in the
// below-the-line review sample the same way.
func bucketID(kind, subject string, bucket time.Time) string {
	sum := sha256.Sum256([]byte(kind + "\x00" + subject + "\x00" + strconv.FormatInt(bucket.Unix(), 10)))
	return "fold_" + hex.EncodeToString(sum[:16])
}

// tx renders an observation as the engine's transaction, under the QUALIFIED
// tenant key. The key is the model index, the aggregate key and the snapshot
// identity, so it is set HERE, once, from a value the caller could not supply.
//
// The subject is namespaced by its kind, so a person and an account that happen
// to share an identifier are two subjects and never one aggregate.
func (o observation) tx(t tenant) types.Transaction {
	return types.Transaction{
		ID:                o.id,
		OrgID:             string(t),
		AccountID:         o.kind + ":" + o.subject,
		Counterparty:      o.peer,
		DeviceFingerprint: o.device,
		USD:               o.usd,
		Notional:          o.usd,
		Currency:          "USD",
		Timestamp:         o.at,
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
	mu  sync.Mutex
	key tenant
	cfg anomaly.Config
	mod *anomaly.Store
	vel *rings // THIS tenant's aggregates; see ring.go
	// edge is the newest timestamp the rings carry. The rings only move forward
	// (see [placeable]), so this is what a backdated observation is measured
	// against before it is allowed to claim it happened now.
	edge time.Time
	// replayed is how many of this tenant's own recorded observations rebuilt its
	// rings when it became resident. Held here so the fold can quote it whenever
	// the fold actually starts, which is not always the request that planted it.
	replayed int
	// refused counts buckets of this tenant's own surface a fold had to skip
	// because a subject on them is longer than [maxField].
	refused int
	// unsaved is how many events this model has learned since its state was last
	// written down. It is the SIZE OF WHAT AN UNGRACEFUL STOP WOULD LOSE — an OOM
	// kill, a lost node, a forced delete — and it is why the masses are not written
	// only at shutdown: a shutdown hook runs on the graceful path and on no other,
	// and a model that came back empty refuses to score, which reads as clean to
	// anything that does not check the refusal.
	unsaved int
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
	// shape is the model space this residency's arithmetic runs in: the feature
	// inventory in order and the detector's geometry parameters, as the engine's own
	// Digest. It is held here so EVERY SCORE CAN CITE IT at no cost — it is what an
	// auditor pins an alert to, because a score is only meaningful against the shape
	// that produced it, and recomputing it per score would hash the whole inventory
	// on the hot path to learn something that cannot change.
	//
	// It cannot change for a residency: the digest covers the geometry parameters,
	// and the only writers of those are [plane.plant], which builds the store, and
	// [plane.install], which REFUSES a state whose shape differs. Restating a regime
	// rebuilds the store ([plane.restoreRegime]) but moves only the appetite and the
	// shadow flag, neither of which the digest covers.
	shape string
	// pol is the VERSION of the decision regime this model is deciding under, from
	// the tenant's own policy history (policy.go). It is carried here so every
	// score can cite it without a disk read, and it is set in exactly two places:
	// where a residency is opened and where a regime is enacted. Zero means the
	// organisation has never stated one, which is a fact a score reports rather
	// than a gap it hides.
	pol int
}

// fold is how much of a tenant's OWN event surface has been folded into its
// model, and why that did not happen when it did not. An empty surface and an
// unreachable warehouse are different facts and a model must not report them as
// one.
type fold struct {
	Folded int
	Window time.Duration
	Gap    string
	// again is whether the next touch should try this tenant's fold AGAIN. A fold
	// in flight is not retried and a fold that finished clean is not either; a
	// fold that hit a GAP is, and that is the correction.
	//
	// The sentinel used to be written for every outcome, so a warehouse blip on a
	// tenant's first touch marked it folded for the life of its residency: the moat
	// never applied to that organisation again, with the gap sitting on its state
	// and nothing retrying. Only a fold that SUCCEEDED is a fold that happened.
	again bool
	// Rolled is how many windows of this tenant's SOURCE planes were folded into
	// its feature surface before the read. Zero with no gap means the surface was
	// already current, which is a different fact from the rollup never running.
	Rolled int
	// Replayed is how many of the tenant's own recorded observations rebuilt its
	// aggregates when it became resident. It is what says a rollout was a rebuild
	// rather than a blindness.
	Replayed int
	// Refused is how many buckets of this organisation's own surface the fold could
	// not fold, because a subject on them is longer than [maxField]. Reported
	// because it is history the model does not have: a fold that quietly skipped
	// part of an organisation's past is a model that reads clean for the wrong
	// reason.
	Refused int
}

// plane holds every resident model. It holds NO tenant data of its own: every
// counter, tree and ring belongs to exactly one [resident], keyed by tenant.
//
// ONE OWNER OF THE STATE. The model is in-process mutable state: if one process
// learned and another scored, the two would hold different counters and give two
// different answers to one question, with no error and no log. So the learn path
// and the score path are the same binary, and the app that owns them owns every
// /v1/risk op that touches them.
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
	// built counts residencies CONSTRUCTED, guarded by mu. With `evicted` it is the
	// churn: a rebuild replays that tenant's whole record, so a built count far
	// above the resident count is a plane spending its time rebuilding.
	//
	// It is also what makes the single flight measurable — N concurrent first
	// touches of one tenant must build ONE residency
	// ([TestResident_IsBuiltOnceHoweverManyAskAtOnce]).
	built int64
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
	// opening serialises everything that builds or writes down ONE tenant's
	// residency, guarded by mu. Whoever finds no entry creates one and does the
	// work; everyone else for that tenant waits on its channel and takes the
	// result.
	//
	// TWO DEFECTS, ONE SEAM, because they are the same fact — "this tenant's
	// residency is being changed right now" — and two mechanisms for one fact is
	// how they disagree:
	//
	//   - THE STAMPEDE. Building a residency replays up to [recordRows] of that
	//     tenant's own record and allocates a whole ring set and model. Unserialised,
	//     N concurrent first touches each did the whole thing and N−1 were thrown
	//     away at the end — N × 8 MiB of rings allocated to discard, free, after
	//     every rollout, which is when every tenant touches at once.
	//   - THE STALE READ ON EVICTION. The evicted tenant's state is written down
	//     OUTSIDE p.mu, because a disk write under the plane's lock stalls every
	//     other tenant. Between the drop and the write, a request for that same
	//     tenant would read the shelf as it was BEFORE the save and come back with
	//     an older model, silently.
	//
	// Both are "one tenant, one residency change at a time", so both are this.
	opening map[tenant]*opening
	// ctx bounds every background fold and search; close cancels it and waits.
	ctx  context.Context
	stop context.CancelFunc
	wg   sync.WaitGroup
}

// opening is one tenant's residency change in flight.
type opening struct {
	done chan struct{}
	r    *resident
	err  error
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
	if _, err := anomaly.New(defaultConfig(), newRings().vel); err != nil {
		return nil, fmt.Errorf("risk: model plane: %w", err)
	}
	ctx, stop := context.WithCancel(context.Background())
	p := &plane{
		res:     map[tenant]*resident{},
		shelf:   cloud.NewOrgStore(base, "risk", openShelf),
		log:     base.Log,
		now:     time.Now,
		folded:  map[tenant]fold{},
		running: map[tenant]report{},
		busy:    map[tenant]int{},
		opening: map[tenant]*opening{},
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
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		p.sweep(ctx, saveInterval)
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
	for {
		p.mu.Lock()
		if r, held := p.res[t]; held {
			r.touch = p.now()
			p.foldSoon(r)
			p.mu.Unlock()
			return r, nil
		}
		// SINGLE FLIGHT, PER TENANT. Whoever finds no opening does the work; every
		// other caller for the same tenant waits on it and then re-reads the map,
		// because between that open finishing and this wakeup the tenant may already
		// have been evicted again — the map is the one answer to who is resident.
		if o, held := p.opening[t]; held {
			p.mu.Unlock()
			<-o.done
			if o.err != nil {
				return nil, o.err
			}
			continue
		}
		mine := &opening{done: make(chan struct{})}
		p.opening[t] = mine
		p.mu.Unlock()
		return p.open(t, mine)
	}
}

// open builds one tenant's residency under the slot the caller took for it, and
// releases that slot in the SAME critical section that publishes the resident —
// so a tenant is never both resident and opening, and the evicted tenant's slot
// below can never collide with an open of its own.
func (p *plane) open(t tenant, mine *opening) (r *resident, err error) {
	defer func() {
		mine.r, mine.err = r, err
		if err != nil {
			p.mu.Lock()
			delete(p.opening, t)
			p.mu.Unlock()
		}
		close(mine.done)
	}()
	vel, edge, replayed, err := p.rebuild(t)
	if err != nil {
		// Empty rings are HONEST: every velocity feature then reads blind and the
		// model reports it. What must not happen is a tenant being handed anything
		// other than its own aggregates, so the failure degrades this tenant only.
		p.log.Warn("aggregates could not be rebuilt; this tenant's velocity features start blind",
			"tenant", string(t), "err", err)
	}
	r, err = p.plant(t, defaultConfig(), vel)
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
		p.restoreRegime(r, cfg)
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
	// PLANT BEFORE ANYTHING ELSE CAN. The engine plants a tenant's trees lazily,
	// so a model that has never been touched has no geometry — and no geometry is
	// what makes a caller-supplied seed adoptable. State() is the read that plants
	// it, deterministically from this process's own randomness, and it moves no
	// counter. After this line every restore has something to be checked against.
	r.mod.State(string(t))

	p.mu.Lock()
	p.built++
	gone := p.evict()
	var closing *opening
	if gone != nil {
		// TAKE THE EVICTED TENANT'S OWN SLOT before releasing the lock. Its state is
		// written down below, outside the lock, and until that write lands the shelf
		// still holds the model as it was BEFORE this residency learned anything — so
		// a request for that tenant arriving in between would rebuild from stale
		// state and lose everything since its last save, silently. Holding its slot
		// makes such a request wait for the write instead. It cannot collide with an
		// open of that tenant's own: it was resident, and resident and opening are
		// mutually exclusive.
		closing = &opening{done: make(chan struct{})}
		p.opening[gone.key] = closing
	}
	r.touch = p.now()
	p.res[t] = r
	delete(p.opening, t)
	p.foldSoon(r)
	p.mu.Unlock()

	// The evicted tenant's state is written OUTSIDE the plane's lock. Holding it
	// across a disk write would make every other tenant's first request wait on
	// one tenant's eviction, which is the shape of an outage rather than a
	// slowdown.
	if gone != nil {
		if err := p.save(gone); err != nil {
			p.log.Warn("evicted a model without saving it; that tenant rebuilds from its own record",
				"tenant", string(gone.key), "err", err)
		}
		p.mu.Lock()
		delete(p.opening, gone.key)
		p.mu.Unlock()
		close(closing.done)
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
	if f, attempted := p.folded[t]; attempted && !f.again {
		return
	}
	select {
	case p.folds <- struct{}{}:
	default:
		return // every ticket is taken; the next touch tries again
	}
	replayed := r.replayed
	p.folded[t] = fold{Window: warmWindow, Replayed: replayed, Gap: foldRunning}
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		defer func() { <-p.folds }()
		ctx, cancel := context.WithTimeout(p.ctx, warmDeadline)
		defer cancel()
		f := p.fold(ctx, t)
		f.Replayed = replayed
		// A GAP RE-ARMS. Whatever did not land — an unreachable warehouse, a
		// rollup error, the deadline — is a reason to try again on this tenant's
		// next touch, not a reason to stop. The mark the fold advances makes the
		// retry cheap: it reads only what it has not already applied.
		f.again = f.Gap != ""
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
	f.Refused = p.refused(t)
	if err != nil {
		f.Gap = err.Error()
	}
	// A FOLD IS LEARNING, so it is written down like any other. Without this a
	// fold's masses live only in memory until the next graceful shutdown, and an
	// ungraceful one — an OOM kill, a lost node, a forced delete — throws away
	// however much of that organisation's own history was just read.
	if n > 0 {
		if err := p.saveOf(t); err != nil {
			p.log.Warn("folded surface could not be written down; it will be folded again",
				"tenant", string(t), "err", err)
		}
	}
	return f
}

// saveOf writes down a tenant's model IF it is still resident. It never builds a
// residency: a save is a consequence of work already done, and rebuilding a
// tenant in order to save it would be this plane teaching itself.
func (p *plane) saveOf(t tenant) error {
	p.mu.Lock()
	r := p.res[t]
	p.mu.Unlock()
	if r == nil {
		return nil
	}
	return p.save(r)
}

// warmDeadline bounds one fold. A warehouse that is slow must not hold a model
// warming forever with nothing said about it.
const warmDeadline = 2 * time.Minute

// foldRunning is the gap a fold IN FLIGHT reports. It is a named value because
// two states of this app read it — a caller polling its own model state, and the
// arming rule in [plane.foldSoon] — and a sentinel spelled twice is a sentinel
// that eventually differs in one of the places.
const foldRunning = "folding this organisation's own surface in"

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
func (p *plane) plant(t tenant, cfg anomaly.Config, vel *rings) (*resident, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return nil, fmt.Errorf("risk: seed: %w", err)
	}
	cfg.Seed = binary.LittleEndian.Uint64(b[:])
	cfg.MaxOrgs = 1 // one store, one tenant: the store IS the tenant boundary here
	mod, err := anomaly.New(cfg, vel.vel)
	if err != nil {
		return nil, fmt.Errorf("risk: model for %q: %w", string(t), err)
	}
	return &resident{key: t, cfg: cfg, mod: mod, vel: vel, shape: mod.Digest(), advance: make(chan struct{}, 1)}, nil
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
	// THE GEOMETRY IS OURS, not the caller's. The engine regenerates the trees from
	// the snapshot's SEED, so a caller that chooses the seed chooses WHERE THE
	// REGIONS ARE — precisely the state a snapshot is supposed not to disclose,
	// arriving through the other door. A model that has already been planted has a
	// geometry of its own, minted here from this process's own randomness or
	// restored with this tenant's own state, and a snapshot carrying a different
	// seed did not come from it.
	//
	// The seed is CARRIED rather than dropped because it is what makes the check
	// possible: masses learned in one geometry and scored in another are wrong in a
	// way nothing reports, so the two must be compared, and the only safe answer to
	// a mismatch is to refuse. Every residency plants before it accepts a restore
	// ([plane.open]), so the "no geometry yet" branch below is reachable only on the
	// tenant's own state coming off its own shelf.
	if cur, planted := r.mod.Snapshot(string(r.key)); planted && cur.Seed != snap.Seed {
		return zip.ErrBadRequest("this snapshot was taken under a different geometry than this organisation's model holds, so its counters do not describe these trees")
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
func (p *plane) score(t tenant, o observation) (decided, error) {
	r, err := p.resident(t)
	if err != nil {
		return decided{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	// The verdict, the regime it was reached under and the SHAPE it was reached in
	// are read in the SAME critical section, so a concurrent policy change cannot
	// make a score cite a regime that did not produce its cut and a concurrent
	// adoption cannot make it cite a model space it did not run in.
	return decided{
		A:       r.mod.Inspect(o.tx(t), types.Entity{OrgID: string(t)}),
		Version: r.pol,
		Shape:   r.shape,
	}, nil
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
//
// A RETRY CONVERGES, IN MEMORY TOO. The record deduplicates on the caller's own
// event id and says which rows were new ([plane.note]); only those move the rings
// and the masses. Idempotence of the durable half alone is not idempotence at all
// — the rings and the counters are what a decision is made from, and a retried
// batch that skipped the rows and still moved them counts every event twice in
// exactly the numbers that matter. Duplicates are still JUDGED, because the
// caller asked what its model makes of them and the answer costs the same work.
func (p *plane) learn(t tenant, obs ...observation) ([]decided, error) {
	if len(obs) == 0 {
		return nil, nil
	}
	r, err := p.resident(t)
	if err != nil {
		return nil, err
	}
	// DURABLE FIRST. The aggregates are a projection of this record; writing the
	// counters and not the record is how a deploy blinds a tenant.
	first, err := p.note(t, obs)
	if err != nil {
		return nil, err
	}
	now := p.now().UTC()
	out := make([]decided, 0, len(obs))
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, o := range obs {
		tx := o.tx(t)
		if !first[i] {
			// Already in this tenant's record, so already in the projection of it. Judge
			// it against the model as it stands and move nothing.
			out = append(out, decided{A: r.mod.Inspect(tx, types.Entity{OrgID: string(t)}), Version: r.pol})
			continue
		}
		// THE RINGS ONLY MOVE FORWARD. An observation the aggregates cannot hold at
		// its own bucket would be folded to the leading edge — counted as having
		// happened NOW — so it is kept out of them. The model still learns from it.
		if placeable(o.at, r.edge, now) {
			r.vel.record(tx)
			if o.at.After(r.edge) {
				r.edge = o.at
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
		out = append(out, decided{A: a, Version: r.pol})
	}
	// ONCE PER BATCH, never per event: it locks every shard. This is what turns
	// velocity's silent LRU into a number — see [rings].
	r.vel.reconcile()
	for _, applied := range first {
		if applied {
			r.unsaved++
		}
	}
	due := r.unsaved >= saveEvery
	r.mu.Unlock()
	if due {
		// OUTSIDE the tenant's lock. A snapshot is a disk write and holding the
		// model lock across it would put that organisation's own live traffic behind
		// its own durability.
		if err := p.save(r); err != nil {
			p.log.Warn("learned state could not be written down; an ungraceful stop would lose it",
				"tenant", string(t), "err", err)
		}
	}
	r.mu.Lock()
	return out, nil
}

// saveEvery is how many events one organisation's model may learn before its
// state is written down. It bounds the LOSS of an ungraceful stop in events, and
// [saveInterval] bounds the same loss in time; a model that learned a little and
// then went quiet is covered by the second, one learning in a loop by the first.
const saveEvery = 500

// saveInterval is how often every resident with unwritten learning is written
// down.
const saveInterval = 30 * time.Second

// sweep writes down every resident that has learned since its last save, every
// `every`. ONE goroutine for the whole plane, and it takes no tenant's lock for
// longer than a snapshot.
//
// The schedule is a PARAMETER and not a constant read from inside, because it is
// the one thing about this loop a caller decides — [newPlane] states
// [saveInterval] at the call site, where a reader can see it. It is also what
// makes the loop itself measurable: the alternative, a thirty-second constant
// baked in, is a control whose only test would be a thirty-second wait, which is
// to say no test at all.
func (p *plane) sweep(ctx context.Context, every time.Duration) {
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			p.mu.Lock()
			all := make([]*resident, 0, len(p.res))
			for _, r := range p.res {
				all = append(all, r)
			}
			p.mu.Unlock()
			for _, r := range all {
				r.mu.Lock()
				due := r.unsaved > 0
				r.mu.Unlock()
				if !due {
					continue
				}
				if err := p.save(r); err != nil {
					p.log.Warn("learned state could not be written down; an ungraceful stop would lose it",
						"tenant", string(r.key), "err", err)
				}
			}
		}
	}
}

// state reports the tenant's model: what it has learned, the threshold in force,
// the stated appetite beside the realised one, every refusal by reason, and every
// feature that took its neutral value for want of data.
func (p *plane) state(t tenant) (anomaly.State, strain, error) {
	r, err := p.resident(t)
	if err != nil {
		return anomaly.State{}, strain{}, err
	}
	r.vel.reconcile()
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.mod.State(string(t)), r.vel.strain(), nil
}

// appetite restates the share of the stream the tenant's model may send for
// examination, and whether it is live.
//
// The appetite is a property of the Config, and the Config is fixed at
// construction — so the change is made the only honest way: snapshot the learned
// state, build the model the tenant asked for, restore into it. The digest covers
// the model's SHAPE (the inventory and the geometry parameters) and not the
// appetite, so the restore is exact and nothing is unlearned by a policy change.
// It is DURABLE BEFORE IT IS IN FORCE, and that order is the whole point. The
// model beside this policy may hold no learned mass yet, and the writer of the
// learned state correctly declines to write when there is nothing learned — so
// while the regime lived only on that row, an organisation that went live before
// its model had learned anything was told live=true and had nothing written down.
// This binary deploys Recreate at ONE replica, so the next rollout rebuilt it
// from [defaultConfig] — shadow — and the model decided nothing, silently. The
// regime is now its own versioned record ([plane.enact]) written BEFORE anything
// in memory moves, so a policy that cannot be written down is refused instead.
func (p *plane) appetite(t tenant, review, sample float64, live bool, by string) (anomaly.State, strain, int, error) {
	r, err := p.resident(t)
	if err != nil {
		return anomaly.State{}, strain{}, 0, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	// BUILD FIRST. Constructing the model the tenant asked for is pure and can
	// fail, so it is done before the durable write — a recorded version whose
	// regime could never be built would be a history that does not describe what
	// the plane did.
	want := regime{Review: review, Sample: sample, Live: live}
	cfg := want.applyTo(r.cfg)
	next, err := anomaly.New(cfg, r.vel.vel)
	if err != nil {
		return anomaly.State{}, strain{}, 0, fmt.Errorf("risk: appetite: %w", err)
	}
	if snap, held := r.mod.Snapshot(string(t)); held {
		if err := next.Restore(snap); err != nil {
			return anomaly.State{}, strain{}, 0, fmt.Errorf("risk: appetite: carry learned state: %w", err)
		}
	}
	// THE COMMIT POINT. Past here the regime is recorded and readable back; before
	// here nothing has changed.
	rec, _, err := p.enact(t, want, by, time.Now())
	if err != nil {
		return anomaly.State{}, strain{}, 0, err
	}
	r.cfg, r.mod, r.pol = cfg, next, rec.Version
	// The learned state is written down too when there is any, so the masses and
	// the regime come back together. Its failure is NOT fatal here: the regime is
	// already durable, and refusing a recorded policy change because the snapshot
	// beside it could not be written would undo nothing and report a lie.
	if err := p.persist(r); err != nil {
		p.log.Warn("the regime was recorded but the learned state beside it was not written",
			"tenant", string(t), "version", rec.Version, "err", err)
	}
	// The VERSION IS RETURNED, not re-read. Re-reading it would resolve the
	// residency a second time, outside the lock this change was made under, so two
	// concurrent restatements could each report the other's version — an audit
	// surface disagreeing with the record it describes.
	return r.mod.State(string(t)), r.vel.strain(), rec.Version, nil
}

// adopt puts one of the organisation's OWN PUBLISHED VALUES back in force, by
// name.
//
// It takes an ADDRESS and never masses. That is the whole decomplect in this file:
// the caller used to hand back 466 KiB of its own model's counters, which meant the
// caller was the custodian of that state — it had to hold it, transport it, and be
// trusted not to have shaped it. The engine's own Restore says as much ("the caller
// owes the snapshot integrity — it belongs where the tenant's own data belongs, and
// sealed if it travels"). Addressed, IT NEVER TRAVELS: the masses are read from the
// organisation's own encrypted shelf, so the only thing the caller supplies is
// which of its own values it wants, and the mass invariant it is checked against
// can no longer be a defence against a body somebody composed.
//
// An address this organisation has not published resolves to NOT FOUND, and that
// is the isolation boundary rather than a lookup failure — see address.go's header
// and [TestAddress_AForeignOrgResolvesNothing]. The organisation is stamped onto
// the state from the validated principal on the way in, so the stored row's own
// OrgID is never the thing trusted.
func (p *plane) adopt(t tenant, addr string) (anomaly.State, strain, error) {
	r, err := p.resident(t)
	if err != nil {
		return anomaly.State{}, strain{}, err
	}
	snap, ok, err := p.masses(t, addr)
	if err != nil {
		return anomaly.State{}, strain{}, err
	}
	if !ok {
		return anomaly.State{}, strain{}, zip.ErrNotFound("this organisation has published no model value by that name")
	}
	// FROM THE PRINCIPAL, never from the row. The engine plants into the slot the
	// snapshot names, so the one field that decides whose model this becomes is
	// taken from the validated tenant and not from stored bytes.
	snap.OrgID = string(t)
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := p.install(r, snap); err != nil {
		return anomaly.State{}, strain{}, err
	}
	if err := p.persist(r); err != nil {
		return anomaly.State{}, strain{}, err
	}
	return r.mod.State(string(t)), r.vel.strain(), nil
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
//
// THE MARK ADVANCES AS IT GOES, bucket by completed bucket, in the same lock that
// applies them. A fold reads up to [maxRows] under a two-minute deadline, so
// stopping partway is the ORDINARY case and not the exotic one — and a mark
// written only at the end means every retry re-teaches everything the last
// attempt already applied. The mark moves to the newest bucket applied IN FULL,
// so a retry loses nothing and repeats nothing.
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
	now := p.now().UTC()
	// TO THE SURFACE'S OWN HORIZON, never to the present. [plane.horizon] is the
	// newest instant the rollup is allowed to have written; reading — and, the part
	// that bites, MARKING — past it says "folded through now" about buckets the
	// rollup has not produced yet, and the fold never returns for them.
	end := horizon(now)
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
	if !from.Before(end) {
		// The surface has not advanced a whole bucket since the last fold. Nothing to
		// read, and NO mark: the mark already names this horizon.
		return 0, nil
	}
	rs, err := rows(ctx, t, query{start: from, end: end})
	if err != nil {
		return 0, err
	}
	// Judged against the real clock, not the horizon: "is this stamp believable"
	// is a question about now, while "is it inside the window I read" is a question
	// about the read. Conflating them would refuse every bucket between the horizon
	// and the present as if it were from the future.
	obs, refused := replayable(rs, now, now.Sub(from))
	if refused > 0 {
		r.refuse(refused)
	}
	if len(obs) == 0 {
		// Nothing new to fold is still a fold that happened: the mark moves to the
		// horizon — which is bounded by what the surface can contain — so the next one
		// does not re-read this window either.
		r.mark(end)
		return 0, nil
	}
	// ONE BUCKET AT A TIME, AND NEVER HALF OF ONE.
	//
	// A fold reads up to [maxRows] under a two-minute deadline, so stopping partway
	// is the ORDINARY case and not the exotic one — which makes the watermark's
	// exactness the whole property. The mark's grain is a BUCKET, because
	// [replayable] returns one observation per (subject, bucket) and several
	// subjects share a bucket; a mark that could name a bucket whose subjects were
	// only partly applied would either re-teach those subjects on the retry or lose
	// the rest of them for good.
	//
	// So the interruption point is moved to where the mark is exact: [ctx.Err] is
	// consulted at BUCKET BOUNDARIES only, the bucket is applied whole, and the mark
	// then names it. A partly-applied bucket is not a state this loop can be in, so
	// "resume exactly where it stopped" is arithmetic rather than a hope. The
	// overrun that buys is one bucket of work past the deadline.
	//
	// The tenant's lock is taken and released PER OBSERVATION, not held across the
	// whole fold: two minutes of held lock is two minutes in which that
	// organisation's own live requests queue. The forward-only rule below is
	// evaluated under the lock each time, so interleaving with live traffic is
	// correct rather than merely tolerated.
	applied := 0
	for applied < len(obs) {
		if err := ctx.Err(); err != nil {
			return applied, err
		}
		at := obs[applied].at
		last := applied
		for last < len(obs) && obs[last].at.Equal(at) {
			last++
		}
		for _, o := range obs[applied:last] {
			tx := o.tx(t)
			r.mu.Lock()
			// Same forward-only rule as the live path, against the real clock — the
			// question is whether the stamp is believable, not whether the rollup has
			// caught up to it. A tenant whose aggregates were already rebuilt from its
			// own record must not have them rewound by a month-old bucket landing at
			// the leading edge; the model learns from the history either way.
			if placeable(o.at, r.edge, now) {
				r.vel.record(tx)
				if o.at.After(r.edge) {
					r.edge = o.at
				}
			}
			r.mod.Assess(tx, types.Entity{OrgID: string(t)})
			r.mu.Unlock()
		}
		r.markBucket(at)
		applied = last
	}
	r.vel.reconcile()
	r.mark(end)
	return applied, nil
}

// refuse counts buckets of this tenant's own surface the fold could not read.
func (r *resident) refuse(n int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.refused += n
}

// refused is how many buckets of this tenant's surface the folds have had to
// skip. It never builds a residency: a fact about a tenant that is not here is
// not a fact worth planting a model for.
func (p *plane) refused(t tenant) int {
	p.mu.Lock()
	r := p.res[t]
	p.mu.Unlock()
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.refused
}

// mark records that this tenant's surface has been folded up to `to`.
func (r *resident) mark(to time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if to.After(r.warmed) {
		r.warmed = to
	}
}

// markBucket records that every bucket up to and including `done` was applied.
func (r *resident) markBucket(done time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.warmed = bucketMark(done, r.warmed)
}

// bucketMark is the watermark that EXCLUDES a bucket already applied in full.
//
// The step is ONE SECOND because that is the finest value the transport can
// carry: the surface read is half-open [start, end) on `bucket >= ?`, the bound
// is rendered by [tsLiteral], and tsLiteral's format is second-grained. A finer
// mark — a nanosecond past the bucket, say — is TRUNCATED BACK ONTO THE BUCKET
// IT MEANT TO EXCLUDE the moment it is bound, so every retry re-reads and
// re-teaches the last bucket it had already applied. The mark and the wire have
// to agree about resolution or the mark is decoration.
//
// One second cannot skip a bucket either: surface buckets are whole seconds
// apart (five minutes, by toStartOfFiveMinute), so done+1s is at or before the
// next one and the half-open read still includes it.
func bucketMark(done, held time.Time) time.Time {
	if done.IsZero() {
		return held
	}
	if next := done.Add(time.Second); next.After(held) {
		return next
	}
	return held
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
func replayable(rs []row, now time.Time, back time.Duration) (out []observation, refused int) {
	rs = ordered(rs)
	out = make([]observation, 0, len(rs))
	for _, r := range rs {
		if r.Subject == "" || r.Bucket.IsZero() {
			continue
		}
		if within(r.Bucket, now, back) != nil {
			continue
		}
		// THROUGH THE ONE DOOR, and the refusals are COUNTED. The surface is written
		// from an ingest door that takes a caller's own subject, so [maxField] has to
		// hold here too or the fold is the way around it — but a bucket dropped in
		// silence is a piece of that organisation's own history the model never sees
		// and nobody can find out about. The count travels on the fold report.
		o, err := observe(
			bucketID(r.Kind, r.Subject, r.Bucket),
			actor{Kind: r.Kind, Subject: r.Subject},
			r.Value["spend"]/1e9, // nano-USD on the wire, USD in the model
			r.Bucket)
		if err != nil {
			refused++
			continue
		}
		out = append(out, o)
	}
	return out, refused
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

// charge is the money seam as the plane sees it: GATE n screens, and get back the
// meter for what was actually done. It is ONE function and not a gate-and-a-book
// pair because the two halves are one decision made twice — a bound, then its
// outcome — and a plane that took them as two parameters could be handed a gate
// for one thing and a meter for another. [ops.gate] is the only implementation;
// the plane never reaches the request, the ledger or the price.
type charge func(kind string, n int) (meter func(done int), err error)

// begin accepts a search: it reads the tenant's own history NOW (so a caller
// learns immediately whether there is anything to replay) and runs the grid in
// the background, writing the result to the tenant's own shelf.
//
// ONE RUN PER TENANT. A second while one is in flight is a conflict, not a queue:
// the point of the search is to answer one question about one history, and two
// answers racing to the same shelf row is not two answers.
//
// A SEARCH COSTS TWICE AND IS PRICED TWICE, each half before the half it prices.
// The two are genuinely different work and a single price would be wrong in both
// directions:
//
//	THE SURFACE   rolling up to four source planes into this organisation's own
//	              feature surface and reading the window back. Its size is the
//	              WINDOW, which the caller states, so it is known before anything
//	              runs — the same unit and the same price [ops.features] pays for
//	              the same work.
//	THE GRID      every candidate over every event. Its size is the measured
//	              history, so it is known only after the surface read.
//
// The surface half used to run BEFORE ANY GATE AT ALL: a caller with no balance
// drove the whole warehouse cost of a search, was refused at the very end, and
// paid for none of it — as often as it cared to ask. Pricing the grid on its
// upper bound instead would have closed that and priced out every small tenant,
// because the upper bound is [maxHistory] × the grid whatever the tenant's actual
// history holds. Two bounds, each where its own size is a number.
//
// price is the money seam: it GATES n screens and returns the meter for what was
// actually DONE. Each half gates before its work and meters after it, which is
// why the grid's meter is called when the grid ENDS however it ends — a run
// cancelled by a rollout after four of sixty-four candidates is billed for four.
func (p *plane) begin(ctx context.Context, t tenant, lookback time.Duration, price charge) (report, error) {
	if _, err := p.resident(t); err != nil {
		return report{}, err
	}
	// CLAIM THE SLOT BEFORE THE EXPENSIVE WORK, atomically with the check that
	// grants it. This used to check the slot here and SET it two warehouse
	// operations later, which is check-then-act with the whole cost of a search
	// setup in the window: every one of a tenant's concurrent callers passed the
	// check, and all of them rolled the tenant's source planes and read its entire
	// history before any of them claimed anything. Sixteen callers, sixteen full
	// history reads, sixteen background grids, one shelf row for all of them to
	// race — from a bound whose own comment says ONE RUN PER TENANT.
	id := runID()
	if err := p.claim(t, id); err != nil {
		return report{}, err
	}
	// Released on every path that does not reach the run. A claim that outlived its
	// refusal would 409 that organisation's every later search, naming a run that
	// never started.
	started := false
	defer func() {
		if !started {
			p.unclaim(t)
		}
	}()

	// GATED BEFORE THE WAREHOUSE IS TOUCHED, on the window the caller stated.
	surface, err := price("search", windowScreens(lookback))
	if err != nil {
		return report{}, err
	}

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
	// METERED HERE, BEFORE ANY REFUSAL BELOW. The roll and the read have run by
	// this line however they went, and the meter's contract is what was DONE — the
	// same rule [ops.features] applies when its own read fails after its roll.
	surface(windowScreens(lookback))
	if err != nil {
		return report{}, err
	}
	hist, _ := replayable(rs, end, lookback)
	if len(hist) == 0 {
		return report{}, zip.ErrNotFound("this organisation's history over the window is empty, so a replay would prove nothing")
	}
	if len(hist) > maxHistory {
		hist = hist[len(hist)-maxHistory:]
	}
	grid, err := price("search", len(hist)*len(candidates()))
	if err != nil {
		return report{}, err
	}

	pending := report{ID: id, Tenant: string(t), Started: end, Events: len(hist)}
	p.settle(t, pending)
	started = true

	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		runCtx, cancel := context.WithTimeout(p.ctx, searchDeadline)
		defer cancel()
		// The slot is released whatever happens. A run that panicked and left the
		// slot held would refuse every later search for that organisation with a
		// conflict naming a run that is not running.
		defer p.unclaim(t)
		rep := p.search(runCtx, t, id, hist)
		grid(len(rep.Trials) * rep.Events)
		if err := p.keep(t, rep); err != nil {
			p.log.Warn("search finished but could not be saved", "tenant", string(t), "run", id, "err", err)
		}
	}()
	return pending, nil
}

// claim takes this tenant's one search slot, or refuses with the run that holds
// it. It is the CHECK AND THE ACT IN ONE ACQUISITION of p.mu, which is the whole
// point: a search's setup rolls the tenant's source planes and reads its entire
// history, so a slot granted by a check that acts later grants that work to every
// concurrent caller at once.
//
// The slot is per TENANT, so a search running for one organisation refuses only
// that organisation's next one — at it, that tenant is told to wait and nobody
// else notices.
func (p *plane) claim(t tenant, id string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if held, running := p.running[t]; running {
		return zip.ErrConflict("a search is already running for this organisation: " + held.ID)
	}
	// Held with the id alone until the history is measured; [plane.settle] replaces
	// it with the real report. A poll in that window sees a run that is genuinely
	// in flight, which is what it is.
	p.running[t] = report{ID: id, Tenant: string(t), Started: p.now().UTC()}
	return nil
}

// settle publishes the measured report onto the slot this tenant already holds.
func (p *plane) settle(t tenant, rep report) {
	p.mu.Lock()
	p.running[t] = rep
	p.mu.Unlock()
}

// unclaim releases this tenant's search slot.
func (p *plane) unclaim(t tenant) {
	p.mu.Lock()
	delete(p.running, t)
	p.mu.Unlock()
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
	mod, err := anomaly.New(cfg, vel.vel)
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
	now := hist[len(hist)-1].at
	for i, o := range hist {
		tx := o.tx(t)
		if placeable(o.at, edge, now) {
			vel.record(tx)
			if o.at.After(edge) {
				edge = o.at
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
		// THE RESUME CELL, and it is a PLACE on purpose. One row per organisation,
		// overwritten by the sweep and by shutdown, holding what an ungraceful stop
		// would otherwise lose. Nobody names it, nobody cites it and nothing audits
		// it — the values an organisation deliberately publishes are addressed by
		// their own content in `published` (address.go), and that is what a rollback
		// names. Keeping the two apart is what makes the 30-second write cheap and
		// the audited history bounded; see address.go's header for the measurement
		// that forces it.
		//
		// warmed is HOW FAR this organisation's own surface has been folded into the
		// model, and it lives in THIS ROW on purpose: the two facts are one fact. A
		// state that came back and a fold that already happened must be restored
		// together or not at all — write the watermark somewhere else and a tenant
		// whose state failed to save returns with an empty model that believes it has
		// already read its history.
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
		// The tenant's own POLICY history: every distinct decision regime it has
		// adopted, versioned, immutable and durable on its own terms — never
		// conditional on whether the model beside it has learned anything yet
		// (policy.go).
		policyDDL,
		// The tenant's own MODEL VALUE history: every state it deliberately
		// published, named by its own content, immutable and append-only
		// (address.go). It is the sibling of the `model` row above and the opposite
		// of it — that row is ONE overwritten cell whose job is resuming a killed
		// process, this table is what a rollback names and what an adverse decision
		// is reconstructed against.
		publishedDDL,
		publishedIndexDDL,
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
	held = held && snap.Learned > 0
	cfg, warmed := r.cfg, r.warmed
	// Cleared against the state ABOUT to be written, and restored if the write
	// fails: the counter says "learning this snapshot does not contain", so
	// clearing it for a write that never landed would leave the loss unmeasured
	// and the next sweep uninterested.
	cleared := r.unsaved
	r.unsaved = 0
	r.mu.Unlock()
	if !held {
		return nil // nothing learned yet; there is no state to lose
	}
	if err := p.write(r.key, snap, cfg, warmed); err != nil {
		r.mu.Lock()
		r.unsaved += cleared
		r.mu.Unlock()
		return err
	}
	return nil
}

// persist writes a resident whose lock the caller already holds.
func (p *plane) persist(r *resident) error {
	snap, held := r.mod.Snapshot(string(r.key))
	if !held || snap.Learned == 0 {
		return nil
	}
	if err := p.write(r.key, snap, r.cfg, r.warmed); err != nil {
		return err
	}
	r.unsaved = 0
	return nil
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

// drainBudget is the MOST of a shutdown window spent letting background work
// finish. What is left is for the saves, and that split is the whole ordering
// argument: a fold or a search that does not land re-runs from its own watermark
// on the next boot, and a model that was not written down is gone.
const drainBudget = 5 * time.Second

// ErrDrainIncomplete says background work was still running when the shutdown
// window ran out. It is JOINED into close's error rather than replacing it: every
// resident model was still written down, and a fold or search cut short is a
// separate, NAMED fact an operator can act on instead of a silence.
var ErrDrainIncomplete = errors.New("risk: background work did not finish inside the shutdown window")

// close snapshots EVERY resident model and shuts the shelves, inside the window
// the caller gives it.
//
// This binary is deployed one replica at a time with the old pod stopped before
// the new one starts, so every rollout drops every warming model and the
// threshold it had computed. Without this, a deploy silently returns every tenant
// to warming — and a warming model refuses to score, which reads as "clean" to
// anything that does not check the refusal.
//
// THE SAVES RUN UNCONDITIONALLY, AND NEVER BEHIND THE DRAIN. This used to be
// `p.stop(); p.wg.Wait()` with the saves after it, which made the durable half of
// a rollout depend on background work finishing — and it does not have to. The
// process gets a 30-second window (serve.go) inside a 60-second grace period,
// while ONE search finishing after cancellation writes its result to a shelf
// whose durable Sync is bounded at durableOpTimeout — thirty seconds, the whole
// window, on its own. So the wait outlived the window, the process was killed,
// and not one tenant's model had been written down. Every tenant, once per
// deploy, from one tenant's search.
func (p *plane) close(ctx context.Context) error {
	p.stop()
	drained := p.drain(ctx)

	p.mu.Lock()
	all := make([]*resident, 0, len(p.res))
	for _, r := range p.res {
		all = append(all, r)
	}
	p.res = map[tenant]*resident{}
	p.mu.Unlock()

	var errs []error
	for _, r := range all {
		if err := p.save(r); err != nil {
			errs = append(errs, err)
		}
	}
	if !drained {
		// Named, and it names no tenant: how many models were written down anyway is
		// the number that says this was a degradation and not a loss.
		p.log.Warn("background work did not finish inside the shutdown window; every resident model was written down regardless",
			"saved", len(all), "budget", drainBudget)
		errs = append(errs, ErrDrainIncomplete)
	}
	if err := p.shelf.CloseAll(); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// drain waits for the plane's background work, bounded by BOTH the caller's
// shutdown window and [drainBudget], and reports whether it actually finished.
//
// The bound is the caller's because the caller is the only one that knows it: the
// composition root already builds a shutdown context with the window in it and
// hands it to every teardown hook. This used to throw that context away and wait
// forever, which is how a bound that was already present became a hang.
//
// The waiter goroutine can outlive this call. That is deliberate and it is
// bounded at one: the alternative is threading cancellation into every background
// task purely so shutdown can observe it, and the process is on its way out.
func (p *plane) drain(ctx context.Context) bool {
	done := make(chan struct{})
	go func() {
		p.wg.Wait()
		close(done)
	}()
	timer := time.NewTimer(drainBudget)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-timer.C:
		return false
	case <-ctx.Done():
		return false
	}
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
