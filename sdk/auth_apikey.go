// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package cloud

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"github.com/hanzoai/cloud/sdk/iam"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// The identity boundary (SanitizeIdentity) validates a JWT and mints the identity
// headers every subsystem trusts. An opaque API key (pk-/sk-) is not a
// JWT, so it yielded no principal — and a subsystem that gates on the minted
// identity (zen's billing gate) refused an API-key request as anonymous, though the
// key is a first-class credential. keyResolver closes that gap: it turns a key into
// the SAME idClaims a JWT yields, so ONE minting path serves both credentials and
// key auth and session auth can never disagree on who a request is.

// keyResolver turns an opaque API key into the principal it authenticates, or
// into IAM's reason it authenticates nobody. A nil principal stays anonymous — a
// bad key never grants trust.
type keyResolver interface {
	resolve(ctx context.Context, key string) keyAnswer
	// resolveIn is resolve for a request that selected org: the key acting there,
	// IAM's refusal of it, or resolve's answer when org is the key's own or empty.
	resolveIn(ctx context.Context, key, org string) keyAnswer
	// known is the answer resolve would give without asking anyone: a cached one,
	// or false when resolving would ask IAM.
	known(key string) (keyAnswer, bool)
}

// keyAnswer is what IAM said about one presented key: the principal it resolved
// to, or, when it resolved to nobody, IAM's machine-readable reason (`code`:
// key_expired, key_revoked, …). An unreachable IAM or an unconfigured resolver
// gives no reason at all.
type keyAnswer struct {
	claims *idClaims
	// why is IAM's refusal of the KEY — a key_* code (store.KeyFailure). Empty for a
	// principal, and for an IAM that said nothing about the key: unreachable, a
	// 5xx, a refusal of cloud's own credential, a throttle. Only an answer with a
	// why is remembered or held against the address that presented the key.
	why string
}

// The two key endpoints, named once each. Both are under `keys` and the address
// says what comes BACK — an org from a publishable key, a principal from a secret
// one — which is the only thing separating them. Spelling a path inline at its
// call site is how two callers of one endpoint come to disagree about where it is.
const (
	iamKeyOrg       = "/v1/iam/keys/org"
	iamKeyPrincipal = "/v1/iam/keys/principal"
)

// iamKeys resolves an `sk-` key against IAM's keys/principal endpoint,
// authenticating as the confidential `hanzo-console` client (the credential
// clients/account already uses). The resolved user is exactly what a JWT for that
// user carries, so SanitizeIdentity mints identical headers for a key and a session.
// A brief cache keeps the hot auth path off the network; it caches IAM's misses
// too, so a bad key cannot hammer IAM — but never a question IAM did not answer, so
// an IAM outage is not remembered as a minute of "nobody". The cache is bounded
// (cache).
//
// Both caches are keyed by the key's SHA-256 (keyID), never by the key string. The
// string a handler reads from a header aliases fasthttp's request buffer, which the
// next request on the connection overwrites in place, so a key kept by reference
// became the next caller's key and its cached principal answered that caller.
type iamKeys struct {
	base  string
	auth  string // client_secret_basic, or "" when unconfigured
	http  *http.Client
	cache Cache[keyID, keyAnswer] // secret key -> principal (keys/principal)
	// refused is IAM's refusals — unknown, expired, revoked keys — kept apart from
	// the principals, because a caller mints refusals for free: a flood of junk keys
	// fills and drops this cache, never a customer's answer in the other one.
	refused Cache[keyID, keyAnswer]
	orgs    Cache[keyID, string] // publishable key -> org, and no principal (keys/org-key)
	// asking collapses concurrent lookups of one key into one question to IAM, so a
	// burst of requests on a key not yet cached — or a member's key, never cached —
	// asks once, not once per request.
	asking flight
}

// flight is one call per key in flight: whoever asks while a key's lookup runs
// waits for that lookup's answer instead of starting another.
type flight struct {
	mu sync.Mutex
	m  map[keyID]*lookupCall
}

type lookupCall struct {
	done chan struct{}
	a    keyAnswer
}

// do runs fn for id unless a call for id is already running, and answers that
// call's answer either way.
func (f *flight) do(id keyID, fn func() keyAnswer) keyAnswer {
	f.mu.Lock()
	if c, ok := f.m[id]; ok {
		f.mu.Unlock()
		<-c.done
		return c.a
	}
	if f.m == nil {
		f.m = map[keyID]*lookupCall{}
	}
	c := &lookupCall{done: make(chan struct{})}
	f.m[id] = c
	f.mu.Unlock()
	c.a = fn()
	f.mu.Lock()
	delete(f.m, id)
	f.mu.Unlock()
	close(c.done)
	return c.a
}

