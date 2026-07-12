package costs

import (
	"errors"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/middleware"
	"github.com/hanzoai/cloud/clients/commerce/middleware/iammiddleware"
	"github.com/hanzoai/cloud/clients/commerce/util/bit"
	"github.com/hanzoai/cloud/clients/commerce/util/json/http"
	"github.com/hanzoai/cloud/clients/commerce/util/permission"
	"github.com/hanzoai/cloud/clients/commerce/util/router"
)

// Route registers the vendor-cost (COGS) endpoints. These are a PLATFORM
// god-view (what WE pay our vendors across the whole business), so on top of the
// route-level token gate every handler ALSO enforces requireCostsAdmin — see its
// doc for why the middleware alone is not enough.
func Route(r router.Router, args ...gin.HandlerFunc) {
	adminRequired := middleware.TokenRequired(permission.Admin)

	api := r.Group("costs")
	api.Use(adminRequired)

	api.GET("", GetCosts)
	api.GET("/margin", GetMargin)
}

// requireCostsAdmin is the in-handler gate for the vendor-cost god-view. It fails
// closed (403) unless the caller is a PLATFORM admin. It is enforced INSIDE each
// handler because the route-level middleware.TokenRequired(permission.Admin) is a
// NO-OP on the IAM path: it short-circuits (c.Next) for ANY IAM-authenticated
// request WITHOUT checking the Admin bit, and IAMTokenRequired stamps
// iam_authenticated=true whenever an X-Org-Id header is present — which the gateway
// sets on EVERY authenticated call. A handler must never trust that gate on its own.
//
// These figures are cross-tenant, PLATFORM spend (DigitalOcean + the LLM providers
// we resell) plus the whole-platform revenue/margin, so — UNLIKE the per-org money
// handlers that use middleware.RequireAdmin (org-level IsAdmin OK) — this gates on
// the STRICTER SuperAdmin predicate (see auth.IAMClaims.SuperAdmin: "the ONLY
// predicate safe to gate cross-org/superadmin actions on"). An org owner carries
// IsAdmin=true within their own org and the gateway mints permission.Admin from it
// (edgeauth.permsHeader), so NEITHER the Admin permission bit NOR IsAdmin may admit
// an IAM user here — only SuperAdmin.
//
// Two ways in, fail-closed:
//  1. A JWT-verified SuperAdmin (isSuperAdmin claim, or the built-in admin org).
//  2. The trusted M2M service token — the console's OWN SuperAdmin-gated proxy
//     (console → commerce) forwards with COMMERCE_SERVICE_TOKEN and X-Org-Id, but
//     NO user identity. accesstoken.go authorizes that token and sets
//     permission.Admin; it carries no IAM user (empty Subject). An IAM user ALWAYS
//     carries a Subject (the JWT sub the gateway mints alongside any permission),
//     so "Admin bit AND no IAM Subject" uniquely identifies the verified service
//     token and can never be an org admin. The console proxy is the party that
//     already enforced SuperAdmin (getAdminGate) before it ever reached here.
//
// Reads c["permissions"] without MustGet so a handler mounted without the token
// gate fails closed (403) rather than panicking (500).
func requireCostsAdmin(c *gin.Context) bool {
	claims := iammiddleware.GetIAMClaims(c) // non-nil by contract
	if claims.IsSuperAdmin() {
		return true
	}
	// Trusted M2M service token: Admin bit present AND no IAM user identity.
	if claims.Subject == "" {
		if v, ok := c.Get("permissions"); ok {
			if f, ok := v.(bit.Field); ok && f.Has(permission.Admin) {
				return true
			}
		}
	}
	http.Fail(c, 403, "platform admin required to read vendor costs", errors.New("caller is not a SuperAdmin"))
	return false
}

// GetCosts returns the per-vendor COGS breakdown for a period.
//
//	GET /v1/costs?period=YYYY-MM
//
// Response: {period, vendors:[{vendor,service,amountCents,period,source,note}], totalCents, currency}.
func GetCosts(c *gin.Context) {
	if !requireCostsAdmin(c) {
		return
	}
	org := middleware.GetOrganization(c)
	ctx := org.Namespaced(c)
	p := period(c.Query("period"))

	report, _ := buildReport(ctx, org.TestMode(), p)
	c.JSON(200, report)
}

// GetMargin returns revenue (the usage ledger) minus COGS (this package) plus the
// gross-margin percentage — the MARGIN view for admin.hanzo.ai.
//
//	GET /v1/costs/margin?period=YYYY-MM
//
// Revenue is the sum of api-usage charges in the period (what customers paid);
// COGS is the vendor total. Kept DRY with GetCosts: buildReport returns both the
// vendor lines and the ledger aggregate, so revenue and cost come from one walk.
func GetMargin(c *gin.Context) {
	if !requireCostsAdmin(c) {
		return
	}
	org := middleware.GetOrganization(c)
	ctx := org.Namespaced(c)
	p := period(c.Query("period"))

	report, ledger := buildReport(ctx, org.TestMode(), p)

	revenue := ledger.RevenueCents
	cogs := report.TotalCents
	margin := revenue - cogs

	c.JSON(200, MarginReport{
		Period:         p,
		RevenueCents:   revenue,
		COGSCents:      cogs,
		MarginCents:    margin,
		GrossMarginPct: grossMarginPct(revenue, margin),
		Vendors:        report.Vendors,
		Currency:       "usd",
	})
}
