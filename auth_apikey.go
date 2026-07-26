// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package cloud

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// The identity boundary (SanitizeIdentity) validates a JWT and mints the identity
// headers every subsystem trusts. An opaque API key (hk-/sk-/pk-/fw_/hz_) is not a
// JWT, so it yielded no principal — and a subsystem that gates on the minted
// identity (zen's billing gate) refused an API-key request as anonymous, though the
// key is a first-class credential. keyResolver closes that gap: it turns a key into
// the SAME idClaims a JWT yields, so ONE minting path serves both credentials and
// key auth and session auth can never disagree on who a request is.

// keyResolver turns an opaque API key into the principal it authenticates, or nil
// for an unknown key or an unconfigured resolver (the request stays anonymous — a
// bad key never grants trust).
type keyResolver interface {
	resolve(ctx context.Context, key string) *idClaims
}

// iamKeys resolves an `hk-` key against IAM's get-user?accessKey endpoint,
// authenticating as the confidential `hanzo-console` client (the credential
// clients/account already uses). The resolved user is exactly what a JWT for that
// user carries, so SanitizeIdentity mints identical headers for a key and a session.
// A brief cache keeps the hot auth path off the network; it caches misses too, so a
// bad key cannot hammer IAM.
type iamKeys struct {
	base     string
	auth     string // client_secret_basic, or "" when unconfigured
	http     *http.Client
	cache    cache[string, *idClaims] // hk-/sk-/fw_/hz_ → principal (get-user)
	pubCache cache[string, pubOrg]    // pk-            → write-only org (resolve-key)
}

// newIAMKeys reads the same IAM env clients/account does. With no confidential
// credential it returns a resolver that resolves nothing (keys stay anonymous —
// never a fabricated principal), so a deployment lacking the credential is safe.
func newIAMKeys() *iamKeys {
	return &iamKeys{
		base:     iamHost(),
		auth:     iamCred(),
		http:     &http.Client{Timeout: 5 * time.Second},
		cache:    newCache[string, *idClaims](60 * time.Second),
		pubCache: newCache[string, pubOrg](60 * time.Second),
	}
}

// sharedKeys memoizes ONE API-key resolver (and its 60s cache) for the whole
// binary. The identity boundary (SanitizeIdentity, via newIdentityValidator) and
// any subsystem that must resolve a key OUT-OF-BAND of the Authorization header
// (analytics capture: a project key posted in the SDK body/query) both go through
// this ONE seam, so a key resolves to the SAME org either way and IAM sees one
// warm cache — never a second, drifting resolver.
var (
	sharedKeysOnce sync.Once
	sharedKeysInst *iamKeys
)

func sharedKeys() *iamKeys {
	sharedKeysOnce.Do(func() { sharedKeysInst = newIAMKeys() })
	return sharedKeysInst
}

// maxKeyOrgLen bounds a resolved org key the same way principal.MaxOrgLen does: the
// org becomes a warehouse partition key, so an over-long value (malformed / hostile)
// is refused rather than stored.
const maxKeyOrgLen = 128

// OrgForKey resolves an opaque Hanzo API key to the org it may write into — the
// exported door a keyed, bearer-less SDK/ingest path uses to attribute a request to a
// tenant. It returns an ORG only, NEVER a principal, so it is inherently write-safe;
// it is called ONLY on write/ingest paths (there are zero read call sites).
//
// It routes by key kind, because a write-only publishable key and a read-capable key
// resolve through DIFFERENT IAM doors:
//
//   - pk- (write-only PUBLISHABLE, browser-shippable) → IAM resolve-key, which
//     returns just {org, scope} and only when the key is scope=="publish". This is
//     the write-only sibling of the principal path: a pk- can attribute an INGEST to
//     its org and NOTHING else (the identity boundary already refuses it a principal,
//     so it can never read).
//   - hk-/sk-/fw_/hz_ (read-capable) → get-user?accessKey, the SAME owner org
//     SanitizeIdentity mints when that key arrives as a bearer.
//
// FAILS CLOSED: ("", false) for a non-key-shaped string, an unknown/unresolvable
// key, a non-publish pk-, an unconfigured resolver, or an out-of-bounds org — never a
// fabricated or default tenant, so a bad key can never be written into another org's
// partition. The isAPIKey prefix gate keeps garbage strings off the IAM network path.
func OrgForKey(ctx context.Context, key string) (string, bool) {
	key = strings.TrimSpace(key)
	if !isAPIKey(key) {
		return "", false
	}
	if publishableKey(key) {
		return sharedKeys().orgForPublishable(ctx, key)
	}
	claims := sharedKeys().resolve(ctx, key)
	if claims == nil {
		return "", false
	}
	owner := strings.TrimSpace(claims.Owner)
	if owner == "" || len(owner) > maxKeyOrgLen {
		return "", false
	}
	return owner, true
}

// scopePublish is the IAM key scope that certifies a write-only publishable key. The
// resolve-key door accepts a pk- ONLY when IAM states this scope, so a key that is
// NOT write-only (a secret / read-capable key that somehow reached this door) can
// never resolve to a writable tenant — the write-only property is enforced on BOTH
// sides (IAM refuses to resolve a non-publish key; cloud refuses to accept one).
const scopePublish = "publish"

// pubOrg is a cached resolve-key answer: the org a write-only pk- may write into, and
// whether it resolved. A cached miss (ok=false) is a valid answer — a bad pk- cannot
// hammer IAM — exactly like the principal path's cached nil.
type pubOrg struct {
	org string
	ok  bool
}

