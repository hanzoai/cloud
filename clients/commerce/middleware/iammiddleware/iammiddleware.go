// Copyright © 2026 Hanzo AI. MIT License.

// Package iammiddleware is the gateway-trust shim for legacy call
// sites. It used to do JWKS fetch + JWT validation in-binary (293
// LOC). That trust boundary is now hanzoai/gateway: gateway validates
// the JWT, populates X-Org-Id / X-User-Id / X-User-Email, and only
// gateway-routed traffic reaches commerced.
//
// This file preserves the public API the rest of commerce depends on
// (Init, InitKV, Client, IAMTokenRequired, IsIAMAuthenticated,
// GetIAMClaims, GetIAMTier) so the 13 call sites compile, but every
// function reads identity from the gateway-supplied headers via
// pkg/auth.
//
// Deletion target: once all call sites migrate to pkg/auth + pkg/org,
// this file can be removed wholesale.
package iammiddleware

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/auth"
	"github.com/hanzoai/cloud/clients/commerce/billing/trial"
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	pkgAuth "github.com/hanzoai/cloud/clients/commerce/pkg/auth"
	"github.com/hanzoai/cloud/clients/commerce/pkg/org"
	"github.com/hanzoai/cloud/clients/commerce/util/bit"
	jsonhttp "github.com/hanzoai/cloud/clients/commerce/util/json/http"
)

// KVCache mirrors the pkg/org KVCache interface so existing wiring
// (commerce.go: iammiddleware.InitKV(kv)) keeps working unchanged.
type KVCache = org.KVCache

var (
	mu          sync.RWMutex
	initialized bool
	iamClient   *auth.IAMClient
)

// Init builds the IAM client used by the directly-exposed commerce-api
// edge — the surfaces that face a raw user Bearer JWT instead of
// gateway-minted headers: EdgeAuth (middleware/edgeauth.go) and the
// /admin/billing UI gate (billing/handler.go). It verifies tokens
// against the IAM JWKS. The gateway-trust middleware chain
// (IAMTokenRequired) is unchanged — it still reads validated headers.
//
// A nil config or a build error leaves the client nil, so those edge
// surfaces fail closed (every request treated as unauthorized).
func Init(cfg *auth.IAMConfig) error {
	mu.Lock()
	defer mu.Unlock()
	initialized = true
	if cfg == nil {
		iamClient = nil
		return nil
	}
	cl, err := auth.NewIAMClient(cfg)
	if err != nil {
		iamClient = nil
		return err
	}
	iamClient = cl
	return nil
}

// InitKV wires the KV cache used by org-id resolution.
func InitKV(kv KVCache) { org.Bind(kv) }

// Client returns the initialized IAM client, or nil if IAM is disabled or
// Init() has not been called. Consumers outside the middleware chain (e.g.
// SPA handlers with their own auth gate) use this to validate bearer tokens
// against the same JWKS the /v1 middleware uses. Fail-closed: a nil return
// means "treat every request as unauthenticated".
//
// Returns the client built by Init from the IAM config, or nil when IAM
// is disabled / misconfigured (fail-closed at the call site). This is the
// non-gateway entry point hook: commerce-api.hanzo.ai is exposed directly
// (not behind hanzoai/gateway), so its edge surfaces verify the JWT here.
func Client() *auth.IAMClient {
	mu.RLock()
	defer mu.RUnlock()
	return iamClient
}

// orgCacheKey returns the KV key for an IAM owner → org ID mapping.
func orgCacheKey(owner string) string {
	return "iam:org_by_name:" + owner
}

