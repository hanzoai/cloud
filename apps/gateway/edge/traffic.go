package edge

// Traffic is the edge's live picture of WHO is calling — the sensor the abuse
// gate reads and the /v1/gateway/traffic op reports. It lives here, beside the
// policy store, for the same reason the policy does: this is a LEAF package, so
// the middleware (package cloud) and the subsystem (apps/gateway) share ONE
// object rather than two that drift.
//
// It is a SENSOR, not a scorer. It counts, it classes the traffic into a lane,
// and it remembers a verdict; it never decides. Deciding is /v1/risk's job,
// reached through cloud.Decide — which is why nothing here has a threshold on a
// SCORE, a weight or a model.
//
// WHAT IT MEASURES, per rolling window, per (scope, caller):
//
//	requests   — cadence. Per CALLER, which no other limiter does: EdgeRateLimit
//	             keys on client IP (pre-auth) and ScopeRateLimit on
//	             (org, project, service). A stolen key inside a normal org
//	             ceiling is invisible to both.
//	failures   — 401/403 outcomes. Credential stuffing and a replayed stolen
//	             token both show up here before they show up anywhere else.
//	paths      — path spread. One caller touching many distinct paths is
//	             scraping; a real integration walks a handful.
//	peers      — how many DISTINCT credentials one client IP presented. This is
//	             the stuffing signature: one host, many keys.
//
// A CALLER IS A FACT THE SERVER STATED, NEVER A STRING THE CALLER PICKED. The key
// every count and every held verdict lives under is the fingerprint of a
// credential the identity boundary VALIDATED (Signal.Cred), and the client
// address when it validated none. An unvalidated Authorization header is not an
// identity — it is a value the caller chooses per request, so keying on it let a
// blocked caller walk out of its own hold by typing a different one, and made
// presenting garbage strictly better for an attacker than presenting nothing. The
// credential a request merely PRESENTED still counts, but only as spread
// (Signal.Presented → the peers signature), never as a key.
//
// TENANCY IS THE DATA STRUCTURE, NOT A FILTER OVER IT. One org's callers, hosts
// and lane counters live in that org's OWN tables, reached only by indexing
// tenants[org]. There is no shared map with org-prefixed keys, so there is no
// query that could return another tenant's row and no sweep that could reclaim
// one — a cross-tenant read or eviction is not refused here, it is unwritable.
//
// NOTHING IS EVER REMOVED TO MAKE ROOM FOR SOMETHING ELSE. That is the ONE
// reclaim rule, and it is what makes the bounds below safe. A table at its
// ceiling first drops its own keys that have gone IDLE — unseen for a full
// window, so they carry no live count and no live verdict — and if that frees
// nothing it REFUSES the new key. Eviction of live state is not guarded against
// here, it is absent: there is no code path that deletes a key a caller is still
// using, so one caller's traffic cannot erase another's counts, cannot release
// another's held verdict, and cannot quietly disarm the control that was watching
// it. The cost of a full table falls on the caller who could not be admitted, and
// that caller is COUNTED and REPORTED (Strain) rather than silently unmeasured.
//
// THE BOUND IS IN BYTES, BECAUSE BYTES ARE WHAT RUNS OUT. A cap on the NUMBER of
// keys is not a bound when the values behind them can be any size, so every
// string that can enter an entry is clamped at the door here and every entry has
// a published worst-case size (callerBytes, hostBytes, tenantBytes). Every
// admission — a tenant, a caller, an address — charges that size against ONE
// process budget (MaxBytes), so count × size IS the byte bound and the ceiling in
// the doc is the ceiling in memory. TestTraffic_FootprintIsBoundedInBytes fills a
// table with worst-case values, measures it, and fails if these numbers understate
// it.
//
// PER-SCOPE CEILINGS ARE THE FAIRNESS BOUND, NOT THE MEMORY BOUND. maxCallers and
// maxHosts cap what ONE scope may hold — 4.0 MiB, about 3% of the budget — so the
// anonymous lane, which is the whole internet's lane and the only scope an
// attacker can reach without a credential, cannot consume the room the tenants
// need. Everything else is a verified org, which cannot be minted for free.
//
// BOUNDED WITHIN A KEY, TOO. Each key holds a fixed-size ring of buckets and two
// 64-bit words; nothing grows with traffic. Path spread and peer spread are
// counted by hashing into one word and taking its population count — 8 bytes per
// key, saturating at 64, which is far past the point where the answer stops
// changing the decision.

import (
	"sort"
	"sync"
	"time"

	"hash/maphash"

	"github.com/hanzoai/cloud/internal/shorten"
)

const (
	// window is the span every count is taken over. One minute: long enough that
	// a burst is distinguishable from a single request, short enough that a key
	// that stops being abused stops looking abused within a minute.
	window = time.Minute
	// buckets divides the window so it can roll instead of stepping. Six ten-second
	// buckets: a count is never more than a sixth of a window stale, and the ring
	// is 6 machine words.
	buckets = 6
	// bucketSpan is the duration one bucket covers.
	bucketSpan = window / buckets

	// maxCallers bounds ONE SCOPE's caller table. It is a FAIRNESS cap, not the
	// memory bound (MaxBytes is): it is what stops the anonymous lane — the one
	// scope reachable without a credential — from taking the room the tenants
	// need. Reached by an org presenting four thousand distinct credentials inside
	// one minute, which is an attack on that org's own account and costs that
	// org's own sensor resolution and nobody else's.
	maxCallers = 4_096
	// maxHosts bounds ONE SCOPE's address table, which is what an unauthenticated
	// caller lands in. It is the bound that makes a forged address harmless: the
	// worst a flood of distinct addresses can do is fill the lane it arrived in.
	maxHosts = 4_096

	// spreadCeiling is where a population count saturates. Both spread words are
	// 64 bits, so 64 is "many" and the exact number past it is not a fact the
	// sensor claims to know.
	spreadCeiling = 64

	// holdCap bounds how long a verdict is held. A held verdict is enforcement
	// without a fresh judgement, so it expires quickly and the scorer is asked
	// again; five minutes is the ceiling a caller may request.
	holdCap = 5 * time.Minute

	// tenantIdle is how long a scope may go untouched before its state is
	// reclaimed whole. Longer than holdCap by a window, so a scope is never
	// dropped while one of its callers is still under a live verdict.
	tenantIdle = holdCap + window
)

