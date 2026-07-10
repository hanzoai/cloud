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
	"os"
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

	Owner             string `json:"owner"`              // org slug (the org)
	Name              string `json:"name"`               // display name (id fallback)
	PreferredUsername string `json:"preferred_username"` // id fallback
	Email             string `json:"email"`
	IsAdmin           bool   `json:"isAdmin"`
}

// userID resolves the canonical user id: sub, then preferred_username, then
// name. IAM may leave sub empty. This is the STABLE identifier (a UUID when IAM
// sets sub) stamped as X-User-Id and consumed as the attribution key everywhere.
func (c *idClaims) userID() string {
	if c.Subject != "" {
		return c.Subject
	}
	if c.PreferredUsername != "" {
		return c.PreferredUsername
	}
	return c.Name
}

// username resolves the IAM USERNAME — the `name` half of the `<owner>/<name>`
// key IAM's privileged ops (mint-user-keys, get-user) parse. Prefers the `name`
// claim (IAM's canonical username, e.g. "z"), then preferred_username. It NEVER
// returns the subject: sub is a UUID, and `<owner>/<uuid>` fails IAM's
// GetOwnerAndNameFromId user lookup ("password or code is incorrect"). This is
// the distinct-from-userID() value stamped as X-User-Name so the direct-Bearer
// path builds owner/name correctly — the gateway historically minted
// X-User-Id==name, which userID() (sub-first) breaks on the in-binary path.
func (c *idClaims) username() string {
	if c.Name != "" {
		return c.Name
	}
	return c.PreferredUsername
}

// jwtSigAlgs is the accepted signature-algorithm allowlist passed to
// jwt.ParseSigned (go-jose v4 requires it explicitly). RSA + ECDSA + PSS, the
// set IAM may sign with — never "none".
var jwtSigAlgs = []gojose.SignatureAlgorithm{
	gojose.RS256, gojose.RS384, gojose.RS512,
	gojose.ES256, gojose.ES384, gojose.ES512,
	gojose.PS256, gojose.PS384, gojose.PS512,
}

// identityValidator validates an IAM JWT against a cached JWKS. Issuer (any of a
// trusted SET) + audience + expiry are always enforced.
//
// The issuer is a SET so ONE cloud binary validates every white-label brand's
// tokens (hanzo iss=hanzo.id AND lux iss=lux.id, ...). Signature verification is
// unaffected: the in-cluster IAM serves EVERY brand's signing cert in one JWKS
// (cert-hanzo/cert-lux/cert-zoo/...), keyed by the token kid, so a single
// jwksURL verifies all brands. Only the issuer-string comparison had to widen.
type identityValidator struct {
	issuers   []string
	audiences []string
	cache     *jwksCache
}

// newIdentityValidator builds a validator whose trusted-issuer set is the primary
// issuer UNIONED with every white-label brand issuer (BrandIssuers) plus any
// WHITELABEL_ISSUERS override. ttl<=0 uses the 15m JWKS default. The union is
// fail-secure: it only ADDS the known-good brand issuers, never an arbitrary one.
func newIdentityValidator(issuer, jwksURL string, audiences []string, ttl time.Duration) *identityValidator {
	return &identityValidator{
		issuers:   trustedIssuers(issuer),
		audiences: audiences,
		cache:     newJWKSCache(jwksURL, ttl),
	}
}

// kmsMachineAudSuffix is the fixed suffix of a per-org PaaS-KMS sync machine
// identity's audience. Each org's KMS sync authenticates as a dedicated,
// NON-shared IAM application named "<org>-platform-kms" (Organization=<org>,
// client_credentials grant), so IAM stamps the token's aud == the app's own
// clientId == "<org>-platform-kms" (a non-shared app's audience is its clientId,
// object/token_jwt.go tokenAudience) and owner == <org>
// (object/token_oauth.go GetClientCredentialsToken sets owner = app.Organization).
//
// That audience is, by construction, absent from CLOUD_JWT_AUDIENCES — it is
// per-org, not a fixed app — which is EXACTLY why the sync stayed pending: the
// machine token failed the audience check below, SanitizeIdentity treated it as
// anonymous, and the /v1/kms org-scope guard 403'd it before the store. The fix is
// to accept this one audience, but ONLY when it equals the token's OWN owner claim
// plus this suffix, so it certifies "the KMS sync identity for its own org" and
// grants nothing wider. Org-scoping is still enforced downstream by owner at the guard
// (owner == :org); this only lets a legitimately-minted, owner-scoped machine token
// clear validation. A per-org application means a per-org clientSecret — never
// a shared platform-wide reader, which would be a cross-org hole.
const kmsMachineAudSuffix = "-platform-kms"

