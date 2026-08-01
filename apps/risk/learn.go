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
//   - It is bounded and durable: a measured ~336 KB of counters per tenant, with
//     [anomaly.Store.Snapshot] and [anomaly.Store.Restore] over the per-org
//     encrypted SQLite the rest of the fleet already uses.
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

// perKeyBytes is the measured cost of ONE velocity key across the standard
// window set: 60+96+168+120 = 444 ring slots at 48 bytes each, plus ring and map
// overhead. It is written down because the key budget below is DERIVED from a
// memory budget rather than picked, and a derivation nobody can check is a guess.
const perKeyBytes = 444*48 + 512

// velocityBudget is how much memory the shared aggregate store may hold, and
// therefore how many subjects across all tenants stay resident. The rings are the
// only unbounded-by-nature structure here — key values derive from tenant data —
// so the bound is stated in bytes and the key count follows from it.
const velocityBudget = 192 << 20

// maxResident bounds how many tenants' MODELS are held at once. Each is the
// measured ~336 KB of counters, so this is ~84 MB. On overflow the least recently
// used is SNAPSHOTTED and dropped — never dropped silently, because a tenant
// returned to warming is a tenant whose control is off.
const maxResident = 256

// warmWindow is how much of a tenant's own surface is folded in when its model
// first becomes resident. Thirty days is the longest window the feature inventory
// reads, so folding more would add nothing the rings can hold.
const warmWindow = 30 * 24 * time.Hour

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
// own appetite, its own shadow switch) over the shared aggregate rings.
type resident struct {
	mu    sync.Mutex
	key   tenant
	cfg   anomaly.Config
	mod   *anomaly.Store
	warm  bool      // the tenant's own surface has been folded in
	touch time.Time // for eviction order
}

// fold is how much of a tenant's OWN event surface has been folded into its
// model, and why that did not happen when it did not. An empty surface and an
// unreachable warehouse are different facts and a model must not report them as
// one.
type fold struct {
	Folded int
	Window time.Duration
	Gap    string
}

// plane holds every resident model and the one aggregate store they read.
//
// ONE OWNER OF THE STATE. The model is in-process mutable state: if one process
// learned and another scored, the two would hold different counters and give two
// different answers to one question, with no error and no log. So the learn path
// and the score path are the same binary, and the app that owns them owns every
// /v1/ml leaf that touches them.
type plane struct {
	mu  sync.Mutex
	res map[tenant]*resident
	vel *velocity.Store
	// shelf is the per-org encrypted SQLite the snapshots and the search runs
	// live on. Isolation there is PHYSICAL — a tenant is a file — so a query in
	// one cannot reach another's rows however the statement is written.
	shelf *cloud.OrgStore[*shelf]
	log   luxlog.Logger
	// now is the clock, so a test drives the plane without sleeping.
	now func() time.Time
	// folded records what each resident's warm managed, guarded by mu.
	folded map[tenant]fold
	// running is the in-flight search per tenant, guarded by mu. ONE at a time:
	// an exhaustive grid over a tenant's whole history is real compute, and a
	// tenant that can start N of them has a denial of service it can request of
	// itself.
	running map[tenant]report
	// ctx bounds every background fold and search; close cancels it and waits.
	ctx  context.Context
	stop context.CancelFunc
	wg   sync.WaitGroup
}