// ─────────────────────────────────────────────────────────────────────────────
// The byte bound
// ─────────────────────────────────────────────────────────────────────────────

// MaxBytes is the whole sensor's memory ceiling. Every admission charges its
// entry's published size against it and an admission that would exceed it is
// REFUSED — never satisfied by removing something else — so this is the number
// the process cannot pass, not an estimate of what it usually costs.
//
// 128 MiB against a pod that requests 6 GiB: a sensor that can be a measurable
// fraction of the process it protects is a liability, and a sensor whose ceiling
// is a product of three counts (tenants × callers × bytes) is not a ceiling at
// all — that spelling published 60 GB and called it a bound.
const MaxBytes = 128 << 20

// The published worst-case size of ONE entry, in bytes: the struct, its map
// overhead, its key, and every string it can hold — all of which are clamped at
// the door below, which is what makes these ceilings rather than averages.
const (
	callerBytes = 768
	hostBytes   = 256
	tenantBytes = 4096
)

// The clamps that make an entry's size a fact rather than a hope. Every value
// that enters the sensor from outside passes one of them, so no caller and no
// scorer can make one entry cost more than the constants above.
const (
	// maxKeyLen bounds a caller key: a 12-character fingerprint or a
	// 45-character address, with room to spare and none for a header.
	maxKeyLen = 64
	// maxOrgLen bounds a scope key. It is the validated IAM owner claim, which
	// principal.MaxOrgLen already bounds at the identity boundary; bounded again
	// here because this package must not depend on its caller having done it.
	maxOrgLen = 128
	// maxActionLen, maxCauseLen and maxDecisionLen bound what a HELD verdict
	// carries. These come from the scorer, not from the client, and the scorer is
	// still an input: a model that answers with a megabyte of prose must not be
	// able to turn a 4,096-key table into a gigabyte.
	maxActionLen   = 16
	maxCauseLen    = 64
	maxDecisionLen = 64
)

// clamp truncates s to at most n bytes, on a rune boundary so a clamped value is
// still valid UTF-8 when it is reported.
func clamp(s string, n int) string {
	return shorten.To(s, n)
}

// budget is the sensor's memory ceiling, charged in bytes. It is guarded by
// Traffic.mu like everything else here.
//
// A process-wide budget is NOT the shared-cap defect this file exists to rule
// out. That defect is EVICTION — one tenant's traffic removing another's state —
// and this budget can only ever REFUSE a new entry. No key is deleted to satisfy
// an admission, so no tenant can lose a count, a verdict or a sensor because
// another tenant got busy. What a full budget costs is resolution for whoever is
// arriving next, and that refusal is counted and reported in that scope's own
// view.
type budget struct {
	used int
	max  int
}

func (b *budget) take(n int) bool {
	if b.used+n > b.max {
		return false
	}
	b.used += n
	return true
}

func (b *budget) give(n int) { b.used -= n }

// ─────────────────────────────────────────────────────────────────────────────
// The lane — what kind of caller this is
// ─────────────────────────────────────────────────────────────────────────────

// The credential classes. A syntactic fact about HOW a caller authenticated —
// the credential's own shape plus whether the identity boundary validated it.
// The classification of a REQUEST is cloud's (it holds the request); the
// vocabulary and the lane rule are here, where the lanes are counted, so there is
// one answer to "what lane is this" rather than one per reader.
const (
	// CredSession is a validated bearer that is not an opaque key: a browser or
	// CLI session minted by IAM for a person.
	CredSession = "session"
	// CredSecret is an sk- key: a machine credential issued to a principal.
	// It may not be shipped to a browser, so possession attributes.
	CredSecret = "secret"
	// CredPublishable is a pk- key: org-only by design, shipped in client
	// bundles, and therefore trivially copied. It names a tenant, not a caller.
	CredPublishable = "publishable"
	// CredAnonymous is no credential, or one the identity boundary refused.
	CredAnonymous = "anonymous"
)

// The lanes. Values, not booleans, because "we could not tell" is a distinct
// state from "we decided it is a bot", and collapsing them is how a false
// positive becomes a blocked customer.
const (
	AgencyAgent   = "agent"
	AgencyHuman   = "human"
	AgencyBot     = "bot"
	AgencyUnknown = "unknown"
)

// The shapes that make unattributable traffic a bot. Constants, not per-org
// configuration: they are properties of the protocol, not preferences of a
// tenant, and a knob here is a knob an attacker's target can be talked into
// widening. They are deliberately far above anything a normal client produces.
const (
	// stuffPeers — distinct credentials presented from one address in a window.
	// A browser presents one. A CI runner presents one. Eight is a script.
	stuffPeers = 8
	// guessFailures — 401/403 outcomes in a window. A client with a stale token
	// retries a few times and stops; twenty-five is someone trying keys.
	guessFailures = 25
	// sweepPaths — distinct paths one unattributable caller touched in a window.
	// A real integration walks a handful of endpoints; a crawler walks the map.
	sweepPaths = 40
)

