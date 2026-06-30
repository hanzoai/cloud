package cloud

// In-binary IAM JWT validation — the trust anchor for SanitizeIdentity.
//
// This MIRRORS github.com/hanzoai/gateway/v2/iamauth, the canonical edge
// validator, but cloud deliberately does NOT import that package: iamauth lives
// in the heavyweight gateway module (KrakenD/gin/traefik) AND gateway/v2 already
// imports github.com/hanzoai/cloud, so importing it back would braid a module
// cycle and pull the gateway's whole dependency tree into cloud for ~150 lines
// of validation. The gateway remains the PRIMARY edge authority — in production
// it fronts cloud-api (universe routes.yaml). This validator is the in-binary
// defense-in-depth layer for the in-cluster / direct path, kept tiny and
// auditable on go-jose alone (already in cloud's module graph).
//
// What it enforces, exactly like iamauth.ValidateToken: signature against the
// IAM JWKS, issuer (strict), audience (allowlist, OR semantics), and expiry —
// always. A token missing the issuer is rejected.

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	gojose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

// idClaims is the subset of Hanzo IAM JWT claims the identity sanitizer needs.
// Shape mirrors iamauth.Claims so a token resolves identically at both layers.
type idClaims struct {
	jwt.Claims

	Owner             string `json:"owner"`              // org slug (the tenant)
	Name              string `json:"name"`               // display name (id fallback)
	PreferredUsername string `json:"preferred_username"` // id fallback
	Email             string `json:"email"`
	IsAdmin           bool   `json:"isAdmin"`
}

// userID resolves the canonical user id: sub, then preferred_username, then
// name. IAM may leave sub empty.
func (c *idClaims) userID() string {
	if c.Subject != "" {
		return c.Subject
	}
	if c.PreferredUsername != "" {
		return c.PreferredUsername
	}
	return c.Name
}

// jwtSigAlgs is the accepted signature-algorithm allowlist passed to
// jwt.ParseSigned (go-jose v4 requires it explicitly). RSA + ECDSA + PSS, the
// set IAM may sign with — never "none".
var jwtSigAlgs = []gojose.SignatureAlgorithm{
	gojose.RS256, gojose.RS384, gojose.RS512,
	gojose.ES256, gojose.ES384, gojose.ES512,
	gojose.PS256, gojose.PS384, gojose.PS512,
}

// identityValidator validates an IAM JWT against a cached JWKS. Issuer +
// audience + expiry are always enforced.
type identityValidator struct {
	issuer    string
	audiences []string
	cache     *jwksCache
}

// newIdentityValidator builds a validator. ttl<=0 uses the 15m JWKS default.
func newIdentityValidator(issuer, jwksURL string, audiences []string, ttl time.Duration) *identityValidator {
	return &identityValidator{
		issuer:    strings.TrimSpace(issuer),
		audiences: audiences,
		cache:     newJWKSCache(jwksURL, ttl),
	}
}

// validate parses raw, verifies its signature against the JWKS, and enforces
// issuer/audience/expiry. Returns the claims on success, an error otherwise.
func (v *identityValidator) validate(raw string) (*idClaims, error) {
	tok, err := jwt.ParseSigned(raw, jwtSigAlgs)
	if err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	keys, err := v.cache.get()
	if err != nil {
		return nil, fmt.Errorf("jwks: %w", err)
	}

	var claims idClaims
	if err := verifyAgainstKeys(tok, keys, &claims); err != nil {
		return nil, err
	}

	// Reject a missing issuer: an empty expected issuer would skip the
	// comparison and let tokens from any issuer pass.
	if claims.Issuer == "" {
		return nil, fmt.Errorf("missing issuer")
	}
	// Reject a missing expiry: ValidateWithLeeway only enforces exp when present
	// (it checks `if c.Expiry != nil`), so a token with NO exp would never expire.
	// An IAM access token always carries exp; require it.
	if claims.Expiry == nil {
		return nil, fmt.Errorf("missing expiry")
	}
	expected := jwt.Expected{Issuer: v.issuer}
	if len(v.audiences) > 0 {
		expected.AnyAudience = jwt.Audience(v.audiences)
	}
	if err := claims.Claims.ValidateWithLeeway(expected, 2*time.Minute); err != nil {
		return nil, fmt.Errorf("claims: %w", err)
	}
	return &claims, nil
}

