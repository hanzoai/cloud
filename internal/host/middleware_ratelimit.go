package host

// ScopeRateLimit — the ONE per-scope request-rate limiter (issue #70). It caps
// requests/min per (org, project, service) scope using the rate limit an org
// configures on its spend-alert rows (RateLimitRpm). It is DISTINCT from the
// per-IP pre-auth limiters (clients/kms login, clients/crm intake): those
// throttle anonymous abuse by IP BEFORE identity; this throttles an authenticated
// org's own configured ceiling per scope, keyed off the VALIDATED principal.
//
// ONE COUNT FOR THE DEPLOYMENT, ONE QUESTION PER REQUEST. Every ceiling a request
// binds — its key's own and its org's — is a one-minute window in the named Window
// "rate", charged together in one call to the deployment's count (coord.Ceiling),
// so every process of every pod charges the same count and an org held to 60 a
// minute is held to 60 however many replicas serve it. A refusal is remembered in
// this process until the window resets (rl.over), so a refused flood is answered
// here and never asks the count again.
//
// The org's whole ceiling binds every request; a tighter project or service rule
// binds besides it at its own scope, so it never leaks across projects, and no
// number of scoped rules adds up past the org's.
//
// Fail-open, twice. The limit config is asked of commerce over the internal plane
// and cached with a short TTL; if commerce is unreachable the request is NOT
// limited (the funds/spend-cap gate still applies). And when the shared count
// cannot be asked — the deployment's KV is down or slow — each process counts
// alone, admitting its share of each ceiling rounded up (coord.Ceiling), so for
// that time the ceiling holds roughly per deployment instead of exactly. A limiter
// that refused because its counter was unreachable would turn a KV outage into an
// API outage; a rate-limit outage must never take down paid traffic.

import (
	"context"
	"github.com/hanzoai/cloud/sdk"
	"math"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud/apps/gateway/edge"
	"github.com/hanzoai/cloud/client/commerce"
	"github.com/hanzoai/cloud/manifest"
	"github.com/hanzoai/cloud/openapi"
	"github.com/hanzoai/cloud/sdk/brand"
	"github.com/hanzoai/cloud/sdk/coord"
	"github.com/hanzoai/cloud/sdk/meter"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/account"
	"github.com/hanzoai/authz"
	"github.com/zap-proto/zip"
)

// rateDeferLocal carries the limiter itself to the op-invoke leg, for a request
// whose path names a door rather than a service. The two halves of the ceiling
// have to share one set of counters or they are two ceilings, and the request is
// the only thing both halves hold — so it is what carries the one instance from
// the half that gave up on the value to the half that has it.
const rateDeferLocal = "cloud.rateDefer"

// isDoor answers whether a route carries operations named in the payload rather
// than in the path. There are four and they are written down once each, so this
// reads the same constants the addresses are served from rather than a second
// copy of them: the fleet's agent address and zip's framework default, the
// op-call plane, and the graph.
//
// An address that is added without being named here does not become a bypass. It
// becomes an over-count in the worst case: the leg still fires on it, because the
// leg's own test is that the matched route is not the operation's route, so the
// operation's ceiling is applied either way and the edge may additionally spend a
// token against the door's own service — if anyone ever configures one for it.
// Erring toward charging twice rather than not at all is the only direction a
// ceiling may fail in.
func isDoor(path string) bool {
	switch path {
	case manifest.MCPPath, manifest.FrameworkMCPPath, openapi.GraphPath:
		return true
	}
	return strings.HasPrefix(path, zip.CallPath)
}

// Ceiling is the op-invoke half of the rate ceiling: the same decision the edge
// makes, on the operation, for a request that reached one through a door whose
// path named no service. It answers nothing for any other request — the edge
// stashes a deferral only where it stood down, so a request the edge already
// spent a token on cannot be charged a second one here, and a request the
// exemptions cover is never offered.
func Ceiling() zip.Authorizer {
	return func(ctx context.Context, op zip.Op, _ any) (zip.Decision, error) {
		allow := zip.Decision{Effect: zip.Allow}
		c, live := cloud.Request(ctx)
		if !live {
			return allow, nil // an in-process invoke has no door and no counter.
		}
		rl, ok := c.Fiber().Locals(rateDeferLocal).(*scopeRateLimiter)
		if !ok || cloud.Matched(c) == op.Path {
			// No offer, or the request addressed the operation directly and the
			// edge already charged the right bucket. Either way there is nothing
			// here to decide that has not been decided.
			return allow, nil
		}
		org, ok := principal.Org(c)
		if !ok {
			return allow, nil
		}
		// A ceiling refuses with a STATUS — 429, not the 403 a Deny renders — so it
		// travels the error lane, which zip renders as the status the error carries.
		return allow, rl.charge(c, rl.rules(c, org, principal.Project(c), canonicalService(op.Path)))
	}
}

