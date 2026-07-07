// billing.go — the per-tenant billing DATA bridge, the Go port of console's
// app/billing/v1/[...path]/route.ts (task #41, the BFF catch-all sweep). It lets the
// statically-exported console reach its own money surface at the CANONICAL same-origin
// /v1/billing/* (nothing before /v1/): GET|POST /v1/billing/<path> forwards to
// commerce's /v1/billing/<path> with the admin COMMERCE_SERVICE_TOKEN, SCOPING every
// request to the VALIDATED caller's own billing subject — so a tenant can only ever
// read/act on its OWN ledger (balance / usage / invoices / subscriptions /
// payment-methods / spend-alerts / …), never another's.
//
// WHY A SERVER HANDLER (not a same-origin passthrough). Commerce's billing surface is
// service-token-gated and filters DIFFERENT endpoints on DIFFERENT subject params —
// subscriptions on ?userId, payment-methods on ?customerId, usage on ?user. Pinning
// only ONE leaves the others UNFILTERED, so a request with no (or a forged) param
// returns every subject's rows in the namespace. This handler pins ALL of them to the
// server-resolved subject (and drops ?org), on the query AND the write body — exactly
// mirroring console's billing-scope.ts and commerce's own edge-auth billingSubjectKeys.
//
// IDOR-safe: the subject is derived from the VALIDATED identity (resolveCaller →
// principal.Validated / c.Org() / c.User()), NEVER a client-supplied userId/org. A
// bearer-less request with a forged X-Org-Id has no validated principal and is refused.
package console

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"unicode"

	"github.com/zap-proto/zip"
)

// billingSubjectKeys — every query/body param through which a commerce billing endpoint
// identifies its subject. Kept identical to commerce's edge-auth billingSubjectKeys
// {user,userId,customerId} AND console's billing-scope.ts BILLING_SUBJECT_KEYS. Change
// all three together — pinning ALL of them is what scopes EVERY endpoint no matter which
// param it filters on.
var billingSubjectKeys = []string{"user", "userId", "customerId"}

func isSubjectKey(k string) bool {
	for _, s := range billingSubjectKeys {
		if s == k {
			return true
		}
	}
	return false
}

// personalBillingOrgs — orgs whose members bill per-USER (the shared catch-all).
// Mirrors commerce's PERSONAL_BILLING_ORGS and billing-scope.ts personalBillingOrgs:
// default "hanzo"; PERSONAL_BILLING_ORGS or HANZO_DEFAULT_ORG overrides (comma list).
func personalBillingOrgs() map[string]bool {
	raw := getenv("PERSONAL_BILLING_ORGS", getenv("HANZO_DEFAULT_ORG", "hanzo"))
	out := map[string]bool{}
	for _, p := range strings.Split(raw, ",") {
		if p = strings.ToLower(strings.TrimSpace(p)); p != "" {
			out[p] = true
		}
	}
	return out
}

// billingSubject — the commerce billing subject for an org+user, the SAME subject the
// gateway debits: a member of a personal-billing org bills per-user as "<org>/<name>";
// a dedicated org bills per-org as "<org>". Mirrors billing-scope.ts billingSubject.
func billingSubject(org, name string) string {
	o := strings.ToLower(strings.TrimSpace(org))
	if o == "" {
		return ""
	}
	if personalBillingOrgs()[o] {
		if n := strings.ToLower(strings.TrimSpace(name)); n != "" {
			return o + "/" + n
		}
	}
	return o
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

// isSafeSegment reports whether a path segment is safe to forward. It is the ONE segment
// guard for this package (the billing AND commerce bridges): a segment is safe only if
// it is non-empty, not "." / "..", and free of any character a downstream router could
// re-split or re-decode into traversal — slash, backslash, percent-escape (`%2f`/`%2e`,
// single- or N-encoded), matrix param (`;`), or a control char (incl. null). The router
// leaves `%2f`/`%2e` UNdecoded in the wildcard param, but the Go http client — and
// commerce's own router — WILL decode+normalize them downstream, turning
// `x/..%2fbilling` into `/v1/billing`: a tunnel PAST the allow-list into the money
// surface. Rejecting `%`/`;` at the segment makes single-, double-, and N-encoded
// traversal impossible. Billing endpoints / commerce ids are opaque + escape-free, so
// this never over-blocks. Mirrors console's bearer-proxy pathIsClean.
func isSafeSegment(s string) bool {
	if s == "" || s == "." || s == ".." {
		return false
	}
	for _, r := range s {
		if r == '/' || r == '\\' || r == '%' || r == ';' || unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// commerceCreds resolves the commerce base + admin S2S token from server-only env
// (COMMERCE_URL default the public gateway; COMMERCE_SERVICE_TOKEN sourced from KMS —
// never a browser value). Same wiring as clients/admin + topup.go's HUSD credit.
func commerceCreds() (base, token string) {
	base = strings.TrimRight(getenv("COMMERCE_URL", "https://api.hanzo.ai"), "/")
	token = getenv("COMMERCE_SERVICE_TOKEN", "")
	return
}

// billingData forwards GET|POST /v1/billing/<path> to commerce's /v1/billing/<path>,
// scoped to the caller's OWN subject. Mirrors GET/POST app/billing/v1/[...path]/route.ts.
func (s *svc) billingData(c *zip.Ctx) error {
	// IDOR boundary: the subject is the VALIDATED caller's own org/user, never a client
	// value. requireOwner=true — billing is always org-scoped (a zero-org user has none).
	cr, ok := resolveCaller(c, true)
	if !ok {
		return zip.ErrForbidden("sign in to view billing")
	}

	method := c.Method()
	if method != http.MethodGet && method != http.MethodPost {
		return zip.Errorf(http.StatusMethodNotAllowed, "method not allowed")
	}

	base, token := commerceCreds()
	if token == "" {
		// Honest "not configured" (mirrors the Node route's 501 when COMMERCE_TOKEN
		// is unset) — the console shows a truthful state, never a fabricated balance.
		return zip.Errorf(http.StatusNotImplemented, "billing is not configured on this deployment (COMMERCE_SERVICE_TOKEN unset)")
	}

	sub := strings.Trim(strings.TrimPrefix(c.Fiber().Params("*"), "/"), "/")
	if sub == "" {
		return zip.Errorf(http.StatusNotFound, "billing endpoint required")
	}
	for _, seg := range strings.Split(sub, "/") {
		if !isSafeSegment(seg) {
			return zip.ErrBadRequest("invalid billing path")
		}
	}

	// Scope EVERY request to the caller's OWN subject — query AND write body — so
	// commerce's per-tenant isolation can never be crossed from the browser.
	subject := billingSubject(cr.owner, cr.name)
	inQuery, _ := url.ParseQuery(string(c.Fiber().Request().URI().QueryString()))
	q := scopedBillingSearch(inQuery, subject)

	var body []byte
	if method == http.MethodPost {
		body = scopedBillingBody(c.Body(), subject)
	}

	raw, status, err := commerceDo(c.Context(), base, token, method, "/v1/billing/"+sub, q, cr.owner, body)
	if err != nil {
		return zip.Errorf(http.StatusBadGateway, "billing upstream unreachable: %v", err)
	}
	// A per-tenant money response must NEVER be cached (a stale balance after a
	// completion/top-up); commerce answers JSON, so pin JSON + no-store.
	c.SetHeader("Content-Type", "application/json")
	c.SetHeader("Cache-Control", "no-store, must-revalidate")
	return c.Bytes(status, raw)
}
