package risk

// bound.go is where every number that costs memory is decided, ONCE, and where
// the only constructor for a tenant's aggregates lives.
//
// TWO DEFECT CLASSES THIS FILE EXISTS TO MAKE UNREPRESENTABLE.
//
// CLASS A — A BOUND ON A COUNT OF CALLER-SIZED VALUES IS NOT A BOUND. "100,000
// keys", "20,000 list entries", "1,024 cached answers": every one of those is a
// count over a value the CALLER sizes, so the byte figure an operator reads is
// whatever the caller decides to make it. A 64 KiB subject id put one tenant at
// 100 MiB against a published 8 MiB. The shape that cannot express it: ONE cap
// on the LENGTH of any caller-supplied text (textMax), enforced at the wire door
// (door.go) before the value reaches anything that keeps it — so count x textMax
// IS the byte bound, and every count below is DERIVED from a byte budget rather
// than chosen.
//
// CLASS B — ONE STORE SHARED BY EVERY TENANT WITH A GLOBAL CAP IS A CROSS-TENANT
// EVICTOR. velocity.Store evicts the least-recently-updated key in a SHARD, and
// shards are keyed by a hash that mixes tenants together, so one org filling the
// store deletes ANOTHER org's counters — the victim's rules then read zero,
// decline nothing, and report success. The shape that cannot express it: state
// is PER TENANT with a PER-TENANT bound. aggregates() and forest() are the ONLY
// constructors in this package, they are unexported, and they always apply the
// bound (pinned by TestOnlyOneConstructorIsBounded).
//
// AND NOTHING DEGRADES QUIETLY. The engine's own eviction is invisible — no
// counter, no callback, nothing a caller can read — so this app does not use it.
// The store is built with a cardinality this app's own gate can never reach, and
// the GATE is what refuses: exactly at the bound, counted, and published as
// `strained` on the decision and on the probe. See rings.record.
//
// THE WORST CASE IS ARITHMETIC, NOT A HOPE:
//
//	per key      bucketBytes * buckets + keyOverhead + 3*textMax  = 6,560 B
//	per tenant   velBytes  (aggregates)                           = 8 MiB → 1,278 keys
//	             + cellBytes (forest + file + agency memo)        = 784 KiB
//	             + the governance cache at its byte budgets       ≤ 3.25 MiB
//	                                                              ≤ 12 MiB
//	node         memBytes, and a tenant is priced at what it HOLDS, not at its
//	             ceiling: an ordinary tenant costs cellBytes, so 512 MiB serves
//	             ~660 of them, or 42 simultaneously at their full ceiling.
//
// THE NODE BOUND IS BYTES, AND THAT IS THE WHOLE OPERATING POINT. An earlier cut
// bounded the COUNT of resident tenants at 48 and priced every one of them at its
// worst case, so the 49th concurrently-active org was refused the entire risk
// surface — class A again, at the process layer. Bytes is the dimension that
// matters, a tenant is charged for the counters it actually holds, and the
// refusal is the LAST answer rather than the first: a node with no room reclaims
// tenants that have been silent past the retire floor before it turns anyone
// away, and both the reclaim and the refusal are counted where an operator reads
// them.

import (
	"fmt"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/luxfi/aml/pkg/anomaly"
	"github.com/luxfi/aml/pkg/velocity"
)

// ── the one cap on caller-supplied text ─────────────────────────────────────

// textMax is the longest any single text value this app is handed may be: a
// subject id, a signal value, an agent reference, a list entry, a rule term, a
// path segment, a query value. ONE number for all of them, enforced once at the
// wire door, because a rule per field is a rule the next field will not have.
//
// 256 bytes is chosen from what an identifier has to be able to say: an RFC 5321
// address is at most 254 octets, a UUID is 36, a ULID 26, an IPv6 literal 45, and
// every payment-processor object id in circulation is well under a hundred. A
// value longer than that is not naming a subject, and this app aggregates on it —
// so accepting one is accepting an unpriced key.
//
// It is what turns every count below into a byte figure. Change it and every
// derived cap moves with it, which is the point.
const textMax = 256

// bodyMax is the largest request body this app accepts. The edge's own limit is
// 16 MiB (GATEWAY_BODY_LIMIT) for the whole fleet; a decision is a few hundred
// bytes and the largest batch any op here takes is 1,000 observations, so a
// quarter of a megabyte per observation is already absurd. It bounds the
// TRANSIENT cost of decoding, which the resident ceilings above do not cover:
// without it, N connections each hold N x 16 MiB of half-decoded JSON.
const bodyMax = 4 << 20

