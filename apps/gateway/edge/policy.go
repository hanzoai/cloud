package edge

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud/sdk/dialect"
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/namespace"
	_ "github.com/hanzoai/sqlite" // the ONE "sqlite" driver.
)

// Policy is the edge policy for one scope. Zero-valued fields mean "inherit"
// (from the static default, then the platform policy) — so a PUT that sets only
// OrgRPM leaves the platform CORS/per-IP untouched. Every field is ENFORCED by a
// consumer; there is no stored-but-ignored knob.
// Every field carries its OWN doc comment rather than sharing a section header,
// because zipdoc lifts a field's comment into the published schema property and a
// header lifted onto three fields would document the GROUP where the FIELD goes.
type Policy struct {
	// CORSOrigins is the PLATFORM-scope CORS allowlist EdgeCORS admits: an exact
	// origin, a bare host, or a "*.host" wildcard. Writable only by a SuperAdmin —
	// CORS is evaluated before identity, so it has no tenant to scope to.
	CORSOrigins []string `json:"cors_origins,omitempty"`
	// PerIPRPM is the PLATFORM-scope flood cap: requests admitted per WindowSec from
	// one client IP that presents no credential, or a credential that resolves to
	// nobody. A request presenting a credential is held to ten times this from its
	// address until the credential is checked; a validated caller is then held by
	// its key and org ceilings instead. SuperAdmin-only, same reason.
	PerIPRPM int `json:"per_ip_rpm,omitempty"`
	// WindowSec is the window PerIPRPM is counted over, in seconds. SuperAdmin-only.
	WindowSec int `json:"window_sec,omitempty"`

	// OrgRPM is the org's OWN authenticated rate ceiling, requests per minute, as
	// ScopeRateLimit enforces it across the deployment. An organization may lower it
	// but never raise it past its cap (org_cap); unset, the org is held to its cap.
	// On the platform row it is every organization's default cap, and it does not
	// inherit into any organization's own row. Read back, it is the ceiling in
	// force: the org's own, clipped to its cap.
	OrgRPM int `json:"org_rpm,omitempty"`
	// OrgCap is the most this organization's ceiling may be, requests per minute.
	// Unset, it is the platform's org_rpm, else 60,000. Only a SuperAdmin sets it: it
	// is how the platform raises one organization above the default, and the
	// organization a limit governs does not get to remove it. It does not inherit
	// from the platform row. Read back, it is the cap in force.
	OrgCap int `json:"org_cap,omitempty"`
	// CacheTTLSec is the org's default edge-cache TTL for its responses, in seconds;
	// 0 means no caching. Unset inherits the platform default.
	CacheTTLSec int `json:"cache_ttl_sec,omitempty"`
	// CachePaths overrides CacheTTLSec per path PREFIX (key "/v1/models" → seconds).
	// The longest matching prefix wins.
	CachePaths map[string]int `json:"cache_paths,omitempty"`
	// Methods is the allowlist of HTTP methods the edge accepts for this org. Empty
	// means all are accepted.
	Methods []string `json:"methods,omitempty"`
	// Mode is the abuse gate's posture for THIS scope: "shadow" scores traffic and
	// records the verdict without acting on it, "live" enforces it. Unset means
	// shadow.
	//
	// It is the one per-org field that does NOT inherit. Every other field here
	// layers a platform default under the org's own value, which is right for a
	// default: a tenant that sets no rate ceiling should get the platform's. Mode
	// is not a default, it is an ARMING DECISION — it is what makes a statistical
	// judgement start refusing real traffic — and inheriting it means arming one
	// scope arms every tenant that never asked for it, without a write to their
	// row and without anything in their config changing. So a tenant is live only
	// if that tenant's OWN row says live, and the platform row's mode governs
	// exactly one scope: the anonymous lane, which has no tenant of its own.
	//
	// It is also not self-service. Writing it requires SuperAdmin (see the
	// /v1/gateway config op): the subject of an abuse control does not get to
	// switch the control off.
	Mode string `json:"mode,omitempty"`

	// UpdatedAt is the unix second this policy row was last written. Server-stamped;
	// a client-supplied value is ignored.
	UpdatedAt int64 `json:"updated_at,omitempty"`
	// UpdatedBy is the validated user id that wrote this policy row. Server-stamped;
	// a client-supplied value is ignored.
	UpdatedBy string `json:"updated_by,omitempty"`
}