// kmsMachineAudience returns the audience an org org's PaaS-KMS sync identity
// carries: "<owner>-platform-kms". An empty owner yields empty — no machine
// audience is ever granted to an org-less token (fail closed).
func kmsMachineAudience(owner string) string {
	if owner == "" {
		return ""
	}
	return owner + kmsMachineAudSuffix
}

// isKMSMachinePrincipal reports whether a validated token is a per-org KMS-sync
// machine identity: its audience set contains the owner-bound machine audience
// (<owner>-platform-kms). Such a principal is a client_credentials machine identity
// scoped to exactly one org. SanitizeIdentity uses this to DENY it global-admin
// authority even if it somehow carries isAdmin=true and owner==adminOrg, so V6's
// audience widening can never be leveraged (via an admin-org machine token) into a
// cross-org read. Its org-scoped data access is unaffected — this gates ONLY the
// admin grant, keeping the machine path decoupled from admin inside cloud (rather
// than resting on the external invariant "IAM never stamps isAdmin=true on a
// machine-aud token", which cloud cannot see or enforce).
func isKMSMachinePrincipal(claims *idClaims) bool {
	mach := kmsMachineAudience(claims.Owner)
	if mach == "" {
		return false
	}
	for _, a := range claims.Audience {
		if a == mach {
			return true
		}
	}
	return false
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

	// Reject a missing issuer: an empty issuer must never pass the set check.
	if claims.Issuer == "" {
		return nil, fmt.Errorf("missing issuer")
	}
	// Reject a missing expiry: ValidateWithLeeway only enforces exp when present
	// (it checks `if c.Expiry != nil`), so a token with NO exp would never expire.
	// An IAM access token always carries exp; require it.
	if claims.Expiry == nil {
		return nil, fmt.Errorf("missing expiry")
	}
	// Issuer must be one of the trusted brand issuers. go-jose's jwt.Expected
	// checks a SINGLE issuer, so the issuer is validated here against the set and
	// left out of Expected (audience + expiry stay with Expected).
	if !issuerAllowed(claims.Issuer, v.issuers) {
		return nil, fmt.Errorf("untrusted issuer %q", claims.Issuer)
	}
	// Audience: the static allowlist (CLOUD_JWT_AUDIENCES / brand app client_ids)
	// PLUS the per-org PaaS-KMS sync machine audience bound to THIS token's own
	// owner (<owner>-platform-kms). The machine audience is added only when the
	// allowlist is active (non-empty — always so in production) and only for the
	// token's own org, so accepting it never widens org-scoping: the /v1/kms guard still
	// gates on owner == :org. Without this, a real client_credentials machine token
	// (aud == its per-org clientId, never in the allowlist) fails here and the
	// sync silently stays pending — the activation blocker.
	expected := jwt.Expected{}
	if len(v.audiences) > 0 {
		auds := v.audiences
		if mach := kmsMachineAudience(claims.Owner); mach != "" {
			auds = append(append(make([]string, 0, len(v.audiences)+1), v.audiences...), mach)
		}
		expected.AnyAudience = jwt.Audience(auds)
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

// trustedIssuers returns the full trusted-issuer set for the in-binary validator:
// the PRIMARY issuer (the deployment's own brand, cfg.IAMIssuer) UNIONED with every
// white-label brand issuer (BrandIssuers) and any WHITELABEL_ISSUERS override
// (comma-separated). Fail-secure: it only ADDS known-good issuers; a nil/empty
// result is impossible when a primary is set, so the issuer check is always
// enforced. Duplicates are removed; order is primary-first.
func trustedIssuers(primary string) []string {
	out := make([]string, 0, 6)
	add := func(v string) {
		v = strings.TrimSpace(v)
		if v == "" {
			return
		}
		for _, e := range out {
			if e == v {
				return
			}
		}
		out = append(out, v)
	}
	add(primary)
	for _, iss := range BrandIssuers() {
		add(iss)
	}
	for _, iss := range splitTrim(os.Getenv("WHITELABEL_ISSUERS")) {
		add(iss)
	}
	return out
}

// issuerAllowed reports whether iss is one of the trusted issuers. An empty set
// (no primary, no brands — never the case in production) skips the check, matching
// the prior "empty issuer disables the check" behavior; a non-empty set is
// fail-secure (a token whose iss is not in the set is rejected).
func issuerAllowed(iss string, trusted []string) bool {
	if len(trusted) == 0 {
		return true
	}
	for _, t := range trusted {
		if iss == t {
			return true
		}
	}
	return false
}
