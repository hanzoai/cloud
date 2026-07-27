package cloud

import (
	"crypto/sha256"
	"sync"
	"time"
)

// identity_cache.go — parse an IAM access token ONCE, then never again while it
// is still valid.
//
// The identity boundary (SanitizeIdentity → validatedPrincipal) verifies a JWT on
// every inbound request. For REST that is once per request, which is the cost of
// the boundary and fine. For ZAP it was not: a WebSocket connection authenticates
// at upgrade, and every frame on it was replayed as an in-process HTTP request, so
// the SAME token was signature-verified again for every single call on an
// already-authenticated socket. A chatty client paid an RSA verification per frame.
//
// This memoizes the pure part — token ⇒ claims — and nothing else:
//
//   - The KEY is the token. A hit is impossible without presenting the same bytes
//     that validated, so the cache adds NO new way to be trusted. This is the whole
//     safety argument, and it is why the cache holds no identity that was not
//     produced by the one validator.
//   - Only SUCCESSES are stored. A rejected token is re-verified every time, so a
//     garbage or expired credential can never be answered from memory.
//   - Entries die at the TOKEN'S OWN exp, never later. The boundary's expiry
//     semantics are unchanged: a token expires at exactly the moment it did before.
//   - Revocation is NOT weakened, because JWT validation never consulted a
//     revocation list. A revoked-but-unexpired token was accepted before this and
//     is accepted now, for the same bounded window.
//
// The key is a SHA-256 of the token rather than the token itself: fixed size, and
// a credential never sits in a map key where a dump or a stray log could surface it.
type identityCache struct {
	mu      sync.RWMutex
	entries map[[32]byte]identityEntry
}

type identityEntry struct {
	claims *idClaims
	expiry time.Time
}

// maxIdentityCacheEntries bounds the map so a flood of distinct tokens cannot grow
// it without limit. On overflow the cache is swept of expired entries first, and
// only cleared wholesale if that frees nothing — a clear costs re-verification,
// never admission.
const maxIdentityCacheEntries = 8192

func newIdentityCache() *identityCache {
	return &identityCache{entries: make(map[[32]byte]identityEntry)}
}

// get returns the cached claims for token when they were verified and have not
// expired. A miss returns nil, and the caller verifies for real.
func (c *identityCache) get(token string, now time.Time) *idClaims {
	k := sha256.Sum256([]byte(token))
	c.mu.RLock()
	e, ok := c.entries[k]
	c.mu.RUnlock()
	if !ok || !now.Before(e.expiry) {
		return nil
	}
	return e.claims
}

// put records a VERIFIED token's claims until the token's own expiry. Claims with
// no usable expiry are not cached: the boundary already refuses a token with no
// exp, and caching one would invent a lifetime the token never had.
func (c *identityCache) put(token string, claims *idClaims) {
	if claims == nil || claims.Expiry == nil {
		return
	}
	exp := claims.Expiry.Time()
	if !time.Now().Before(exp) {
		return
	}
	k := sha256.Sum256([]byte(token))

	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) >= maxIdentityCacheEntries {
		c.sweepLocked(time.Now())
		if len(c.entries) >= maxIdentityCacheEntries {
			c.entries = make(map[[32]byte]identityEntry)
		}
	}
	c.entries[k] = identityEntry{claims: claims, expiry: exp}
}

// sweepLocked drops every entry whose token has expired. Callers hold c.mu.
func (c *identityCache) sweepLocked(now time.Time) {
	for k, e := range c.entries {
		if !now.Before(e.expiry) {
			delete(c.entries, k)
		}
	}
}
