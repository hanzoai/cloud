package edge

// Traffic is the edge's live picture of WHO is calling — the sensor the abuse
// gate reads and the /v1/gateway/traffic op reports. It lives here, beside the
// policy store, for the same reason the policy does: this is a LEAF package, so
// the middleware (package cloud) and the subsystem (apps/gateway) share ONE
// object rather than two that drift.
//
// It is a SENSOR, not a scorer. It counts and it remembers a verdict; it never
// decides. Deciding is /v1/risk's job, reached through cloud.Decide — which is
// why nothing here has a threshold, a weight or a score.
//
// WHAT IT MEASURES, per rolling window, per (org, credential):
//
//	requests   — cadence. Per CREDENTIAL, which no other limiter does:
//	             EdgeRateLimit keys on client IP (pre-auth) and ScopeRateLimit on
//	             (org, project, service). A stolen key inside a normal org
//	             ceiling is invisible to both.
//	failures   — 401/403 outcomes. Credential stuffing and a replayed stolen
//	             token both show up here before they show up anywhere else.
//	paths      — path spread. One credential touching many distinct paths is
//	             scraping; a real integration walks a handful.
//	peers      — how many DISTINCT credentials one client IP presented. This is
//	             the stuffing signature: one host, many keys.
//
// TENANCY IS THE DATA STRUCTURE, NOT A FILTER OVER IT. One org's callers, hosts
// and lane counters live in that org's OWN tables, reached only by indexing
// tenants[org]. There is no shared map with org-prefixed keys, so there is no
// query that could return another tenant's row and no sweep that could reclaim
// one — a cross-tenant read or eviction is not refused here, it is unwritable.
//
// THE BOUND IS PER TENANT, FOR THE SAME REASON. A process-wide cap over a shared
// table looks like memory hygiene and is a cross-tenant denial of service: one
// org (or one anonymous flood) fills the table, the global sweep reclaims the
// least-recently-seen keys — which belong to whoever was quietest — and the
// victim's abuse controls go quiet with no error and no alert. Here each tenant
// has its own ceiling and reclaims only its own keys, so a tenant at its limit
// can degrade exactly one tenant: itself. And it does not do so silently — the
// count is carried in the tenant's own view (TrafficView.Saturated), so "this
// org's sensor is at its ceiling" is a fact an operator can read rather than an
// inference from missing data.
//
// BOUNDED WITHIN A KEY, TOO. Each key holds a fixed-size ring of buckets and two
// 64-bit words; nothing grows with traffic. Path spread and peer spread are
// counted by hashing into one word and taking its population count — 8 bytes per
// key, saturating at 64, which is far past the point where the answer stops
// changing the decision.

import (
	"hash/maphash"
	"sort"
	"sync"
	"time"
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

	// maxCallers bounds ONE TENANT's credential table. Reached only by an org
	// presenting tens of thousands of distinct credentials inside one minute,
	// which is an attack on that org's own account — and costs that org's own
	// sensor resolution and nobody else's.
	maxCallers = 20_000
	// maxHosts bounds ONE TENANT's address table, which is what an unauthenticated
	// caller lands in. It is the bound that makes a forged address harmless: the
	// worst a flood of distinct addresses can do is fill the lane it arrived in.
	maxHosts = 20_000
	// maxTenants bounds how many orgs the sensor holds state for at once. Only a
	// VERIFIED org opens a tenant (the gate keys on the identity boundary's own
	// attestation, never on a header), so this is a bound on real tenants plus the
	// single anonymous lane — reached by an estate far larger than any we run, and
	// swept by idleness long before.
	maxTenants = 10_000

	// spreadCeiling is where a population count saturates. Both spread words are
	// 64 bits, so 64 is "many" and the exact number past it is not a fact the
	// sensor claims to know.
	spreadCeiling = 64

	// holdCap bounds how long a verdict is held. A held verdict is enforcement
	// without a fresh judgement, so it expires quickly and the scorer is asked
	// again; five minutes is the ceiling a caller may request.
	holdCap = 5 * time.Minute

	// tenantIdle is how long a tenant may go untouched before its state is
	// reclaimed whole. Longer than holdCap by a window, so a tenant is never
	// dropped while one of its callers is still under a live verdict.
	tenantIdle = holdCap + window
)

// Signal is one observation: the facts about a request that the sensor counts.
// It carries a credential FINGERPRINT, never a credential — see cloud.Fingerprint.
type Signal struct {
	Org    string // the VERIFIED tenant; "" for an anonymous caller
	Cred   string // credential fingerprint; "" when the caller presented none
	IP     string // client IP, the anonymous caller's only identity
	Path   string // request path, hashed into the spread word (never stored)
	Agency string // the lane this request was classed into
}