// rateConfigTTL bounds how stale a cached per-org rate-limit config may be. Short
// so an org's limit change takes effect within seconds, long enough that the
// config fetch is amortized far below the request rate.
const rateConfigTTL = 5 * time.Second

// scopeRulesTimeout bounds the plane read. A rate ceiling is a POLICY overlay,
// never a gate on availability, so a slow commerce must fail open FAST rather
// than hold the request that asked.
const scopeRulesTimeout = 3 * time.Second

// defaultServiceRPM is the ceiling a SERVICE CLASS carries when nothing has
// configured one for it. It is the floor under the two config sources, not an
// override of them: a configured rule still wins by most-restrictive-wins, and
// an org that has never been configured is still bounded.
//
// It exists because "no rule" used to mean "no limit". For a priced service that
// is defensible — consumption bills, so an unbounded caller is an unbounded
// invoice, and the funds gate ends it. It is not defensible for a service that
// bills ZERO: the speech models are unpriced by design, so nothing downstream
// ever says stop, and they are served from a two-replica CPU deployment that is
// the whole estate's capacity for them. Free and unbounded is a free denial of
// service, and the endpoint that most needs a limit was the one relying on an
// operator to remember to set one.
//
// Keyed by the class canonicalService derives, so it is deliberately narrow: a
// class not named here behaves exactly as before. A platform-wide default is a
// separate decision with a blast radius this is not the change to take.
var defaultServiceRPM = map[string]int{
	// /v1/audio/* — transcription and synthesis. One request per second sustained
	// is far above interactive use and far below what one org could use to
	// monopolize the upstream, which the in-flight ceiling in hanzoai/ai bounds
	// separately. The two compose: this bounds how OFTEN one org may ask, that
	// bounds how MUCH work can be running at once.
	"audio": 60,
	// /v1/sbom/* — the bill of materials of an image. Unpriced by design, and a
	// LOOKUP THAT MISSES is not a lookup: it opens a connection to the registry,
	// reads the attached document and writes the shared table, all on the asking.
	// A console renders one image at a time, so one per second sustained is far
	// above what the surface is for and is the only thing that says stop.
	"sbom": 60,
}

// admittedOf answers whether a request continues work this caller was already
// admitted for: a push or a close on a live transcript its own credential opened.
// Installed at wire-up by the package that holds those sessions (apps/ai, see
// SetAdmitted); nil until then, and nil exempts nothing.
var admittedOf func(method, path, auth string) bool

// SetAdmitted installs the continuation check ScopeRateLimit exempts. It is given
// the request's method, its router path and its Authorization header — the same
// three the session's own handler is given, so the two cannot disagree about whose
// session it is.
//
// A live transcript is ONE request. Its open is counted, like any request, and is
// also where the session is admitted against the speech capacity; then it pushes
// a chunk every chunk_ms for as long as the speaker talks, about four a second.
// Counted as requests, the pushes spent the audio floor's sixty a minute in
// fifteen seconds and the dictation died mid-sentence. They are bounded by the
// session instead — max_bytes a push, max_seconds of audio, the idle timeout —
// and a push to a session that is gone, or that another principal opened, is not
// a continuation and is counted like anything else.
//
// Called once at startup, before the app serves, so the read in the handler
// needs no lock — the shape SetCompletionCeiling has.
func SetAdmitted(f func(method, path, auth string) bool) { admittedOf = f }

