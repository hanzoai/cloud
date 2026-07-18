// Package edge is the runtime-mutable store for the cloud edge ("gateway
// role") policy: the CORS allowlist, the pre-auth per-client-IP flood cap, and the
// authenticated per-org rate ceiling. It is a LEAF package (stdlib + the Hanzo
// SQLite driver only, no import of the root cloud package) so BOTH consumers can
// use it without an import cycle:
//
//   - the edge middleware (package cloud, middleware_edge.go / middleware_ratelimit.go)
//     reads the effective policy live, per request, so an operator's change takes
//     effect without a redeploy;
//   - the /v1/gateway HTTP subsystem (clients/gateway) serves GET/PUT over the
//     SAME store, IAM-scoped.
//
// SCOPES. There are two, keyed by org in one encrypted per-tenant SQLite file:
//
//   - PLATFORM policy — the row stored under the admin org (cfg.AdminOrg). It holds
//     the pre-auth edge knobs (CORS origins, per-IP cap + window) that have no
//     tenant at evaluation time (CORS preflight + the anonymous-flood cap run
//     BEFORE identity). Only a SuperAdmin may write it. It is layered over the
//     static boot defaults (env/flags), so an un-provisioned deployment behaves
//     exactly as the static config until an operator PUTs an override.
//   - PER-ORG policy — a tenant's own row, holding its self-service edge config:
//     OrgRPM (authenticated rate ceiling), CacheTTLSec + CachePaths (edge-cache
//     TTL, default and per-path), and Methods (accepted-method allowlist). An org
//     admin writes its own; a SuperAdmin may write any. An unset field inherits the
//     platform default, then the static default.
//
// Fail-soft: every resolver (Platform/OrgRPM/CacheTTL/Methods) returns the static/platform default
// on any store error, so a policy-store outage never takes the edge down. Writes
// fail loud (an unavailable store returns an error to the PUT handler).
package edge

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud/cek" // opens gateway.db encrypted at rest; a leaf pkg, no import cycle.
	_ "github.com/hanzoai/sqlite"  // the ONE "sqlite" driver, for cek's no-key plaintext fallback.
)

// Policy is the edge policy for one scope. Zero-valued fields mean "inherit"
// (from the static default, then the platform policy) — so a PUT that sets only
// OrgRPM leaves the platform CORS/per-IP untouched. Every field is ENFORCED by a
// consumer; there is no stored-but-ignored knob.
type Policy struct {
	// Platform-scope (admin-org row) — consumed by the pre-auth edge middleware.
	CORSOrigins []string `json:"cors_origins,omitempty"` // EdgeCORS allowlist (exact origin | bare host | "*.host").
	PerIPRPM    int      `json:"per_ip_rpm,omitempty"`   // EdgeRateLimit: requests per WindowSec per client IP.
	WindowSec   int      `json:"window_sec,omitempty"`   // EdgeRateLimit window, seconds.

	// Per-org scope — a tenant's OWN edge config (self-service). An org's own row
	// wins; an unset field inherits the platform default, then the static default.
	OrgRPM      int            `json:"org_rpm,omitempty"`       // authenticated per-org ceiling, requests/min (ScopeRateLimit).
	CacheTTLSec int            `json:"cache_ttl_sec,omitempty"` // default edge-cache TTL for this org's responses, seconds (0 = no cache).
	CachePaths  map[string]int `json:"cache_paths,omitempty"`   // per-path-prefix TTL overrides; the longest matching prefix wins over CacheTTLSec.
	Methods     []string       `json:"methods,omitempty"`       // allowlist of HTTP methods the edge accepts (empty = all).

	// Metadata (server-stamped; ignored on input).
	UpdatedAt int64  `json:"updated_at,omitempty"`
	UpdatedBy string `json:"updated_by,omitempty"`
}

