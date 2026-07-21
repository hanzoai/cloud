// billing_coresident.go — PinBillingSubject, the subject-pinning middleware that lets
// commerce's OWN billing READ handlers serve co-resident in the unified cloud binary.
//
// WHY IT EXISTS. Co-resident, commerce is EMBEDDED (apps/commerce.go mountCommerce →
// commerce.Embed on the shared zip app), and in prod there is NO standalone commerce
// backend — the in-cluster `commerce` Service selects the cloud pods themselves. So the
// /v1/billing/* bridge (billing.go), which forwards to COMMERCE_URL, has nowhere to send
// a read but back into cloud: the default base (the public api.hanzo.ai edge) re-enters
// the same bridge in an unbounded self-dispatch loop that surfaces as a 502
// ("billing upstream unreachable: Get https://api.hanzo.ai/v1/billing/<path>"). The fix
// is to serve those reads co-resident from the embedded commerce — the same co-resident
// move mountCommerce already makes for GET /v1/billing/plans — so the specific route
// shadows the bridge wildcard (order 100 < 122) and never leaves the process.
//
// WHAT IT GUARANTEES. commerce's read handlers scope to the org by NAMESPACE (from the
// gateway-validated X-Org-Id via iammiddleware) but filter finer scope (the billing
// subject) only from a query param — an UNPINNED ListInvoices returns every user's rows
// in the org namespace. The /v1/billing/* bridge is what pins that subject today; this
// middleware carries the SAME pin onto the co-resident route so the isolation is
// byte-for-byte the shipped behavior. It is the ONE subject rule (account.Payer, the same
// function the ai spend-gate and the top-up resolve), fed the account the credential NAMES
// — so a read scopes to exactly the account the gate debits, never wider.
package account

import (
	"net/url"

	"github.com/hanzoai/account"

	"github.com/hanzoai/cloud/clients/principal"
	"github.com/zap-proto/zip"
)

// PinBillingSubject pins every billing subject key in the request query to the VALIDATED
// caller's own subject (dropping ?org), so a co-resident commerce read handler downstream
// can only ever return the caller's OWN rows. It is the co-resident twin of billingData's
// subject-pinning, reusing the SAME resolveCaller → account.Payer rule and the SAME
// scopedBillingSearch, so the two paths scope identically.
//
// Three cases, mirroring billingData exactly:
//   - Browser customer (a validated principal with an org): OVERWRITE the subject keys
//     with the caller's own subject and drop ?org. The client cannot widen scope.
//   - Trusted in-proc S2S (the verified COMMERCE_SERVICE_TOKEN bearer, carrying its own
//     X-Org-Id): pass the query through VERBATIM — it legitimately names its own subject,
//     scoped by the EdgeAuth-controlled org.
//   - Neither: refuse. A bearer-less request with a forged X-Org-Id has no validated
//     principal and is fail-closed here, before the read handler runs.
//
// The pin rewrites the request URI's query string in place; fasthttp's SetQueryString
// resets the parsed-args cache, so the handler's later c.Query() reads the pinned values.
func PinBillingSubject() zip.Handler {
	return func(c *zip.Ctx) error {
		inQuery, _ := url.ParseQuery(string(c.Fiber().Request().URI().QueryString()))

		cr, ok := resolveCaller(c, true)
		if !ok {
			// Not a validated customer — admit ONLY a trusted in-proc S2S caller that
			// names its own org (same admission billingData makes), leaving its query
			// untouched. Everything else is refused before the read runs.
			if s2sBillingCall(c) && c.Org() != "" {
				return c.Next()
			}
			return zip.ErrForbidden("sign in to view billing")
		}

		subject := account.Payer(account.Credential{
			Owner:   cr.owner,
			Name:    cr.username,
			Account: principal.BillingAccount(c),
		}).Subject()

		c.Fiber().Request().URI().SetQueryString(scopedBillingSearch(inQuery, subject).Encode())
		return c.Next()
	}
}