// ScopeRateLimit returns the per-scope rate-limit middleware. Every request an
// authenticated org makes is charged to the org's whole ceiling — its cap, lowered
// by its own /v1/gateway org_rpm and org-wide commerce rules — and to a tighter
// scoped one where a project or service commerce rule or a service floor
// (defaultServiceRPM) covers it (see rules).
//
// An API key's own rate (Grant.Rate) binds besides all of them, in a window of its
// own. A validated caller with no org meets the default cap by who it is. With no
// policy store (gp nil) there is no cap, so an unwired deployment blocks nothing it
// was not configured to.
func ScopeRateLimit(m *meter.Client, gp *edge.Store) zip.Handler {
	return newScopeRateLimiter(m, gp).handler
}

func newScopeRateLimiter(m *meter.Client, gp *edge.Store) *scopeRateLimiter {
	return &scopeRateLimiter{
		m:     m,
		gp:    gp,
		ttl:   rateConfigTTL,
		cache: map[string]scopeCacheEntry{},
		rate:  cloud.NewWindow("rate", 0, 0),
	}
}

type scopeCacheEntry struct {
	rules  []meter.ScopeRule
	expiry time.Time
}

type scopeRateLimiter struct {
	m   *meter.Client
	gp  *edge.Store // /v1/gateway per-org OrgRPM override (nil-safe).
	ttl time.Duration

	// rate is every ceiling's count, a fixed one-minute window per ceiling (its
	// key), shared by name across the deployment.
	rate *cloud.Window
	// over is what this process saw refused, until the window that refused resets.
	over cloud.Cache[string, barred]

	mu    sync.Mutex
	cache map[string]scopeCacheEntry
}

func (rl *scopeRateLimiter) handler(c *zip.Ctx) error {
	// Never gate the commerce billing surface: it is internal S2S plumbing, not
	// metered user traffic, and the per-IP pre-auth limiters plus commerce's own
	// gates still cover it — so exempting it loosens no user-facing ceiling.
	// (rulesFor no longer reaches commerce through this app, so this is policy
	// now and not a self-reference guard; see rulesFor.)
	// Compared against the ROUTER's path, not the raw spelling: a prefix test over
	// c.Path() answers a question about how the client typed the URL, while the
	// exemption is about which handler will run (see cloud.RoutePath). ONE
	// normalization, the same one the abuse gate and the grant list use.
	path := cloud.RoutePath(c.Path())
	// A push or close on a live transcript this caller opened was counted once, at
	// its open, by every ceiling — its key's own included. See SetAdmitted.
	if continues(c) {
		return c.Next()
	}
	// AN API KEY'S OWN CEILING, in a window of its own, BESIDES every rule below
	// rather than instead of one: a key limited to 10 a minute is held there however
	// much the org allows, and no key spends past the org's ceiling either. It is a
	// property of the credential, so it is counted before any exemption by address,
	// and a door counts it like any other request.
	var ws []ceiling
	if p, ok := principal.Minted(c); ok && p.Key != "" && p.Org != "" {
		if rpm := cloud.Grant(p.Limit).Rate(); rpm > 0 {
			ws = append(ws, ceiling{class: "key", parts: []string{p.Org, p.Key}, rpm: rpm})
		}
	}
	for _, p := range []string{"/v1/billing/", "/v1/commerce/", "/_/commerce/"} {
		if cloud.UnderPrefix(path, p) {
			return rl.next(c, ws)
		}
	}

	// Only an authenticated org is scope-rate-limited. Without a validated
	// principal there is no org to key on; anonymous abuse is handled by the
	// per-IP pre-auth limiters, and priced paths are refused by each subsystem's
	// own principal gate.
	org, ok := principal.Org(c)
	if !ok {
		// A VALIDATED caller with no org — a token whose home org is absent or
		// unrepresentable, which acts in nothing — is held by who it is, at the cap
		// an unconfigured org carries. The edge no longer counts a validated caller
		// by its address, so without this it would meet no ceiling at all.
		if who := validated(c); who != "" {
			if rpm := rl.gp.Cap(""); rpm > 0 {
				ws = append(ws, ceiling{class: "who", parts: []string{who}, rpm: rpm})
			}
		}
		return rl.next(c, ws)
	}
	// A DOOR IS NOT AN OPERATION, and this ceiling is a ceiling on services. A
	// request that names its operation in the PAYLOAD carries a path that names
	// the door, so canonicalService answers about the door: "" for the framework
	// MCP address and the call plane, and — worse, because it looks like an
	// answer — "mcp" for the host's agent address and "graphql" for the graph.
	// Every operation in the fleet reached that way lands in one bucket that no
	// service floor covers. Measured: 120 calls of a 60/min operation over MCP,
	// not one refused (ratelimit_doors_test.go).
	//
	// The operation is known one layer in, at the op-invoke seam, so the decision
	// is deferred to the leg the rule runs there. Deferring rather than answering
	// with what is known here is what keeps it ONE decision: charge on the door's
	// own name and the leg's charge on the operation is a second token for one
	// call, and an org-wide rule matched here would also mask the tighter service
	// floor only the leg can see. The exemptions above come with it — an exempt
	// prefix and an unauthenticated caller have already returned — so a deferral
	// is offered only for a request this ceiling really does cover.
	if isDoor(path) {
		c.Fiber().Locals(rateDeferLocal, rl)
		return rl.next(c, ws)
	}

	// Charged here: the op leg stands down for a request the edge already charged,
	// so one request costs one token however many halves saw it.
	ws = append(ws, rl.rules(c, org, principal.Project(c), canonicalService(path))...)
	return rl.next(c, ws)
}

