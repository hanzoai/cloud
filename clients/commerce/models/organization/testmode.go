package organization

import (
	"os"
	"strings"
)

// TestMode reports whether this org transacts in TEST mode on the current
// deployment. It is the SINGLE authority for BOTH the payment environment
// (Square sandbox vs production) AND the ledger (trans.Test, balance bucket,
// pay.Live). Keeping the charge environment and the ledger on ONE authority is
// what prevents a sandbox charge from crediting the live (spendable) balance and
// a production charge from booking test (unbilled) revenue.
//
// SQUARE_ENVIRONMENT is the deployment authority and makes the 3-env split pure
// per-env config (mainnet=production, testnet/devnet=sandbox). It is fail-CLOSED:
// only an explicit, recognized "production" (or prod/live) selects production;
// anything else that is SET — sandbox/test, an empty placeholder, or a typo —
// selects test/sandbox, so a misconfiguration can never silently charge real
// cards. When SQUARE_ENVIRONMENT is UNSET, the org's own live flag decides
// (live→production, test→sandbox) so local and un-templated deploys still resolve.
func (o Organization) TestMode() bool {
	if v, ok := os.LookupEnv("SQUARE_ENVIRONMENT"); ok {
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "production", "prod", "live":
			return false
		case "sandbox", "test":
			return true
		default:
			// Set but unrecognized (empty placeholder or typo): fail CLOSED to
			// test/sandbox — only an explicit "production" unlocks real charges.
			return true
		}
	}
	return !o.Live
}

// SquareEnvironment returns "sandbox" or "production" — the value the Square SDK
// uses to select its API base URL — derived from TestMode (one authority).
func (o Organization) SquareEnvironment() string {
	if o.TestMode() {
		return "sandbox"
	}
	return "production"
}
