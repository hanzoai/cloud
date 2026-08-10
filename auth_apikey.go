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

	"github.com/hanzoai/authz"
)

// The identity boundary (SanitizeIdentity) validates a JWT and mints the identity
// headers every subsystem trusts. An opaque API key (pk-/sk-) is not a
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

// iamKeys resolves an `sk-` key against IAM's get-user?accessKey endpoint,
// authenticating as the confidential `hanzo-console` client (the credential
// clients/account already uses). The resolved user is exactly what a JWT for that
// user carries, so SanitizeIdentity mints identical headers for a key and a session.
// A brief cache keeps the hot auth path off the network; it caches misses too, so a
// bad key cannot hammer IAM.
type iamKeys struct {
	base  string
	auth  string // client_secret_basic, or "" when unconfigured
	http  *http.Client
	cache cache[string, *idClaims]  // secret key -> principal (get-user?accessKey)
	orgs  cache[string, string]     // publishable key -> org, and no principal (resolve-key)
	why   cache[string, KeyRefusal] // secret key -> WHY it did not resolve, for diagnosis only
}

// newIAMKeys reads the same IAM env clients/account does. With no confidential
// credential it returns a resolver that resolves nothing (keys stay anonymous —
// never a fabricated principal), so a deployment lacking the credential is safe.
func newIAMKeys() *iamKeys {
	return &iamKeys{
		base:  iamHost(),
		auth:  iamCred(),
		http:  &http.Client{Timeout: 5 * time.Second},
		cache: newCache[string, *idClaims](60 * time.Second),
		orgs:  newCache[string, string](60 * time.Second),
		why:   newCache[string, KeyRefusal](60 * time.Second),
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

// ResolvePublishableKeyOrg resolves a publishable (pk-) key to its org slug through
// the ONE binary-wide resolver, or "" when the key names no org. Exported for the
// out-of-band ingest paths in sibling packages — the embedded o11y sentry runtime
// (apps/o11y) — so a key attributes errors to the SAME org it attributes events to,
// off the SAME warm cache, never a second drifting resolver.
func ResolvePublishableKeyOrg(ctx context.Context, key string) string {
	return sharedKeys().resolveOrg(ctx, key)
}

// maxKeyOrgLen bounds a resolved org key the same way principal.MaxOrgLen does: the
// org becomes a warehouse partition key, so an over-long value (malformed / hostile)
// is refused rather than stored.
const maxKeyOrgLen = 128

// OrgForKey resolves an opaque Hanzo API key (pk-/sk-) to the org it belongs
// to — the SAME owner org SanitizeIdentity mints when that key arrives as a bearer
// — and is the exported door a keyed, bearer-less SDK path uses to attribute a
// project key to a tenant.
//
// TWO doors in IAM, because a publishable key and a secret key are resolved by
// different questions and the answers must not be interchangeable:
//
//   - a SECRET key (sk-) asks WHO, and get-user?accessKey answers with the
//     principal. IAM refuses a pk- there BY DESIGN (store.UserByAccessKey), which
//     is right and was also the bug: cloud sent every prefix down this one door, so
//     a publishable key resolved to nothing and the ingest path it exists for could
//     never attribute a beacon. A publishable key that resolves to nobody is a
//     publishable key that does not work.
//   - a PUBLISHABLE key (pk-) asks WHICH ORG, and resolve-key answers with the org
//     and nothing else — no user, no email, no admin bit. That is the property that
//     makes it safe to ship in client JS, so it is a separate door with its own
//     narrower capability (CapPublishableResolve), not a flag on the first.
//
// FAILS CLOSED: ("", false) for a non-key-shaped string, an unknown/unresolvable
// key, an unconfigured resolver, or an out-of-bounds org — never a fabricated or
// default tenant, so a bad key can never be written into another org's partition.
// The isAPIKey prefix gate keeps garbage strings off the IAM network path.
func OrgForKey(ctx context.Context, key string) (string, bool) {
	key = strings.TrimSpace(key)
	if !isAPIKey(key) {
		return "", false
	}
	var owner string
	if IsPublishableKey(key) {
		owner = sharedKeys().resolveOrg(ctx, key)
	} else if claims := sharedKeys().resolve(ctx, key); claims != nil {
		owner = claims.Owner
	}
	owner = strings.TrimSpace(owner)
	if owner == "" || len(owner) > maxKeyOrgLen {
		return "", false
	}
	return owner, true
}

// iamHost is the standalone IAM origin cloud talks to; iamCred is the service
// credential (client_secret_basic) it presents — the ONE IAM identity, shared by
// the API-key resolver here and the /v1/iam edge (iam_edge.go), so both
// authenticate to IAM the same way. Empty cred → a deployment lacking the
// credential stays safe (the caller treats "" as unconfigured).
func iamHost() string { return IAMBase() }

func iamCred() string {
	id := strings.TrimSpace(os.Getenv("IAM_MINT_CLIENT_ID"))
	secret := strings.TrimSpace(os.Getenv("IAM_MINT_CLIENT_SECRET"))
	if id == "" || secret == "" {
		return ""
	}
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(id+":"+secret))
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

// resolveOrg resolves a PUBLISHABLE key to the org that holds it, and to nothing
// else. "" for an unknown/expired/non-publishable key or an unconfigured resolver.
//
// It shares this resolver's credential and cache shape but NOT its cache: the two
// doors answer different questions, and one map keyed only on the key string would
// let a pk- entry read as a principal or a sk- entry read as a bare org. The values
// are different types precisely so they cannot be confused.
func (k *iamKeys) resolveOrg(ctx context.Context, key string) string {
	if k.auth == "" || k.base == "" || key == "" {
		return ""
	}
	if org, ok := k.orgs.get(key); ok {
		return org // may be a cached "" — a valid "unresolvable" answer
	}
	org := k.lookupOrg(ctx, key)
	k.orgs.put(key, org)
	return org
}

// lookupOrg performs the authenticated resolve-key call — the ORG-ONLY dual of
// get-user?accessKey. It reads `org` and deliberately nothing else: the envelope
// carries no principal, and this function would have nowhere to put one.
func (k *iamKeys) lookupOrg(ctx context.Context, key string) string {
	u := k.base + "/v1/iam/resolve-key?" + url.Values{"accessKey": {key}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("Authorization", k.auth)
	req.Header.Set("Accept", "application/json")
	resp, err := k.http.Do(req)
	if err != nil {
		return ""
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return ""
	}
	var env struct {
		Status string `json:"status"`
		Data   *struct {
			Org string `json:"org"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &env) != nil || env.Status != "ok" || env.Data == nil {
		return ""
	}
	return strings.TrimSpace(env.Data.Org)
}

// KeyRefusal is the machine-readable reason IAM gives for not resolving a key —
// `code` on the get-user?accessKey / resolve-key envelope (iam internal/store
// apikey.go). Cloud does not interpret it; it carries it, so the surface that faces
// a human can say "revoked, mint a new one" instead of IAM's generic "the entity
// does not exist". "" means IAM gave no reason (an older IAM, or a store fault,
// which is NOT a bad credential).
type KeyRefusal string

// RefusalForKey resolves an opaque secret key and reports WHY it failed, for the
// surface that must explain the failure to a person. It shares resolve()'s cache, so
// asking why costs no extra IAM call on the hot path: a resolved key answers ("",
// true) from the same cached principal the auth path uses.
func RefusalForKey(ctx context.Context, key string) (KeyRefusal, bool) {
	key = strings.TrimSpace(key)
	if !isAPIKey(key) || IsPublishableKey(key) {
		return "", false
	}
	if claims := sharedKeys().resolve(ctx, key); claims != nil {
		return "", true
	}
	return sharedKeys().refusal(ctx, key), false
}

// refusal reports the cached reason a key did not resolve. lookup records it when it
// asks IAM, so this never issues a second call — the reason is a by-product of the
// resolution that already happened, not a separate question.
func (k *iamKeys) refusal(_ context.Context, key string) KeyRefusal {
	r, _ := k.why.get(key)
	return r
}

// KeyHint is the ONE way a key is named in a log line or an error message: its
// prefix and nothing else. A credential must never be echoed whole, and "sk-902abd…"
// is enough for a holder to tell WHICH of their keys failed while being useless to
// anyone who intercepts it.
func KeyHint(key string) string {
	key = strings.TrimSpace(key)
	const shown = 9 // "sk-" + 6
	if len(key) <= shown {
		return "…"
	}
	return key[:shown] + "…"
}

// lookup performs the authenticated get-user?accessKey call and maps the user row
// to idClaims. Any failure (unreachable, denied, unknown key) yields nil. Name is
// both the username IAM's owner/name lookups parse and the id fallback: a key has
// no UUID subject, so userID() falls through to name — the gateway's historical
// X-User-Id==name behavior the owner/name path expects.
//
// A refusal's REASON is recorded beside the (nil) principal rather than discarded.
// It changes no decision here — a key that does not resolve is anonymous either way,
// and that stays true — but throwing it away is what left every downstream surface
// rendering IAM's generic "the entity does not exist" to users whose key had simply
// been revoked.
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
		Code   string `json:"code"` // WHY, when IAM refused (iam store.KeyFailure)
		Data   *struct {
			Owner   string `json:"owner"`
			Name    string `json:"name"`
			Email   string `json:"email"`
			IsAdmin bool   `json:"isAdmin"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &env) != nil || env.Status != "ok" || env.Data == nil {
		k.why.put(key, KeyRefusal(strings.TrimSpace(env.Code)))
		return nil
	}
	if strings.TrimSpace(env.Data.Owner) == "" {
		k.why.put(key, KeyRefusal(strings.TrimSpace(env.Code)))
		return nil
	}
	owner := strings.TrimSpace(env.Data.Owner)
	return &idClaims{
		Claims: authz.Claims{
			Owner:             owner,
			Name:              strings.TrimSpace(env.Data.Name),
			PreferredUsername: strings.TrimSpace(env.Data.Name),
			Email:             strings.TrimSpace(env.Data.Email),
			IsAdmin:           env.Data.IsAdmin,
		},
		// The org came from the SUBJECT: IAM resolved this accessKey to a user row,
		// and that row's owner is the tenant. No application mints it and no claim
		// carries it, so it is NOT the app-selected value homeOrg exists to reject —
		// it is the same "the organization comes from the token subject" rule IAM
		// states for itself in internal/authz/authz.go. Recorded here so the identity
		// boundary can tell a KEY principal (which legitimately has no `orgs`, because
		// a machine is a member of nothing) from a HUMAN token that has merely lost
		// its claim — the latter must still fail closed.
		subjectOrg: owner,
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

// put stores a value. The ZERO cache is usable: its map is created on first write,
// so a partially-constructed iamKeys (any caller that names only the caches it cares
// about) records rather than panicking on a nil map. A zero ttl expires immediately,
// which is the right reading of "no ttl was configured" — never cache forever.
func (c *cache[K, V]) put(k K, v V) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = make(map[K]entry[V])
	}
	c.m[k] = entry[V]{v: v, exp: time.Now().Add(c.ttl)}
}