// Lane classes a request from its credential class and the pattern its caller
// has been showing. Pure and total: same inputs, same lane, no clock, no I/O —
// which is what makes the table test the whole specification.
//
// It is computed HERE, inside the observation that produced the pattern, because
// the lane is DERIVED from the counts: a caller cannot state it, and a gate that
// tried to pass it in had to pass it before it knew the counts, which is how the
// lane split came to report "unknown" for every request ever made.
//
// The bot rule is deliberately CONJUNCTIVE. Unattributable alone is not bot:
// every first request from every new integration is unattributable. It takes an
// abuse SHAPE as well — many credentials from one address (stuffing), a wall of
// refusals (guessing), or a path sweep (scraping) — and then the caller is
// judged, not the anonymity.
func Lane(class string, p Pattern) string {
	switch class {
	case CredSecret:
		return AgencyAgent
	case CredSession:
		return AgencyHuman
	case CredPublishable, CredAnonymous:
		if abusive(p) {
			return AgencyBot
		}
		return AgencyUnknown
	}
	return AgencyUnknown
}

func abusive(p Pattern) bool {
	return p.Peers >= stuffPeers || p.Failures >= guessFailures || p.Paths >= sweepPaths
}

// ─────────────────────────────────────────────────────────────────────────────
// Strain — what a ceiling is doing right now
// ─────────────────────────────────────────────────────────────────────────────

// The states of a scope's ceilings, graded. A bound that binds MUST be readable:
// a control that quietly stops measuring is worse than no control, because the
// numbers it keeps publishing look like an answer.
const (
	// StrainClear — below the ceiling. Every caller is measured.
	StrainClear = "clear"
	// StrainFull — at the ceiling. A new caller is admitted only if a sweep frees
	// a slot first; the callers already here are unaffected.
	StrainFull = "full"
	// StrainRefuse — the ceiling turned a caller away inside this window. That
	// caller is UNMEASURED: its counts are zero because nothing was counted, not
	// because nothing happened, and the gate is told so rather than treating it
	// as a caller making its first request.
	StrainRefuse = "refuse"
	// StrainBlind — the request carried NO identity at all: no credential the
	// boundary validated, and no client address. There is nothing to count it
	// against, so it is counted only as traffic and nothing is ever held against
	// it.
	//
	// This is a DEPLOYMENT fact, not an attack: it is what a plane looks like when
	// the client address never arrives — an edge that terminates the connection
	// without passing the peer on (a TCP load balancer with no PROXY protocol, an
	// ingress that does not forward). The alternative spelling — key every
	// unidentifiable caller under the empty address — would put the entire
	// internet in ONE row, make it look like the worst credential-stuffing run
	// ever recorded, and enforce one verdict against everybody. "We cannot tell
	// who this is" must not be spelled the same way as "this is caller X".
	StrainBlind = "blind"
)

// ─────────────────────────────────────────────────────────────────────────────
// Observations
// ─────────────────────────────────────────────────────────────────────────────

// Signal is one observation: the facts about a request that the sensor counts.
// It carries credential FINGERPRINTS, never a credential — see cloud.Fingerprint.
type Signal struct {
	// Org is the VERIFIED tenant; "" for a caller with no tenant — the anonymous
	// lane, which is one scope for the whole internet.
	Org string
	// Cred is the fingerprint of a credential the identity boundary VALIDATED,
	// and "" when it validated none. It is the ONLY thing that can key a caller,
	// because it is the only credential fact the caller does not choose: we minted
	// it, to a named principal, and we can revoke it. A request whose credential
	// did not validate has no identity beyond where it came from, and is keyed on
	// its address.
	Cred string
	// Presented is the fingerprint of whatever credential the request carried,
	// valid or not. It is counted only as SPREAD — how many distinct credentials
	// one address tried, which is the stuffing signature — and is never a key: a
	// value the caller picks per request cannot be an identity.
	Presented string
	// IP is the client address, resolved by cloud.ClientIP from the socket peer
	// and our own forwarding hops. It is the anonymous caller's only identity.
	IP string
	// Path is the request path, hashed into the spread word (never stored).
	Path string
	// Class is the credential class the identity boundary's answer implies:
	// session, secret, publishable or anonymous. It selects the lane together with
	// the pattern; it is not itself a lane.
	Class string
}

// Pattern is what the sensor knows about one caller right now. Every count is
// over the rolling window — no scores, no thresholds, no judgement.
type Pattern struct {
	// Requests is how many requests this caller made in the window.
	Requests int
	// Failures is how many of them ended 401 or 403.
	Failures int
	// Paths is the approximate number of distinct paths it touched, saturating at 64.
	Paths int
	// Peers is the approximate number of distinct credentials this client IP
	// presented in the window, saturating at 64 — the stuffing signature.
	Peers int
	// Lane is the lane this request was classed into, derived from Class and the
	// counts above by Lane. It is an OUTPUT: the caller does not state it and the
	// gate does not compute it a second time.
	Lane string
	// Strain is what the scope's ceiling did to THIS observation: "" when the
	// caller was measured, StrainRefuse when the ceiling was full of live callers
	// and this one could not be admitted — so every count above is zero because
	// nothing was measured. A gate reads it to avoid treating an unmeasured caller
	// as a caller making its first request.
	Strain string
	// Rise names a strain grade the FIRST time this scope reaches it, and is ""
	// on every other observation. It exists so a degraded sensor produces one log
	// line per grade change instead of one per request, without this package
	// holding a logger.
	Rise string
}