// next charges ws and runs the rest of the chain, or answers the refusal.
func (rl *scopeRateLimiter) next(c *zip.Ctx, ws []ceiling) error {
	if err := rl.charge(c, ws); err != nil {
		return err
	}
	return c.Next()
}

// rules is what an org's request is charged besides its key's own: the org's WHOLE
// ceiling, always, and a scoped one when that is tighter still.
//
// The org's ceiling starts at its cap (edge.Store.Cap) — the most it may be, which
// only a SuperAdmin raises — and is lowered by the org's own org_rpm and by any
// org-wide commerce rule; a tenant can tighten it and never loosen it, because every
// free signup administers an org of its own and a ceiling its admin could raise is
// no ceiling. It is charged on every request the org makes, so services with rules
// of their own cannot add up past it.
//
// FIRST-PARTY TRAFFIC (fleet) carries no default cap: only a cap a SuperAdmin set on
// its org's own row (OwnCap) holds it. An application of a first-party org is held in
// a window of its own, never its org's, so a fleet bot and the people of the org it
// belongs to do not spend one budget.
//
// The scoped ceiling is the tightest covering project or service commerce rule and
// the service floor (defaultServiceRPM): a configured rule may tighten the floor and
// never loosen it, for the same reason. It is charged when it is tighter than the
// org's.
func (rl *scopeRateLimiter) rules(c *zip.Ctx, org, project, service string) []ceiling {
	if isUnlimitedPayer(c, org) {
		return nil
	}
	all := rl.rulesFor(org)
	first := fleet(c, org)
	limit := rl.gp.Ceiling(org)
	if first {
		// The reserved org's org_rpm is the platform row's — the default itself — so
		// neither it nor a brand org's is read as a cap here.
		limit = rl.gp.OwnCap(org)
	}
	if n := orgWide(all); n > 0 && (limit == 0 || n < limit) {
		limit = n
	}
	who := counted(c, org)
	var ws []ceiling
	if limit > 0 {
		ws = append(ws, window(c, org, who, first, limit))
	}
	if w, ok := scoped(all, who, project, service); ok && (limit == 0 || w.rpm < limit) {
		ws = append(ws, w)
	}
	return ws
}

// counted is who an org's windows are counted for: the org — except in the SIGNUP
// org (account.SignupOrg), where every self-serve person who has not been placed in
// a real org is a stranger to the next and pays from an account of their own
// (person:hanzo/<name>). One window per org there would be one budget for all of
// them, so every window is counted per person, split exactly as the wallets are
// (principal.PayerIn): the org's whole ceiling, its service floors and its scoped
// rules alike.
func counted(c *zip.Ctx, org string) string {
	if org == account.SignupOrg {
		if who := meter.PayerIn(c, org).Subject(); who != "" {
			return who
		}
	}
	return org
}

