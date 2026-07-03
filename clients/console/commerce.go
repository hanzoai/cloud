// commerce.go — the per-tenant STORE data bridge, the Go port of console2's
// app/commerce/[...path]/route.ts (task #41, the BFF catch-all sweep; the store twin
// of billing.go). It lets the statically-exported console reach its merchant store at
// the CANONICAL same-origin /v1/commerce/* (nothing before /v1/): GET|POST|PUT|PATCH|
// DELETE /v1/commerce/<path> forwards to commerce's bare store surface /v1/<path> with
// the admin COMMERCE_SERVICE_TOKEN, SCOPING every request to the VALIDATED caller's own
// org — so a merchant only ever reads/writes its OWN org's catalog (products / orders /
// customers / variants / collections / discounts / storefront), never another's.
//
// WHY /v1/commerce/<x> → commerce /v1/<x> (the `commerce` segment is DROPPED, not
// preserved like billing's /v1/billing/<x> → /v1/billing/<x>). The DEPLOYED commerce
// binary (hanzoai/commerce cmd/commerced) mounts its whole REST surface with
// `api.Route(router.Group("/v1"))`: the store models live at BARE /v1/<kind>
// (/v1/product, /v1/order, /v1/user, …) while money lives at /v1/billing/*. The
// console namespaces the store under /v1/commerce/* only to keep the generic store
// heads (product/order/user/store) from colliding with the rest of the /v1 surface;
// this bridge strips that console-side namespace and forwards to commerce's real bare
// head — EXACTLY the mapping console2's next.config rewrite already proved live
// (`/v1/commerce/:path*` → `/commerce/v1/:path*` → commerce.svc/v1/:path*).
//
// WHY A SERVER HANDLER (not a same-origin passthrough). Commerce's store is
// service-token-gated: its EdgeAuth resolves the org from the X-Org-Id header ONLY
// after it verifies the bearer is the COMMERCE_SERVICE_TOKEN, then scopes every store
// row to that org. A browser passthrough would have to carry that admin token (a
// cross-tenant skeleton key) or a per-tenant selector the browser could forge — either
// leaks another org's store. This handler injects the token SERVER-SIDE and pins the
// org to the caller's own, so tenancy can never be crossed from the browser.
//
// IDOR-safe: the org is derived from the VALIDATED identity (resolveCaller →
// principal.Validated / c.Org() / c.User()), NEVER a client-supplied value. A
// bearer-less request with a forged X-Org-Id has no validated principal and is refused
// (403) BEFORE any commerce call — the exact off-gateway forge principal.Validated
// closes. Least privilege on the path: only the merchant store heads are reachable, so
// this bridge can NOT tunnel to /v1/billing (its own subject-scoped bridge), /v1/checkout
// (the money path), or /v1/_/commerce/tenants (tenant admin) — mirroring console2's
// proxy-allow.ts allowCommerceSurface.
package console

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/zap-proto/zip"
)

// commerceStoreHeads — the merchant store REST heads reachable through /v1/commerce/*.
// Kept IDENTICAL to console2's proxy-allow.ts COMMERCE_HEADS (the same defense-in-depth
// allow-list the Node /commerce proxy enforced), matching commerce's `rest.New(<kind>{})`
// route names. Change both together. This is what keeps the bridge a STORE proxy: a head
// not in this set (billing, checkout, namespace, _) is 404'd before any upstream call, so
// the store token can never reach the money or tenant-admin surfaces that share commerce's
// binary — those have their OWN scoped bridges (billing.go) or are unreachable.
var commerceStoreHeads = map[string]bool{
	"product":       true, // products
	"variant":       true, // inventory / SKUs
	"collection":    true, // catalog collections
	"order":         true, // orders
	"user":          true, // customers
	"discount":      true, // promotions & discounts
	"coupon":        true, // discount codes
	"saleschannel":  true, // sales channels
	"stocklocation": true, // stock locations
	"store":         true, // storefront settings
}

// isCommerceStoreHead reports whether sub (the path after /v1/commerce/) targets an
// allow-listed store head — the FIRST segment, so `product`, `product/<id>`, and
// `store/current` all resolve to their head (`product`, `store`).
func isCommerceStoreHead(sub string) bool {
	head := sub
	if i := strings.IndexByte(sub, '/'); i >= 0 {
		head = sub[:i]
	}
	return commerceStoreHeads[head]
}