// ── the aggregates ──────────────────────────────────────────────────────────

// windows is the ONE declaration of the sliding aggregates this plane keeps.
//
// The NAMES and SPANS are the engine's standard set, because they are the rule
// vocabulary: `velocity.<axis>.<window>.<stat>` is what a tenant's rules are
// written against, the dictionary publishes them, and anomaly's inventory reads
// them by name at construction.
//
// The RESOLUTIONS are this plane's own, and they are lower than the AML defaults
// on purpose. Those were chosen for statutory structuring detection over a
// compliance feed — 444 buckets per key, a measured 22.7 KiB — which puts ONE
// tenant's plausible entity cardinality into gigabytes on a shared pod. Here the
// same four windows cost 94 buckets, 4.4 KiB of rings per key, and the
// quantisation is stated rather than assumed: a window of W with B buckets
// resolves to W/B, so the boundary of the 1h window is exact to five minutes and
// of the 30d window to a day. Every rule in the starter set compares a COUNT or a
// SUM over a whole window, and none of them can distinguish a boundary that
// fuzzy — precision that outruns the meaning of the number it measures is not
// precision, it is four times the memory.
//
// An operator who wants the compliance resolution raises velBytes; the two are
// the same knob seen from either end.
func windows() []velocity.Window {
	return []velocity.Window{
		{Name: "1h", Span: time.Hour, Buckets: 12},
		{Name: "24h", Span: 24 * time.Hour, Buckets: 24},
		{Name: "7d", Span: 7 * 24 * time.Hour, Buckets: 28},
		{Name: "30d", Span: 30 * 24 * time.Hour, Buckets: 30},
	}
}

// longestWindow is the span past which every ring of an untouched key reads
// zero. A key idle for longer holds no information, so reclaiming it costs
// nothing — which is the one reclaim that is free of any judgement call.
func longestWindow() time.Duration {
	var d time.Duration
	for _, w := range windows() {
		if w.Span > d {
			d = w.Span
		}
	}
	return d
}

// bucketBytes is sizeof(velocity's ring bucket) on a 64-bit build: an int64
// index, two ints, two float64s and a uint64 day mask. It is stated here rather
// than measured at run time because a bound computed from the thing it bounds is
// a bound that moves.
const bucketBytes = 48

// keyOverhead is everything a key costs BESIDES its buckets and its text: four
// ring headers, the pointer slice holding them, the entry, the engine's map slot
// and this app's ledger slot, plus allocator rounding. Deliberately generous — a
// bound that under-states is not a bound.
const keyOverhead = 1280

// bytesPerKey is what one aggregated entity costs THIS tenant. Three text values
// fit inside it: the engine keeps the composite id (org + kind + value) and this
// app's admission ledger keeps its own (kind + value), so 3 x textMax covers both
// with the axis name inside the slack.
//
// Derived from the windows and from textMax, so a change to either moves the key
// count and can never leave a stale constant behind.
func bytesPerKey() int {
	n := 0
	for _, w := range windows() {
		n += w.Buckets
	}
	return n*bucketBytes + keyOverhead + 3*textMax
}

// The knobs. Each is an environment override over a documented default, because
// the right ceiling is a property of the pod's memory limit and not of this
// source file — and because an operator who has to patch a constant to survive a
// capacity event will instead raise the constant to infinity.
const (
	envVelBytes = "RISK_VELOCITY_BYTES" // per-tenant aggregate budget, bytes
	envMemory   = "RISK_MEMORY"         // the node's whole budget for risk state, bytes
	envIdle     = "RISK_RECLAIM_IDLE"   // how long a tenant must be silent before it is retired
)

// velBytes is the per-tenant aggregate budget. 8 MiB is ~1,278 entities at this
// plane's resolution: enough that a tenant's ACTIVE set fits, small enough that
// a node holding a lot of them is still a node.
func velBytes() int { return envInt(envVelBytes, 8<<20, 1<<20, 1<<30) }

// memBytes is the node's whole budget for resident risk state, and it is the
// ONLY process bound. 512 MiB sits well inside cloud's 6 GiB request / 11 GiB
// limit and leaves the rest of the binary — 130-odd other apps in the same
// process — the room it had.
//
// A pod serving more than this is a SHARDING answer, not a bigger-number answer:
// the shard router pins an org to one pod, so capacity scales by adding writers.
// Raising the knob past what the pod's memory limit supports trades a loud
// refusal for an OOM kill, which takes every tenant down.
func memBytes() int { return envInt(envMemory, 512<<20, cellBytes, 32<<30) }