// orgForPublishable resolves an IAM write-only publishable key (pk-) to its org via
// IAM's resolve-key endpoint. The endpoint returns ONLY {org, scope} — never a user,
// name, email, isAdmin, or any principal material — and cloud accepts the org ONLY
// when scope=="publish", so the value can attribute an INGEST and can never be read
// back as identity. Caches hits and misses (60s), like resolve(). Fails closed on an
// unconfigured resolver, an unreachable IAM, a non-publish/unknown key, or an
// out-of-bounds org.
func (k *iamKeys) orgForPublishable(ctx context.Context, key string) (string, bool) {
	if k.auth == "" || k.base == "" || key == "" {
		return "", false
	}
	if c, ok := k.pubCache.get(key); ok {
		return c.org, c.ok
	}
	org, ok := k.lookupPublishable(ctx, key)
	if ok {
		org = strings.TrimSpace(org)
		if org == "" || len(org) > maxKeyOrgLen {
			org, ok = "", false
		}
	}
	k.pubCache.put(key, pubOrg{org: org, ok: ok})
	return org, ok
}

// lookupPublishable performs the authenticated resolve-key?accessKey call and returns
// the org ONLY when IAM vouches scope=="publish". Any failure (unreachable, denied,
// unknown key, non-publish scope, malformed body) yields ("", false). The response
// carries no principal fields, so nothing readable can leak through this door even on
// a misbehaving IAM.
func (k *iamKeys) lookupPublishable(ctx context.Context, key string) (string, bool) {
	u := k.base + "/v1/iam/resolve-key?" + url.Values{"accessKey": {key}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", false
	}
	req.Header.Set("Authorization", k.auth)
	req.Header.Set("Accept", "application/json")
	resp, err := k.http.Do(req)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", false
	}
	var env struct {
		Status string `json:"status"`
		Data   *struct {
			Org   string `json:"org"`
			Scope string `json:"scope"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &env) != nil || env.Status != "ok" || env.Data == nil {
		return "", false
	}
	// WRITE-ONLY ENFORCEMENT: accept the org ONLY for a key IAM certifies write-only.
	if env.Data.Scope != scopePublish {
		return "", false
	}
	return strings.TrimSpace(env.Data.Org), true
}

// iamHost is the standalone IAM origin cloud talks to; iamCred is the service
// credential (client_secret_basic) it presents — the ONE IAM identity, shared by
// the API-key resolver here and the /v1/iam edge (iam_edge.go), so both
// authenticate to IAM the same way. Empty cred → a deployment lacking the
// credential stays safe (the caller treats "" as unconfigured).
func iamHost() string { return strings.TrimRight(env("IAM_URL", "IAM_INTERNAL_URL"), "/") }

func iamCred() string {
	id := strings.TrimSpace(os.Getenv("IAM_MINT_CLIENT_ID"))
	secret := strings.TrimSpace(os.Getenv("IAM_MINT_CLIENT_SECRET"))
	if id == "" || secret == "" {
		return ""
	}
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(id+":"+secret))
}

func env(names ...string) string {
	for _, n := range names {
		if v := strings.TrimSpace(os.Getenv(n)); v != "" {
			return v
		}
	}
	return ""
}

func (k *iamKeys) resolve(ctx context.Context, key string) *idClaims {
	if k.auth == "" || k.base == "" || key == "" {
		return nil
	}
	if c, ok := k.cache.get(key); ok {
		return c // may be a cached nil — a valid "anonymous" answer
	}
	c := k.lookup(ctx, key)
	k.cache.put(key, c)
	return c
}

// lookup performs the authenticated get-user?accessKey call and maps the user row
// to idClaims. Any failure (unreachable, denied, unknown key) yields nil. Name is
// both the username IAM's owner/name lookups parse and the id fallback: a key has
// no UUID subject, so userID() falls through to name — the gateway's historical
// X-User-Id==name behavior the owner/name path expects.
func (k *iamKeys) lookup(ctx context.Context, key string) *idClaims {
	u := k.base + "/v1/iam/get-user?" + url.Values{"accessKey": {key}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil
	}
	req.Header.Set("Authorization", k.auth)
	req.Header.Set("Accept", "application/json")
	resp, err := k.http.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil
	}
	var env struct {
		Status string `json:"status"`
		Data   *struct {
			Owner   string `json:"owner"`
			Name    string `json:"name"`
			Email   string `json:"email"`
			IsAdmin bool   `json:"isAdmin"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &env) != nil || env.Status != "ok" || env.Data == nil {
		return nil
	}
	if strings.TrimSpace(env.Data.Owner) == "" {
		return nil
	}
	return &idClaims{
		Owner:             strings.TrimSpace(env.Data.Owner),
		Name:              strings.TrimSpace(env.Data.Name),
		PreferredUsername: strings.TrimSpace(env.Data.Name),
		Email:             strings.TrimSpace(env.Data.Email),
		IsAdmin:           env.Data.IsAdmin,
	}
}

// cache is a tiny concurrency-safe TTL map — one generic type for every
// resolve-once-reuse-briefly lookup, so no bespoke cache is hand-rolled per caller.
type cache[K comparable, V any] struct {
	ttl time.Duration
	mu  sync.Mutex
	m   map[K]entry[V]
}

type entry[V any] struct {
	v   V
	exp time.Time
}

func newCache[K comparable, V any](ttl time.Duration) cache[K, V] {
	return cache[K, V]{ttl: ttl, m: make(map[K]entry[V])}
}

func (c *cache[K, V]) get(k K) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[k]
	if !ok || time.Now().After(e.exp) {
		var zero V
		return zero, false
	}
	return e.v, true
}

func (c *cache[K, V]) put(k K, v V) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m[k] = entry[V]{v: v, exp: time.Now().Add(c.ttl)}
}