// commerceData forwards GET|POST|PUT|PATCH|DELETE /v1/commerce/<path> to commerce's
// store surface /v1/<path>, scoped to the caller's OWN org. Mirrors the five method
// exports of app/commerce/[...path]/route.ts (the store dashboard reads AND writes:
// create/delete a product, etc. — full CRUD, unlike billing's read-mostly GET|POST).
func (s *svc) commerceData(c *zip.Ctx) error {
	// IDOR boundary: the org is the VALIDATED caller's own, never a client value.
	// requireOwner=true — the store is always org-scoped (a zero-org user has none).
	cr, ok := resolveCaller(c, true)
	if !ok {
		return zip.ErrForbidden("sign in to manage your store")
	}

	switch c.Method() {
	case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
	default:
		return zip.Errorf(http.StatusMethodNotAllowed, "method not allowed")
	}

	base, token := commerceCreds()
	if token == "" {
		// Honest "not configured" (mirrors billing.go's 501 when the token is unset) —
		// the console shows a truthful state, never a fabricated store.
		return zip.Errorf(http.StatusNotImplemented, "commerce is not configured on this deployment (COMMERCE_SERVICE_TOKEN unset)")
	}

	sub := strings.Trim(strings.TrimPrefix(c.Fiber().Params("*"), "/"), "/")
	if sub == "" {
		return zip.Errorf(http.StatusNotFound, "commerce endpoint required")
	}
	for _, seg := range strings.Split(sub, "/") {
		// isSafeSegment rejects empty/./../slash/backslash/null; the extra `%`/`;`
		// rejection closes ENCODED traversal. The router leaves `%2f`/`%2e` UNdecoded
		// in this wildcard param (they carry no literal `/` here, so isSafeSegment
		// alone misses them), but the Go http client — and commerce's own router —
		// WILL decode+normalize them downstream, turning `product/..%2fbilling` into
		// `/v1/billing`: a tunnel PAST the store-head allow-list into the money surface.
		// Rejecting any residual percent-escape / matrix-param segment makes single-,
		// double-, and N-encoded traversal impossible here, mirroring console2's
		// bearer-proxy pathIsClean (commerce store ids are opaque + escape-free).
		if !isSafeSegment(seg) || strings.ContainsAny(seg, "%;") {
			return zip.ErrBadRequest("invalid commerce path")
		}
	}
	// Least privilege: only the merchant store heads (defense in depth). A non-store
	// head (billing/checkout/namespace/…) is 404'd here, so this bridge can never be a
	// general tunnel into commerce's money / tenant-admin surfaces.
	if !isCommerceStoreHead(sub) {
		return zip.Errorf(http.StatusNotFound, "not a commerce store endpoint")
	}

	// The store query (limit/page/q/sort) passes through verbatim — commerce scopes the
	// store by the X-Org-Id header (bound below), not a query param, so there is no
	// subject to pin as in billing. The org is NEVER read from the query.
	q, _ := url.ParseQuery(string(c.Fiber().Request().URI().QueryString()))

	// Forward the write body verbatim on mutating methods (commerce validates it).
	var body []byte
	if c.Method() != http.MethodGet && len(c.Body()) > 0 {
		body = c.Body()
	}

	// commerceDo binds X-Org-Id = the caller's OWN validated org + the admin service
	// token; commerce's EdgeAuth trusts that org ONLY behind the token and scopes the
	// store to it. This is the SAME S2S transport billing.go / topup.go share.
	raw, status, err := commerceDo(c.Context(), base, token, c.Method(), "/v1/"+sub, q, cr.owner, body)
	if err != nil {
		return zip.Errorf(http.StatusBadGateway, "commerce upstream unreachable: %v", err)
	}
	// A per-tenant store response must never be cached across tenants; commerce answers
	// JSON, so pin JSON + no-store (identical to billing.go).
	c.SetHeader("Content-Type", "application/json")
	c.SetHeader("Cache-Control", "no-store, must-revalidate")
	return c.Bytes(status, raw)
}
