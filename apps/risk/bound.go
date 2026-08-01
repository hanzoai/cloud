package risk

// bound.go is where every number that costs memory is decided, ONCE, and where
// the only constructors for the two in-memory planes live.
//
// THE DEFECT THIS FILE EXISTS TO MAKE UNREPRESENTABLE. One process-wide
// velocity.Store with a GLOBAL key cap, shared by every tenant, is not a
// performance choice: velocity.Store evicts the least-recently-updated key in a
// SHARD, and shards are keyed by a hash that mixes tenants together. So one org
// filling the store deletes ANOTHER org's counters — the victim's rules then read
// zero, decline nothing, and report success. A silently disarmed control is worse
// than an absent one, because nobody goes looking for it.
//
// The shape that cannot express it: state is PER TENANT with a PER-TENANT bound.
// aggregates() and forest() are the ONLY constructors in this package, they are
// unexported, and they always apply the bound — velocity.New and anomaly.New are
// not called anywhere else (pinned by TestOnlyOneConstructorIsBounded). A tenant
// that reaches its own bound evicts its OWN oldest key and nobody else's, and
// says so (see resident.strained).
//
// THE WORST CASE IS ARITHMETIC, NOT A HOPE. Every quantity below is a constant
// or an environment override, and the ceiling is their product:
//
//	per key      bucketBytes * buckets(windows()) + keyOverhead   = 6,048 B
//	per tenant   velBytes                                         = 8 MiB   → 1,386 keys
//	             + one half-space forest (measured upstream)       ≈ 336 KiB
//	             + the governance cache (listCap entries + rules)  ≤ 2 MiB
//	                                                              ≈ 10.4 MiB
//	process      tenantMax tenants holding all three              = 48
//	                                                              ≈ 499 MiB
//
// ONE CEILING, NOT TWO. An earlier cut bounded open FILES separately from armed
// MEMORY, and since a tenant is armed the moment it is admitted the two numbers
// could never diverge — a knob that cannot bind is a knob an operator will read
// as protection they do not have. tenantMax is the single number, and it bounds
// the expensive half.
//
// A tenant beyond tenantMax is REFUSED ADMISSION, loudly (503 + an error log +
// a degraded probe) — never admitted by evicting an incumbent, which is the
// defect wearing a different hat. Reclaim is driven by a tenant's OWN idleness
// and by nothing else, so no tenant's activity can cost another tenant a ring.

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/luxfi/aml/pkg/anomaly"
	"github.com/luxfi/aml/pkg/velocity"
)

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
// same four windows cost 94 buckets, 6 KiB per key, and the quantisation is
// stated rather than assumed: a window of W with B buckets resolves to W/B, so
// the boundary of the 1h window is exact to five minutes and of the 30d window to
// a day. Every rule in the starter set compares a COUNT or a SUM over a whole
// window, and none of them can distinguish a boundary that fuzzy — precision
// that outruns the meaning of the number it measures is not precision, it is
// four times the memory.
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

// keyOverhead is everything a key costs BESIDES its buckets: four ring headers,
// the pointer slice holding them, the entry, the map slot and the composite id
// string, plus allocator rounding. Deliberately generous — a bound that
// under-states is not a bound. With the engine's own 444-bucket default this
// formula yields 22,848 B against a measured 22,745, which is the direction an
// estimate used as a ceiling has to err in.
const keyOverhead = 1536

// bytesPerKey is what one aggregated entity costs, from the windows actually
// configured. Derived, so a change to windows() moves the key count and can
// never leave a stale constant behind.
func bytesPerKey() int {
	n := 0
	for _, w := range windows() {
		n += w.Buckets
	}
	return n*bucketBytes + keyOverhead
}

// The knobs. Each is an environment override over a documented default, because
// the right ceiling is a property of the pod's memory limit and not of this
// source file — and because an operator who has to patch a constant to survive a
// capacity event will instead raise the constant to infinity.
const (
	envVelBytes  = "RISK_VELOCITY_BYTES" // per-tenant aggregate budget, bytes
	envTenantMax = "RISK_TENANTS"        // tenants this process holds at once
	envIdle      = "RISK_RECLAIM_IDLE"   // how long a tenant must be silent before it is retired
)

