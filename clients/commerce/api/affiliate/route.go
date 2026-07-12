package affiliate

import (
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/middleware"
	mdlaffiliate "github.com/hanzoai/cloud/clients/commerce/models/affiliate"
	"github.com/hanzoai/cloud/clients/commerce/models/contributor"
	"github.com/hanzoai/cloud/clients/commerce/util/permission"
	"github.com/hanzoai/cloud/clients/commerce/util/rest"
	"github.com/hanzoai/cloud/clients/commerce/util/router"
)

// Route registers affiliate and contributor routes.
// This builds on top of the referral base layer: revenue share,
// commissions, payouts, OSS contributor attribution.
func Route(r router.Router, args ...gin.HandlerFunc) {
	tokenRequired := middleware.TokenRequired()
	adminRequired := middleware.TokenRequired(permission.Admin)

	// mintRequired gates the treasury payout machinery on the internal service
	// token OR a platform SuperAdmin ONLY — NEVER an org-level Admin (a legacy
	// per-org access token or a gateway-minted org owner). executePayouts
	// DISBURSES real value (CreditGrants, queued Stripe transfers, on-chain HUSD);
	// calculatePayouts computes the split; the SBOM writes feed that attribution.
	// adminRequired alone let any org owner drive the treasury (latent while
	// mainnet HUSD is off, HIGH once enabled) — the same C1 org-owner-mint hole
	// PlatformOnly closes on the billing routes. Reads stay admin/token-scoped.
	mintRequired := middleware.PlatformOnly()

	// --- Affiliate CRUD + custom endpoints ---
	affiliateRest := rest.New(mdlaffiliate.Affiliate{})
	affiliateRest.Create = affiliateCreate(affiliateRest)
	affiliateRest.GET("/:affiliateid/connect", tokenRequired, middleware.Namespace(), affiliateConnect)
	affiliateRest.GET("/:affiliateid/referrals", append(args, affiliateGetReferrals)...)
	affiliateRest.GET("/:affiliateid/referrers", append(args, affiliateGetReferrers)...)
	affiliateRest.GET("/:affiliateid/orders", append(args, affiliateGetOrders)...)
	affiliateRest.GET("/:affiliateid/transactions", append(args, affiliateGetTransactions)...)
	affiliateRest.Route(r, args...)

	// --- Contributor CRUD + custom endpoints ---
	contributorRest := rest.New(contributor.Contributor{})
	contributorRest.Create = contributorCreate(contributorRest)
	contributorRest.POST("/register", tokenRequired, registerContributor)
	contributorRest.GET("/by-login/:login", tokenRequired, contributorGetByLogin)
	// Money-OUT / payout machinery: adminRequired FIRST resolves the org and
	// stamps the service-token marker + permissions; mintRequired (PlatformOnly)
	// then NARROWS to the service token / SuperAdmin. PlatformOnly must run
	// AFTER TokenRequired (it reads what TokenRequired sets), so both are chained.
	contributorRest.POST("/sbom", adminRequired, mintRequired, createSBOMEntry)
	contributorRest.GET("/sbom", adminRequired, listSBOMEntries)
	contributorRest.GET("/sbom/:sbomid", adminRequired, getSBOMEntry)
	contributorRest.PUT("/sbom/:sbomid", adminRequired, mintRequired, updateSBOMEntry)
	contributorRest.POST("/payouts/calculate", adminRequired, mintRequired, calculatePayouts)
	contributorRest.GET("/payouts/preview", adminRequired, previewPayouts)
	contributorRest.POST("/payouts/execute", adminRequired, mintRequired, executePayouts)
	contributorRest.GET("/:contributorid/earnings", tokenRequired, getEarnings)
	contributorRest.GET("/:contributorid/attributions", tokenRequired, getAttributions)
	contributorRest.Route(r, args...)
}