// MaxCacheTTLSec bounds a cache TTL (7 days) so a fat-fingered PUT can't pin a
// stale edge response indefinitely.
const MaxCacheTTLSec = 7 * 24 * 60 * 60

// MaxPerIPRPM bounds the per-IP cap a write may set. The edge multiplies it — the
// backstop for a request presenting a credential is ten times it — so a value near
// the top of an int would overflow into a negative limit that refuses everything. A
// million a window is far past any address a flood cap is for.
const MaxPerIPRPM = 1_000_000

// MaxOrgRPM bounds an org's ceiling and cap: a billion requests a minute is past any
// organization, and nothing larger is a limit.
const MaxOrgRPM = 1_000_000_000

// PresentedScale is how far past the per-IP limit a request presenting a credential
// may go from one /64 before identity has looked at the credential.
const PresentedScale = 10

// DefaultOrgRPM is the cap, requests a minute, of an organization nobody set one for
// and on a platform whose org_rpm nobody wrote: about a thousand requests a second
// for the whole org across the deployment. A SuperAdmin moves it for everyone by
// writing the platform's org_rpm, and for one org with org_cap.
const DefaultOrgRPM = 60_000

// canonicalMethods is the set of HTTP methods an org may allow at the edge.
var canonicalMethods = map[string]bool{
	"GET": true, "HEAD": true, "POST": true, "PUT": true,
	"PATCH": true, "DELETE": true, "OPTIONS": true,
}

// Normalize canonicalizes free-form input in place (methods → upper-case, trimmed)
// so a config round-trips in one stable shape. Applied at the write boundary,
// before Validate.
func (p *Policy) Normalize() {
	for i, m := range p.Methods {
		p.Methods[i] = strings.ToUpper(strings.TrimSpace(m))
	}
}

// Validate checks structural bounds on the client-settable fields, returning a
// human-readable error for a 400. It is the ONE validation gate shared by every
// writer, so the store never persists an incoherent policy.
func (p Policy) Validate() error {
	if p.PerIPRPM < 0 || p.WindowSec < 0 || p.OrgRPM < 0 {
		return fmt.Errorf("rate fields must be non-negative")
	}
	if p.PerIPRPM > MaxPerIPRPM {
		return fmt.Errorf("per_ip_rpm must be at most %d", MaxPerIPRPM)
	}
	if p.OrgCap < 0 {
		return fmt.Errorf("org_cap must be non-negative")
	}
	if p.OrgRPM > MaxOrgRPM || p.OrgCap > MaxOrgRPM {
		return fmt.Errorf("org_rpm and org_cap must be at most %d", MaxOrgRPM)
	}
	if p.CacheTTLSec < 0 || p.CacheTTLSec > MaxCacheTTLSec {
		return fmt.Errorf("cache_ttl_sec must be in [0, %d]", MaxCacheTTLSec)
	}
	for path, ttl := range p.CachePaths {
		if path == "" || path[0] != '/' {
			return fmt.Errorf("cache_paths key %q must be a path starting with /", path)
		}
		if ttl < 0 || ttl > MaxCacheTTLSec {
			return fmt.Errorf("cache_paths[%q] must be in [0, %d]", path, MaxCacheTTLSec)
		}
	}
	for _, m := range p.Methods {
		if !canonicalMethods[m] {
			return fmt.Errorf("methods: unknown HTTP method %q", m)
		}
	}
	for _, o := range p.CORSOrigins {
		if strings.TrimSpace(o) == "" {
			return fmt.Errorf("cors_origins must not contain empty entries")
		}
	}
	if p.Mode != "" && p.Mode != ModeShadow && p.Mode != ModeLive {
		return fmt.Errorf("mode must be %q or %q", ModeShadow, ModeLive)
	}
	return nil
}

// The abuse gate's two postures. They are values, not booleans, because "off"
// and "watching" are different states and a deployment must be able to tell
// which one it is in.
const (
	// ModeShadow scores and records; it never refuses. The DEFAULT.
	ModeShadow = "shadow"
	// ModeLive enforces the scorer's action.
	ModeLive = "live"
)

