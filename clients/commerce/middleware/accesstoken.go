package middleware

import (
	"encoding/base64"
	"errors"
	"os"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/config"
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/middleware/iammiddleware"
	"github.com/hanzoai/cloud/clients/commerce/middleware/svcorg"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/util/bit"
	"github.com/hanzoai/cloud/clients/commerce/util/json/http"
	"github.com/hanzoai/cloud/clients/commerce/util/permission"
	"github.com/hanzoai/cloud/clients/commerce/util/session"
)

func splitAuthorization(fieldValue string) (string, string) {
	parts := strings.Split(fieldValue, " ")
	if len(parts) == 1 {
		return "", parts[0]
	}
	return parts[0], parts[1]
}

func accessTokenFromHeader(fieldValue string) string {
	method, accessToken := splitAuthorization(fieldValue)
	if method == "Basic" {
		bytes, _ := base64.StdEncoding.DecodeString(accessToken)
		return strings.Split(string(bytes), ":")[0]
	}
	return accessToken
}

func ParseToken(c *gin.Context) {
	query := c.Request.URL.Query()

	// Check for `key` query param by default
	accessToken := query.Get("key")

	// Fallback to `token` query param
	if accessToken == "" {
		accessToken = query.Get("token")
	}

	// Try to grab key from Authorization header (basic auth)
	if accessToken == "" {
		accessToken = accessTokenFromHeader(c.Request.Header.Get("Authorization"))
	}

	// If it's still not set and dev server is running, grab from session
	if accessToken == "" && config.IsDevelopment {
		value, _ := session.Get(c, "access-token")
		if tokstr, ok := value.(string); ok {
			accessToken = tokstr
		}
	}

	c.Set("access-token", accessToken)
}

// Permissions required to access route
func TokenPermits(masks ...bit.Mask) gin.HandlerFunc {
	// Any permissions acceptable by default (i.e., only valid token required)
	permissions := permission.All

	// Any arguments passed will be used as new permissions
	if len(masks) > 0 {
		permissions = permission.None
		for _, mask := range masks {
			permissions |= mask
		}
	}

	return func(c *gin.Context) {
		// Verify permissions
		if !GetPermissions(c).Has(permissions) {
			http.Fail(c, 403, "Token doesn't support this scope", errors.New("Token doesn't support this scope"))
		}
	}
}

// ensureIAMOrg resolves the request's org from the VALIDATED IAM identity (the
// gateway-minted X-Org-Id) and sets the "organization" context key — so commerce
// derives the org from IAM, the ONE org/user/auth authority, never its own
// Organization table and never requiring commerce-side pre-provisioning. It is
// idempotent (a no-op when iammiddleware already resolved the org upstream) and
// uses the SAME cached GetOrCreate resolver as the service-token path, so an IAM
// principal's first request auto-projects a thin billing record keyed by the IAM
// org. No org header, or a bearer-shaped selector, leaves the key unset (the
// handler's GetOrganizationOK degrades cleanly rather than seeding a bogus org).
func ensureIAMOrg(c *gin.Context) {
	if _, ok := c.Get("organization"); ok {
		return
	}
	orgName := strings.TrimSpace(c.GetHeader("X-Org-Id"))
	if orgName == "" || organization.IsSecretLikeName(orgName) {
		return
	}
	org, err := svcorg.Resolve(orgName)
	if err != nil {
		log.Warn("TokenRequired: IAM org resolve for '%s' failed: %v", orgName, err)
		return
	}
	// Per-request Live view on a COPY (the resolver returns a shared cached org).
	reqOrg := *org
	reqOrg.Live = !strings.EqualFold(strings.TrimSpace(c.GetHeader("X-Hanzo-Test")), "true")
	c.Set("organization", &reqOrg)
	c.Set("active-organization", reqOrg.Id())
}

