// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package cloud

import (
	"context"
	"encoding/base64"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/authz"
	iamstore "github.com/hanzoai/iam/pkg/store"
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
	cache cache[string, *idClaims]  // secret key -> principal (get-user?accessKey)
	orgs  cache[string, string]     // publishable key -> org, and no principal (resolve-key)
	why   cache[string, KeyRefusal] // secret key -> WHY it did not resolve, for diagnosis only
}

// newIAMKeys reads the same IAM env clients/account does. With no confidential
// credential it returns a resolver that resolves nothing (keys stay anonymous —
// never a fabricated principal), so a deployment lacking the credential is safe.
func newIAMKeys() *iamKeys {
	return &iamKeys{
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

// maxKeyOrgLen bounds a resolved org key the same way principal.MaxOrgLen does: the
// org becomes a warehouse partition key, so an over-long value (malformed / hostile)
// is refused rather than stored.
const maxKeyOrgLen = 128

// OrgForKey resolves an opaque Hanzo API key (pk-/sk-/hk-) to the org it belongs
// to — the SAME owner org SanitizeIdentity mints when that key arrives as a bearer
// — and is the exported door a keyed, bearer-less SDK path uses to attribute a
// project key to a tenant.
//
// TWO doors in IAM, because a publishable key and a secret key are resolved by
// different questions and the answers must not be interchangeable:
//
//   - a SECRET key (sk-/hk-) asks WHO, and get-user?accessKey answers with the
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

func env(names ...string) string {
	for _, n := range names {
		if v := strings.TrimSpace(os.Getenv(n)); v != "" {
			return v
		}
	}
	return ""
}

func (k *iamKeys) resolve(ctx context.Context, key string) *idClaims {
	if key == "" {
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
	if key == "" {
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
	db := iamStore()
	if db == nil {
		return "" // IAM not mounted: cannot answer, so nothing is resolved
	}
	row, err := iamstore.PublishableKeyByAccessKey(ctx, db, key, time.Now())
	if err != nil || row == nil {
		return ""
	}
	return strings.TrimSpace(row.Owner)
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
// prefix and nothing else. A credential must never be echoed whole, and "hk-902abd…"
// is enough for a holder to tell WHICH of their keys failed while being useless to
// anyone who intercepts it.
func KeyHint(key string) string {
	key = strings.TrimSpace(key)
	const shown = 9 // "hk-" + 6
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
	db := iamStore()
	if db == nil {
		// IAM is not mounted. That is not a refusal — there is no answer to give —
		// so no reason is recorded and the key simply stays anonymous.
		return nil
	}
	u, err := iamstore.UserByAccessKey(ctx, db, key)
	if err != nil || u == nil {
		// Reason() reads "" for a real store fault, so an infrastructure failure is
		// never reported to a person as a bad credential.
		k.why.put(key, KeyRefusal(iamstore.Reason(err)))
		return nil
	}
	owner := strings.TrimSpace(u.Owner)
	if owner == "" {
		k.why.put(key, KeyRefusal(iamstore.Reason(err)))
		return nil
	}
	return &idClaims{
		Claims: authz.Claims{
			Owner:             owner,
			Name:              strings.TrimSpace(u.Name),
			PreferredUsername: strings.TrimSpace(u.Name),
			Email:             strings.TrimSpace(u.Email),
			IsAdmin:           u.IsAdmin,
		},
		// The org came from the SUBJECT: IAM resolved this accessKey to a user row,
		// and that row's owner is the tenant. No application mints it and no claim
		// carries it, so it is NOT the app-selected value homeOrg exists to reject —
		// it is the same "the organization comes from the token subject" rule IAM
		// states for itself. Recorded here so the identity boundary can tell a KEY
		// principal (which legitimately has no `orgs`, because a machine is a member
		// of nothing) from a HUMAN token that has merely lost its claim — the latter
		// must still fail closed.
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

// iamHost is the IAM origin the /v1/iam EDGE proxies to, and the signal serve.go
// reads to decide whether a deployment that has not enabled the embedded "iam"
// still has a standalone one to fall back on. The API-key resolver no longer uses
// it: it reads the embedded store in process (iam_store.go). When the edge follows,
// this and iamCred go with it.
func iamHost() string { return strings.TrimRight(env("IAM_URL", "IAM_INTERNAL_URL"), "/") }

func iamCred() string {
	id := strings.TrimSpace(os.Getenv("IAM_MINT_CLIENT_ID"))
	secret := strings.TrimSpace(os.Getenv("IAM_MINT_CLIENT_SECRET"))
	if id == "" || secret == "" {
		return ""
	}
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(id+":"+secret))
}