// idleReclaim is how long a tenant must send NOTHING before the background sweep
// retires it. Six hours: long enough that a bursty tenant keeps its 24h and 7d
// counts across a quiet afternoon, short enough that memory comes back the same
// day. The trigger is the tenant's own silence and nothing else.
//
// THE FLOOR IS LOAD-BEARING, not caution. Retiring a tenant CLOSES its file, and
// the one thing that holds that file outside a request is a search worker
// (searchBudget). A floor comfortably above that budget is what makes the close
// safe without a reference count on every op: a cell can only be retired when no
// request has resolved it for idleFloor, and nothing in this process can hold a
// handle that long. TestARetirementCannotRaceAWorker pins it.
//
// The floor is ALSO what a node under memory pressure reclaims at: pressure may
// take a cell that has been silent that long and no other, so a tenant that is
// using the node can never lose its rings to a tenant that is arriving.
func idleReclaim() time.Duration {
	d := time.Duration(envInt(envIdle, int(6*time.Hour/time.Second), 60, int(400*24*time.Hour/time.Second))) * time.Second
	if d < idleFloor {
		return idleFloor
	}
	return d
}

// idleFloor is the shortest idleness that may retire a tenant: eight times the
// longest a search may hold a tenant's file.
const idleFloor = 8 * searchBudget

// maxKeys is the per-tenant cardinality bound, computed from the budget rather
// than chosen. At least one, so a misconfigured budget degrades to a tiny store
// rather than to velocity's own 100,000-key default.
func maxKeys() int {
	n := velBytes() / bytesPerKey()
	if n < 1 {
		return 1
	}
	return n
}

// envInt reads a bounded integer override. Out of range or unparseable is the
// default, not a failure: a typo in a capacity knob must not stop a payment
// plane from booting, and the value it lands on is the documented one.
func envInt(name string, def, lo, hi int) int {
	v, err := strconv.Atoi(os.Getenv(name))
	if err != nil || v < lo || v > hi {
		return def
	}
	return v
}

// ── one tenant's aggregates, and the gate that bounds them ──────────────────

// rings is ONE TENANT's sliding aggregates together with the ledger that bounds
// them. The two travel as one value because a store without its gate is exactly
// the shape both defect classes had: bounded by a number nobody can read, and
// evicting silently when it binds.
//
// THE LEDGER IS WHAT MAKES `strained` EXACT. velocity offers no membership test,
// so without it the app cannot tell a new key from one it already holds and
// cannot refuse at the bound — which is how the engine's per-SHARD eviction
// (MaxKeys/64+1) came to fire at ~72% of the nominal bound while the tenant was
// told nothing. The ledger costs one map entry per key and it is PRICED: see
// bytesPerKey.
type rings struct {
	mu   sync.Mutex
	vel  *velocity.Store
	seen map[string]struct{}
	// held and dropped are the two numbers ANYONE may read: what this tenant is
	// priced at, and whether it is strained. They are atomics rather than fields
	// under mu because the node's memory gate is asked from INSIDE admit — so a
	// reclaim triggered by one tenant's new key prices another tenant's cell, and
	// pricing it through mu would re-enter this lock from underneath itself. One
	// writer (admit, under mu) keeps them exact.
	held    atomic.Int64
	dropped atomic.Int64
}

// record writes one observation onto every ring it has a value for, through the
// gate. The rings are keyed {OrgID, Kind, Value} with the tenant leading, so two
// tenants naming the same device or the same address never share a counter.
//
// A key the gate refuses is COUNTED and nothing else happens to it: the tenant's
// existing counters keep moving (refusing those too would turn a cardinality
// bound into a total stop), and `strained` becomes true for as long as this arm
// lasts, on the decision and on the probe.
//
// room is the node's own gate — nil when the caller is not accounting, which is
// what lets a search sandbox replay a tenant's history under the same per-tenant
// bound without charging the node twice for counters it already holds.
func (g *rings) record(t Tenant, o observation, room func(int) bool) {
	usd := nanoUSD(o.amount)
	for axis := range velocityAxes {
		v := o.axisOf(axis)
		if v == "" {
			continue
		}
		if !g.admit(axis, v, room) {
			continue
		}
		g.vel.Record(velocity.Key{OrgID: t.String(), Kind: axis, Value: v}, o.at, usd, structuringThreshold)
	}
}

// admit reports whether this key may be written, reserving its cost when it is
// new. A key already in the ledger is always admitted: it costs nothing more.
func (g *rings) admit(axis, value string, room func(int) bool) bool {
	id := axis + "\x00" + value
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, have := g.seen[id]; have {
		return true
	}
	if len(g.seen) >= maxKeys() {
		g.dropped.Add(1)
		return false
	}
	if room != nil && !room(bytesPerKey()) {
		g.dropped.Add(1)
		return false
	}
	g.seen[id] = struct{}{}
	g.held.Store(int64(len(g.seen)))
	return true
}

