package cloud

import (
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4/jwt"
)

// The cache exists to stop re-verifying a token that is already authenticated —
// notably per ZAP frame. These tests pin the properties that make that safe, not
// that it merely remembers things. Each one is a way the memo could become a
// second route to being trusted, which is exactly what it must never be.

func claimsExpiring(in time.Duration) *idClaims {
	c := &idClaims{Owner: "acme"}
	c.Expiry = jwt.NewNumericDate(time.Now().Add(in))
	return c
}

// A hit requires the SAME token bytes. If a different credential could ever be
// answered from another's entry, the cache would be an impersonation primitive.
func TestIdentityCache_KeyedByToken(t *testing.T) {
	c := newIdentityCache()
	want := claimsExpiring(time.Hour)
	c.put("token-a", want)

	if got := c.get("token-a", time.Now()); got != want {
		t.Fatalf("same token: got %v, want the cached claims", got)
	}
	if got := c.get("token-b", time.Now()); got != nil {
		t.Fatalf("DIFFERENT token must never hit: got %v, want nil", got)
	}
}

// Expiry is the token's own. Serving a token past its exp would extend a
// credential's life beyond what it was signed for.
func TestIdentityCache_HonoursTokenExpiry(t *testing.T) {
	c := newIdentityCache()
	c.put("tok", claimsExpiring(time.Hour))

	if c.get("tok", time.Now()) == nil {
		t.Fatal("unexpired token must hit")
	}
	if got := c.get("tok", time.Now().Add(2*time.Hour)); got != nil {
		t.Fatalf("token past its exp must MISS: got %v, want nil", got)
	}
}

// An already-expired token is never stored, so it can never be served even once.
func TestIdentityCache_RefusesAlreadyExpired(t *testing.T) {
	c := newIdentityCache()
	c.put("stale", claimsExpiring(-time.Minute))
	if got := c.get("stale", time.Now()); got != nil {
		t.Fatalf("expired-on-arrival token cached: got %v, want nil", got)
	}
}

// A token with no exp gets no invented lifetime. The boundary refuses such a
// token anyway; caching one would be the cache disagreeing with the boundary.
func TestIdentityCache_RefusesClaimsWithoutExpiry(t *testing.T) {
	c := newIdentityCache()
	c.put("no-exp", &idClaims{Owner: "acme"})
	if got := c.get("no-exp", time.Now()); got != nil {
		t.Fatalf("claims with no exp cached: got %v, want nil", got)
	}
	c.put("nil-claims", nil)
	if got := c.get("nil-claims", time.Now()); got != nil {
		t.Fatalf("nil claims cached: got %v, want nil", got)
	}
}

// Only verified claims are ever put(), so a rejected token leaves no trace and is
// re-verified every time. Pinned by construction: nothing writes on the failure
// path in validatedPrincipal.
func TestIdentityCache_MissIsTheDefault(t *testing.T) {
	c := newIdentityCache()
	if got := c.get("never-seen", time.Now()); got != nil {
		t.Fatalf("unknown token hit: got %v, want nil", got)
	}
}

// The map is bounded. A flood of distinct tokens must not grow it without limit;
// overflow costs re-verification, never admission.
func TestIdentityCache_Bounded(t *testing.T) {
	c := newIdentityCache()
	for i := 0; i < maxIdentityCacheEntries+500; i++ {
		c.put(string(rune(i%1114111))+"-"+time.Now().String()+string(rune(i)), claimsExpiring(time.Hour))
	}
	c.mu.RLock()
	n := len(c.entries)
	c.mu.RUnlock()
	if n > maxIdentityCacheEntries {
		t.Fatalf("cache grew to %d, want <= %d", n, maxIdentityCacheEntries)
	}
}

// Expired entries are reclaimed rather than pinning memory until overflow.
func TestIdentityCache_SweepsExpired(t *testing.T) {
	c := newIdentityCache()
	c.put("short", claimsExpiring(time.Millisecond))
	c.put("long", claimsExpiring(time.Hour))

	c.mu.Lock()
	c.sweepLocked(time.Now().Add(time.Second))
	n := len(c.entries)
	c.mu.Unlock()

	if n != 1 {
		t.Fatalf("after sweep len=%d, want 1 (only the unexpired entry)", n)
	}
	if c.get("long", time.Now()) == nil {
		t.Fatal("sweep dropped an unexpired entry")
	}
}