// window is the window an org's whole ceiling is counted in for this request: the
// org's, shared by its people and its own applications — except where the org is
// not one tenant. In the signup org it is the person's (counted). A first-party
// application (fleet) is counted in a window of its own, so a brand's bot and the
// brand's people never spend one budget.
func window(c *zip.Ctx, org, who string, first bool, limit int) ceiling {
	if p, minted := principal.Minted(c); first && minted && p.Kind == principal.App {
		return ceiling{class: "app", parts: []string{org, p.User}, rpm: limit}
	}
	if who != org {
		return ceiling{class: "person", parts: []string{who}, rpm: limit}
	}
	return ceiling{class: "org", parts: []string{org}, rpm: limit}
}

// fleet reports whether a request is first-party traffic the default cap does not
// hold: anyone acting in the reserved admin org — SuperAdmins and the platform's
// own machines — or an application acting as itself for an operating brand's org
// (brand.Registered), the fleet's bots. A brand org's PEOPLE are not: they carry the
// default like any org's. A tenant's applications are not either: they spend their
// org's ceiling with its people, so minting applications cannot multiply an org's
// cap.
func fleet(c *zip.Ctx, org string) bool {
	if org == authz.AdminOrg || isUnlimitedPayer(c, org) {
		return true
	}
	p, ok := principal.Minted(c)
	// VERBATIM, like every org comparison: the brand registry folds case and
	// space, and an org named "Hanzo" is not hanzo.
	return ok && p.Kind == principal.App && brand.Registered(org) && brand.For(org).ID == org
}

// isUnlimitedPayer reports whether the caller is an unlimited first-party
// operator ("hanzo/z", "admin/z") exempt from per-scope rate throttling.
func isUnlimitedPayer(c *zip.Ctx, org string) bool {
	if c == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(meter.PayerIn(c, org).Subject())) {
	case "hanzo/z", "admin/z":
		return true
	}
	if p, ok := principal.Minted(c); ok {
		switch strings.ToLower(strings.TrimSpace(p.Subject)) {
		case "hanzo/z", "admin/z":
			return true
		}
		if (strings.EqualFold(strings.TrimSpace(p.Org), "hanzo") || strings.EqualFold(strings.TrimSpace(p.Org), "admin")) &&
			strings.EqualFold(strings.TrimSpace(p.User), "z") {
			return true
		}
	}
	return false
}

// validated is who the boundary attested: its subject, or its user id. Empty for a
// request the boundary resolved to nobody, and for one no boundary saw.
func validated(c *zip.Ctx) string {
	p, ok := principal.Minted(c)
	if !ok {
		return ""
	}
	if p.Subject != "" {
		return p.Subject
	}
	return p.User
}

// orgWide is the tightest commerce rule that covers the whole org (no project,
// no service), or 0.
func orgWide(rules []meter.ScopeRule) int {
	best := 0
	for _, r := range rules {
		if r.RateLimitRpm > 0 && r.Project == "" && r.Service == "" && (best == 0 || r.RateLimitRpm < best) {
			best = r.RateLimitRpm
		}
	}
	return best
}

// scoped is the tightest ceiling narrower than the org that covers a request: a
// commerce rule naming its project or its service, or its service's floor, counted
// for who (counted). ok is false when none does. Covering: each of a rule's axes is
// the wildcard "" or equals the request's (with the default project folded onto "").
func scoped(rules []meter.ScopeRule, who, project, service string) (ceiling, bool) {
	if principal.IsDefaultProject(project) {
		project = ""
	}
	var best ceiling
	if d := defaultServiceRPM[service]; d > 0 {
		best = ceiling{class: "floor", parts: []string{who, service}, rpm: d}
	}
	for _, r := range rules {
		if r.RateLimitRpm <= 0 || (r.Project == "" && r.Service == "") {
			continue
		}
		covers := (r.Project == "" || r.Project == project) &&
			(r.Service == "" || r.Service == service)
		if covers && (best.rpm == 0 || r.RateLimitRpm < best.rpm) {
			best = ceiling{class: "rule", parts: []string{who, r.Project, r.Service}, rpm: r.RateLimitRpm}
		}
	}
	return best, best.rpm > 0
}

// ceiling is one window a request is charged to: what kind of ceiling it is, the
// scope it counts — the org first, so no window is shared across orgs and org A's
// limit can never throttle org B — and its limit a minute.
type ceiling struct {
	class string
	parts []string
	rpm   int
}

