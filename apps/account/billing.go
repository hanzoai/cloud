// billing.go — the two things the retired /v1/billing/* forwarder left behind that
// were never the forwarding: WHOSE data a billing read may return, and WHO counts as
// a trusted in-process service caller.
//
// The forwarder is gone (see the package doc). It answered GET|POST /v1/billing/<path>
// by re-dialing commerce with the admin COMMERCE_SERVICE_TOKEN — a credential that
// satisfies commerce's MayMintMoney, so ANY subpath reaching commerce executed with
// PLATFORM authority rather than the caller's. Forwarding WAS authorization, bounded
// only by a hand-maintained per-method allowlist, and every endpoint on that allowlist
// is served natively by billing (order 121) or the co-resident commerce embed (order
// 100) at a manifest prefix DEEPER than the bare /v1/billing stem — so those native
// routes already won every one of them and the forwarder was reachable by nobody.
//
// What remains is the tenancy rule those native routes need, because commerce's own
// billing handlers scope to the ORG by namespace but filter the finer BILLING SUBJECT
// only from a request param — and they filter DIFFERENT endpoints on DIFFERENT params
// (subscriptions on ?userId, payment-methods on ?customerId, usage on ?user). Pinning
// only one leaves the others unfiltered, so a request with no (or a forged) param
// returns every subject's rows in the namespace. scopedBillingSearch/scopedBillingBody
// pin ALL of them, on the query AND the write body, to the server-resolved subject;
// billing_coresident.go's PinBillingSubject is the middleware that applies them in
// front of each co-resident commerce handler (apps/commerce/mount.go).
//
// IDOR-safe: the subject is derived from the VALIDATED identity (resolveCaller →
// principal.Validated / c.Org() / c.User()), NEVER a client-supplied userId/org. A
// bearer-less request with a forged X-Org-Id has no validated principal and is refused.

package account

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"net/url"
	"slices"
	"strings"

	"github.com/hanzoai/cloud/internal/environ"
	"github.com/zap-proto/zip"
)

// billingSubjectKeys — every query/body param through which a commerce billing endpoint
// identifies its subject. Kept identical to commerce's edge-auth billingSubjectKeys
// {user,userId,customerId} AND console's billing-scope.ts BILLING_SUBJECT_KEYS. Change
// all three together — pinning ALL of them is what scopes EVERY endpoint no matter which
// param it filters on.
var billingSubjectKeys = []string{"user", "userId", "customerId"}

func isSubjectKey(k string) bool {
	return slices.Contains(billingSubjectKeys, k)
}

// scopedBillingSearch — pin every billingSubjectKey to subject (OVERWRITING any client
// value — the browser cannot widen scope) and DROP org. Every OTHER param (currency,
// status, date range) passes through untouched. Mirrors billing-scope.ts.
func scopedBillingSearch(in url.Values, subject string) url.Values {
	out := url.Values{}
	for k, v := range in {
		if k == "org" || isSubjectKey(k) {
			continue // org dropped; subject keys set authoritatively below
		}
		out[k] = v
	}
	for _, k := range billingSubjectKeys {
		out.Set(k, subject)
	}
	return out
}

// scopedBillingBody — pin every billingSubjectKey on a top-level JSON object to subject
// (commerce reads the subject from the JSON body on writes like create-spend-alert). A
// non-JSON / non-object / empty body is returned UNCHANGED — this only ever narrows a
// JSON object to the caller; it never invents a body. Mirrors billing-scope.ts.
func scopedBillingBody(raw []byte, subject string) []byte {
	if len(bytes.TrimSpace(raw)) == 0 {
		return raw
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return raw // JSON array / scalar / form / binary — leave untouched
	}
	subj, err := json.Marshal(subject)
	if err != nil {
		return raw
	}
	for _, k := range billingSubjectKeys {
		obj[k] = subj
	}
	out, err := json.Marshal(obj)
	if err != nil {
		return raw
	}
	return out
}

// commerceServiceToken reads the admin S2S token from server-only env
// (COMMERCE_SERVICE_TOKEN, sourced from KMS — never a browser value). It is no longer
// forwarded anywhere from this package; it is only ever COMPARED against, to recognise a
// trusted in-process caller. topup.go resolves its own base+token for the one remaining
// outbound S2S call (the HUSD credit).
func commerceServiceToken() string { return environ.Or("COMMERCE_SERVICE_TOKEN", "") }

// s2sBillingCall reports whether the request carries the verified COMMERCE_SERVICE_TOKEN
// as its Bearer — a trusted IN-PROC service-to-service caller (the metering cap-gate's
// authorize, the SuperAdmin cap-oversight Forward). Safety rests on the edge: the gateway
// 401s a public Bearer that is not an IAM JWT / pk-|sk- API key (the 64-hex service
// token is a JWT candidate that fails to parse), so an EXTERNAL client can never reach a
// handler holding it — only in-proc commerce-transport dispatch does. Constant-time
// compare; the token is never logged.
func s2sBillingCall(c *zip.Ctx) bool {
	token := commerceServiceToken()
	if token == "" {
		return false
	}
	bearer := strings.TrimSpace(strings.TrimPrefix(c.Header("Authorization"), "Bearer "))
	return bearer != "" && subtle.ConstantTimeCompare([]byte(bearer), []byte(token)) == 1
}

// IsServiceToken is the exported view of s2sBillingCall — whether the request is a trusted
// in-proc S2S caller bearing the verified COMMERCE_SERVICE_TOKEN. Used by co-resident route
// gates (e.g. the spend-alert admin gate) that must admit the metering cap-gate and the
// SuperAdmin cap-oversight Forward alongside org admins, while refusing a plain member.
func IsServiceToken(c *zip.Ctx) bool { return s2sBillingCall(c) }