// Hold is a verdict the gate is enforcing without re-asking. A held verdict is
// the reason an attack does not cost one screen per request.
type Hold struct {
	// Action is the enforced action: challenge, restrict or block.
	Action string
	// Reason is the scorer's short cause, carried for the audit record.
	Reason string
	// Decision is the scorer's decision id, so a held enforcement is traceable
	// back to the judgement that produced it.
	Decision string
	// Until is when the hold lapses and the scorer is asked again.
	Until time.Time
}

// ring is a fixed-size rolling counter. slot i holds the count for the bucket
// whose start is at.Truncate(bucketSpan); a slot older than one window is zeroed
// on touch rather than swept, so an idle key costs nothing to keep accurate.
type ring struct {
	at  [buckets]int64 // bucket start, unix nano
	n   [buckets]int
	bad [buckets]int
}

func (r *ring) bump(now time.Time, failed bool) {
	start := now.Truncate(bucketSpan).UnixNano()
	i := int(start/int64(bucketSpan)) % buckets
	if r.at[i] != start {
		r.at[i], r.n[i], r.bad[i] = start, 0, 0
	}
	if failed {
		r.bad[i]++
		return
	}
	r.n[i]++
}

func (r *ring) sum(now time.Time) (n, bad int) {
	floor := now.Add(-window).UnixNano()
	for i := range r.at {
		if r.at[i] > floor {
			n += r.n[i]
			bad += r.bad[i]
		}
	}
	return n, bad
}

// spread approximates a distinct count in one 64-bit word: each member sets one
// bit, and the population count is the answer. It saturates at 64 and it is
// one-way — a path or a credential fingerprint that went in cannot be read back
// out, which is why the sensor can keep it in memory without keeping the values.
type spread struct {
	word uint64
	at   int64 // window start this word belongs to, unix nano
}

func (s *spread) add(now time.Time, h uint64) {
	start := now.Truncate(window).UnixNano()
	if s.at != start {
		s.at, s.word = start, 0
	}
	s.word |= 1 << (h % spreadCeiling)
}

func (s *spread) count(now time.Time) int {
	if s.at != now.Truncate(window).UnixNano() {
		return 0
	}
	n := 0
	for w := s.word; w != 0; w &= w - 1 {
		n++
	}
	return n
}

// caller is the per-credential state within ONE scope. seen is the reclaim
// policy's liveness stamp.
type caller struct {
	req   ring
	paths spread
	hold  Hold
	seen  time.Time
}

// live reports whether this key is still in use: a caller seen inside the window,
// or one under a verdict that has not lapsed. Only a key that is live in NEITHER
// sense may be reclaimed, which is what makes reclaim garbage collection rather
// than eviction.
func (c *caller) live(now time.Time) bool {
	return !c.seen.Before(now.Add(-window)) || now.Before(c.hold.Until)
}

// host is the per-address state within ONE scope: how many distinct credentials
// this address presented. Kept apart from caller because the stuffing question is
// asked of an ADDRESS, and an address presenting many keys has, by definition, no
// one key.
type host struct {
	creds spread
	seen  time.Time
}

func (h *host) live(now time.Time) bool { return !h.seen.Before(now.Add(-window)) }

// lane counts one scope's traffic by lane over the current window — what the
// /v1/gateway/traffic op reports. Small and fixed: four lanes, four counters.
type lane struct {
	req      ring
	denied   ring
	screened ring
	unscored ring
	at       int64
	byLane   map[string]int
}

// ─────────────────────────────────────────────────────────────────────────────
// The bounded table — ONE reclaim policy, written once
// ─────────────────────────────────────────────────────────────────────────────

// keeper is what a table needs to know about a value to reclaim it safely.
type keeper interface {
	live(now time.Time) bool
}

// table is a bounded map for ONE scope. It is the only place a key is ever
// removed, so the policy — reclaim what is dead, refuse what does not fit, never
// touch what is live, never touch anyone else's — exists once and cannot differ
// between the two tables that use it.
//
// The cap, the per-entry cost and the budget are CONSTRUCTOR arguments and the
// map is unexported, so there is no way to build an unbounded one or one that
// charges nothing: the wrong shape is unrepresentable rather than discouraged.
type table[V keeper] struct {
	m      map[string]V
	max    int
	cost   int
	budget *budget
	// refused counts admissions this table's ceiling turned away, over the same
	// rolling window as every other count here — so "this scope is refusing
	// callers" is a live fact that decays when it stops being true, not a total
	// that is still being reported an hour later.
	refused ring
	// swept bounds how often a full table rescans itself for dead keys, so a
	// sustained flood against a full table costs one scan per bucket rather than
	// one per request.
	swept time.Time
}

func newTable[V keeper](max, cost int, b *budget) table[V] {
	return table[V]{m: make(map[string]V), max: max, cost: cost, budget: b}
}

func (t *table[V]) get(k string) (V, bool) {
	v, ok := t.m[k]
	return v, ok
}

