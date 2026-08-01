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
// BOUNDED BY CONSTRUCTION. Each key holds a fixed-size ring of buckets and two
// 64-bit words; nothing grows with traffic. Path spread and peer spread are
// counted by hashing into one word and taking its population count — 8 bytes per
// key, saturating at 64, which is far past the point where the answer stops
// changing the decision. The key table is capped and swept, so an attacker
// minting keys cannot grow the sensor's memory without bound.
//
// TENANT SCOPING IS STRUCTURAL. Every key leads with org (mirroring
// scopeBucketKey in middleware_ratelimit.go), View filters on org, and there is
// no exported method that reads across orgs. Org A's traffic cannot appear in,
// or move, org B's numbers.

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

	// maxKeys bounds the sensor's table. Reached only by an adversary presenting
	// tens of thousands of distinct credentials; the sweep below reclaims idle
	// keys first, so the cap is the last line, not the first.
	maxKeys = 50_000

	// spreadCeiling is where a population count saturates. Both spread words are
	// 64 bits, so 64 is "many" and the exact number past it is not a fact the
	// sensor claims to know.
	spreadCeiling = 64

	// holdCap bounds how long a verdict is held. A held verdict is enforcement
	// without a fresh judgement, so it expires quickly and the scorer is asked
	// again; five minutes is the ceiling a caller may request.
	holdCap = 5 * time.Minute
)

// Signal is one observation: the facts about a request that the sensor counts.
// It carries a credential FINGERPRINT, never a credential — see cloud.Fingerprint.
type Signal struct {
	Org    string // the validated tenant; "" for an anonymous caller
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

// caller is the per-(org, credential) state. seen is the sweep's liveness stamp.
type caller struct {
	req   ring
	paths spread
	hold  Hold
	seen  time.Time
}

// host is the per-(org, IP) state: how many distinct credentials this address
// presented. Kept apart from caller because the stuffing question is asked of an
// ADDRESS, and an address presenting many keys has, by definition, no one key.
type host struct {
	creds spread
	seen  time.Time
}

// lane counts one org's traffic by agency over the current window — what the
// /v1/gateway/traffic op reports. Small and fixed: four lanes, three counters.
type lane struct {
	req      ring
	denied   ring
	screened ring
	at       int64
	byAgency map[string]int
}

// Traffic is the sensor. Safe for concurrent use; every method takes the one
// lock for a handful of arithmetic operations, which is the same cost profile as
// the token buckets already on this path.
type Traffic struct {
	mu    sync.Mutex
	seed  maphash.Seed
	kv    map[string]*caller
	hosts map[string]*host
	lanes map[string]*lane
}

// NewTraffic returns an empty sensor. The hash seed is per-process and never
// leaves it, so a fingerprint or path bit cannot be reproduced off-box.
func NewTraffic() *Traffic {
	return &Traffic{
		seed:  maphash.MakeSeed(),
		kv:    map[string]*caller{},
		hosts: map[string]*host{},
		lanes: map[string]*lane{},
	}
}

// callerKey is the per-credential identity. Org LEADS — the hard tenant boundary,
// so one org's counters can never be reached by another's traffic. An anonymous
// caller has no credential, so its address stands in for one: it is still
// counted, and still only within its own (empty) tenant.
func callerKey(org, cred, ip string) string {
	if cred == "" {
		return org + "|ip:" + ip
	}
	return org + "|" + cred
}

func hostKey(org, ip string) string { return org + "|" + ip }

func (t *Traffic) hash(s string) uint64 {
	var h maphash.Hash
	h.SetSeed(t.seed)
	_, _ = h.WriteString(s)
	return h.Sum64()
}

// Observe counts one request and returns what is now known about its caller.
// Called on the way IN, before the handler runs, so the gate decides on the
// pattern that includes this request.
func (t *Traffic) Observe(s Signal, now time.Time) Pattern {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.sweepLocked(now)

	ck := callerKey(s.Org, s.Cred, s.IP)
	c := t.kv[ck]
	if c == nil {
		c = &caller{}
		t.kv[ck] = c
	}
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
		hk := hostKey(s.Org, s.IP)
		h := t.hosts[hk]
		if h == nil {
			h = &host{}
			t.hosts[hk] = h
		}
		h.seen = now
		h.creds.add(now, t.hash(s.Cred))
		peers = h.creds.count(now)
	}

	t.laneLocked(s.Org, now).count(s.Agency, now)

	n, bad := c.req.sum(now)
	return Pattern{Requests: n, Failures: bad, Paths: c.paths.count(now), Peers: peers}
}