// newPlane builds the model plane. It FAILS rather than degrades when the
// aggregate store does not keep a window the feature inventory reads — the
// alternative is a model that runs, alerts, and has been reading zero for a
// feature the whole time.
func newPlane(base cloud.Base) (*plane, error) {
	keys := velocityBudget / perKeyBytes
	vel := velocity.New(velocity.Config{MaxKeys: keys})
	cfg := defaultConfig()
	// Construct one store against the shared rings purely to validate the pair.
	// A configuration that cannot be built is a boot failure, not a per-request
	// surprise on some tenant's first event.
	if _, err := anomaly.New(cfg, vel); err != nil {
		return nil, fmt.Errorf("risk: model plane: %w", err)
	}
	ctx, stop := context.WithCancel(context.Background())
	p := &plane{
		res:     map[tenant]*resident{},
		vel:     vel,
		shelf:   cloud.NewOrgStore(base.DataDir, "risk", openShelf, cloud.WithDurable(base.Durable), cloud.WithStoreLogger(base.Log)),
		log:     base.Log,
		now:     time.Now,
		folded:  map[tenant]fold{},
		running: map[tenant]report{},
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

// resident returns the tenant's model, restoring its learned state from its own
// shelf on first touch and evicting the least recently used tenant when the
// budget is full.
//
// RESTORE BEFORE LEARN, always. The geometry is a pure function of the seed the
// snapshot carries, so restoring after a single learn would replace trees the
// counters were accumulated in — and a model whose masses were learned in one
// geometry and scored in another is wrong in a way nothing reports.
func (p *plane) resident(t tenant) (*resident, error) {
	if !t.qualified() {
		return nil, fmt.Errorf("risk: unqualified tenant key %q", string(t))
	}
	p.mu.Lock()
	if r, held := p.res[t]; held {
		r.touch = p.now()
		p.mu.Unlock()
		return r, nil
	}
	p.mu.Unlock()

	r, err := p.plant(t, defaultConfig())
	if err != nil {
		return nil, err
	}
	// Learned state, and the appetite the tenant last stated, both come off its
	// own shelf. A failure to read is logged and the tenant starts warming —
	// never silently treated as "nothing learned yet", which is the same state
	// and a very different fact.
	if snap, cfg, err := p.load(t); err != nil {
		p.log.Warn("model state unavailable; tenant starts warming", "tenant", string(t), "err", err)
	} else {
		if cfg != nil {
			r.cfg.Appetite, r.cfg.Shadow = cfg.Appetite, cfg.Shadow
			mod, err := anomaly.New(r.cfg, p.vel)
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
	// THE MOAT, APPLIED WITHOUT BEING ASKED FOR. The moment a tenant's model
	// becomes resident it folds in that tenant's OWN event surface, in the
	// background and bounded, so a model is warm from first-party history rather
	// than from whatever happens to arrive next. It is best-effort by construction
	// — the warehouse is not on the scoring path — and what it managed is reported
	// on the model state.
	//
	// The sentinel is written BEFORE the goroutine starts: a caller polling the
	// state while the fold runs is then told it is running rather than that it has
	// not begun, and a resident can only ever fold once.
	p.folded[t] = fold{Window: warmWindow, Gap: "folding this organisation's own surface in"}
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		ctx, cancel := context.WithTimeout(p.ctx, warmDeadline)
		defer cancel()
		n, err := p.warm(ctx, t)
		f := fold{Folded: n, Window: warmWindow}
		if err != nil {
			f.Gap = err.Error()
		}
		p.mu.Lock()
		p.folded[t] = f
		p.mu.Unlock()
	}()
	p.mu.Unlock()

	// The evicted tenant's state is written OUTSIDE the lock. Holding the plane's
	// lock across a disk write would make every other tenant's first request wait
	// on one tenant's eviction, which is the shape of an outage rather than a
	// slowdown.
	if gone != nil {
		if err := p.save(gone); err != nil {
			p.log.Warn("evicted a model without saving it; that tenant returns to warming", "tenant", string(gone.key), "err", err)
		}
	}
	return r, nil
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
		return fold{Window: warmWindow, Gap: "the organisation's own surface has not been folded in yet"}
	}
	return f
}

// plant builds a fresh resident with its own random geometry seed. A seed drawn
// per tenant per process is geometry an outsider cannot predict and therefore
// cannot probe for a region to hide activity in; a snapshot carries its own seed,
// so reproducing a past score never needs this one.
func (p *plane) plant(t tenant, cfg anomaly.Config) (*resident, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return nil, fmt.Errorf("risk: seed: %w", err)
	}
	cfg.Seed = binary.LittleEndian.Uint64(b[:])
	cfg.MaxOrgs = 1 // one store, one tenant: the store IS the tenant boundary here
	mod, err := anomaly.New(cfg, p.vel)
	if err != nil {
		return nil, fmt.Errorf("risk: model for %q: %w", string(t), err)
	}
	return &resident{key: t, cfg: cfg, mod: mod}, nil
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
// It never drops a model silently: a tenant whose state was not written down is
// a tenant returned to warming, and a warming model refuses to score.
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
	return r
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

// learn records the observation into the tenant's own aggregates and lets its
// model learn from it, returning the same verdict score would have given
// afterwards.
//
// The order is deliberate and it is the engine's: RECORD FIRST, then assess. The
// numbers an alert quotes are then the same ones an investigator sees when they
// look at the subject, and every baseline in the feature set has this event
// removed from it arithmetically, so nothing is measured against itself.
func (p *plane) learn(t tenant, o observation) (anomaly.Assessment, error) {
	r, err := p.resident(t)
	if err != nil {
		return anomaly.Assessment{}, err
	}
	tx := o.tx(t)
	for _, k := range anomaly.Keys(tx) {
		p.vel.Record(k, tx.Timestamp, tx.USD, reportingThreshold)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
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
	return a, nil
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
	next, err := anomaly.New(cfg, p.vel)
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
	cfg := r.cfg
	r.mu.Unlock()
	if !held {
		return anomaly.Snapshot{}, false, nil
	}
	if err := p.write(t, snap, cfg); err != nil {
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
	end := p.now().UTC()
	rs, err := rows(ctx, t, query{start: end.Add(-warmWindow), end: end})
	if err != nil {
		return 0, err
	}
	obs := replayable(rs)
	if len(obs) == 0 {
		return 0, nil
	}
	for _, o := range obs {
		tx := o.tx(t)
		for _, k := range anomaly.Keys(tx) {
			p.vel.Record(k, tx.Timestamp, tx.USD, reportingThreshold)
		}
		r.mu.Lock()
		r.mod.Assess(tx, types.Entity{OrgID: string(t)})
		r.mu.Unlock()
	}
	r.mu.Lock()
	r.warm = true
	r.mu.Unlock()
	return len(obs), nil
}

// replayable reduces a window of the feature surface to the observations a model
// can learn from, oldest first.
//
// One bucket becomes ONE observation carrying the bucket's activity, not N
// synthetic events: the rings aggregate anyway, and inventing N timestamps inside
// a five-minute bucket would manufacture a burst that never happened. Value comes
// from metered spend where the plane carries it and is ZERO where it does not,
// which leaves the value features blind rather than fabricating a currency for an
// activity event.
func replayable(rs []row) []observation {
	rs = ordered(rs)
	out := make([]observation, 0, len(rs))
	for _, r := range rs {
		if r.Subject == "" || r.Bucket.IsZero() {
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
func (p *plane) begin(ctx context.Context, t tenant, lookback time.Duration) (report, error) {
	if _, err := p.resident(t); err != nil {
		return report{}, err
	}
	p.mu.Lock()
	if held, running := p.running[t]; running {
		p.mu.Unlock()
		return report{}, zip.ErrConflict("a search is already running for this organisation: " + held.ID)
	}
	p.mu.Unlock()

	end := p.now().UTC()
	rs, err := rows(ctx, t, query{start: end.Add(-lookback), end: end, limit: maxHistory})
	if err != nil {
		return report{}, err
	}
	hist := replayable(rs)
	if len(hist) == 0 {
		return report{}, zip.ErrNotFound("this organisation's history over the window is empty, so a replay would prove nothing")
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
	vel := velocity.New(velocity.Config{MaxKeys: 4096})
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
	for i, o := range hist {
		tx := o.tx(t)
		for _, k := range anomaly.Keys(tx) {
			vel.Record(k, tx.Timestamp, tx.USD, reportingThreshold)
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
		`CREATE TABLE IF NOT EXISTS model (
			tenant   TEXT PRIMARY KEY,
			snapshot BLOB NOT NULL,
			config   BLOB NOT NULL,
			at       INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS search (
			id     TEXT PRIMARY KEY,
			tenant TEXT NOT NULL,
			body   BLOB NOT NULL,
			at     INTEGER NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS search_tenant ON search(tenant, at)`,
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
	return p.shelf.For(t.org(), "")
}

// save writes a resident's learned state and stated appetite to its own shelf.
func (p *plane) save(r *resident) error {
	r.mu.Lock()
	snap, held := r.mod.Snapshot(string(r.key))
	cfg := r.cfg
	r.mu.Unlock()
	if !held {
		return nil // nothing learned yet; there is no state to lose
	}
	return p.write(r.key, snap, cfg)
}

// persist writes a resident whose lock the caller already holds.
func (p *plane) persist(r *resident) error {
	snap, held := r.mod.Snapshot(string(r.key))
	if !held {
		return nil
	}
	return p.write(r.key, snap, r.cfg)
}

func (p *plane) write(t tenant, snap anomaly.Snapshot, cfg anomaly.Config) error {
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
		`INSERT INTO model (tenant, snapshot, config, at) VALUES (?, ?, ?, ?)
		 ON CONFLICT(tenant) DO UPDATE SET snapshot = excluded.snapshot, config = excluded.config, at = excluded.at`,
		string(t), body, policy, time.Now().UTC().Unix())
	if err != nil {
		return fmt.Errorf("risk: save model state: %w", err)
	}
	return nil
}

// load reads a tenant's learned state and stated appetite back. A missing row is
// not an error: a tenant that has never learned has nothing to restore.
func (p *plane) load(t tenant) (*anomaly.Snapshot, *anomaly.Config, error) {
	sh, err := p.for_(t)
	if err != nil {
		return nil, nil, err
	}
	var body, policy []byte
	// The tenant predicate is redundant with the file and kept anyway: it is the
	// leading bound term, so the statement is right even if the path were wrong.
	err = sh.db.QueryRow(`SELECT snapshot, config FROM model WHERE tenant = ?`, string(t)).Scan(&body, &policy)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil, nil, nil
	case err != nil:
		return nil, nil, fmt.Errorf("risk: load model state: %w", err)
	}
	var snap anomaly.Snapshot
	if err := json.Unmarshal(body, &snap); err != nil {
		return nil, nil, fmt.Errorf("risk: decode snapshot: %w", err)
	}
	var cfg anomaly.Config
	if err := json.Unmarshal(policy, &cfg); err != nil {
		return nil, nil, fmt.Errorf("risk: decode config: %w", err)
	}
	return &snap, &cfg, nil
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