// IAMTokenRequired returns a Gin middleware that:
//  1. Reads the gateway-supplied X-Org-Id / X-User-Id / X-User-Email
//     headers (already JWT-validated upstream).
//  2. Resolves the Organization via pkg/org.Resolve (KV-cached).
//  3. Sets the legacy gin context keys downstream handlers expect:
//     iam_authenticated, iam_org, iam_user_id, iam_email,
//     organization, active-organization, permissions.
//
// Missing headers: falls through (handler chain may use a legacy
// org-token instead). The gateway is the trust boundary; commerced is
// only reachable via the gateway in production, where COMMERCED_REQUIRE_IDENTITY
// rejects header-less requests at the edge of the binary.
func IAMTokenRequired() gin.HandlerFunc {
	return func(c *gin.Context) {
		// pkg/auth.Gin has already attached headers to ctx; read from there.
		// If pkg/auth.Gin wasn't installed, fall back to direct header reads
		// so this middleware still works in legacy mounts.
		ctx := c.Request.Context()
		ownerID := pkgAuth.OrgID(ctx)
		userID := pkgAuth.UserID(ctx)
		email := pkgAuth.UserEmail(ctx)
		if ownerID == "" {
			ownerID = c.GetHeader(pkgAuth.HeaderOrgID)
		}
		if userID == "" {
			userID = c.GetHeader(pkgAuth.HeaderUserID)
		}
		if email == "" {
			email = c.GetHeader(pkgAuth.HeaderUserEmail)
		}

		if ownerID == "" {
			// No identity headers — fall through to legacy auth.
			c.Next()
			return
		}

		// Bound DB context — request ctx may be canceled by a navigation.
		dbCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		o, err := org.Resolve(dbCtx, ownerID)
		if err != nil {
			log.Warn("iammiddleware: org resolve failed for %q: %v", ownerID, err)
			jsonhttp.Fail(c, http.StatusServiceUnavailable,
				"Unable to resolve organization: "+err.Error(), err)
			return
		}

		// Best-effort new-signup on-ramp on first encounter: a 7-day trial of
		// the $20 plan funded with one unified credit (billing/trial). Billing
		// is per-org, so it MUST be keyed to the org slug (the same key
		// GetMyBalance reads and the gateway gate debits) inside the org's
		// namespace. New-signup only + idempotent: existing/comped orgs (any
		// prior subscription or deposit) are left untouched. A card added later
		// extends the trial to 30 days (see api/billing.CreatePaymentMethod).
		orgSlug := strings.ToLower(strings.TrimSpace(ownerID))
		signupIsTest := !liveFromHeaders(c)
		nsCtx := o.Namespaced(context.Background())
		go func() {
			_, _ = trial.Start(datastore.New(nsCtx), orgSlug, false, signupIsTest)
		}()

		// Gateway-trusted identity counts as live by default. An explicit
		// gateway-propagated X-Hanzo-Test: true opts the request into TEST
		// mode (org.Live=false) so charges hit sandbox processors and write
		// the test ledger — the same Live=false semantics a test access
		// token carries (see middleware/accesstoken.go). The gateway is the
		// trust boundary: it only forwards this header for test orgs. The
		// flag is additive and never widens scope (test is strictly less
		// privileged than live).
		o.Live = liveFromHeaders(c)

		// Permissions are derived strictly from gateway-supplied headers,
		// never granted by mere presence of identity. The gateway MUST
		// mint X-User-Permissions from the validated JWT (see
		// hanzoai/gateway/auth_middleware.go and HEADERS.md). If the
		// header is absent we fail closed: zero permissions, no Admin,
		// no Live. The gateway is the trust boundary; this binary
		// trusts the bits it provides and nothing else.
		perms := parsePermissionsHeader(c.GetHeader(HeaderUserPermissions))

		// Mirror onto Gin keys for legacy handlers.
		c.Set("iam_authenticated", true)
		c.Set("iam_user_id", userID)
		c.Set("iam_email", email)
		c.Set("iam_org", ownerID)
		c.Set("organization", o)
		c.Set("active-organization", o.Id())
		c.Set("permissions", perms)

		c.Next()
	}
}

// HeaderTest is the gateway-propagated test-mode signal. When the
// gateway forwards "true" (only for test orgs), the request runs in
// TEST mode: org.Live=false, charges hit sandbox processors, ledger
// rows are flagged Test. Absent/any-other value ⇒ live. Mirrors the
// X-Hanzo-Test semantics in middleware/accesstoken.go. The gateway is
// the trust boundary; commerced trusts the bit it forwards.
const HeaderTest = "X-Hanzo-Test"

// liveFromHeaders reports whether a gateway-trusted request is live.
// It is live unless the gateway forwarded HeaderTest == "true". This
// is the single place that turns the gateway test signal into the
// org.Live authority that payment.ProcessorsForOrg keys sandbox-vs-
// production off of (see payment/orgsetup.go SquareConfig(!org.Live)).
// Forwards-only: never widen test → live based on identity presence.
func liveFromHeaders(c *gin.Context) bool {
	return !strings.EqualFold(strings.TrimSpace(c.GetHeader(HeaderTest)), "true")
}

// HeaderUserPermissions is the canonical gateway-minted permission
// header. It carries the bit.Field value as a base-10 int64 string
// (e.g. "3" for Live|Test). The gateway MUST set it from the
// validated JWT roles/claims; commerced reads it as-is. Missing or
// malformed values fail closed (zero permissions). Documented in
// HEADERS.md.
const HeaderUserPermissions = "X-User-Permissions"