// rateBarredLocal carries a refusal by an API key's OWN ceiling from this limiter
// to the edge, which runs before identity and bars the key there for the rest of
// the window: a key past its own rate then costs no lookup and no count. Only the
// key's own ceiling, because only it binds every request the key makes — an org's
// ceiling does not bind its billing surface, and a project or service rule binds
// one scope — so barring the key for those would refuse what the limiter admits.
const rateBarredLocal = "cloud.rateBarred"

// key names the window. Each part is path-escaped and the class leads, so no scope
// can spell another's window: a service floor and a commerce rule, or an org named
// like a class, are always two windows.
func (w ceiling) key() string {
	var b strings.Builder
	b.WriteString(w.class)
	for _, p := range w.parts {
		b.WriteByte('/')
		b.WriteString(url.PathEscape(p))
	}
	return b.String()
}

// barred is a refusal remembered: when the window that refused resets, and its limit.
type barred struct {
	until time.Time
	limit int
}

// charge spends one request against every ceiling it binds, in ONE question to the
// deployment's count: admitted by all of them, or refused by the one that is full
// and charged to none, so a request a key's ceiling refuses spends nothing of its
// org's. A ceiling this process already saw refuse is answered from rl.over until
// its window resets, without asking.
//
// ONE ACCOUNTING, and that is the point of it being a method rather than a
// handler. The ceiling has two halves — the edge, for a request whose own path
// names its service, and the op-invoke leg for one that reached an operation
// through a door that does not — and two accountings would be two allowances for
// the same org. Both halves render the same sentence, so a caller cannot tell
// which one stopped them: there is only one ceiling to be stopped by.
func (rl *scopeRateLimiter) charge(c *zip.Ctx, ws []ceiling) error {
	if len(ws) == 0 {
		return nil
	}
	keys := make([]string, len(ws))
	for i, w := range ws {
		keys[i] = w.key()
		// Only while the limit the refusal was counted at still stands: raised since,
		// the window is asked again.
		if h, ok := rl.over.Get(keys[i]); ok && w.rpm <= h.limit {
			return turnAway(c, w, h)
		}
	}
	cw := make([]coord.Window, len(ws))
	for i, w := range ws {
		cw[i] = coord.Window{Key: keys[i], Limit: w.rpm, TTL: time.Minute}
	}
	used, wait, ok := rl.rate.Ceiling(cw)
	now := time.Now()
	if !ok {
		// The fullest window refused: past its limit, or, while the deployment's count
		// cannot be asked, past this pod's share of it.
		full := 0
		for i := range ws {
			if used[i]*ws[full].rpm > used[full]*ws[i].rpm {
				full = i
			}
		}
		h := barred{until: now.Add(wait[full]), limit: ws[full].rpm}
		rl.over.PutUntil(keys[full], h, h.until)
		return turnAway(c, ws[full], h)
	}
	for i, w := range ws {
		if err := pace(c, w.rpm, w.rpm-used[i], now.Add(wait[i])); err != nil {
			return err
		}
	}
	return nil
}

// turnAway answers a request ceiling w refused, until h says, and when w is an API
// key's own ceiling tells the edge so (rateBarredLocal).
func turnAway(c *zip.Ctx, w ceiling, h barred) error {
	if w.class == "key" {
		c.Fiber().Locals(rateBarredLocal, h)
	}
	return pace(c, h.limit, -1, h.until)
}

// continues reports whether c continues work its credential was already admitted
// for: a push or close on a live transcript it opened (SetAdmitted). Neither the
// ceilings nor the edge's bar on a key count one.
func continues(c *zip.Ctx) bool {
	return admittedOf != nil && admittedOf(c.Method(), cloud.RoutePath(c.Path()), c.Header("Authorization"))
}

