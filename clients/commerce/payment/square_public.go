package payment

import (
	"os"
	"strings"

	"github.com/hanzoai/cloud/clients/commerce/models/organization"
)

// SquarePublic is the PUBLIC Square Web Payments config the browser needs to
// tokenize a card for an org: the application id + location id + environment.
// Every field is public (Square exposes them to the client SDK) — no secret,
// no access token, no webhook key.
type SquarePublic struct {
	ApplicationID string `json:"applicationId"`
	LocationID    string `json:"locationId"`
	Environment   string `json:"environment"` // "sandbox" | "production"
	Live          bool   `json:"live"`
}

// SquarePublicConfig resolves the PUBLIC Square config for an org through the
// SINGLE test-mode authority (org.TestMode / SQUARE_ENVIRONMENT) and the same
// KMS-then-env fallback the charge path uses, so the app id the browser
// tokenizes with always matches the environment + access token commerce will
// vault/charge with.
//
// This is the ONE place that resolution lives: both the authenticated
// /v1/billing/payment-config handler and the public host→org tenant projection
// (/v1/commerce/tenant) call it, so a card form and its charge can never drift
// onto different Square applications.
func SquarePublicConfig(org *organization.Organization) SquarePublic {
	useSandbox := org.TestMode()
	env := org.SquareEnvironment()

	sq := org.SquareConfig(useSandbox)
	appID := sq.ApplicationId
	locationID := sq.LocationId

	// Env fallback (KMS disabled or no per-org Square creds — the common case
	// for cloud orgs). The SAME useSandbox authority picks the sandbox vs
	// production public vars, so the app id always matches `env`.
	if appID == "" || locationID == "" {
		appID = strings.TrimSpace(os.Getenv("SQUARE_APPLICATION_ID"))
		locationID = strings.TrimSpace(os.Getenv("SQUARE_LOCATION_ID"))
		if useSandbox {
			if a := strings.TrimSpace(os.Getenv("SQUARE_SANDBOX_APPLICATION_ID")); a != "" {
				appID = a
			}
			if l := strings.TrimSpace(os.Getenv("SQUARE_SANDBOX_LOCATION_ID")); l != "" {
				locationID = l
			}
		}
	}

	return SquarePublic{
		ApplicationID: appID,
		LocationID:    locationID,
		Environment:   env,
		Live:          org.Live,
	}
}
