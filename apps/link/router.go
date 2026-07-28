package link

// router.go is the EXECUTION half of the redundancy seam route.go defines. route.go
// turns a user's linked accounts into an ordered list of candidates and says, in so
// many words, that "actually dialing a provider, detecting a live 429, and advancing
// to the next candidate is the gateway's job (the deferred failover-execution
// increment)." This is that increment.
//
// ── THREAT MODEL (the cross-tenant routing boundary is the asset) ─────────────────
//
// ASSET. A tenant's sealed provider credentials and the isolation between tenants:
// org A must NEVER route a request through, meter against, or read usage for org B's
// account, and a credential must never leak into a log, response, or error.
//
// ADVERSARY. A signed-in caller of org A (or a lower-privilege member of org A)
// trying to (a) route their inference through org B's paid account, (b) read org B's
// per-account usage, (c) exfiltrate any account's token, or (d) make the gateway fall
// back to a shared platform key by starving their own accounts.
//
// ATTACK SURFACE + DEFENCE.
//   - The account SELECTOR (header / model-suffix / session pin) is the only
//     client-controlled input to routing. It carries provider+profile, NEVER org or
//     subject (Selection has no such field). So the worst a forged selector does is
//     name an account the caller lacks → "unavailable". Tenancy is bound only from the
//     validated principal the caller passes (principal.Org + c.User()).
//   - The CANDIDATE SET is Links.ListLinked(org, subject) and nothing else. The router
//     has no other source of accounts, and Resolve is only ever called with an account
//     drawn from that scoped list, so a cross-org/user account is unrepresentable, not
//     merely refused — the critical isolation test holds by construction.
//   - A CREDENTIAL is resolved per request, handed to Upstream.Call, and never
//     retained: not stored on a field, not logged (Credential redacts), not placed in
//     an error or the audit line — those carry only the account IDENTITY, org, subject.
//   - FAIL SECURE: an account whose credential can't be fetched, or is expired, is
//     skipped (cooldown + cycle past). The router NEVER falls back to a platform key or
//     to another account's credential to "make the call work". Starving your own
//     accounts yields ErrAllExhausted (→ 429), never a silent platform-key call.
//   - CYCLING stays in-org: the next candidate on a 429 comes from the SAME scoped
//     list. There is no path from one org's exhaustion to another org's account.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	luxlog "github.com/luxfi/log"
)

// Links is the read seam over the linked-account registry the router routes across.
// *Store satisfies it; a unit test supplies a fake. The router depends on THIS, not
// the concrete SQLite store, so its selection + cycling policy is proven without a
// database or KMS — and the isolation guarantee is a deterministic assertion.
type Links interface {
	ListLinked(ctx context.Context, org, subject string) ([]Link, error)
}

// Request is the provider-agnostic inference the Upstream executes. The router owns
// SELECTION and CYCLING, not prompt shaping, so Payload is opaque: it is never
// inspected, mutated, or logged here.
type Request struct {
	Model   string
	Payload any
}

// Result is a served call's outcome the meter records. A routed call's usage is
// EXACT — the upstream returns real token counts — unlike a plan-percent snapshot.
type Result struct {
	Model            string
	PromptTokens     int64
	CompletionTokens int64
	TotalTokens      int64
	Response         any
}

// Upstream dials ONE provider account with its resolved credential and returns the
// served usage, or a classified error. A quota / rate-limit / 429 outcome MUST be
// returned as an error satisfying IsQuota, so the router knows to CYCLE; any other
// error is terminal for the request (cycling cannot fix a bad request and might
// double-serve). cred is for this one call — the Upstream must not retain or log it.
type Upstream interface {
	Call(ctx context.Context, cred Credential, a Account, req Request) (Result, error)
}

// Meter records a served routed call to the per-account usage ledger. A nil Meter
// disables metering (routing still works). It is a separate seam so the router's
// policy is tested without a warehouse or a billing client.
type Meter interface {
	RecordRouted(ctx context.Context, org, subject string, a Account, kind string, res Result)
}

// Policy is the cycle order over a caller's candidate accounts.
type Policy int