// admit returns the slot for k, creating it with mk when absent, and reports
// whether it could. When the table is at its ceiling or the process budget is
// spent it first reclaims its OWN dead keys; if that frees nothing it REFUSES.
// It never removes a live key — not its own, and it holds no reference to
// anybody else's.
func (t *table[V]) admit(k string, now time.Time, mk func() V) (V, bool) {
	if v, ok := t.m[k]; ok {
		return v, true
	}
	if len(t.m) >= t.max || t.budget.used+t.cost > t.budget.max {
		t.reclaim(now)
	}
	if len(t.m) >= t.max || !t.budget.take(t.cost) {
		t.refused.bump(now, false)
		var zero V
		return zero, false
	}
	v := mk()
	t.m[k] = v
	return v, true
}

// reclaim drops this table's DEAD keys: unseen for a full window and under no
// live verdict, so they hold no count anyone can read and no enforcement anyone
// is relying on. There is deliberately no second pass that drops live keys when
// this one frees nothing — that pass is what turned a memory bound into a
// cross-caller eviction channel and a way to release a held verdict by filling a
// map.
func (t *table[V]) reclaim(now time.Time) {
	if now.Sub(t.swept) < bucketSpan {
		return
	}
	t.swept = now
	for k, v := range t.m {
		if !v.live(now) {
			delete(t.m, k)
			t.budget.give(t.cost)
		}
	}
}

// drop releases the whole table's charge — called when its scope is reclaimed
// whole, so the budget is refunded exactly once per admitted key.
func (t *table[V]) drop() { t.budget.give(t.cost * len(t.m)) }

// strain grades what this table's ceiling is doing right now.
func (t *table[V]) strain(now time.Time) string {
	if n, _ := t.refused.sum(now); n > 0 {
		return StrainRefuse
	}
	if len(t.m) >= t.max {
		return StrainFull
	}
	return StrainClear
}

// ─────────────────────────────────────────────────────────────────────────────
// The scope — one org's (or the anonymous lane's) whole sensor state
// ─────────────────────────────────────────────────────────────────────────────

// tenant is everything the sensor knows about ONE scope. It is reached only by
// indexing Traffic.tenants, so every read and every reclaim is inside one scope
// by construction.
type tenant struct {
	callers table[*caller]
	hosts   table[*host]
	lane    lane
	// blind counts observations that carried no identity to count them against,
	// over the same rolling window as everything else.
	blind ring
	seen  time.Time
	// told is the strain grade this scope has already announced, so a rise is
	// reported once instead of once per request.
	told string
}

func newTenant(b *budget) *tenant {
	return &tenant{
		callers: newTable[*caller](maxCallers, callerBytes, b),
		hosts:   newTable[*host](maxHosts, hostBytes, b),
		lane:    lane{byLane: map[string]int{}},
		told:    StrainClear,
	}
}

// strain grades what this scope's sensor could not do: the worst of its two
// ceilings and its blind traffic.
func (tn *tenant) strain(now time.Time) string {
	worst := tn.callers.strain(now)
	for _, s := range []string{tn.hosts.strain(now), tn.blindStrain(now)} {
		if grade(s) > grade(worst) {
			worst = s
		}
	}
	return worst
}

func (tn *tenant) blindStrain(now time.Time) string {
	if n, _ := tn.blind.sum(now); n > 0 {
		return StrainBlind
	}
	return StrainClear
}

// grade orders the states by how much of the sensor is unavailable: at the
// ceiling, past it, and — worst — unable to tell callers apart at all.
func grade(s string) int {
	switch s {
	case StrainBlind:
		return 3
	case StrainRefuse:
		return 2
	case StrainFull:
		return 1
	}
	return 0
}

// rise reports a strain grade the first time this scope reaches it, and "" every
// other time. A grade that falls back is re-announced if it rises again.
func (tn *tenant) rise(now time.Time) string {
	s := tn.strain(now)
	if grade(s) <= grade(tn.told) {
		tn.told = s
		return ""
	}
	tn.told = s
	return s
}

// Traffic is the sensor. Safe for concurrent use; every method takes the one
// lock for a handful of arithmetic operations, which is the same cost profile as
// the token buckets already on this path.
type Traffic struct {
	mu      sync.Mutex
	seed    maphash.Seed
	tenants map[string]*tenant
	budget  budget
	swept   time.Time
}

// NewTraffic returns an empty sensor. The hash seed is per-process and never
// leaves it, so a fingerprint or path bit cannot be reproduced off-box.
func NewTraffic() *Traffic {
	return &Traffic{
		seed:    maphash.MakeSeed(),
		tenants: map[string]*tenant{},
		budget:  budget{max: MaxBytes},
	}
}

// callerKey is the caller's identity WITHIN a scope, and reports whether the
// request had one at all. The org is not in it because the org is the table the
// key lives in — which is what makes a cross-tenant collision unrepresentable
// rather than merely unlikely.
//
// It reads Signal.Cred, which is a credential the identity boundary VALIDATED,
// and falls back to the address when there is none. It deliberately cannot see
// Signal.Presented: a caller that could pick its own key could leave a hold by
// changing one header, and could open one table entry per request.
//
// Neither one means there is no caller to speak of, and the honest answer is to
// say so (StrainBlind) rather than to invent one: an empty address is a perfectly
// good map key, and using it would file every unidentifiable request in the world
// under one row.
func callerKey(s Signal) (string, bool) {
	switch {
	case s.Cred != "":
		return clamp("cred:"+s.Cred, maxKeyLen), true
	case s.IP != "":
		return clamp("ip:"+s.IP, maxKeyLen), true
	}
	return "", false
}

func (t *Traffic) hash(s string) uint64 {
	var h maphash.Hash
	h.SetSeed(t.seed)
	_, _ = h.WriteString(s)
	return h.Sum64()
}