// merge overlays the non-zero fields of over onto base and returns the result.
// A nil/empty slice or zero int in over means "keep base"; this is what makes a
// partial PUT (e.g. only OrgRPM) additive rather than a full replace.
func merge(base, over Policy) Policy {
	out := base
	if len(over.CORSOrigins) > 0 {
		out.CORSOrigins = over.CORSOrigins
	}
	if over.PerIPRPM > 0 {
		out.PerIPRPM = over.PerIPRPM
	}
	if over.WindowSec > 0 {
		out.WindowSec = over.WindowSec
	}
	if over.OrgRPM > 0 {
		out.OrgRPM = over.OrgRPM
	}
	if over.OrgCap > 0 {
		out.OrgCap = over.OrgCap
	}
	if over.CacheTTLSec > 0 {
		out.CacheTTLSec = over.CacheTTLSec
	}
	if len(over.CachePaths) > 0 {
		out.CachePaths = over.CachePaths
	}
	if len(over.Methods) > 0 {
		out.Methods = over.Methods
	}
	if over.Mode != "" {
		out.Mode = over.Mode
	}
	if over.UpdatedAt > 0 {
		out.UpdatedAt = over.UpdatedAt
		out.UpdatedBy = over.UpdatedBy
	}
	return out
}

// resolveTTL bounds how stale a cached resolver value may be — short so an
// operator's PUT takes effect within seconds, long enough to amortize the read
// far below the request rate (mirrors ScopeRateLimit's rateConfigTTL).
const resolveTTL = 5 * time.Second

// Store persists Policy per org to one encrypted SQLite file and resolves the
// effective platform / per-org policy for the edge middleware, cached with a
// short TTL. A nil db (SQLite unavailable at boot) degrades to STATIC-ONLY: reads
// return the static default, writes error — the edge never goes down.
type Store struct {
	db       *sql.DB
	adminOrg string
	static   Policy

	mu    sync.Mutex
	cache map[string]cacheEntry // key "" = platform; key org = that org's effective row.
}

type cacheEntry struct {
	pol    Policy
	expiry time.Time
}