// Parses token, default permissions check
func TokenRequired(masks ...bit.Mask) gin.HandlerFunc {
	// Any permissions acceptable by default (i.e., only valid token required)
	permissions := permission.All

	// Any arguments passed will be used as new permissions
	if len(masks) > 0 {
		permissions = permission.None
		for _, mask := range masks {
			permissions |= mask
		}
	}

	return func(c *gin.Context) {
		// IAM/gateway identity path. IAMTokenRequired has already set
		// c["permissions"] from the gateway-minted X-User-Permissions (0 when
		// absent — fail closed). Enforce the SAME masks the legacy and
		// service-token paths enforce: a bare c.Next() here made every masked gate
		// (e.g. TokenRequired(Admin, Published) on the checkout money routes) a
		// NO-OP for ANY IAM-authenticated principal, so a low-privilege or forged
		// (perms=0) IAM caller reached the money handlers. No masks
		// (TokenRequired()) still means "any authenticated principal" — the billing
		// read path is unchanged; with masks the caller must actually hold them.
		if iammiddleware.IsIAMAuthenticated(c) {
			if len(masks) == 0 || hasScope(c, permissions) {
				// Commerce derives the org from IAM, never its own table: resolve the
				// validated X-Org-Id via the SAME cached GetOrCreate resolver the
				// service-token path uses, auto-projecting a thin billing record so an
				// IAM principal's org "just works" with no commerce-side provisioning.
				// Idempotent — a no-op when iammiddleware already resolved it upstream.
				ensureIAMOrg(c)
				c.Next()
				return
			}
			http.Fail(c, 403, "Token doesn't support this scope",
				errors.New("IAM principal lacks required permission scope"))
			return
		}

		// Service token: shared secret for service-to-service calls (e.g., cloud-api → commerce).
		// Set COMMERCE_SERVICE_TOKEN env var on both the caller and Commerce.
		if svcToken := os.Getenv("COMMERCE_SERVICE_TOKEN"); svcToken != "" {
			header := c.GetHeader("Authorization")
			if strings.HasPrefix(header, "Bearer ") && strings.TrimPrefix(header, "Bearer ") == svcToken {
				// Resolve the caller-named org, now that the service token is verified.
				// EdgeAuth (standalone edge) stashes the client's requested org in a
				// private ctx key AFTER stripping the raw X-Org-Id header — read it
				// here so an unvalidated token's selector never reached resolution.
				// Fallbacks: the raw X-Org-Id header (gateway / EdgeAuth-off
				// deployments, where the private key is unset and the header is
				// trustworthy), then COMMERCE_SERVICE_ORG, then the default "hanzo".
				orgName := clientOrgFromContext(c)
				if orgName == "" {
					orgName = c.GetHeader("X-Org-Id")
				}
				if orgName == "" {
					orgName = os.Getenv("COMMERCE_SERVICE_ORG")
				}
				if orgName == "" {
					orgName = "hanzo"
				}
				// A verified service token authorizes provisioning, but the org
				// SELECTOR still comes from the caller (stashed client org /
				// X-Org-Id). Never let a bearer-shaped selector become an org: a
				// caller forwarding a raw API key here would otherwise seed an
				// org named after the key (incident 2026-07-02). Reject, don't create.
				if organization.IsSecretLikeName(orgName) {
					log.Warn("TokenRequired: service-token org selector is bearer-shaped; refusing to provision")
					http.Fail(c, 400, "Invalid organization identifier.", errors.New("bearer-shaped org selector"))
					return
				}

				// Resolve via the CACHED, singleflighted, deadline-detached resolver
				// (middleware/svcorg). Steady state (org already exists) does NO
				// datastore work; a cache miss runs ONE GetOrCreate on commerce's own
				// bounded context, so a busy SQLite writer fast-fails here instead of
				// being canceled mid-create by the caller's request deadline. This is
				// the money-critical fix for the 2026-07-04 wedge: the create-on-every-
				// request read/write that piled behind the single writer under load and
				// cascaded to a bogus 401 → cloud-api 402 outage.
				org, err := svcorg.Resolve(orgName)
				if err != nil {
					// The service token WAS verified — this is a transient backing-store
					// failure, NOT a bad credential. Fail CLOSED but RETRYABLE (503), and
					// do NOT fall through to the legacy per-org-token path: that path would
					// Peek the 64-hex service token as a JWT ("Invalid Segments") and 401,
					// masking a retryable hiccup as an auth failure (the old cascade).
					log.Warn("TokenRequired: service-token org resolve for '%s' failed: %v", orgName, err)
					http.Fail(c, 503, "Billing service temporarily unavailable; retry.", err)
					return
				}

				// Service callers are live by default. An explicit
				// X-Hanzo-Test: true opts the call into TEST mode so it
				// charges sandbox processors and writes the test ledger —
				// the same Live=false semantics a test access token carries.
				// Existing callers omit the header and stay live; the flag
				// is additive and never widens scope.
				// Positively mark this request as an internal service caller —
				// the ONE signal that survives EdgeAuth's header strip and
				// distinguishes the trusted platform service (cloud-api →
				// commerce, holding COMMERCE_SERVICE_TOKEN) from an org-scoped
				// principal that merely holds the Admin bit (an org owner's IAM
				// isAdmin, or a legacy per-org access token). Money-MINT routes
				// (PlatformOnly) gate on this + SuperAdmin ONLY, never on Admin,
				// so an org owner can never reach them. Set in BOTH the test and
				// live sub-branches (once here, before the split).
				c.Set(ctxKeyServiceToken, true)
				// Live/Test is a per-REQUEST view. The resolver returns a SHARED,
				// cached *Organization, so never mutate its Live in place — copy it
				// per request and set Live on the copy. This keeps steady-state
				// datastore-free while guaranteeing a concurrent test call can't flip
				// the cached org's Live for live callers (or vice-versa).
				reqOrg := *org
				test := strings.EqualFold(strings.TrimSpace(c.GetHeader("X-Hanzo-Test")), "true")
				reqOrg.Live = !test
				if test {
					c.Set("permissions", bit.Field(permission.Admin|permission.Test))
				} else {
					c.Set("permissions", bit.Field(permission.Admin|permission.Live))
				}
				c.Set("organization", &reqOrg)
				c.Next()
				return
			}
		}

		// Parse token
		ParseToken(c)

		// Try to fetch access token
		accessToken := GetAccessToken(c)

		// Bail if we still don't have an access token
		if accessToken == "" {
			http.Fail(c, 401, "No access token provided.", errors.New("No access token provided"))
			return
		}

		ctx := GetContext(c)
		db := datastore.New(ctx)
		org := organization.New(db)

		// Try to find organization using access token
		tok, err := org.GetWithAccessToken(accessToken)
		if err != nil {
			http.Fail(c, 401, "Unable to retrieve organization associated with access token: "+err.Error(), err)
			return
		}

		// Verify token signature
		if ok, err := tok.Verify(org.SecretKey); !ok {
			log.Error("Org '%s' == '%s', Token '%s' == '%s', Verify error '%s'", org.Id(), tok.Subject, tok.String, accessToken, err, ctx)
			http.Fail(c, 403, "Unable to verify token.", err)
			return
		}

		// Verify permissions
		if !tok.HasPermission(permissions) {
			http.Fail(c, 403, "Token doesn't support this scope", errors.New("Token doesn't support this scope"))
		}

		// Whether or not we can make live calls
		org.Live = tok.HasPermission(permission.Live)

		// Save organization in context
		c.Set("permissions", tok.Permissions)
		c.Set("organization", org)
		c.Set("token", tok)
	}
}