// tenantLocked returns org's scope, creating it on first sight, and nil when the
// budget cannot afford one. Idle scopes are swept on a fixed cadence rather than
// on demand, so admitting a scope never depends on reclaiming another one that is
// still in use.
func (t *Traffic) tenantLocked(org string, now time.Time) *tenant {
	org = clamp(org, maxOrgLen)
	if tn := t.tenants[org]; tn != nil {
		tn.seen = now
		return tn
	}
	if !t.budget.take(tenantBytes) {
		return nil
	}
	tn := newTenant(&t.budget)
	tn.seen = now
	t.tenants[org] = tn
	return tn
}

// lookupLocked returns org's scope WITHOUT creating it — the read path, so a
// report on an org the sensor has never seen does not mint state for it.
func (t *Traffic) lookupLocked(org string) *tenant { return t.tenants[clamp(org, maxOrgLen)] }

// sweepLocked reclaims whole scopes that have gone quiet for longer than any
// verdict can live, on a fixed cadence. It never reclaims a scope to make room
// for another scope's traffic — only genuinely idle ones — so the scope table
// cannot become a cross-tenant eviction channel either.
func (t *Traffic) sweepLocked(now time.Time) {
	if now.Sub(t.swept) < bucketSpan {
		return
	}
	t.swept = now
	floor := now.Add(-tenantIdle)
	for org, tn := range t.tenants {
		if tn.seen.Before(floor) {
			tn.callers.drop()
			tn.hosts.drop()
			t.budget.give(tenantBytes)
			delete(t.tenants, org)
		}
	}
}

// Observe counts one request, classes it into a lane, and returns what is now
// known about its caller. Called on the way IN, before the handler runs, so the
// gate decides on the pattern that includes this request.
func (t *Traffic) Observe(s Signal, now time.Time) Pattern {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.sweepLocked(now)

	tn := t.tenantLocked(s.Org, now)
	if tn == nil {
		return Pattern{Lane: Lane(s.Class, Pattern{}), Strain: StrainRefuse}
	}

	key, known := callerKey(s)
	if !known {
		// Traffic with no identity is still traffic: it lands in the scope's totals
		// and its lane, so the volume is visible even though nothing can be
		// attributed. It is never held and never accumulates.
		tn.blind.bump(now, false)
		p := Pattern{Lane: Lane(s.Class, Pattern{}), Strain: StrainBlind, Rise: tn.rise(now)}
		tn.lane.roll(now)
		tn.lane.count(p.Lane, now)
		return p
	}

	c, ok := tn.callers.admit(key, now, func() *caller { return &caller{} })
	if !ok {
		// Unmeasured, and said so. The request is still counted in its lane, so a
		// scope's own totals never understate its traffic just because the sensor
		// ran out of room to attribute it.
		p := Pattern{Lane: Lane(s.Class, Pattern{}), Strain: StrainRefuse, Rise: tn.rise(now)}
		tn.lane.roll(now)
		tn.lane.count(p.Lane, now)
		return p
	}
	c.seen = now
	c.req.bump(now, false)
	if s.Path != "" {
		c.paths.add(now, t.hash(s.Path))
	}

	// The peer count is asked of the ADDRESS, and only means something when a
	// credential was presented — valid or not, since a wall of invalid ones from
	// one address IS the stuffing signature. A stream of requests presenting no
	// credential at all is a flood (EdgeRateLimit's question), not stuffing.
	peers := 0
	if s.IP != "" && s.Presented != "" {
		if h, ok := tn.hosts.admit(clamp(s.IP, maxKeyLen), now, func() *host { return &host{} }); ok {
			h.seen = now
			h.creds.add(now, t.hash(s.Presented))
			peers = h.creds.count(now)
		}
	}

	n, bad := c.req.sum(now)
	p := Pattern{Requests: n, Failures: bad, Paths: c.paths.count(now), Peers: peers}
	p.Lane = Lane(s.Class, p)
	p.Rise = tn.rise(now)

	tn.lane.roll(now)
	tn.lane.count(p.Lane, now)
	return p
}

// Fail records that a request ended 401 or 403. Called on the way OUT, because
// the outcome is not known on the way in — and it is the outcome, not the
// attempt, that separates a client with a stale token from one guessing tokens.
func (t *Traffic) Fail(s Signal, now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	tn := t.lookupLocked(s.Org)
	if tn == nil {
		return
	}
	key, known := callerKey(s)
	if !known {
		return
	}
	c, ok := tn.callers.get(key)
	if !ok {
		return
	}
	c.seen = now
	c.req.bump(now, true)
}

// Deny records that the gate refused a request. It bumps ONLY the refusal
// counter: the request itself was already counted by Observe on the way in, and
// counting it twice would make an org's own report say it sent more traffic than
// it did — the sort of number a customer notices before we do.
func (t *Traffic) Deny(org string, now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	tn := t.tenantLocked(org, now)
	if tn == nil {
		return
	}
	tn.lane.roll(now)
	tn.lane.denied.bump(now, false)
}

// Screen records that the org's traffic was put to the scorer once, and whether
// an answer came back: refusal is "" for a scored verdict and names the failure
// otherwise (cloud.RiskVerdict.Refusal).
//
// It is the BILLABLE UNIT of the risk product, counted here as well as metered,
// because the two answer different questions: the ledger answers "what is owed"
// and only once the SKU carries a price, while this answers "how much judgement
// did this org consume" from the first request.
//
// The split is what makes a dark scorer visible. An unanswered screen allows
// ordinary traffic, so a scorer that has silently stopped answering looks exactly
// like a quiet day — unless the count of questions that got no answer is a number
// on the org's own report, next to the ones that did.
func (t *Traffic) Screen(org, refusal string, now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	tn := t.tenantLocked(org, now)
	if tn == nil {
		return
	}
	tn.lane.roll(now)
	tn.lane.screened.bump(now, false)
	if refusal != "" {
		tn.lane.unscored.bump(now, false)
	}
}