// observe reads one key's windows. It is the only read path, so the ledger and
// the store can never be consulted through different doors.
func (g *rings) observe(t Tenant, axis, value string) []velocity.Observation {
	return g.vel.Observe(velocity.Key{OrgID: t.String(), Kind: axis, Value: value})
}

// keys is how many distinct entities this tenant's rings hold. Exact, because
// nothing inside the store evicts.
func (g *rings) keys() int { return int(g.held.Load()) }

// stored is how many keys the ENGINE holds, asked of the engine.
//
// It exists because `keys` cannot answer the question the margin above is bought
// to answer. The ledger counts what the GATE admitted and knows nothing about
// what the store did with it, so an assertion on `keys` is true whether or not
// the engine evicted — which is a test that cannot fail for the thing it names.
// The two numbers agree exactly when nothing inside the store has evicted, and
// that agreement is the property, so it has to be readable from both sides.
func (g *rings) stored() int { return g.vel.Keys() }

// strained reports that this tenant's rings stopped tracking its own traffic.
//
// It is published rather than logged because the consumer is the TENANT: a rule
// that fires on `velocity.ip.1h.count >= 5` stops firing on a key that was never
// admitted, and there is no other way for the tenant to learn that its threshold
// is being measured against a partial ring.
func (g *rings) strained() bool { return g.missed() > 0 }

// missed is how many distinct keys this tenant's rings could not take. On the
// probe, so an operator sees the size of the gap and not only its existence.
func (g *rings) missed() int64 { return g.dropped.Load() }

// aggregates builds ONE TENANT's rings.
//
// It is the only call to velocity.New in this package and it is unexported, so
// `velocity.New` with a zero Config — a 100,000-key store shared by everyone —
// is not a thing a future edit can reach for by accident.
//
// MaxKeys is set BEYOND ANYTHING THE GATE ADMITS, and that is deliberate. The
// engine's cap is enforced per shard at MaxKeys/shardCount+1 and its eviction is
// silent; multiplying by the shard count puts it out of reach even if every key
// this tenant is allowed hashed into one shard, so the only thing that can ever
// refuse a key is this app's own gate, which says so. MaxKeys costs nothing —
// the store allocates per key recorded, never per key allowed.
func aggregates() *rings {
	return &rings{
		vel:  velocity.New(velocity.Config{Windows: windows(), MaxKeys: maxKeys()*velShards + velShards}),
		seen: make(map[string]struct{}, 64),
	}
}

// velShards is the number of shards velocity spreads keys over (its own
// shardCount, pkg/velocity/velocity.go). Stated here because the multiplication
// above is what makes the engine's silent eviction unreachable, and a change
// upstream has to be met here. TestNothingIsEvictedInsideATenantsAggregates
// measures it rather than trusting it.
const velShards = 64

// forest builds ONE TENANT's half-space forest over its own rings. It is the
// only call to anomaly.New in this package, and it OVERRIDES two fields of
// whatever config it is handed, which is why the live plane and the search
// sandbox can share it.
//
// MaxOrgs is forced to 1 and that is the whole point. anomaly.Store holds a map
// of tenants with a GLOBAL LRU over it, so a shared store lets one tenant's
// arrival delete another's learned model — and an evicted model returns to
// warming, which declines to score and reads as clean. One tenant per store makes
// that map hold exactly one key, so the eviction path is unreachable rather than
// merely unlikely.
//
// Shadow is forced on: it is the deployment-wide default, and the tenant's own
// live switch in its record plane is the second gate. Two in series, so a flag
// flipped by mistake still cannot make a tenant act, and a search sandbox can
// never alert for real.
func forest(cfg anomaly.Config, g *rings) (*anomaly.Store, error) {
	cfg.MaxOrgs, cfg.Shadow = 1, true
	m, err := anomaly.New(cfg, g.vel)
	if err != nil {
		return nil, fmt.Errorf("risk: %w", err)
	}
	return m, nil
}

// ── what an armed cell costs before it has counted anything ─────────────────

