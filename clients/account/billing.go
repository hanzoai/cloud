// billing.go — the per-tenant billing DATA bridge, the Go port of console's
// app/billing/v1/[...path]/route.ts (task #41, the BFF catch-all sweep). It lets the
// statically-exported console reach its own money surface at the CANONICAL same-origin
// /v1/billing/* (nothing before /v1/): GET|POST /v1/billing/<path> forwards to
// commerce's /v1/billing/<path> with the admin COMMERCE_SERVICE_TOKEN, SCOPING every
// request to the VALIDATED caller's own billing subject — so a tenant can only ever
// read/act on its OWN ledger (balance / usage / invoices / subscriptions /
// payment-methods / spend-alerts / …), never another's.
//
// TWO INDEPENDENT BOUNDS, because the token makes this a privileged forwarder:
//  1. WHICH ENDPOINT — billingForwardable, the per-method allowlist below. It is the
//     authorization gate: an unlisted path is 404'd before the token is ever attached, so
//     no money-MINT route (deposit/credit/refund/…) can be reached through this bridge.
//  2. WHOSE DATA — the subject-pinning below. It aims a permitted call at the caller's own
//     ledger. It is an IDOR control and NOT an authority control: on a mint route it would
//     have pinned the CREDIT to the attacker's own account. (1) is what stops that.
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
package account

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"unicode"

	"github.com/hanzoai/account"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
)

// billingForwardable — THE allowlist of billing endpoints this bridge may forward, keyed
// by method. It is the whole authorization story of the bridge, because forwarding IS
// authorization here: every forwarded request carries the admin COMMERCE_SERVICE_TOKEN,
// and commerce's money gate is MayMintMoney(c) = IsServiceToken(c) || IsSuperAdmin(c)
// (middleware/platformonly.go). The token satisfies IsServiceToken, so ANY subpath that
// reaches commerce is executed with PLATFORM authority — not the caller's. Commerce 403s
// an org admin who calls POST /v1/billing/deposit directly; without this table the bridge
// handed that same person the platform's own credential and minted it for them, scoped —
// by the subject-pinning below — to their OWN account. That is the escalation, and
// subject-pinning is what AIMS it, not what stops it. Only a path gate stops it.
//
// It is an ALLOWLIST, never a denylist: a denylist must enumerate every mint route
// (deposit/credit/refund/credit-grants/payouts/husd/allotment…) and stays correct only
// until commerce adds the next one — a route this file has never heard of is then
// forwarded by default. Here the default is REFUSE, so a new commerce mint route is
// unreachable the day it lands, with no change on this side. One table, one place; a path
// not in it cannot reach commerce, by construction.
//
// GET and POST are SEPARATE sets because a read bridge and a write bridge are different
// concerns: `payouts` is a legitimate read and a money-MINT write (api/billing/handlers.go
// `api.Get("/payouts", ListPayouts)` vs `api.Post("/payouts", mintRequired, CreatePayout)`),
// so one method-blind set would hand the mint to every reader. The POST set is therefore
// deliberately tiny and holds NOTHING that creates spendable balance from a client-named
// amount: cancel/reactivate a subscription, vault a card, create a budget, and a top-up
// that CHARGES a real card (money in, not minted). Every entry is a call the console
// actually makes; `{}` matches exactly one opaque id segment.
//
// EVIDENCE — each entry is a live console call (repo hanzoai/console):
//
//	GET  balance             src/lib/api/billing.ts:397   sidebar wallet + billing overview
//	GET  usage               src/lib/api/billing.ts:415   cost reports / AI metrics
//	GET  invoices            src/lib/api/billing.ts:419   invoice history table
//	GET  invoices/{}/pdf     src/components/products/billing/BillingInvoices.tsx:31
//	GET  subscriptions       src/lib/api/billing.ts:423   subscriptions list
//	GET  payment-methods     src/lib/api/billing.ts:450   saved cards (masked)
//	GET  spend-alerts        src/lib/api/billing.ts:482   budgets / spend caps
//	GET  payment-config      src/lib/api/billing.ts:552   public Square app/location id
//	GET  plans               src/lib/api/plans.ts:126     published tiers
//	GET  payouts             src/components/products/SettlementModule.tsx:61  settlement view
//	POST subscriptions/{}/cancel      src/lib/api/billing.ts:434
//	POST subscriptions/{}/reactivate  src/lib/api/billing.ts:444
//	POST payment-methods              src/lib/api/billing.ts:461  vault a Square nonce (no PAN)
//	POST spend-alerts                 src/lib/api/billing.ts:500  create a budget
//	POST topup/token                  src/lib/api/billing.ts:565  charge a card → credit
//
// balance/usage/payment-methods are ALSO served natively by clients/billing (order 121),
// which wins over this catch-all (122), so those entries are reached only on a deploy
// where that subsystem is disabled. They are listed because they are legitimate reads of
// the caller's own ledger, not because this bridge is their primary route.
//
// NOT LISTED, deliberately: `me/welcome` and `grant-starter` (console calls the first at
// billing.ts:407 and the second server-side at src/lib/server/billing-grant.ts:35) exist
// in NEITHER the pinned commerce (v1.48.5) route table — both 404 today whether or not
// this bridge forwards them, and grant-starter is mint-gated and browser-unreachable by
// design. The console's PATCH/DELETE calls (spend-alerts/{}, payment-methods/{}) are absent
// because routesBridge mounts GET+POST only, so they never reached this handler.
var billingForwardable = map[string][]string{
	http.MethodGet: {
		"balance",
		"usage",
		"invoices",
		"invoices/{}/pdf",
		"subscriptions",
		"payment-methods",
		"spend-alerts",
		"payment-config",
		"plans",
		"payouts",
	},
	http.MethodPost: {
		"subscriptions/{}/cancel",
		"subscriptions/{}/reactivate",
		"payment-methods",
		"spend-alerts",
		"topup/token",
	},
}

// isForwardableBilling reports whether method+sub is in billingForwardable. sub has
// already passed isSafeSegment, so no segment can contain a slash, a percent-escape, or a
// traversal — a pattern segment therefore matches exactly one real segment and `{}` cannot
// swallow a path. Fail-closed: an unknown method or an unlisted path is false.
func isForwardableBilling(method, sub string) bool {
	got := strings.Split(sub, "/")
	for _, pattern := range billingForwardable[method] {
		want := strings.Split(pattern, "/")
		if len(want) != len(got) {
			continue
		}
		match := true
		for i, seg := range want {
			if seg != "{}" && seg != got[i] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

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
func billingData(s *cloud.Service[state], c *zip.Ctx) error {
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
	// THE authorization gate. Forwarding is authorization: the request below carries the
	// admin service token, which satisfies commerce's MayMintMoney. So refuse anything the
	// console does not actually call — BEFORE the token is attached. Fail closed (404, the
	// same answer an unrouted path gives, so this leaks no map of the money surface).
	if !isForwardableBilling(method, sub) {
		return zip.Errorf(http.StatusNotFound, "not a forwardable billing endpoint")
	}

	// Scope EVERY request to the caller's OWN subject — query AND write body — so
	// commerce's per-tenant isolation can never be crossed from the browser. The
	// subject comes from the ONE rule (ai/object.Payer), keyed on the IAM username
	// (cr.username = X-User-Name) the gate also keys on — so a top-up credits the
	// SAME account the gate debits. Keying on cr.name (X-User-Id, a UUID on the
	// direct-bearer path) would fund an account the gate never reads: the split.
	subject := account.Payer(account.Credential{Owner: cr.owner, Name: cr.username}).Subject()
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