// Hold pins a verdict to a caller for d, so an attack costs one screen rather
// than one per request. d is clamped to holdCap: enforcement without a fresh
// judgement is bounded, always. Every string is clamped at this door, so a held
// verdict has a published size.
//
// A hold on a caller the sensor could not admit is not stored — there is nowhere
// to put it — and the gate simply asks again next request. Enforcement is never
// silently downgraded: what is stored is enforced, and what could not be stored
// was answered by the scorer on this request anyway.
func (t *Traffic) Hold(s Signal, h Hold, d time.Duration, now time.Time) {
	if d <= 0 {
		return
	}
	if d > holdCap {
		d = holdCap
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	key, known := callerKey(s)
	if !known {
		return // nothing to hold it against; see StrainBlind.
	}
	tn := t.tenantLocked(s.Org, now)
	if tn == nil {
		return
	}
	c, ok := tn.callers.admit(key, now, func() *caller { return &caller{} })
	if !ok {
		return
	}
	c.seen = now
	c.hold = Hold{
		Action:   clamp(h.Action, maxActionLen),
		Reason:   clamp(h.Reason, maxCauseLen),
		Decision: clamp(h.Decision, maxDecisionLen),
		Until:    now.Add(d),
	}
}

// Held returns the verdict in force for this caller, if one has not lapsed.
func (t *Traffic) Held(s Signal, now time.Time) (Hold, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	tn := t.lookupLocked(s.Org)
	if tn == nil {
		return Hold{}, false
	}
	key, known := callerKey(s)
	if !known {
		return Hold{}, false
	}
	c, ok := tn.callers.get(key)
	if !ok || c.hold.Action == "" || !now.Before(c.hold.Until) {
		return Hold{}, false
	}
	return c.hold, true
}

// Lapsed reports whether a verdict WAS in force for this caller and has since
// expired. It is what stops a hold from buying an attacker a free minute at a
// time: a caller the scorer refused is asked about again on its next request
// after the hold ends, whether or not the local pattern still looks unusual.
//
// That distinction matters because the scorer sees more than the sensor does. A
// verdict reached from the org's own history — prior accounts on this device, a
// spend curve, a chargeback — leaves no trace in a rolling minute of request
// counts, so waiting for the local pattern to re-trip would wait forever.
//
// The record is dropped by Release, which the gate calls once the scorer allows
// the caller again. So this is true exactly between "a hold ended" and "the
// scorer said it is fine now".
func (t *Traffic) Lapsed(s Signal, now time.Time) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	tn := t.lookupLocked(s.Org)
	if tn == nil {
		return false
	}
	key, known := callerKey(s)
	if !known {
		return false
	}
	c, ok := tn.callers.get(key)
	return ok && c.hold.Action != "" && !now.Before(c.hold.Until)
}

// Release drops any held verdict for this caller — the operator's undo, and the
// gate's own acknowledgement that a lapsed hold has been re-judged.
func (t *Traffic) Release(s Signal) {
	t.mu.Lock()
	defer t.mu.Unlock()
	tn := t.lookupLocked(s.Org)
	if tn == nil {
		return
	}
	key, known := callerKey(s)
	if !known {
		return
	}
	if c, ok := tn.callers.get(key); ok {
		c.hold = Hold{}
	}
}

// roll resets the per-lane split when the window turns over. Called by every
// writer before it counts, so the split and the rings always describe the same
// window.
func (l *lane) roll(now time.Time) {
	if start := now.Truncate(window).UnixNano(); l.at != start {
		l.at = start
		l.byLane = map[string]int{}
	}
}

// count records one request in its lane. Called exactly once per request, from
// Observe, with the lane Observe itself derived — so the split and the totals
// can never disagree and the map can only ever hold the four lane constants.
func (l *lane) count(name string, now time.Time) {
	l.byLane[name]++
	l.req.bump(now, false)
}