// MaxCacheTTLSec bounds a cache TTL (7 days) so a fat-fingered PUT can't pin a
// stale edge response indefinitely.
const MaxCacheTTLSec = 7 * 24 * 60 * 60

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
	return nil
}

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
	if over.CacheTTLSec > 0 {
		out.CacheTTLSec = over.CacheTTLSec
	}
	if len(over.CachePaths) > 0 {
		out.CachePaths = over.CachePaths
	}
	if len(over.Methods) > 0 {
		out.Methods = over.Methods
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

// New opens (or creates) {dataDir}/gateway.db and returns a Store layered over the
// static boot defaults. On any open/migrate error it logs nothing here (the caller
// owns logging) and returns a static-only Store plus the error, so the caller can
// wire the edge middleware with a working fallback regardless.
func New(dataDir, adminOrg string, static Policy) (*Store, error) {
	s := &Store{adminOrg: adminOrg, static: static, cache: map[string]cacheEntry{}}
	if dataDir == "" {
		return s, fmt.Errorf("edge: empty dataDir; running static-only")
	}
	if err := os.MkdirAll(dataDir, 0o750); err != nil {
		return s, fmt.Errorf("edge: mkdir %s: %w", dataDir, err)
	}
	db, err := cek.Open(filepath.Join(dataDir, "gateway.db"))
	if err != nil {
		return s, fmt.Errorf("edge: open: %w", err)
	}
	db.SetMaxOpenConns(1) // one writer; the file lock serializes.
	for _, pragma := range []string{
		"PRAGMA busy_timeout=5000", "PRAGMA journal_mode=WAL", "PRAGMA foreign_keys=ON",
	} {
		if _, err := db.Exec(pragma); err != nil {
			_ = db.Close()
			return s, fmt.Errorf("edge: pragma: %w", err)
		}
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS policy (
		org        TEXT PRIMARY KEY,
		doc        TEXT NOT NULL DEFAULT '{}',
		updated_at INTEGER NOT NULL DEFAULT 0
	)`); err != nil {
		_ = db.Close()
		return s, fmt.Errorf("edge: migrate: %w", err)
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
	var doc string
	err := s.db.QueryRowContext(ctx, `SELECT doc FROM policy WHERE org=?`, org).Scan(&doc)
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
	cur, _, err := s.Get(ctx, org)
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
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO policy (org, doc, updated_at) VALUES (?,?,?)
		 ON CONFLICT(org) DO UPDATE SET doc=excluded.doc, updated_at=excluded.updated_at`,
		org, string(doc), next.UpdatedAt); err != nil {
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

// Effective returns the read-back view for org: the platform edge policy (CORS +
// per-IP + window) with the org's OWN per-org config (rate ceiling, cache TTL /
// per-path overrides, method allowlist) overlaid. This is what
// GET /v1/gateway/config returns — a tenant sees the platform edge policy in force
// plus its own configuration.
func (s *Store) Effective(org string) Policy {
	p := s.Platform()
	eo := s.effectiveOrg(org)
	p.OrgRPM, p.CacheTTLSec, p.CachePaths, p.Methods = eo.OrgRPM, eo.CacheTTLSec, eo.CachePaths, eo.Methods
	return p
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
// platform per-org defaults — cached under key=org with a short TTL, fail-open to
// the platform defaults. Every per-org resolver (OrgRPM / CacheTTL / Methods) and
// the Effective read-back share this ONE value, so an org has exactly one resolved
// config and one cache entry.
func (s *Store) effectiveOrg(org string) Policy {
	if s == nil || org == "" {
		return Policy{}
	}
	return s.resolve(org, func() Policy {
		plat := s.Platform()
		base := Policy{OrgRPM: plat.OrgRPM, CacheTTLSec: plat.CacheTTLSec, CachePaths: plat.CachePaths, Methods: plat.Methods}
		row, ok, err := s.Get(context.Background(), org)
		if err != nil || !ok {
			return base
		}
		return merge(base, Policy{OrgRPM: row.OrgRPM, CacheTTLSec: row.CacheTTLSec, CachePaths: row.CachePaths, Methods: row.Methods})
	})
}

// OrgRPM returns the authenticated per-org rate ceiling (requests/min) for org: the
// org's own row wins, else the platform default's OrgRPM, else 0 (no policy limit).
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