// The fixed half of a resident, reserved at admission. Everything here is
// bounded by a constant rather than by the caller, so reserving it is exact;
// the caller-scaled half (the rings, and the governance cache below) is priced
// live as it lands. That split is the operating point: an ordinary tenant costs
// cellBytes and nothing else, so a node serves hundreds of them.
const (
	// forestBytes is luxfi/aml's own measured figure for one half-space forest
	// at this app's geometry.
	forestBytes = 336 << 10
	// fileBytes is the tenant's open SQLite handle, its connection pool and the
	// cell's own structs.
	fileBytes = 64 << 10
	// agencyEntry is one memoised registry answer: the reference, the verdict,
	// its expiry and the map slot.
	agencyEntry = textMax + 128
	// agencyMemo is the whole per-tenant agency cache. Bounded by the entry
	// count TIMES the one text cap, which is what makes it a byte figure.
	agencyMemo = agencyCacheMax * agencyEntry
	// cellBytes is what one armed cell costs before it counts anything.
	cellBytes = forestBytes + fileBytes + agencyMemo
)

// ── the governance bounds ───────────────────────────────────────────────────
//
// Every one of these is a BYTE budget, and the row count is DERIVED from it —
// the other way round is class A. Each is enforced at WRITE time and INSIDE the
// transaction that does the write, which is what makes it a bound rather than a
// truncation and what stops two concurrent writes from both seeing room. The
// matching LIMIT on the read is defence in depth for rows that predate a cap.

const (
	// ruleMax is the largest a rule's stored form may be. A rule is an id, a
	// name and a handful of terms; 4 KiB holds sixteen terms at textMax each,
	// which is far past anything readable.
	ruleMax = 4 << 10
	// ruleBudget is the per-tenant budget for rules. They are ALL evaluated on
	// every decision, so this is a latency bound as much as a memory one.
	ruleBudget = 1 << 20
	// supMax is the largest a suppression's stored form may be: seven text
	// fields at textMax each, with room over.
	supMax = 2 << 10
	// supBudget is the per-tenant budget for suppressions. Each one is matched
	// against every hit.
	supBudget = 512 << 10
	// entryMax is what one list entry costs in the map the authorization path
	// loads it into: the value at its cap plus the map slot.
	entryMax = textMax + 128
	// listBudget is the per-tenant budget for list membership across ALL of a
	// tenant's lists — the map is one map, so a per-list cap would be a bound on
	// nothing, reachable by creating more lists.
	listBudget = 2 << 20
	// controlMax and controlBudget bound the controls plane. Controls are not on
	// the decision path — the money plane reads them — so this is a disk figure
	// rather than a resident one, and the budget is larger.
	controlMax    = 2 << 10
	controlBudget = 4 << 20
	// recordMax is the largest one decision row may be: the subject, the stage,
	// the signals map and the evidence, all at textMax.
	recordMax = 16 << 10
	// recordBudget is how much of the shared volume ONE tenant's decision log
	// may hold. THIS IS THE SAME CLASS ONE LAYER DOWN: every tenant's SQLite file
	// lives on the pod's one DataDir volume, so a log that only ever grows is
	// "one org quiets another" moved from RAM to disk — a few thousand calls
	// fill the volume and every tenant's writes start failing. 256 MiB is a
	// long history for a tenant and a small share of a pod volume.
	recordBudget = 256 << 20
	// pruneEvery is how many writes pass between prunes. Counting the log on
	// every decision would be a table scan inside an authorization window, so
	// the overshoot is bounded at this many rows instead — and the log is also
	// pruned once when the tenant's file is opened, which catches a tenant that
	// writes fewer than this between rollouts.
	pruneEvery = 256
)

// recordCap is how many decisions one tenant's log retains. Derived from the
// budget, like every other count here.
func recordCap() int { return recordBudget / recordMax }

// governMemo is the whole per-tenant governance cache at its budgets. Published
// so the per-tenant ceiling is arithmetic; priced LIVE rather than reserved,
// because governance is loaded on the authorization path and refusing it would
// disarm the tenant's controls — the one degradation this package will not do.
const governMemo = ruleBudget + supBudget + listBudget

// The derived row counts. Each is budget / largest-row, so the count and the
// byte figure can never disagree.
func ruleCap() int    { return ruleBudget / ruleMax }
func supCap() int     { return supBudget / supMax }
func listCap() int    { return listBudget / entryMax }
func controlCap() int { return controlBudget / controlMax }

// errCap is what a write past a budget answers. It names the budget and the
// number, so the tenant can act on it rather than guess.
func errCap(what string, cap int) error {
	return fmt.Errorf("this tenant already holds the maximum of %d %s; retire one before adding another", cap, what)
}

// errLong is what the wire door answers for a value it cannot price. Stated
// here, beside the cap it enforces, so the number and its refusal are one edit.
func errLong(what string) error {
	return fmt.Errorf("%s is longer than %d bytes, which is the most this plane will aggregate on or store", what, textMax)
}