func GetAccessToken(c *gin.Context) string {
	tok, ok := c.Get("access-token")
	if !ok {
		return ""
	}

	return tok.(string)
}

func GetPermissions(c *gin.Context) bit.Field {
	return c.MustGet("permissions").(bit.Field)
}

// ctxKeyServiceToken marks that TokenRequired verified this request's bearer
// against COMMERCE_SERVICE_TOKEN — the internal service-to-service secret
// (cloud-api → commerce). It is the ONLY positive signal that a caller is the
// trusted platform service rather than an org-scoped principal that merely holds
// the Admin bit: an org OWNER carries org-level IsAdmin (→ Admin|Live) within
// their own org, and a legacy per-org access token can hold Admin too. The
// money-MINT gate (PlatformOnly, middleware/platformonly.go) reads it so those
// routes admit the service token + platform superadmins ONLY. Set EXCLUSIVELY
// inside the verified-bearer block, never on any other path.
const ctxKeyServiceToken = "service_token_verified"

// IsServiceToken reports whether TokenRequired verified this request's bearer
// against COMMERCE_SERVICE_TOKEN. Fail-closed: a missing or non-bool value → false.
// It is a POSITIVE, source-recorded fact (set where the secret is checked), never
// re-derived from indirect signals like the Admin bit or absence of IAM identity —
// a legacy org access token is also "not IAM authenticated" yet must NOT pass the
// money-mint gate.
func IsServiceToken(c *gin.Context) bool {
	if v, ok := c.Get(ctxKeyServiceToken); ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	return false
}

// hasScope reports whether the request's resolved permissions (set by
// IAMTokenRequired for the IAM path, or the service-token branch) include the
// required mask. It reads c["permissions"] defensively (no MustGet) so a gate
// mounted without a permission-setting middleware fails CLOSED (false → 403)
// instead of panicking. bit.Field.Has is intersection semantics, so a combined
// mask like Admin|Published is satisfied by holding EITHER bit.
func hasScope(c *gin.Context, need bit.Mask) bool {
	v, ok := c.Get("permissions")
	if !ok {
		return false
	}
	f, ok := v.(bit.Field)
	return ok && f.Has(need)
}
