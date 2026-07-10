package billing

import (
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/middleware"
	"github.com/hanzoai/cloud/clients/commerce/payment"
	jsonhttp "github.com/hanzoai/cloud/clients/commerce/util/json/http"
)

// GetPaymentConfig returns the PUBLIC Square config (application id, location id,
// environment) the browser's Web Payments SDK must use to tokenize a card for
// THIS org. It resolves sandbox-vs-production through the SAME single authority
// as the charge path (org.TestMode / SQUARE_ENVIRONMENT) and the same KMS-then-env
// fallback, so the app id the browser tokenizes with always matches the env +
// access token commerce will vault/charge with. All values are public (safe to
// expose to the client).
//
//	GET /v1/billing/payment-config
func GetPaymentConfig(c *gin.Context) {
	org := middleware.GetOrganization(c)

	// NOTE: do NOT KMS-hydrate here. payment-config is called on dialog-open and
	// gates the card field mount, so it must be fast; a KMS round-trip can hang
	// when KMS is slow/unavailable and freeze the UI. Cloud orgs have no per-org
	// Square creds anyway — the env fallback supplies the public app id.
	//
	// Resolution lives in ONE place (payment.SquarePublicConfig) so this
	// authenticated handler and the public host→org tenant projection
	// (/v1/commerce/tenant) can never hand the browser a different Square app.
	sq := payment.SquarePublicConfig(org)

	c.JSON(200, gin.H{
		"provider":      "square",
		"applicationId": sq.ApplicationID,
		"locationId":    sq.LocationID,
		"environment":   sq.Environment,
		"live":          sq.Live,
	})
}

type testModeRequest struct {
	TestMode bool `json:"testMode"`
}

// SetOrgTestMode toggles the org's live flag (org.Live) and its test-mode view.
// org.Live marks transactions Test=true and is the FALLBACK Square-environment
// signal: when the deployment does NOT set SQUARE_ENVIRONMENT, a test org uses
// Square sandbox and a live org uses production (via org.TestMode).
// When the deployment DOES set SQUARE_ENVIRONMENT (the per-env authority:
// mainnet=production, testnet/devnet=sandbox), that env governs which Square
// environment is charged regardless of this flag — on mainnet there is no
// sandbox charge. Admin-only — a user must not be able to move their own org to
// sandbox to dodge real charges.
//
//	POST /v1/billing/test-mode   { testMode: bool }
func SetOrgTestMode(c *gin.Context) {
	org := middleware.GetOrganization(c)

	var req testModeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		jsonhttp.Fail(c, 400, "invalid request body", err)
		return
	}

	org.Live = !req.TestMode
	if err := org.Update(); err != nil {
		log.Error("Failed to set test mode for org %q: %v", org.Name, err, c)
		jsonhttp.Fail(c, 500, "failed to set test mode", err)
		return
	}

	c.JSON(200, gin.H{
		"orgId":    org.Id(),
		"orgName":  org.Name,
		"live":     org.Live,
		"testMode": !org.Live,
	})
}