// New opens (or creates) the deployment's gateway policy database under dataDir and
// returns a Store layered over the static boot defaults. On any open/migrate error it
// logs nothing here (the caller owns logging) and returns a static-only Store plus the
// error, so the caller can wire the edge middleware with a working fallback regardless.
func New(dataDir, adminOrg string, static Policy) (*Store, error) {
	s := &Store{adminOrg: adminOrg, static: static, cache: map[string]cacheEntry{}}
	if dataDir == "" {
		return s, fmt.Errorf("edge: empty dataDir; running static-only")
	}
	// The policy table is the DEPLOYMENT's, keyed by org rather than split per org,
	// so it lives in the system namespace.
	db, err := locus.Use(context.Background(), nil, namespace.System(), "gateway", dataDir, func(db *sql.DB) (*sql.DB, error) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if err := dialect.Of(db).Migrate(ctx, db, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS policy (
		org        TEXT PRIMARY KEY,
		doc        TEXT NOT NULL DEFAULT '{}',
		updated_at BIGINT NOT NULL DEFAULT 0
	)`)
			return err
		}); err != nil {
			return nil, fmt.Errorf("edge: migrate: %w", err)
		}
		return db, nil
	})
	if err != nil {
		return s, err
	}
	s.db = db
	return s, nil
}

// Close releases the underlying handle (nil-safe for a static-only Store).
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// Get returns the raw stored policy for org (found=false when none). A static-only
// store always reports not-found.
func (s *Store) Get(ctx context.Context, org string) (Policy, bool, error) {
	if s == nil || s.db == nil {
		return Policy{}, false, nil
	}
	return get(ctx, s.db, org)
}

// get reads org's stored policy on a handle or a transaction.
func get(ctx context.Context, q dialect.Querier, org string) (Policy, bool, error) {
	var doc string
	err := q.QueryRowContext(ctx, `SELECT doc FROM policy WHERE org=$1`, org).Scan(&doc)
	if err == sql.ErrNoRows {
		return Policy{}, false, nil
	}
	if err != nil {
		return Policy{}, false, fmt.Errorf("edge: get %q: %w", org, err)
	}
	var p Policy
	if err := json.Unmarshal([]byte(doc), &p); err != nil {
		return Policy{}, false, fmt.Errorf("edge: decode %q: %w", org, err)
	}
	return p, true, nil
}

// Put upserts the policy for org (merged over any existing row so a partial write
// is additive) and invalidates the resolver cache. Errors on a static-only store —
// a write must never silently vanish.
func (s *Store) Put(ctx context.Context, org string, p Policy) (Policy, error) {
	if s == nil || s.db == nil {
		return Policy{}, fmt.Errorf("edge: store unavailable")
	}
	if err := dialect.Text(org, p.UpdatedBy); err != nil {
		return Policy{}, fmt.Errorf("edge: put %q: %w", org, err)
	}
	// The read, the merge and the write are one transaction that is the store's only
	// writer, so two partial PUTs at once both land instead of the later one
	// overwriting the earlier with a merge made before it.
	tx, err := dialect.Of(s.db).BeginWrite(ctx, s.db)
	if err != nil {
		return Policy{}, fmt.Errorf("edge: put %q: %w", org, err)
	}
	defer func() { _ = tx.Rollback() }()
	cur, _, err := get(ctx, tx, org)
	if err != nil {
		return Policy{}, err
	}
	next := merge(cur, p)
	next.UpdatedAt = time.Now().Unix()
	next.UpdatedBy = p.UpdatedBy
	doc, err := json.Marshal(next)
	if err != nil {
		return Policy{}, fmt.Errorf("edge: encode: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO policy (org, doc, updated_at) VALUES ($1,$2,$3)
		 ON CONFLICT(org) DO UPDATE SET doc=excluded.doc, updated_at=excluded.updated_at`,
		org, string(doc), next.UpdatedAt); err != nil {
		return Policy{}, fmt.Errorf("edge: put %q: %w", org, err)
	}
	if err := tx.Commit(); err != nil {
		return Policy{}, fmt.Errorf("edge: put %q: %w", org, err)
	}
	s.invalidate()
	return next, nil
}

// PutPlatform upserts the PLATFORM policy — the row under the admin org — merged
// over any existing platform row. This is the ONLY write that may touch the
// pre-auth edge knobs (CORS, per-IP cap); the /v1/gateway subsystem gates it on
// SuperAdmin. Targeting the admin org explicitly (not the caller's possibly
// org-switched X-Org-Id) is what makes a SuperAdmin's platform PUT land on the
// platform row regardless of which tenant they are currently viewing.
func (s *Store) PutPlatform(ctx context.Context, p Policy) (Policy, error) {
	if s == nil {
		return Policy{}, fmt.Errorf("edge: nil store")
	}
	return s.Put(ctx, s.adminOrg, p)
}

// AdminOrg names the org whose row is the platform row.
func (s *Store) AdminOrg() string {
	if s == nil {
		return ""
	}
	return s.adminOrg
}

// Effective returns the read-back view for org: the platform edge policy (CORS +
// per-IP + window) with the org's OWN per-org config (rate ceiling, cache TTL /
// per-path overrides, method allowlist) overlaid. This is what
// GET /v1/gateway/config returns — a tenant sees the platform edge policy in force
// plus its own configuration.
func (s *Store) Effective(org string) Policy {
	p := s.Platform()
	eo := s.effectiveOrg(org)
	p.CacheTTLSec, p.CachePaths, p.Methods, p.Mode = eo.CacheTTLSec, eo.CachePaths, eo.Methods, eo.Mode
	// What is ENFORCED, not what is stored: an org_rpm above the cap is clipped by
	// the limiter, so reading back the stored number would report a ceiling the org
	// does not have.
	p.OrgRPM, p.OrgCap = s.Ceiling(org), s.Cap(org)
	return p
}

// Ceiling is org's own ceiling as the limiter enforces it, requests a minute: the
// org_rpm it set, clipped to its cap, or its cap when it set none. (Commerce rules
// can lower it further, per request.)
func (s *Store) Ceiling(org string) int {
	c := s.Cap(org)
	if own := s.OrgRPM(org); own > 0 && (c == 0 || own < c) {
		return own
	}
	return c
}

// OwnCap is the org_cap a SuperAdmin set on org's own row, or 0: Cap with no
// default under it, for traffic the default does not hold (first-party).
func (s *Store) OwnCap(org string) int { return s.effectiveOrg(org).OrgCap }

// Cap is the most org's ceiling may be, requests a minute: the org_cap a SuperAdmin
// set on the org's own row, else the platform's org_rpm (a SuperAdmin's write to
// the platform row), else DefaultOrgRPM.
//
// It is the line a tenant cannot cross itself. A tenant's org_rpm and its
// commerce rules may only lower an org's ceiling below it; every free signup owns
// an org and administers it, so a ceiling its own admin could raise is no ceiling.
//
// It is NOT derived from the anonymous per-IP cap. Cutting per_ip_rpm in the middle
// of a flood is the edge's business and must not move what every paying org may
// send; the two are separate knobs because they answer separate questions.
func (s *Store) Cap(org string) int {
	if s == nil {
		return 0 // no policy store, no cap: an unwired deployment limits nothing
	}
	if c := s.OwnCap(org); c > 0 {
		return c
	}
	if p := s.Platform(); p.OrgRPM > 0 {
		return p.OrgRPM
	}
	return DefaultOrgRPM
}

// Mode returns the abuse gate's posture for org: THAT ORG'S OWN ROW, else shadow.
// Cached with the same short TTL as every other per-org resolver, and fail-soft to
// SHADOW — a policy-store outage must not be the reason a tenant starts being
// refused.
//
// IT DOES NOT INHERIT, and that is the point. It used to fall back to the platform
// row, so arming the one lane that has no tenant — the anonymous lane, which is
// where a bad bot calls from — armed every tenant in the estate at the same time:
// one PUT, and a statistical judgement began enforcing against customers whose own
// config still said nothing and whose operators were never asked. An arming
// decision that reaches a tenant it was not written for is not a default, it is an
// accident waiting for a scorer to have a bad day.
//
// An EMPTY org is the anonymous lane, and it resolves to the PLATFORM row — which
// is that lane's OWN row, not an inherited one: a caller with no tenant still has
// to be governed by something, and the platform scope is what governs a request
// that has no tenant at evaluation time. A SuperAdmin arms it by targeting the
// reserved admin org (PUT /v1/gateway/config?org=<adminOrg> {"mode":"live"}), since
// the admin org's row IS the platform row. One mechanism, one scope per write.
func (s *Store) Mode(org string) string {
	var m string
	if org == "" {
		m = s.Platform().Mode
	} else {
		m = s.orgMode(org)
	}
	if m == ModeLive {
		return ModeLive
	}
	return ModeShadow
}

// orgMode reads the org's OWN stored mode and nothing else — no platform layer,
// no static default. Cached under the same per-org entry every other resolver
// shares, and fail-soft to "" (⇒ shadow).
func (s *Store) orgMode(org string) string {
	if s == nil || org == "" {
		return ""
	}
	return s.effectiveOrg(org).Mode
}

func (s *Store) invalidate() {
	s.mu.Lock()
	s.cache = map[string]cacheEntry{}
	s.mu.Unlock()
}

// Platform returns the effective PLATFORM policy — the admin-org row merged over
// the static defaults — cached with a short TTL and fail-open to the static
// default. Called per-request by EdgeCORS/EdgeRateLimit (pre-identity).
func (s *Store) Platform() Policy {
	if s == nil {
		return Policy{}
	}
	return s.resolve("", func() Policy {
		row, ok, err := s.Get(context.Background(), s.adminOrg)
		if err != nil || !ok {
			return s.static // fail-open / un-provisioned → static defaults.
		}
		return merge(s.static, row)
	})
}

// effectiveOrg resolves org's per-org edge config — its own row overlaid on the
// platform per-org DEFAULTS — cached under key=org with a short TTL, fail-open to
// the platform defaults. Every per-org resolver (OrgRPM / CacheTTL / Methods) and
// the Effective read-back share this ONE value, so an org has exactly one resolved
// config and one cache entry.
//
// Mode is deliberately absent from the inherited base: it is an arming decision,
// not a default (see Policy.Mode and Store.Mode). The org's own row is its only
// source, so the value below is "" — hence shadow — for every org that has not
// been armed by name.
func (s *Store) effectiveOrg(org string) Policy {
	if s == nil || org == "" {
		return Policy{}
	}
	return s.resolve(org, func() Policy {
		plat := s.Platform()
		// org_rpm does not inherit: the platform's org_rpm is every org's default CAP
		// (Cap), and inheriting it as each org's own ceiling would also clip every
		// cap a SuperAdmin raised above it.
		base := Policy{CacheTTLSec: plat.CacheTTLSec, CachePaths: plat.CachePaths, Methods: plat.Methods}
		row, ok, err := s.Get(context.Background(), org)
		if err != nil || !ok {
			return base
		}
		return merge(base, Policy{OrgRPM: row.OrgRPM, OrgCap: row.OrgCap, CacheTTLSec: row.CacheTTLSec, CachePaths: row.CachePaths, Methods: row.Methods, Mode: row.Mode})
	})
}

// OrgRPM returns the ceiling (requests/min) org set for itself on its own row, or 0.
// It only lowers the org's cap (Cap), which is where the platform's org_rpm acts.
// Cached with a short TTL, fail-open (0). Called per-request by ScopeRateLimit
// (post-identity).
func (s *Store) OrgRPM(org string) int {
	return s.effectiveOrg(org).OrgRPM
}

// CacheTTL returns the edge-cache TTL (seconds) for org+path: the org's own
// longest-matching cache_paths prefix wins, else its default CacheTTLSec, else the
// platform default, else 0 (no caching). Cached with a short TTL, fail-open (0).
// The edge cache middleware reads this live, per request, so an operator's PUT
// takes effect without a redeploy (mirrors OrgRPM / Platform).
func (s *Store) CacheTTL(org, path string) int {
	if s == nil {
		return 0
	}
	p := s.effectiveOrg(org)
	if ttl, ok := longestPrefixTTL(p.CachePaths, path); ok {
		return ttl
	}
	return p.CacheTTLSec
}

// PublicTTL is the edge-cache TTL (seconds) for an answer computed for NO caller —
// the anonymous lane, which has no org of its own and so reads the platform
// policy alone: its longest-matching cache_paths prefix, else its CacheTTLSec,
// else 0 (not cacheable). EdgeCache reads it per request, so an operator's PUT
// takes effect without a redeploy.
func (s *Store) PublicTTL(path string) int {
	if s == nil {
		return 0
	}
	p := s.Platform()
	if ttl, ok := longestPrefixTTL(p.CachePaths, path); ok {
		return ttl
	}
	return p.CacheTTLSec
}

// Catalog reports whether the platform policy names path in its cache_paths with
// a TTL: the paths whose anonymous answer is the same for every caller, and so
// for every origin. A default CacheTTLSec does not make a path one — it says how
// long, not that the answer is shared. EdgeCORS reads it per request.
func (s *Store) Catalog(path string) bool {
	if s == nil {
		return false
	}
	ttl, ok := longestPrefixTTL(s.Platform().CachePaths, path)
	return ok && ttl > 0
}

// Methods returns the allowlist of HTTP methods the edge accepts for org (nil =
// all allowed): the org's own list wins, else the platform default. Fail-open
// (nil). The edge method-guard reads this live.
func (s *Store) Methods(org string) []string {
	if s == nil {
		return nil
	}
	return s.effectiveOrg(org).Methods
}

// longestPrefixTTL returns the TTL of the longest path-prefix in m that prefixes
// path (found=false when none match), so a specific "/v1/models" override wins
// over a broader "/v1".
func longestPrefixTTL(m map[string]int, path string) (int, bool) {
	best, bestLen, found := 0, -1, false
	for prefix, ttl := range m {
		if len(prefix) > bestLen && strings.HasPrefix(path, prefix) {
			best, bestLen, found = ttl, len(prefix), true
		}
	}
	return best, found
}

// resolve returns the cached policy for key, recomputing via load on a miss/expiry.
func (s *Store) resolve(key string, load func() Policy) Policy {
	now := time.Now()
	s.mu.Lock()
	if e, ok := s.cache[key]; ok && now.Before(e.expiry) {
		s.mu.Unlock()
		return e.pol
	}
	s.mu.Unlock()

	pol := load()

	s.mu.Lock()
	s.cache[key] = cacheEntry{pol: pol, expiry: now.Add(resolveTTL)}
	s.mu.Unlock()
	return pol
}