// verifyAgainstKeys tries the kid-matched key first, then any RSA signing key —
// mirrors iamauth's selection so a token verifies the same way at both layers.
func verifyAgainstKeys(tok *jwt.JSONWebToken, keys *gojose.JSONWebKeySet, claims *idClaims) error {
	var lastErr error
	for _, h := range tok.Headers {
		if h.KeyID == "" {
			continue
		}
		for _, k := range keys.Key(h.KeyID) {
			if err := tok.Claims(k.Key, claims); err == nil {
				return nil
			} else {
				lastErr = err
			}
		}
	}
	for _, k := range keys.Keys {
		if k.Use != "sig" && k.Use != "" {
			continue
		}
		if _, ok := k.Key.(*rsa.PublicKey); !ok {
			continue
		}
		if err := tok.Claims(k.Key, claims); err == nil {
			return nil
		} else {
			lastErr = err
		}
	}
	if lastErr != nil {
		return fmt.Errorf("no matching key: %w", lastErr)
	}
	return fmt.Errorf("no matching key in JWKS")
}

// ----------------------------------------------------------------------------
// JWKS cache (TTL refresh, stale-on-error) — mirrors iamauth.JWKSCache.
// ----------------------------------------------------------------------------

type jwksCache struct {
	mu        sync.RWMutex
	keys      *gojose.JSONWebKeySet
	fetchedAt time.Time
	ttl       time.Duration
	url       string
	client    *http.Client
}

func newJWKSCache(url string, ttl time.Duration) *jwksCache {
	if ttl <= 0 {
		ttl = 15 * time.Minute
	}
	return &jwksCache{url: url, ttl: ttl, client: &http.Client{Timeout: 10 * time.Second}}
}

// get returns the cached key set, refreshing past TTL. On a fetch error with a
// previously-cached set, the stale set is returned rather than failing — a
// transient JWKS blip must not flap validation (and so admin auth) closed.
func (c *jwksCache) get() (*gojose.JSONWebKeySet, error) {
	c.mu.RLock()
	if c.keys != nil && time.Since(c.fetchedAt) < c.ttl {
		k := c.keys
		c.mu.RUnlock()
		return k, nil
	}
	c.mu.RUnlock()

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.keys != nil && time.Since(c.fetchedAt) < c.ttl {
		return c.keys, nil
	}
	keys, err := c.fetch()
	if err != nil {
		if c.keys != nil {
			return c.keys, nil
		}
		return nil, err
	}
	c.keys = keys
	c.fetchedAt = time.Now()
	return keys, nil
}

func (c *jwksCache) fetch() (*gojose.JSONWebKeySet, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url, nil)
	if err != nil {
		return nil, fmt.Errorf("request: %w", err)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read: %w", err)
	}
	var set gojose.JSONWebKeySet
	if err := json.Unmarshal(body, &set); err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	return &set, nil
}

// ----------------------------------------------------------------------------
// Token extraction — mirrors iamauth's Bearer / Basic / API-key helpers.
// ----------------------------------------------------------------------------

// isAPIKey reports whether tok is an opaque, backend-validated key (hk-/sk-/…)
// rather than a JWT, so the sanitizer skips JWT parsing for it.
func isAPIKey(tok string) bool {
	return strings.HasPrefix(tok, "hk-") ||
		strings.HasPrefix(tok, "sk-") ||
		strings.HasPrefix(tok, "pk-") ||
		strings.HasPrefix(tok, "fw_") ||
		strings.HasPrefix(tok, "hz_")
}

// bearerFromAuth extracts the token from a "Bearer <token>" header value.
func bearerFromAuth(auth string) string {
	if auth == "" {
		return ""
	}
	parts := strings.SplitN(auth, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return ""
	}
	return strings.TrimSpace(parts[1])
}

// basicFromAuth extracts the token from an HTTP Basic header value: the password
// field (the go/.netrc proxy idiom), falling back to the username when empty.
func basicFromAuth(auth string) string {
	if auth == "" {
		return ""
	}
	parts := strings.SplitN(auth, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Basic") {
		return ""
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(parts[1]))
	if err != nil {
		return ""
	}
	user, pass, ok := strings.Cut(string(raw), ":")
	if !ok {
		return ""
	}
	if pass != "" {
		return pass
	}
	return user
}