// parsePermissionsHeader converts the gateway-minted X-User-Permissions
// value into a bit.Field. Empty or invalid input fails closed (zero
// permissions). This is the only path that turns gateway intent into
// commerced permissions; do not introduce defaults that grant rights
// based on identity presence alone.
func parsePermissionsHeader(v string) bit.Field {
	v = strings.TrimSpace(v)
	if v == "" {
		return bit.Field(0)
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 0 {
		return bit.Field(0)
	}
	return bit.Field(n)
}

// IsIAMAuthenticated reports whether a VALIDATED IAM identity is attached to the
// request — i.e. IAMTokenRequired resolved the org from the trusted (gateway- or
// EdgeAuth-minted, post-strip) X-Org-Id and set iam_authenticated. It does NOT
// fall back to the raw X-Org-Id header: trusting mere header presence let an
// unvalidated opaque bearer + a client X-Org-Id impersonate any org (the checkout
// money-surface bypass). IAMTokenRequired runs on every /v1, /v1/commerce and
// /_/commerce group, so the gin-key check covers every legitimate IAM caller; an
// opaque service token authorizes through TokenRequired's service-token branch
// instead, never here.
func IsIAMAuthenticated(c *gin.Context) bool {
	if v, ok := c.Get("iam_authenticated"); ok {
		if b, ok := v.(bool); ok && b {
			return true
		}
	}
	return false
}

// GetIAMClaims returns a non-nil *auth.IAMClaims populated from the
// gateway-minted identity headers. The gateway validated the JWT and
// stamped X-Org-Id, X-User-Id, X-User-Email, X-User-IsAdmin, X-Roles
// (see hanzoai/gateway/auth_middleware.go). commerced trusts those
// bits and reflects them into a claims struct so call sites can read
// IsAdmin / Owner / Subject / Roles uniformly.
//
// Fail-closed contract: missing headers map to zero-valued fields. In
// particular, missing X-User-IsAdmin yields IsAdmin=false (not "unknown").
// Call sites MUST NOT nil-guard the return — it is always non-nil.
//
// The legacy in-test path stores a *auth.IAMClaims under the
// "iam_claims" gin key; that wins when present so tests can inject
// arbitrary claim shapes without going through HTTP.
func GetIAMClaims(c *gin.Context) *auth.IAMClaims {
	if c == nil {
		return &auth.IAMClaims{}
	}
	if v, ok := c.Get("iam_claims"); ok {
		if claims, ok := v.(*auth.IAMClaims); ok && claims != nil {
			return claims
		}
	}
	owner := strings.TrimSpace(c.GetHeader(pkgAuth.HeaderOrgID))
	user := strings.TrimSpace(c.GetHeader(pkgAuth.HeaderUserID))
	email := strings.TrimSpace(c.GetHeader(pkgAuth.HeaderUserEmail))
	isAdmin := strings.EqualFold(strings.TrimSpace(c.GetHeader(HeaderUserIsAdmin)), "true")
	// HOME org — the identity + platform-sudo anchor (the gateway/EdgeAuth
	// X-User-Owner header, the validated JWT `owner`). DISTINCT from Owner (==
	// X-Org-Id, the EFFECTIVE org a SuperAdmin may switch to another tenant):
	// IsSuperAdmin gates on this, so owner=="admin" survives an org-switch. Absent
	// (pre-rollout) → homeOrg() falls back to Owner, which equals home for a
	// non-switching caller. No spoofable isSuperAdmin boolean is read — the org
	// IS the signal.
	homeOrg := strings.TrimSpace(c.GetHeader(HeaderUserOwner))
	roles := parseRolesHeader(c.GetHeader(HeaderRoles))

	claims := &auth.IAMClaims{
		Owner:   owner,
		HomeOrg: homeOrg,
		Name:    user,
		Email:   email,
		IsAdmin: isAdmin,
		Roles:   roles,
	}
	// Subject is the canonical user id field IAMClaims callers read;
	// the gateway puts the JWT sub into X-User-Id.
	claims.Subject = user
	return claims
}

// HeaderUserIsAdmin is the gateway-minted "true"/"" ORG-level admin flag
// (set for an org owner). Only "true" (case-insensitive) is treated as admin;
// any other value (including absent) fails closed to false. It is org-scoped
// only — NEVER gate cross-org/superadmin actions on it; use IsSuperAdmin().
const HeaderUserIsAdmin = "X-User-IsAdmin"

// HeaderUserOwner is the gateway/EdgeAuth-minted HOME-org header — the validated
// JWT `owner`, the identity + platform-sudo anchor. DISTINCT from X-Org-Id (the
// EFFECTIVE org a SuperAdmin can switch to another tenant): IsSuperAdmin gates on
// this so owner=="admin" survives an org-switch. Replaces the removed
// X-User-IsSuperAdmin boolean — the org IS the signal, not a spoofable flag.
const HeaderUserOwner = "X-User-Owner"

// HeaderRoles is the canonical comma-joined role-name header set by
// the gateway from the JWT roles claim. Empty value -> no roles.
const HeaderRoles = "X-Roles"

// parseRolesHeader splits the X-Roles comma list into a FlexRoles
// slice, trimming whitespace and dropping empties.
func parseRolesHeader(v string) auth.FlexRoles {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	parts := strings.Split(v, ",")
	out := make(auth.FlexRoles, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// GetIAMTier returns "" — tier is no longer derived in-binary. The
// gateway can attach an X-Tier header in a future iteration if needed.
func GetIAMTier(_ *gin.Context) string { return "" }

// orgFromContext is exported for tests that want to assert the legacy
// gin "organization" key was populated correctly.
func orgFromContext(c *gin.Context) *organization.Organization {
	if v, ok := c.Get("organization"); ok {
		if o, ok := v.(*organization.Organization); ok {
			return o
		}
	}
	return nil
}

var _ = orgFromContext // referenced in tests