const (
	// PolicyPlan is route.go's redundancy order: subscription accounts first (the
	// flat-rate pool, most-headroom first), then api-key accounts (the metered
	// backstop). The sensible default; it reuses the ONE ordering Plan already owns.
	PolicyPlan Policy = iota
	// PolicyMostRemaining orders purely by remaining rate-limit headroom, highest
	// first — "route to whichever account has the most quota left", regardless of kind.
	PolicyMostRemaining
	// PolicyRoundRobin spreads load evenly across a provider's accounts by rotating
	// the Plan order per (org, subject) on each request.
	PolicyRoundRobin
)

// Sentinels the router returns. None carries a credential.
var (
	// ErrNoPrincipal — the caller passed a blank org/subject. Fail-closed: an
	// unvalidated request routes nothing.
	ErrNoPrincipal = errors.New("link route: no validated principal")
	// ErrNoLinkedAccount — the caller has no linked account to route through.
	ErrNoLinkedAccount = errors.New("link route: no linked account to route through")
	// ErrAccountUnavailable — the specifically-pinned account is not linked, or its
	// credential could not be resolved. Distinct from "you have none".
	ErrAccountUnavailable = errors.New("link route: the selected account is unavailable")
	// ErrAllExhausted — every candidate was tried and each was rate-limited or
	// unavailable. Maps to HTTP 429.
	ErrAllExhausted = errors.New("link route: all linked accounts are rate-limited or unavailable")
)

// QuotaError marks an upstream outcome as a quota / rate-limit / 429 the router
// should CYCLE on. Upstream adapters wrap their 429s in it (or return HTTP 429,
// which IsQuota also recognises). It carries the provider + status for the audit
// trail, never any credential.
type QuotaError struct {
	Provider string
	Status   int
	Err      error
}

func (e *QuotaError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("quota exhausted for %s (status %d): %v", e.Provider, e.Status, e.Err)
	}
	return fmt.Sprintf("quota exhausted for %s (status %d)", e.Provider, e.Status)
}

func (e *QuotaError) Unwrap() error { return e.Err }

// quotaMarkers are the substrings that, in an upstream error string, denote a quota
// / rate-limit condition the router should cycle on. Lowercased match. Kept explicit
// (not a regex) so the classification is auditable and can't silently widen.
var quotaMarkers = []string{
	"rate limit", "rate_limit", "ratelimit", "too many requests",
	"quota", "insufficient_quota", "resource_exhausted", "resource exhausted",
	"overloaded", "capacity", "status 429", "http 429", "429",
}

// IsQuota reports whether err is a quota / rate-limit / 429 outcome — the ONE
// predicate the cycle decision uses. It recognises a *QuotaError, an error with a
// StatusCode()/Status() of 429, and, as a fallback for opaque provider SDK errors,
// a message naming a quota condition. Everything else is a terminal error the router
// returns without cycling (a non-quota failure is not fixed by another account).
func IsQuota(err error) bool {
	if err == nil {
		return false
	}
	var qe *QuotaError
	if errors.As(err, &qe) {
		return true
	}
	// An error that reports an HTTP status of 429 (many provider clients do).
	type statusCoder interface{ StatusCode() int }
	type statuser interface{ Status() int }
	var sc statusCoder
	if errors.As(err, &sc) && sc.StatusCode() == http.StatusTooManyRequests {
		return true
	}
	var st statuser
	if errors.As(err, &st) && st.Status() == http.StatusTooManyRequests {
		return true
	}
	msg := strings.ToLower(err.Error())
	for _, m := range quotaMarkers {
		if strings.Contains(msg, m) {
			return true
		}
	}
	return false
}

// Config constructs a Router. Links, Resolver, and Upstream are required; Meter and
// Logger are optional; Now defaults to time.Now; Cooldown defaults to 60s.
type Config struct {
	Links    Links
	Resolver Resolver
	Upstream Upstream
	Meter    Meter
	Logger   luxlog.Logger
	Policy   Policy
	Cooldown time.Duration
	Now      func() time.Time
}

// Router runs the failover: it selects a caller's OWN linked accounts, orders them
// by policy, and tries each — resolving its credential and dialing the upstream —
// cycling on a live quota error until one serves or all are spent.
type Router struct {
	links  Links
	res    Resolver
	up     Upstream
	meter  Meter
	log    luxlog.Logger
	policy Policy
	now    func() time.Time
	health *health
	rr     *roundRobin
}