// Pattern is what the sensor knows about one caller right now. Every field is a
// count over the rolling window — no scores, no thresholds, no judgement.
type Pattern struct {
	// Requests is how many requests this credential made in the window.
	Requests int
	// Failures is how many of them ended 401 or 403.
	Failures int
	// Paths is the approximate number of distinct paths it touched, saturating at 64.
	Paths int
	// Peers is the approximate number of distinct credentials this client IP
	// presented in the window, saturating at 64 — the stuffing signature.
	Peers int
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

// caller is the per-credential state within ONE tenant. seen is the reclaim
// policy's liveness stamp.
type caller struct {
	req   ring
	paths spread
	hold  Hold
	seen  time.Time
}

// pinned reports whether this key must survive reclaim: a live verdict is
// enforcement, and dropping it would silently release a caller the scorer
// refused.
func (c *caller) pinned(now time.Time) bool { return now.Before(c.hold.Until) }

// host is the per-address state within ONE tenant: how many distinct credentials
// this address presented. Kept apart from caller because the stuffing question is
// asked of an ADDRESS, and an address presenting many keys has, by definition, no
// one key.
type host struct {
	creds spread
	seen  time.Time
}

func (h *host) pinned(time.Time) bool { return false }

// lane counts one tenant's traffic by agency over the current window — what the
// /v1/gateway/traffic op reports. Small and fixed: four lanes, three counters.
type lane struct {
	req      ring
	denied   ring
	screened ring
	at       int64
	byAgency map[string]int
}

// ─────────────────────────────────────────────────────────────────────────────
// The bounded table — ONE reclaim policy, written once
// ─────────────────────────────────────────────────────────────────────────────

// keeper is what a table needs to know about a value to reclaim it safely: when
// it was last touched, and whether it is pinned.
type keeper interface {
	stamp() time.Time
	pinned(now time.Time) bool
}

func (c *caller) stamp() time.Time { return c.seen }
func (h *host) stamp() time.Time   { return h.seen }

// table is a bounded map for ONE tenant. It is the only place a key is ever
// reclaimed, so the policy — idle first, then oldest, never a pinned key, never
// a key belonging to anyone else — exists once and cannot differ between the two
// tables that use it.
//
// The cap is a CONSTRUCTOR argument and the map is unexported, so there is no
// way to build an unbounded one: the wrong shape is unrepresentable rather than
// discouraged.
type table[V keeper] struct {
	m   map[string]V
	max int
	// dropped counts keys this table's own ceiling forced out. It is the tenant's
	// own number and it is reported to that tenant, so saturation is visible.
	dropped int
}

func newTable[V keeper](max int) table[V] {
	return table[V]{m: make(map[string]V), max: max}
}

func (t *table[V]) get(k string) (V, bool) {
	v, ok := t.m[k]
	return v, ok
}

// admit returns the slot for k, creating it with make when absent. When the
// table is at its ceiling it first reclaims its OWN idle keys, then its OWN
// oldest — never anything outside this tenant.
func (t *table[V]) admit(k string, now time.Time, make func() V) V {
	if v, ok := t.m[k]; ok {
		return v
	}
	if len(t.m) >= t.max {
		t.reclaim(now)
	}
	v := make()
	t.m[k] = v
	return v
}

// reclaim drops idle keys, and — only if that was not enough — the oldest
// remaining. A pinned key (a live verdict) is never dropped: releasing an
// enforced caller because the table filled would turn a memory bound into a
// security bypass.
func (t *table[V]) reclaim(now time.Time) {
	floor := now.Add(-window)
	for k, v := range t.m {
		if v.stamp().Before(floor) && !v.pinned(now) {
			delete(t.m, k)
		}
	}
	if len(t.m) < t.max {
		return
	}
	type aged struct {
		k string
		t time.Time
	}
	all := make([]aged, 0, len(t.m))
	for k, v := range t.m {
		if v.pinned(now) {
			continue
		}
		all = append(all, aged{k, v.stamp()})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].t.Before(all[j].t) })
	// Half, so the cost of reclaiming is amortized rather than paid on every
	// admission once the ceiling is reached.
	for _, a := range all[:len(all)/2] {
		delete(t.m, a.k)
		t.dropped++
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// The tenant — one org's whole sensor state
// ─────────────────────────────────────────────────────────────────────────────

// tenant is everything the sensor knows about ONE org. It is reached only by
// indexing Traffic.tenants, so every read and every reclaim is inside one
// tenant by construction.
type tenant struct {
	callers table[*caller]
	hosts   table[*host]
	lane    lane
	seen    time.Time
}

func newTenant() *tenant {
	return &tenant{
		callers: newTable[*caller](maxCallers),
		hosts:   newTable[*host](maxHosts),
		lane:    lane{byAgency: map[string]int{}},
	}
}

// Traffic is the sensor. Safe for concurrent use; every method takes the one
// lock for a handful of arithmetic operations, which is the same cost profile as
// the token buckets already on this path.
type Traffic struct {
	mu      sync.Mutex
	seed    maphash.Seed
	tenants map[string]*tenant
}

// NewTraffic returns an empty sensor. The hash seed is per-process and never
// leaves it, so a fingerprint or path bit cannot be reproduced off-box.
func NewTraffic() *Traffic {
	return &Traffic{seed: maphash.MakeSeed(), tenants: map[string]*tenant{}}
}

// callerKey is the per-credential identity WITHIN a tenant. The org is not in it
// because the org is the table the key lives in — which is what makes a
// cross-tenant collision unrepresentable rather than merely unlikely. An
// anonymous caller has no credential, so its address stands in for one.
func callerKey(cred, ip string) string {
	if cred == "" {
		return "ip:" + ip
	}
	return "cred:" + cred
}

func (t *Traffic) hash(s string) uint64 {
	var h maphash.Hash
	h.SetSeed(t.seed)
	_, _ = h.WriteString(s)
	return h.Sum64()
}

// tenantLocked returns org's state, creating it on first sight. The tenant table
// is swept by idleness before it admits a new org, so a fleet of short-lived
// tenants cannot accumulate.
func (t *Traffic) tenantLocked(org string, now time.Time) *tenant {
	if tn := t.tenants[org]; tn != nil {
		tn.seen = now
		return tn
	}
	if len(t.tenants) >= maxTenants {
		t.sweepTenantsLocked(now)
	}
	tn := newTenant()
	tn.seen = now
	t.tenants[org] = tn
	return tn
}

// lookupLocked returns org's state WITHOUT creating it — the read path, so a
// report on an org the sensor has never seen does not mint state for it.
func (t *Traffic) lookupLocked(org string) *tenant { return t.tenants[org] }

// sweepTenantsLocked reclaims whole tenants that have gone quiet for longer than
// any verdict can live. It never reclaims a tenant to make room for another
// tenant's traffic — only genuinely idle ones — so the tenant table cannot become
// a cross-tenant eviction channel either.
func (t *Traffic) sweepTenantsLocked(now time.Time) {
	floor := now.Add(-tenantIdle)
	for org, tn := range t.tenants {
		if tn.seen.Before(floor) {
			delete(t.tenants, org)
		}
	}
}

// Observe counts one request and returns what is now known about its caller.
// Called on the way IN, before the handler runs, so the gate decides on the
// pattern that includes this request.
func (t *Traffic) Observe(s Signal, now time.Time) Pattern {
	t.mu.Lock()
	defer t.mu.Unlock()
	tn := t.tenantLocked(s.Org, now)

	c := tn.callers.admit(callerKey(s.Cred, s.IP), now, func() *caller { return &caller{} })
	c.seen = now
	c.req.bump(now, false)
	if s.Path != "" {
		c.paths.add(now, t.hash(s.Path))
	}

	// The peer count is asked of the ADDRESS, and only means something when a
	// credential was presented: a stream of anonymous requests from one IP is a
	// flood (EdgeRateLimit's question), not stuffing.
	peers := 0
	if s.IP != "" && s.Cred != "" {
		h := tn.hosts.admit(s.IP, now, func() *host { return &host{} })
		h.seen = now
		h.creds.add(now, t.hash(s.Cred))
		peers = h.creds.count(now)
	}

	tn.lane.roll(now)
	tn.lane.count(s.Agency, now)

	n, bad := c.req.sum(now)
	return Pattern{Requests: n, Failures: bad, Paths: c.paths.count(now), Peers: peers}
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
	c, ok := tn.callers.get(callerKey(s.Cred, s.IP))
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
	tn.lane.roll(now)
	tn.lane.denied.bump(now, false)
}

// Screen records that the org's traffic was put to the scorer once. It is the
// BILLABLE UNIT of the risk product, counted here as well as metered, because
// the two answer different questions: the ledger answers "what is owed" and only
// once the SKU carries a price, while this answers "how much judgement did this
// org consume" from the first request. An org must be able to see its screen
// count before anyone decides what a screen costs.
func (t *Traffic) Screen(org string, now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	tn := t.tenantLocked(org, now)
	tn.lane.roll(now)
	tn.lane.screened.bump(now, false)
}

// Hold pins a verdict to a caller for d, so an attack costs one screen rather
// than one per request. d is clamped to holdCap: enforcement without a fresh
// judgement is bounded, always.
func (t *Traffic) Hold(s Signal, h Hold, d time.Duration, now time.Time) {
	if d <= 0 {
		return
	}
	if d > holdCap {
		d = holdCap
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	tn := t.tenantLocked(s.Org, now)
	c := tn.callers.admit(callerKey(s.Cred, s.IP), now, func() *caller { return &caller{} })
	c.seen = now
	h.Until = now.Add(d)
	c.hold = h
}

// Held returns the verdict in force for this caller, if one has not lapsed.
func (t *Traffic) Held(s Signal, now time.Time) (Hold, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	tn := t.lookupLocked(s.Org)
	if tn == nil {
		return Hold{}, false
	}
	c, ok := tn.callers.get(callerKey(s.Cred, s.IP))
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
	c, ok := tn.callers.get(callerKey(s.Cred, s.IP))
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
	if c, ok := tn.callers.get(callerKey(s.Cred, s.IP)); ok {
		c.hold = Hold{}
	}
}

// roll resets the per-agency split when the window turns over. Called by every
// writer before it counts, so the split and the rings always describe the same
// window.
func (l *lane) roll(now time.Time) {
	if start := now.Truncate(window).UnixNano(); l.at != start {
		l.at = start
		l.byAgency = map[string]int{}
	}
}

// count records one request in its lane. Called exactly once per request, from
// Observe, so the lane total and the caller totals can never disagree.
func (l *lane) count(agency string, now time.Time) {
	if agency == "" {
		agency = "unknown"
	}
	l.byAgency[agency]++
	l.req.bump(now, false)
}

// TrafficView is one org's live edge picture. Every number in it was counted
// under that org's own tables; there is no aggregate here that another tenant
// contributed to.
type TrafficView struct {
	// Org is the tenant this view was taken for — the validated principal's own,
	// never a value the caller supplied.
	Org string `json:"org"`
	// WindowSec is the span the counts cover, in seconds.
	WindowSec int `json:"window_sec"`
	// Mode is the abuse gate's posture for this org: "shadow" records the scorer's
	// action without enforcing it, "live" enforces it.
	Mode string `json:"mode"`
	// Requests is how many requests this org made in the window.
	Requests int `json:"requests"`
	// Denied is how many of them the gate refused.
	Denied int `json:"denied"`
	// Screens is how many of them were put to the scorer — the billable unit of
	// the risk product. Counted from the first request, whatever the SKU costs.
	Screens int `json:"screens"`
	// Saturated is how many of this org's own sensor keys its own ceiling has
	// reclaimed. Non-zero means this organization is presenting more distinct
	// credentials or addresses than the sensor keeps state for, so its own numbers
	// below are a sample rather than a census. It is reported rather than logged
	// because the alternative — a bound that degrades a tenant silently — is the
	// failure this design exists to rule out. No other tenant can move it.
	Saturated int `json:"saturated,omitempty"`
	// Lanes is the request count per agency lane — agent, human, bot, unknown.
	// This is the split that separates a customer's automation from a scraper.
	Lanes map[string]int `json:"lanes"`
	// Callers is the org's busiest credentials this window, by fingerprint. The
	// fingerprint is a per-process one-way digest: enough to recognise the same
	// caller across requests, never enough to reconstruct the credential.
	Callers []TrafficCaller `json:"callers"`
}

// TrafficCaller is one credential's line in the view.
type TrafficCaller struct {
	// Cred is the credential fingerprint — a per-process one-way digest, not a key.
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

// maxViewCallers bounds a view. A report is for reading; an org with thousands of
// live keys is served its busiest, not a page of noise.
const maxViewCallers = 50

// View returns org's live picture. It reads ONE tenant's tables and cannot reach
// another's — not because it filters, but because it never holds a reference to
// anything else.
func (t *Traffic) View(org, mode string, now time.Time) TrafficView {
	v := TrafficView{Org: org, WindowSec: int(window / time.Second), Mode: mode, Lanes: map[string]int{}}
	t.mu.Lock()
	defer t.mu.Unlock()

	tn := t.lookupLocked(org)
	if tn == nil {
		return v
	}
	v.Saturated = tn.callers.dropped + tn.hosts.dropped

	if tn.lane.at == now.Truncate(window).UnixNano() {
		v.Requests, _ = tn.lane.req.sum(now)
		v.Denied, _ = tn.lane.denied.sum(now)
		v.Screens, _ = tn.lane.screened.sum(now)
		for a, n := range tn.lane.byAgency {
			v.Lanes[a] = n
		}
	}

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
// caller, and the "ip:<addr>" stand-in for an anonymous one — the same string the
// key was built from, so a row in the report names the caller the sensor counted.
func viewCred(key string) string {
	if len(key) > 5 && key[:5] == "cred:" {
		return key[5:]
	}
	return key
}