// velBytes is the per-tenant aggregate budget. 8 MiB is ~1,386 entities at this
// plane's resolution: enough that a tenant's ACTIVE set fits, small enough that
// tenantMax of them is half a gigabyte.
func velBytes() int { return envInt(envVelBytes, 8<<20, 1<<20, 1<<30) }

// tenantMax is how many tenants this process holds at once — each with its own
// aggregates, its own model, its own open file and its own governance cache. It
// is the process's memory bound, and the only one.
//
// A pod serving more tenants than this is a SHARDING answer, not a bigger-number
// answer: the shard router already pins an org to one pod, so capacity scales by
// adding writers. Raising the knob past what the pod's memory limit supports
// trades a loud refusal for an OOM kill, which takes every tenant down.
func tenantMax() int { return envInt(envTenantMax, 48, 1, 4096) }

// idleReclaim is how long a tenant must send NOTHING before it is retired. Six
// hours: long enough that a bursty tenant keeps its 24h and 7d counts across a
// quiet afternoon, short enough that memory comes back the same day. The trigger
// is the tenant's own silence and nothing else — no other tenant's activity can
// ever cause it, which is the property that makes this reclaim and not eviction.
//
// THE FLOOR IS LOAD-BEARING, not caution. Retiring a tenant CLOSES its file, and
// the one thing that holds that file outside a request is a search worker
// (searchBudget). A floor comfortably above that budget is what makes the close
// safe without a reference count on every op: a cell can only be retired when no
// request has resolved it for idleFloor, and nothing in this process can hold a
// handle that long. TestARetirementCannotRaceAWorker pins it.
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

// aggregates builds ONE TENANT's sliding aggregates.
//
// It is the only call to velocity.New in this package and it is unexported, so
// `velocity.New` with a zero Config — a 100,000-key store shared by everyone —
// is not a thing a future edit can reach for by accident. The bound it carries
// is per tenant BECAUSE THE STORE IS per tenant: eviction inside it can only
// ever drop a key this same tenant put there.
func aggregates() *velocity.Store {
	return velocity.New(velocity.Config{Windows: windows(), MaxKeys: maxKeys()})
}

// forest builds ONE TENANT's half-space forest over its own aggregates. It is
// the only call to anomaly.New in this package, and it OVERRIDES two fields of
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
func forest(cfg anomaly.Config, vel *velocity.Store) (*anomaly.Store, error) {
	cfg.MaxOrgs, cfg.Shadow = 1, true
	m, err := anomaly.New(cfg, vel)
	if err != nil {
		return nil, fmt.Errorf("risk: %w", err)
	}
	return m, nil
}

// ── the governance bounds ───────────────────────────────────────────────────
//
// Every one of these is enforced at WRITE time, which is what makes it a bound
// rather than a truncation. The matching LIMIT on the read is defence in depth
// for rows that predate a cap or arrive by any other route.

const (
	// ruleCap is how many rules one tenant may hold. Every one of them is
	// evaluated on every decision, so this is a latency bound as much as a
	// memory one.
	ruleCap = 500
	// listCap is how many list entries one tenant may hold across all its
	// lists. They are loaded into a map on the authorization path, so the cap is
	// what stops a list from being an unbounded read AND an unbounded map on
	// every decision.
	listCap = 20_000
	// suppressionCap is how many suppressions one tenant may hold. Each one is
	// matched against every hit.
	suppressionCap = 500
	// controlCap is how many controls one tenant may declare. Controls are not
	// on the decision path — the money plane reads them — so the cap is larger.
	controlCap = 5_000
)

// errCap is what a write past a cap answers. It names the cap and the number, so
// the tenant can act on it rather than guess.
func errCap(what string, cap int) error {
	return fmt.Errorf("this tenant already holds the maximum of %d %s; retire one before adding another", cap, what)
}