// TrafficView is one scope's live edge picture. Every number in it was counted
// under that scope's own tables; there is no aggregate here that another tenant
// contributed to.
type TrafficView struct {
	// Org is the scope this view was taken for — the validated principal's own,
	// never a value the caller supplied. Empty names the anonymous lane, the one
	// scope that has no tenant.
	Org string `json:"org"`
	// WindowSec is the span the counts cover, in seconds.
	WindowSec int `json:"window_sec"`
	// Mode is the abuse gate's posture for this scope: "shadow" records the scorer's
	// action without enforcing it, "live" enforces it.
	Mode string `json:"mode"`
	// Requests is how many requests this scope made in the window.
	Requests int `json:"requests"`
	// Denied is how many of them the gate refused.
	Denied int `json:"denied"`
	// Screens is how many of them were put to the scorer — the billable unit of
	// the risk product. Counted from the first request, whatever the SKU costs.
	Screens int `json:"screens"`
	// Unscored is how many of those screens got NO answer — the scorer was absent,
	// stuck, slow, erroring or silent. An unanswered screen allows ordinary
	// traffic, so this is the number that separates "a quiet day" from "the judge
	// stopped answering and nothing said so".
	Unscored int `json:"unscored,omitempty"`
	// Strain is what this scope's ceilings are doing: "clear" below them, "full"
	// at them, "refuse" once a caller has been turned away inside this window —
	// which means that caller is UNMEASURED and the numbers here are a sample
	// rather than a census. It is reported rather than logged because the
	// alternative — a bound that degrades a scope silently — is the failure this
	// design exists to rule out. No other scope can move it.
	Strain string `json:"strain"`
	// Tracked is how many callers this scope holds state for right now, and
	// Ceiling is the most it may hold. Tracked == Ceiling is the fact a bound
	// that binds cannot hide.
	Tracked int `json:"tracked"`
	// Ceiling is the most callers this scope may hold at once.
	Ceiling int `json:"ceiling"`
	// Refused is how many callers this scope's ceilings turned away in the window.
	Refused int `json:"refused,omitempty"`
	// Blind is how many requests in the window carried no identity to attribute
	// them to — no validated credential and no client address. Non-zero on a
	// public plane means the client address is not reaching this process (a TCP
	// load balancer with no PROXY protocol in front of it, typically), so this
	// scope's callers cannot be told apart and nothing can be held against them.
	Blind int `json:"blind,omitempty"`
	// Lanes is the request count per lane — agent, human, bot, unknown. This is
	// the split that separates a customer's automation from a scraper.
	Lanes map[string]int `json:"lanes"`
	// Callers is the scope's busiest callers this window. A credentialed caller
	// appears as a FINGERPRINT — a per-process one-way digest: enough to recognise
	// the same caller across requests, never enough to reconstruct the credential.
	Callers []TrafficCaller `json:"callers"`
}

// TrafficCaller is one caller's line in the view.
type TrafficCaller struct {
	// Cred is the caller's key: a credential fingerprint (a per-process one-way
	// digest, not a key) for a validated caller, and "ip:<addr>" for one that
	// presented no credential we could validate.
	Cred string `json:"cred"`
	// Requests is its request count in the window.
	Requests int `json:"requests"`
	// Failures is how many ended 401 or 403.
	Failures int `json:"failures"`
	// Paths is the approximate number of distinct paths it touched (max 64).
	Paths int `json:"paths"`
	// Action is the verdict currently held against it, if any.
	Action string `json:"action,omitempty"`
	// Reason is why that verdict was reached.
	Reason string `json:"reason,omitempty"`
	// HeldUntil is when the held verdict lapses, unix seconds.
	HeldUntil int64 `json:"held_until,omitempty"`
}

// maxViewCallers bounds a view. A report is for reading; a scope with thousands
// of live keys is served its busiest, not a page of noise.
const maxViewCallers = 50

// View returns org's live picture. It reads ONE scope's tables and cannot reach
// another's — not because it filters, but because it never holds a reference to
// anything else.
//
// The scan happens under the lock and the SORT does not. Every request on the
// plane needs that same lock to be observed, so holding it across an
// n·log n comparison of a full table would put the report on the critical path of
// all the traffic it is reporting on.
func (t *Traffic) View(org, mode string, now time.Time) TrafficView {
	v := TrafficView{
		Org: org, WindowSec: int(window / time.Second), Mode: mode,
		Strain: StrainClear, Ceiling: maxCallers, Lanes: map[string]int{},
	}

	t.mu.Lock()
	tn := t.lookupLocked(org)
	if tn == nil {
		t.mu.Unlock()
		return v
	}
	v.Strain = tn.strain(now)
	v.Tracked = len(tn.callers.m)
	refusedCallers, _ := tn.callers.refused.sum(now)
	refusedHosts, _ := tn.hosts.refused.sum(now)
	v.Refused = refusedCallers + refusedHosts
	v.Blind, _ = tn.blind.sum(now)

	if tn.lane.at == now.Truncate(window).UnixNano() {
		v.Requests, _ = tn.lane.req.sum(now)
		v.Denied, _ = tn.lane.denied.sum(now)
		v.Screens, _ = tn.lane.screened.sum(now)
		v.Unscored, _ = tn.lane.unscored.sum(now)
		for a, n := range tn.lane.byLane {
			v.Lanes[a] = n
		}
	}

	v.Callers = make([]TrafficCaller, 0, len(tn.callers.m))
	for k, c := range tn.callers.m {
		n, bad := c.req.sum(now)
		if n == 0 && bad == 0 && c.hold.Action == "" {
			continue
		}
		row := TrafficCaller{Cred: viewCred(k), Requests: n, Failures: bad, Paths: c.paths.count(now)}
		if c.hold.Action != "" && now.Before(c.hold.Until) {
			row.Action, row.Reason, row.HeldUntil = c.hold.Action, c.hold.Reason, c.hold.Until.Unix()
		}
		v.Callers = append(v.Callers, row)
	}
	t.mu.Unlock()

	sort.Slice(v.Callers, func(i, j int) bool {
		if v.Callers[i].Requests != v.Callers[j].Requests {
			return v.Callers[i].Requests > v.Callers[j].Requests
		}
		return v.Callers[i].Cred < v.Callers[j].Cred
	})
	if len(v.Callers) > maxViewCallers {
		v.Callers = v.Callers[:maxViewCallers]
	}
	return v
}

// viewCred renders a caller key for a report: the fingerprint for a credentialed
// caller, and the "ip:<addr>" stand-in for one we could not validate — the same
// string the key was built from, so a row in the report names the caller the
// sensor counted.
func viewCred(key string) string {
	if len(key) > 5 && key[:5] == "cred:" {
		return key[5:]
	}
	return key
}