// keyID is what a key is remembered by: the SHA-256 of the bytes presented. It is
// an array, so it is a copy of the key by construction and nothing the caller does
// with its memory afterwards can change which entry it names; and, as with the
// token memo (identity_cache.go), a credential never sits in a map key.
type keyID [sha256.Size]byte

func idOf(key string) keyID { return sha256.Sum256([]byte(key)) }

// newIAMKeys reads the same IAM env clients/account does. With no confidential
// credential it returns a resolver that resolves nothing (keys stay anonymous —
// never a fabricated principal), so a deployment lacking the credential is safe.
func newIAMKeys() *iamKeys {
	return &iamKeys{
		base:    iamHost(),
		auth:    iamCred(),
		http:    &http.Client{Timeout: 5 * time.Second},
		cache:   NewCache[keyID, keyAnswer](60 * time.Second),
		refused: NewCache[keyID, keyAnswer](60 * time.Second),
		orgs:    NewCache[keyID, string](60 * time.Second),
	}
}

// sharedKeys memoizes ONE API-key resolver (and its 60s cache) for the whole
// binary. The identity boundary (SanitizeIdentity, via newIdentityValidator) and
// any subsystem that must resolve a key OUT-OF-BAND of the Authorization header
// (analytics capture: a project key posted in the SDK body/query) both go through
// this ONE client, so a key resolves to the SAME org either way and IAM sees one
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
// — and is the exported entry point a keyed, bearer-less SDK path uses to
// attribute a project key to a tenant.
//
// TWO endpoints in IAM, because a publishable key and a secret key are resolved by
// different questions and the answers must not be interchangeable:
//
//   - a SECRET key (sk-) asks WHO, and keys/principal answers with the
//     principal. IAM refuses a pk- there BY DESIGN (store.UserByAccessKey), which
//     is right and was also the bug: cloud sent every prefix to this one endpoint,
//     so a publishable key resolved to nothing and the ingest path it exists for
//     could never attribute a beacon. A publishable key that resolves to nobody is
//     a publishable key that does not work.
//   - a PUBLISHABLE key (pk-) asks WHICH ORG, and keys/org answers with the org
//     and nothing else — no user, no email, no admin bit. That is the property that
//     makes it safe to ship in client JS, so it is a separate endpoint with its own
//     narrower capability (CapPublishableResolve), not a flag on the first.
//
// FAILS CLOSED: ("", false) for a non-key-shaped string, an unknown/unresolvable
// key, an unconfigured resolver, or an out-of-bounds org — never a fabricated or
// default tenant, so a bad key can never be written into another org's partition.
// The IsAPIKey prefix gate keeps garbage strings off the IAM network path.
func OrgForKey(ctx context.Context, key string) (string, bool) {
	key = strings.TrimSpace(key)
	if !IsAPIKey(key) {
		return "", false
	}
	var owner string
	if IsPublishableKey(key) {
		owner = sharedKeys().resolveOrg(ctx, key)
	} else if claims := sharedKeys().resolve(ctx, key).claims; claims != nil {
		// The org the boundary acts in for this key: the holder's own for a home key,
		// the org it was minted in for a member's key.
		owner = claims.homeOrg()
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
func iamHost() string { return iam.Base() }

func iamCred() string {
	id := strings.TrimSpace(os.Getenv("IAM_MINT_CLIENT_ID"))
	secret := strings.TrimSpace(os.Getenv("IAM_MINT_CLIENT_SECRET"))
	if id == "" || secret == "" {
		return ""
	}
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(id+":"+secret))
}

func (k *iamKeys) resolve(ctx context.Context, key string) keyAnswer {
	if k.auth == "" || k.base == "" || key == "" {
		return keyAnswer{}
	}
	id := idOf(key)
	if a, ok := k.remembered(id); ok {
		return a // may be a cached refusal — a valid "anonymous" answer
	}
	// One question per key in flight. It runs detached from the caller that started
	// it, so that caller going away does not fail the others waiting on its answer;
	// the client's own timeout still bounds it.
	asked := context.WithoutCancel(ctx)
	return k.asking.do(id, func() keyAnswer {
		a, answered := k.lookup(asked, key)
		// A member's key is asked again every time. Its authority is a membership
		// the org can remove at any moment, and removal must end the key on the next
		// request, not at the end of a cache window. What that costs is bounded at
		// the edge: a member's key its own ceiling refused is barred there for the
		// rest of the window, before this lookup (EdgeRateLimit).
		switch {
		case !answered:
		case a.claims == nil:
			k.refused.Put(id, a)
		case !a.claims.member():
			k.cache.Put(id, a)
		}
		return a
	})
}

// resolveIn is resolve for a request that selected an org (X-Org-Id). A key acts
// in the org it is filed in, so a selection naming that org, or none, is resolve
// itself. Any other org is IAM's to admit (keys/principal ?org=, iam
// store.HolderIn): the holder's home or an org-wide membership they act in, and
// key_org_not_open otherwise. That answer is never cached, for the reason a
// member's key is not: its authority is a membership the org can remove.
//
// An IAM that predates ?org= ignores it and answers the key's own org. That is no
// admission of the selected one, so the request keeps the key's own answer — what
// every request got before — rather than being refused for IAM's version.
func (k *iamKeys) resolveIn(ctx context.Context, key, org string) keyAnswer {
	a := k.resolve(ctx, key)
	org = strings.TrimSpace(org)
	if a.claims == nil || org == "" || org == a.claims.subjectOrg {
		return a
	}
	asked := context.WithoutCancel(ctx)
	in := k.asking.do(idOf(key+"\x00"+org), func() keyAnswer {
		b, answered := k.lookupIn(asked, key, org)
		if !answered {
			return keyAnswer{}
		}
		return b
	})
	switch {
	case in.claims == nil && in.why == "":
		// IAM did not answer: what it says about the key stands, the selection
		// is not admitted.
		return a
	case in.claims != nil && in.claims.subjectOrg != org:
		return a
	}
	return in
}

// known is the cached answer for key, or false when resolving it would ask IAM.
// An unconfigured resolver asks nobody and answers nobody, so it always knows.
func (k *iamKeys) known(key string) (keyAnswer, bool) {
	if k.auth == "" || k.base == "" || key == "" {
		return keyAnswer{}, true
	}
	return k.remembered(idOf(key))
}

// remembered is the cached answer for a key: a principal, or IAM's refusal.
func (k *iamKeys) remembered(id keyID) (keyAnswer, bool) {
	if a, ok := k.cache.Get(id); ok {
		return a, true
	}
	return k.refused.Get(id)
}

// resolveOrg resolves a PUBLISHABLE key to the org that holds it, and to nothing
// else. "" for an unknown/expired/non-publishable key or an unconfigured resolver.
//
// It shares this resolver's credential and cache shape but NOT its cache: the two
// endpoints answer different questions, and one map keyed only on the key string
// would let a pk- entry read as a principal or a sk- entry read as a bare org. The
// values are different types precisely so they cannot be confused.
func (k *iamKeys) resolveOrg(ctx context.Context, key string) string {
	if k.auth == "" || k.base == "" || key == "" {
		return ""
	}
	id := idOf(key)
	if org, ok := k.orgs.Get(id); ok {
		return org // may be a cached "" — a valid "unresolvable" answer
	}
	org, answered := k.lookupOrg(ctx, key)
	if answered {
		k.orgs.Put(id, org)
	}
	return org
}

// lookupOrg performs the authenticated keys/org call — the ORG-ONLY dual of
// keys/principal. It reads `org` and deliberately nothing else: the envelope
// carries no principal, and this function would have nowhere to put one. answered
// is false when IAM gave no answer to remember (see iamAnswer).
func (k *iamKeys) lookupOrg(ctx context.Context, key string) (org string, answered bool) {
	raw, answered := k.ask(ctx, iamKeyOrg, key)
	if !answered {
		return "", false
	}
	var env struct {
		Status string `json:"status"`
		Code   string `json:"code"`
		Data   *struct {
			Org string `json:"org"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &env) != nil {
		return "", false
	}
	if env.Status != "ok" || env.Data == nil {
		_, answered := keyRefused(env.Code)
		return "", answered
	}
	return strings.TrimSpace(env.Data.Org), true
}

// keyRefused reads a refusal IAM gave: an answer about the KEY when its code is one
// of IAM's key_* reasons (store.KeyFailure), and no answer at all otherwise — a 4xx
// that throttles cloud or refuses cloud's own credential says nothing about the key,
// so it is neither remembered for a minute nor held against the address.
func keyRefused(code string) (keyAnswer, bool) {
	code = strings.TrimSpace(code)
	if !strings.HasPrefix(code, "key_") {
		return keyAnswer{}, false
	}
	return keyAnswer{why: code}, true
}

// ask performs one authenticated key lookup at path and answers IAM's body, and
// whether IAM answered at all. A transport failure, an unreadable body, or a 5xx is
// no answer: it says something about IAM and nothing about the key, so it must not
// be remembered as the key's answer.
func (k *iamKeys) ask(ctx context.Context, path, key string, extra ...string) ([]byte, bool) {
	q := url.Values{"accessKey": {key}}
	for i := 0; i+1 < len(extra); i += 2 {
		q.Set(extra[i], extra[i+1])
	}
	u := k.base + path + "?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, false
	}
	req.Header.Set("Authorization", k.auth)
	req.Header.Set("Accept", "application/json")
	resp, err := k.http.Do(req)
	if err != nil {
		return nil, false
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil || resp.StatusCode >= http.StatusInternalServerError {
		return nil, false
	}
	return raw, true
}

// lookup performs the authenticated keys/principal call and maps the user row
// to idClaims. Any failure (unreachable, denied, unknown key) yields nil. Name is
// the username IAM's owner/name lookups parse; the subject is the row's
// `<owner>/<name>`, so userID() is that — never the bare name a holder chose.
//
// A refusal's REASON is returned beside the (nil) principal rather than discarded:
// the boundary tells the holder of an expired or revoked key so, where IAM's
// generic "the entity does not exist" would send them hunting. answered is false
// when IAM gave no answer to remember (ask).
func (k *iamKeys) lookup(ctx context.Context, key string) (keyAnswer, bool) {
	return k.lookupIn(ctx, key, "")
}

// lookupIn is lookup asking IAM whether the key's holder acts in org (resolveIn).
// An empty org is lookup.
func (k *iamKeys) lookupIn(ctx context.Context, key, org string) (keyAnswer, bool) {
	var extra []string
	if org != "" {
		extra = []string{"org", org}
	}
	raw, answered := k.ask(ctx, iamKeyPrincipal, key, extra...)
	if !answered {
		return keyAnswer{}, false
	}
	var env struct {
		Status string `json:"status"`
		// Code is IAM's reason a key resolved to nobody (store.KeyFailure).
		Code string `json:"code"`
		Data *struct {
			Owner string `json:"owner"`
			Name  string `json:"name"`
			// Org is the org the KEY acts in. It is Owner for a key minted at home;
			// a member's key names an org its holder joined by membership. An older
			// IAM omits it, which reads as Owner, the only answer it could give.
			Org     string `json:"org"`
			Email   string `json:"email"`
			IsAdmin bool   `json:"isAdmin"`
			// Scope is the KEY's own limit (iam store.UserAndScopeByAccessKey). An
			// older IAM omits it, which decodes to "" — unrestricted, the same answer
			// a key with no limit gives, so a version skew cannot silently deny.
			Scope string `json:"scope"`
			// BillingAccount names WHICH LEDGER this key spends from, and IAM answers
			// it here exactly as it answers it in a token's claim. Dropping it did not
			// leave the payer unknown — it left it WRONG: account.Payer falls out of
			// its named-account arm to the shape rule, which answers Person(org, name)
			// and never pools the signup org. For a service account that names a
			// wallet no funding path addresses, so a first-party key reads $0 beside
			// the balance it is entitled to spend, while a token for the same identity
			// reads the real one.
			//
			// An older IAM omits the field, which decodes to "" — the same answer as
			// before this was read, so a version skew changes nothing.
			BillingAccount string `json:"billing_account"`
			// Key is the key row's name: which credential this is. An older IAM
			// omits it, and the key then has no rate or spend of its own.
			Key string `json:"key"`
			// KeyOrg is the org the key is filed in, when ?org= moved its holder to
			// another Org. An IAM that predates ?org= never sends it.
			KeyOrg string `json:"keyOrg"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &env) != nil {
		return keyAnswer{}, false
	}
	if env.Status != "ok" || env.Data == nil || strings.TrimSpace(env.Data.Owner) == "" {
		return keyRefused(env.Code)
	}
	owner := strings.TrimSpace(env.Data.Owner)
	name := strings.TrimSpace(env.Data.Name)
	acts := strings.TrimSpace(env.Data.Org)
	if acts == "" {
		acts = owner
	}
	// Every key's subject is its holder's row, `<owner>/<name>` — IAM's own key for
	// a person. A member's key acts in the org it was minted in for a person whose
	// row lives elsewhere, so a consumer that resolves the caller (their roles, their
	// memberships) finds them and not a same-named stranger in the key's org.
	//
	// A home key is no different. It used to keep no subject, so its user id fell
	// through to the bare username — and a username is the holder's to choose: IAM
	// admits a UUID-shaped one, so a key minted by a user named after a colleague's
	// token `sub` WAS that colleague to everything keyed on the id, their sandboxes
	// and their sessions included. A token's `sub` is a UUID and never holds a
	// slash, an application's is marked (idClaims.userID), so this one shape is
	// disjoint from both.
	return keyAnswer{claims: &idClaims{
		Subject:           owner + "/" + name,
		Owner:             owner,
		Name:              name,
		PreferredUsername: name,
		Email:             strings.TrimSpace(env.Data.Email),
		IsAdmin:           env.Data.IsAdmin,
		// Onto the SAME field a token's `billing_account` claim lands on, so
		// renderBillingAccount mints X-Billing-Account-Id identically for a key
		// and a session. One payer, however the caller authenticated.
		BillingAccount: strings.TrimSpace(env.Data.BillingAccount),
		// The org came from the SUBJECT: IAM resolved this accessKey to a user row,
		// and that row's owner is the tenant. No application mints it and no claim
		// carries it, so it is NOT the app-selected value homeOrg exists to reject —
		// it is the same "the organization comes from the token subject" rule IAM
		// states for itself in internal/authz/authz.go. Recorded here so the identity
		// boundary can tell a KEY principal (which legitimately has no `orgs`, because
		// a machine is a member of nothing) from a HUMAN token that has merely lost
		// its claim — the latter must still fail closed.
		subjectOrg: acts,
		grant:      ParseGrant(env.Data.Scope),
		key:        strings.TrimSpace(env.Data.Key),
		keyOrg:     strings.TrimSpace(env.Data.KeyOrg),
	}}, true
}

// Cache is a tiny concurrency-safe TTL map — one generic type for every
// resolve-once-reuse-briefly lookup, so no bespoke Cache is hand-rolled per caller.
//
// BOUNDED, because the caller often picks the key: every distinct presented key, and
// every address or credential a refusal is remembered for, is an entry. Lapsed
// entries are swept on insert at most once a minute, and a map still full after a
// sweep is dropped whole — the shape the CORS proven-host Cache keeps
// (maxProvenCache). Losing an entry costs asking again, never a wrong answer.
type Cache[K comparable, V any] struct {
	ttl   time.Duration
	mu    sync.Mutex
	m     map[K]entry[V]
	swept time.Time
}

// cacheCap is the most entries one cache holds.
const cacheCap = 1 << 14

type entry[V any] struct {
	v   V
	exp time.Time
}

func NewCache[K comparable, V any](ttl time.Duration) Cache[K, V] {
	return Cache[K, V]{ttl: ttl, m: make(map[K]entry[V])}
}

func (c *Cache[K, V]) Get(k K) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[k]
	if !ok || time.Now().After(e.exp) {
		var zero V
		return zero, false
	}
	return e.v, true
}

// put stores a value for the cache's ttl. The ZERO cache is usable: its map is
// created on first write, so a partially-constructed iamKeys (any caller that names
// only the caches it cares about) records rather than panicking on a nil map. A zero
// ttl expires immediately, which is the right reading of "no ttl was configured" —
// never cache forever.
func (c *Cache[K, V]) Put(k K, v V) { c.PutUntil(k, v, time.Now().Add(c.ttl)) }

// putUntil stores a value that lapses at exp: a refusal remembered until the window
// that refused resets.
func (c *Cache[K, V]) PutUntil(k K, v V, exp time.Time) {
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = make(map[K]entry[V])
	}
	if len(c.m) >= cacheCap || now.Sub(c.swept) >= time.Minute {
		c.swept = now
		for key, e := range c.m {
			if now.After(e.exp) {
				delete(c.m, key)
			}
		}
		if len(c.m) >= cacheCap {
			c.m = make(map[K]entry[V])
		}
	}
	c.m[k] = entry[V]{v: v, exp: exp}
}