// Fail records that a request ended 401 or 403. Called on the way OUT, because
// the outcome is not known on the way in — and it is the outcome, not the
// attempt, that separates a client with a stale token from one guessing tokens.
func (t *Traffic) Fail(s Signal, now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	c := t.kv[callerKey(s.Org, s.Cred, s.IP)]
	if c == nil {
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
	t.laneLocked(org, now).denied.bump(now, false)
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
	t.laneLocked(org, now).screened.bump(now, false)
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
	ck := callerKey(s.Org, s.Cred, s.IP)
	c := t.kv[ck]
	if c == nil {
		c = &caller{}
		t.kv[ck] = c
	}
	c.seen = now
	h.Until = now.Add(d)
	c.hold = h
}

// Held returns the verdict in force for this caller, if one has not lapsed.
func (t *Traffic) Held(s Signal, now time.Time) (Hold, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	c := t.kv[callerKey(s.Org, s.Cred, s.IP)]
	if c == nil || c.hold.Action == "" || !now.Before(c.hold.Until) {
		return Hold{}, false
	}
	return c.hold, true
}

// Release drops any held verdict for this caller — the operator's undo.
func (t *Traffic) Release(s Signal) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if c := t.kv[callerKey(s.Org, s.Cred, s.IP)]; c != nil {
		c.hold = Hold{}
	}
}

func (t *Traffic) laneLocked(org string, now time.Time) *lane {
	l := t.lanes[org]
	if l == nil {
		l = &lane{byAgency: map[string]int{}}
		t.lanes[org] = l
	}
	if start := now.Truncate(window).UnixNano(); l.at != start {
		l.at = start
		l.byAgency = map[string]int{}
	}
	return l
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

// sweepLocked reclaims keys idle for longer than a window, and — only if the cap
// is still exceeded — the least recently seen. Called on the write path so the
// table cannot grow between requests, with no timer to leak.
func (t *Traffic) sweepLocked(now time.Time) {
	if len(t.kv) < maxKeys && len(t.hosts) < maxKeys {
		return
	}
	floor := now.Add(-window)
	for k, c := range t.kv {
		if c.seen.Before(floor) && !now.Before(c.hold.Until) {
			delete(t.kv, k)
		}
	}
	for k, h := range t.hosts {
		if h.seen.Before(floor) {
			delete(t.hosts, k)
		}
	}
	// Still over: drop the oldest half rather than refuse to record. A sensor that
	// stops sensing under load is worse than one that forgets its quietest keys.
	if len(t.kv) >= maxKeys {
		type aged struct {
			k string
			t time.Time
		}
		all := make([]aged, 0, len(t.kv))
		for k, c := range t.kv {
			all = append(all, aged{k, c.seen})
		}
		sort.Slice(all, func(i, j int) bool { return all[i].t.Before(all[j].t) })
		for _, a := range all[:len(all)/2] {
			delete(t.kv, a.k)
		}
	}
}

// TrafficView is one org's live edge picture. Every number in it was counted
// under that org's key; there is no aggregate here that another tenant
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

// View returns org's live picture. The org prefix is the ONLY way into the table,
// so a view can contain no other tenant's rows — that is the property, not a
// filter applied afterwards.
func (t *Traffic) View(org, mode string, now time.Time) TrafficView {
	v := TrafficView{Org: org, WindowSec: int(window / time.Second), Mode: mode, Lanes: map[string]int{}}
	t.mu.Lock()
	defer t.mu.Unlock()

	if l := t.lanes[org]; l != nil && l.at == now.Truncate(window).UnixNano() {
		v.Requests, _ = l.req.sum(now)
		v.Denied, _ = l.denied.sum(now)
		v.Screens, _ = l.screened.sum(now)
		for a, n := range l.byAgency {
			v.Lanes[a] = n
		}
	}

	prefix := org + "|"
	for k, c := range t.kv {
		if len(k) <= len(prefix) || k[:len(prefix)] != prefix {
			continue
		}
		n, bad := c.req.sum(now)
		if n == 0 && bad == 0 && c.hold.Action == "" {
			continue
		}
		row := TrafficCaller{Cred: k[len(prefix):], Requests: n, Failures: bad, Paths: c.paths.count(now)}
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