// NewRouter builds a Router from cfg. It returns an error only for a missing
// required seam, so a misconfiguration fails at wiring time, never at request time.
func NewRouter(cfg Config) (*Router, error) {
	if cfg.Links == nil {
		return nil, fmt.Errorf("link: NewRouter requires Links")
	}
	if cfg.Resolver == nil {
		return nil, fmt.Errorf("link: NewRouter requires a Resolver")
	}
	if cfg.Upstream == nil {
		return nil, fmt.Errorf("link: NewRouter requires an Upstream")
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	cooldown := cfg.Cooldown
	if cooldown <= 0 {
		cooldown = 60 * time.Second
	}
	return &Router{
		links:  cfg.Links,
		res:    cfg.Resolver,
		up:     cfg.Upstream,
		meter:  cfg.Meter,
		log:    cfg.Logger,
		policy: cfg.Policy,
		now:    now,
		health: newHealth(cooldown),
		rr:     newRoundRobin(),
	}, nil
}

// Route runs the failover for one request. org and subject are the VALIDATED
// principal's (the caller passes principal.Org(c) + c.User()) — never a request
// field. sel is the parsed, non-secret account selection. It returns the served
// Result and the Account it was served through, or a sentinel error.
func (r *Router) Route(ctx context.Context, org, subject string, sel Selection, req Request) (Result, Account, error) {
	org, subject = trim(org), trim(subject)
	if org == "" || subject == "" {
		return Result{}, Account{}, ErrNoPrincipal
	}

	links, err := r.links.ListLinked(ctx, org, subject)
	if err != nil {
		return Result{}, Account{}, fmt.Errorf("link route: list accounts: %w", err)
	}

	now := r.now()
	cands := r.candidates(org, subject, links, sel, now)
	if len(cands) == 0 {
		if sel.Pinned {
			return Result{}, Account{}, ErrAccountUnavailable
		}
		return Result{}, Account{}, ErrNoLinkedAccount
	}

	tried := 0
	for _, c := range cands {
		a := Account{Provider: c.Provider, Profile: c.Account}

		cred, err := r.res.Resolve(ctx, org, subject, a)
		if err != nil {
			// FAIL SECURE: the credential can't be fetched. The account is unavailable
			// — cooldown + cycle past. NEVER substitute a platform key or another
			// account's credential. The error is about the ref, not the value.
			r.health.trip(org, subject, a, now)
			r.logf("link route: credential unavailable, skipping account", org, a, err)
			continue
		}
		if cred.expired(now) {
			// Expired and (by contract) unrefreshable at read time — the connector
			// re-seals a fresh token before expiry; if it hasn't, this account is out.
			r.health.trip(org, subject, a, now)
			r.logf("link route: credential expired, skipping account", org, a, nil)
			continue
		}

		tried++
		res, cerr := r.up.Call(ctx, cred, a, req)
		if cerr != nil {
			if IsQuota(cerr) {
				// A LIVE quota/429: cool this account down and CYCLE to the next
				// candidate — which is still one of THIS caller's own accounts.
				r.health.trip(org, subject, a, now)
				r.logf("link route: quota reached, cycling", org, a, cerr)
				continue
			}
			// A non-quota upstream error is terminal: another account would not fix a
			// bad request and could double-serve. Return it (no credential in it).
			return Result{}, a, cerr
		}

		if r.meter != nil {
			r.meter.RecordRouted(ctx, org, subject, a, c.Kind, res)
		}
		if r.log != nil {
			r.log.Info("link route served", "org", org, "account", a.String(), "model", res.Model)
		}
		return res, a, nil
	}

	// Every candidate was rate-limited or unavailable.
	if tried == 0 && sel.Pinned {
		return Result{}, Account{}, ErrAccountUnavailable
	}
	return Result{}, Account{}, ErrAllExhausted
}

// candidates selects, filters, and orders the accounts to try. The set starts from
// the caller's OWN linked accounts (the only source), keeps those matching the
// selection and not currently in cooldown, and orders them by policy. It is the ONE
// place the candidate set is derived, so the in-org invariant is enforced once.
func (r *Router) candidates(org, subject string, links []Link, sel Selection, now time.Time) []RouteCandidate {
	filtered := make([]Link, 0, len(links))
	for _, l := range links {
		if l.Status != StatusLinked {
			continue
		}
		if !sel.matches(l) {
			continue
		}
		if r.health.down(org, subject, Account{Provider: l.Provider, Profile: l.Account}, now) {
			continue // cooled down after a recent live 429 in this process
		}
		filtered = append(filtered, l)
	}
	if len(filtered) == 0 {
		return nil
	}
	return r.order(org, subject, filtered, now)
}

// order applies the configured cycle policy over the already-filtered links.
func (r *Router) order(org, subject string, filtered []Link, now time.Time) []RouteCandidate {
	switch r.policy {
	case PolicyMostRemaining:
		cs := make([]RouteCandidate, 0, len(filtered))
		for _, l := range filtered {
			cs = append(cs, candidateOf(l))
		}
		sort.SliceStable(cs, func(i, j int) bool { return cs[i].HeadroomPct > cs[j].HeadroomPct })
		return cs
	case PolicyRoundRobin:
		cs := Plan(filtered, now).Candidates
		return rotate(cs, r.rr.next(org+"\x00"+subject))
	default: // PolicyPlan
		return Plan(filtered, now).Candidates
	}
}

// logf logs a route event WITHOUT the credential — only the account identity, org,
// and (when present) the upstream error, which by construction names a ref/status,
// never a token. Central so no call site can accidentally log a Credential.
func (r *Router) logf(msg, org string, a Account, err error) {
	if r.log == nil {
		return
	}
	if err != nil {
		r.log.Warn(msg, "org", org, "account", a.String(), "err", err.Error())
		return
	}
	r.log.Warn(msg, "org", org, "account", a.String())
}

// rotate returns cs rotated left by n (mod len) — the round-robin start offset.
func rotate(cs []RouteCandidate, n int) []RouteCandidate {
	if len(cs) < 2 {
		return cs
	}
	n %= len(cs)
	if n == 0 {
		return cs
	}
	return append(append(make([]RouteCandidate, 0, len(cs)), cs[n:]...), cs[:n]...)
}

// ── in-memory account health (cooldown after a live 429) ──────────────────────────

// health tracks, per (org, subject, account), a cooldown window after a live quota
// error so the router does not immediately re-hammer an account it just saw 429. It
// is process-local and best-effort: it never persists, and an absent entry always
// means "available". Bounded by opportunistic pruning of expired entries.
type health struct {
	mu       sync.Mutex
	cooldown time.Duration
	until    map[string]time.Time
}

func newHealth(cooldown time.Duration) *health {
	return &health{cooldown: cooldown, until: make(map[string]time.Time)}
}

func healthKey(org, subject string, a Account) string {
	return org + "\x00" + subject + "\x00" + a.String()
}

// trip cools an account down for the configured window from now.
func (h *health) trip(org, subject string, a Account, now time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.until[healthKey(org, subject, a)] = now.Add(h.cooldown)
	h.pruneLocked(now)
}

// down reports whether an account is currently cooled down.
func (h *health) down(org, subject string, a Account, now time.Time) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	until, ok := h.until[healthKey(org, subject, a)]
	if !ok {
		return false
	}
	if !now.Before(until) {
		delete(h.until, healthKey(org, subject, a))
		return false
	}
	return true
}

// pruneLocked drops expired entries so the map cannot grow unbounded. Cheap: the map
// only ever holds accounts seen 429 within one cooldown window.
func (h *health) pruneLocked(now time.Time) {
	for k, until := range h.until {
		if !now.Before(until) {
			delete(h.until, k)
		}
	}
}

// ── round-robin offset ────────────────────────────────────────────────────────────

// roundRobin hands out a monotonically increasing start offset per key, so
// PolicyRoundRobin spreads consecutive requests across a caller's accounts.
type roundRobin struct {
	mu sync.Mutex
	n  map[string]int
}

func newRoundRobin() *roundRobin { return &roundRobin{n: make(map[string]int)} }

func (r *roundRobin) next(key string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	v := r.n[key]
	r.n[key] = v + 1
	return v
}