// pace says where a caller stands against one ceiling, and refuses past it: the
// limit, what is left of it, and when it resets in unix seconds — the spelling
// apps/zen and hanzoai/ai's allowance headers already use.
//
// Several ceilings can bind one request — a key's own rate and its org's, or the
// edge's per-address count — and each used to write the three headers over the
// last, so a client paced itself by whichever happened to run LAST: a key held to
// 10 a minute was shown its org's 1,000. The headers now describe the ceiling with
// the least room left, because that is the one that refuses first and the only one
// worth pacing by; a refusal always describes the ceiling that refused.
func pace(c *zip.Ctx, limit, left int, reset time.Time) error {
	if left < 0 {
		c.SetHeader("X-RateLimit-Limit", strconv.Itoa(limit))
		return overRate(c, reset)
	}
	if shown, ok := c.Fiber().Locals(paceLocal).(int); ok && shown <= left {
		return nil // a tighter ceiling already spoke for this request
	}
	c.Fiber().Locals(paceLocal, left)
	c.SetHeader("X-RateLimit-Limit", strconv.Itoa(limit))
	c.SetHeader("X-RateLimit-Remaining", strconv.Itoa(left))
	c.SetHeader("X-RateLimit-Reset", resetAt(reset))
	return nil
}

// resetAt is a window's reset as X-RateLimit-Reset spells it: unix seconds, rounded
// up like Retry-After, so a client that waits for it never arrives early.
func resetAt(reset time.Time) string {
	return strconv.FormatInt((reset.UnixMilli()+999)/1000, 10)
}

// paceLocal holds the room left under the ceiling the X-RateLimit headers already
// describe, so a looser one charged later does not write over them.
const paceLocal = "cloud.pace"

// overRate is the one 429 every ceiling answers — this one and the edge's per-IP
// cap — and it says when to come back. A 429 with no Retry-After (RFC 9110
// §10.2.3) leaves a client to guess, and a client that guesses retries at once,
// which spends the next window's allowance on requests that were always going to
// be refused. Whole seconds, rounded up, never zero.
func overRate(c *zip.Ctx, reset time.Time) error {
	wait := int(math.Ceil(time.Until(reset).Seconds()))
	if wait < 1 {
		wait = 1
	}
	c.SetHeader("X-RateLimit-Remaining", "0")
	c.SetHeader("X-RateLimit-Reset", resetAt(reset))
	c.SetHeader("Retry-After", strconv.Itoa(wait))
	return zip.Errorf(429, "rate limit exceeded: retry after %ds", wait)
}

// rulesFor returns the org's rate-limit rules, cached with a short TTL. On a
// commerce fetch error it fails OPEN (empty rules) and caches that briefly so a
// commerce blip neither blocks traffic nor hammers commerce. It takes no context:
// the fetch is DETACHED and bounded on its own, because the entry it writes is
// shared and a client disconnect must not poison it for every later request.
//
// It ASKS the process that owns the rows, over the plane. It used to GET
// /v1/billing/alerts through the commerce transport, which dispatches by
// publishing the WHOLE shared app — so the fetch re-ran this very middleware,
// whose cache is filled only AFTER the fetch returns and is therefore still
// cold, which fetched again, to the transport's depth guard: 502. The plane
// socket carries this app's ops and no edge chain, so nothing it reaches can
// re-enter here.
func (rl *scopeRateLimiter) rulesFor(org string) []meter.ScopeRule {
	if !billingEnabled(rl.m) {
		return nil // no metering wired — only the /v1/gateway OrgRPM applies.
	}
	rl.mu.Lock()
	e, ok := rl.cache[org]
	rl.mu.Unlock()
	if ok && time.Now().Before(e.expiry) {
		return e.rules
	}

	// The org is STATED: this runs on a detached context (a client disconnect must
	// not poison the cache), so there is no request for the callee to read it from.
	ctx, cancel := context.WithTimeout(cloud.For(context.Background(), org), scopeRulesTimeout)
	defer cancel()
	var rules []meter.ScopeRule
	if out, err := commerce.FinanceScopeRules(ctx); err == nil && out != nil {
		rules = make([]meter.ScopeRule, 0, len(out.Rules))
		for _, r := range out.Rules {
			rules = append(rules, meter.ScopeRule{Project: r.Project, Service: r.Service, RateLimitRpm: r.RateLimitRpm})
		}
	} // any error, or a commerce that is not deployed here, fails OPEN.

	rl.mu.Lock()
	rl.cache[org] = scopeCacheEntry{rules: rules, expiry: time.Now().Add(rl.ttl)}
	rl.mu.Unlock()
	return rules
}
