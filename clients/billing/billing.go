// Package billing mounts the CUSTOMER-facing, org-scoped billing surface
// (/v1/billing/{usage,balance,gpu-eligibility,gpu-charge,payment-methods}) on the
// unified cloud binary.
//
// WHY THIS EXISTS. On the console host (console.hanzo.ai) the ingress routes
// /v1/* straight to cloud-api:8000 — the console's Next BFF is reached only at
// "/". So the console's /v1/billing/usage + /v1/billing/balance calls land HERE,
// on cloud-api, NOT on the console's per-tenant commerce proxy. cloud-api
// previously wired commerce billing ONLY under the admin-gated aggregate
// (clients/admin, /v1/admin/*), so a normal org owner (e.g. davelorenzini /
// maxpower) hitting /v1/billing/usage had NO customer route and was denied — the
// "Access required" wall on every product overview + o11y usage panel. This adds
// exactly the customer surface those calls need.
//
// TENANT ISOLATION (the whole point). The org is the VALIDATED IAM owner claim
// (principal.Org — the trusted X-Org-Id the identity middleware minted from the
// caller's verified session/bearer, HIP-0026; NEVER a client-supplied header). A
// customer therefore reads ONLY their OWN org's ledger. The commerce billing
// subject is pinned server-side to that org and NO client-supplied subject/org
// query param is ever forwarded, so the browser cannot widen scope. This is the
// per-org READ twin of the admin god-view (clients/admin) — the SAME commerce S2S
// machinery, but scoped to the caller instead of all-orgs (which stays admin-only).
//
// SUBJECT. Prepaid balance is per-ORG: commerce keys the wallet under the BARE org
// slug as the `user` subject + the trusted `X-Org-Id` (admin.orgSubject /
// metering identityFromCtx — verified live: user=<org> + X-Org-Id=<org> returns
// the real wallet, "org/user" reads an empty one). The gateway debits this SAME
// key, so a read here shows exactly what the org is charged.
//
// PASSTHROUGH. The console's normalizeUsageRecords parses commerce's RAW per-request
// ledger ({usage:[{transactionId,amount,metadata,createdAt}]}); balance is the raw
// {balance,holds,available} cents object. So this proxies commerce's body + status
// VERBATIM — it never reshapes or rolls up (the rollup is the admin aggregate's job).
package billing

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/commerceinproc"
	"github.com/hanzoai/cloud/clients/principal"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// commerceProxy is a thin service-to-service reader for the commerce billing
// surface. It authenticates with the admin-scoped COMMERCE_SERVICE_TOKEN (a
// KMS-sourced secret already on the cloud env — never hard-coded) and scopes every
// read to ONE org via the trusted X-Org-Id S2S selector, which commerce's EdgeAuth
// honors only after it verifies the bearer is the service token. It is deliberately
// separate from the admin commerceClient (clients/admin/commerce.go): admin decodes
// typed god-view rollups (MRR/COGS/credits), whereas this forwards the customer's
// OWN raw ledger + status verbatim.
type commerceProxy struct {
	base  string // e.g. http://commerce.hanzo.svc.cluster.local:8001
	token string // admin S2S bearer (secret; never logged)
	http  *http.Client
}

func newCommerceProxy(base, token string) *commerceProxy {
	return &commerceProxy{
		base:  strings.TrimRight(strings.TrimSpace(base), "/"),
		token: strings.TrimSpace(token),
		http:  commerceinproc.Client(15 * time.Second),
	}
}

func (p *commerceProxy) configured() bool { return p != nil && p.base != "" && p.token != "" }

// get performs one service-token commerce GET scoped to org and returns commerce's
// raw body + status VERBATIM (a true passthrough — the caller forwards both). The
// org rides X-Org-Id, the S2S org selector commerce keys the per-org wallet under.
func (p *commerceProxy) get(ctx context.Context, path, org string, q url.Values) ([]byte, int, error) {
	u := p.base + path
	if enc := q.Encode(); enc != "" {
		u += "?" + enc
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.token)
	// Commerce's EdgeAuth trusts X-Org-Id ONLY after it verifies the bearer is the
	// COMMERCE_SERVICE_TOKEN, then resolves the per-org billing namespace from it.
	req.Header.Set("X-Org-Id", org)
	resp, err := p.http.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("commerce unreachable: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return body, resp.StatusCode, nil
}

// post performs one service-token commerce POST scoped to org, forwarding the JSON
// body and returning commerce's raw body + status VERBATIM. Same S2S trust as get: the
// caller's OWN org rides X-Org-Id (the selector commerce keys the per-org wallet under)
// and the admin service token authorizes the write. Used by gpu-charge — the ONLY money
// WRITE on this customer surface.
func (p *commerceProxy) post(ctx context.Context, path, org string, body []byte) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.base+path, bytes.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.token)
	req.Header.Set("X-Org-Id", org)
	resp, err := p.http.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("commerce unreachable: %w", err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return b, resp.StatusCode, nil
}

type svc struct {
	commerce *commerceProxy
	log      luxlog.Logger
}

// Mount registers the customer-facing /v1/billing/* read surface on app.
func Mount(app *zip.App, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("billing.Mount: nil zip.App")
	}
	log := deps.Logger
	if log == nil {
		return fmt.Errorf("billing.Mount: nil deps.Logger")
	}
	log = log.New("subsystem", "billing")

	s := &svc{
		commerce: newCommerceProxy(commerceinproc.BaseURL(os.Getenv("CLOUD_COMMERCE_HTTP_URL")), os.Getenv("COMMERCE_SERVICE_TOKEN")),
		log:      log,
	}

	app.Get("/v1/billing/usage", s.usage)
	app.Get("/v1/billing/balance", s.balance)
	// GPU launch gate + saved cards — the customer half of the prepay-only GPU rule
	// commerce enforces server-side (api/billing/gpu_charge.go). Same org-scoping as
	// usage/balance: the subject is pinned to the caller's OWN org, so the console reads
	// eligibility/card status and charges exactly the wallet a launch debits — never
	// another tenant's. These SPECIFIC customer routes register before (and so shadow)
	// the console pkg's /v1/billing/* wildcard, giving an unauthenticated call an honest
	// 401 (route exists) instead of the wildcard's admin-shaped 403.
	app.Get("/v1/billing/gpu-eligibility", s.gpuEligibility)
	app.Post("/v1/billing/gpu-charge", s.gpuCharge)
	app.Get("/v1/billing/payment-methods", s.paymentMethods)

	// The customer-facing /v1/finance/* PROJECTION of this same commerce plane (the
	// finance.hanzo.ai + console Finance surfaces). It reuses this package's commerceProxy
	// + per-org subject-pinning; the treasury lane owns /v1/finance/treasury alongside it.
	s.mountFinance(app)

	log.Info("billing surface mounted", "prefix", "/v1/billing", "commerce", s.commerce.configured())
	return nil
}

func init() {
	cloud.Register("billing", 121, cloud.Typed(Mount))
}

// billingSubjectKeys — every query/body param through which a commerce billing endpoint
// identifies its subject. Kept identical to commerce's edge-auth billingSubjectKeys
// {user,userId,customerId} AND clients/account's billingData: pinning ALL of them is what
// scopes EVERY endpoint no matter which one it filters on — usage/balance/gpu-eligibility
// read `user`, portal/payment-methods requires `customerId`. Change all three in lockstep.
var billingSubjectKeys = []string{"user", "userId", "customerId"}

// proxy resolves the caller's OWN org from the VALIDATED principal, pins the commerce
// billing subject to it on EVERY subject key (the client can NEVER widen scope — the
// subject is server-resolved, never read from the request; a forged user/userId/
// customerId is overwritten and `org` is dropped), forwards ONLY the safe passthrough
// params, and returns commerce's raw body + status verbatim.
func (s *svc) proxy(c *zip.Ctx, commercePath string, passthrough ...string) error {
	org, ok := principal.Org(c)
	if !ok {
		// No validated principal / no org. This is a customer's OWN billing — never
		// admin-gate it — so an absent identity is a true "not signed in" (401),
		// not a 403 "not authorized for this surface".
		return zip.ErrUnauthorized("sign in to view billing")
	}
	if !s.commerce.configured() {
		return zip.Errorf(http.StatusNotImplemented, "billing is not configured")
	}

	q := scopedBillingQuery(c, org, passthrough...)

	body, status, err := s.commerce.get(c.Context(), commercePath, org, q)
	if err != nil {
		s.log.Warn("commerce billing read failed", "org", org, "path", commercePath, "err", err)
		return zip.Errorf(http.StatusBadGateway, "billing upstream unreachable")
	}
	c.SetHeader("Content-Type", "application/json")
	// Per-tenant money must never be cached by the browser or an intermediary.
	c.SetHeader("Cache-Control", "no-store")
	return c.Bytes(status, body)
}

// scopedBillingQuery pins EVERY commerce billing subject key to the caller's OWN org
// (the bare org slug is cloud's canonical per-org billing key — admin.orgSubject /
// metering identityFromCtx). Pinning the whole set leaves NO endpoint unfiltered
// regardless of which param it reads, so a request with no (or a forged) subject can
// never see another tenant's rows. Only the whitelisted non-subject passthrough params
// are forwarded. The ONE place the subject boundary is built (proxy + usage share it).
func scopedBillingQuery(c *zip.Ctx, org string, passthrough ...string) url.Values {
	q := url.Values{}
	for _, k := range billingSubjectKeys {
		q.Set(k, org)
	}
	for _, k := range passthrough {
		if v := strings.TrimSpace(c.Query(k)); v != "" {
			q.Set(k, v)
		}
	}
	return q
}

// usage → commerce GET /v1/billing/usage: the RAW per-request ledger the console's
// per-product Metrics + AI Metrics pages parse (one row per billed call). start/end
// pass through for a server window (the console also filters client-side).
//
// Beyond the verbatim ledger it ENRICHES + optionally REDUCES the response — the ONE
// place the product/agent cost dimensions the console renders are made real:
//   - Each row's metadata gets a canonical `product` (and `agent` when known) derived
//     from what commerce persists (provider/model), so the console's per-product
//     breakdown POPULATES from the SAME charged ledger (commerce has no product field
//     yet; productOf in usage.go is the read-side adapter).
//   - `?product=<id>` filters to ONE product server-side (was silently ignored).
//   - `?groupBy=product` returns a per-product rollup {product,requests,amountCents}.
//
// On any parse failure it returns commerce's body VERBATIM — enrichment must never
// lose or corrupt the real ledger.
func (s *svc) usage(c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrUnauthorized("sign in to view billing")
	}
	if !s.commerce.configured() {
		return zip.Errorf(http.StatusNotImplemented, "billing is not configured")
	}

	q := scopedBillingQuery(c, org, "start", "end")
	body, status, err := s.commerce.get(c.Context(), "/v1/billing/usage", org, q)
	if err != nil {
		s.log.Warn("commerce billing read failed", "org", org, "path", "/v1/billing/usage", "err", err)
		return zip.Errorf(http.StatusBadGateway, "billing upstream unreachable")
	}
	c.SetHeader("Content-Type", "application/json")
	c.SetHeader("Cache-Control", "no-store")
	if status != http.StatusOK {
		return c.Bytes(status, body) // pass commerce errors through untouched
	}
	if out, ok := enrichUsageLedger(body, strings.TrimSpace(c.Query("product")), strings.TrimSpace(c.Query("groupBy"))); ok {
		return c.Bytes(status, out)
	}
	return c.Bytes(status, body)
}

// balance → commerce GET /v1/billing/balance: the org's prepaid credit balance
// ({balance,holds,available} in USD cents), the SAME wallet the gateway debits.
func (s *svc) balance(c *zip.Ctx) error {
	return s.proxy(c, "/v1/billing/balance", "currency")
}

// gpuEligibility → commerce GET /v1/billing/gpu-eligibility: the read-only launch gate
// ({eligible,reason,prepaidAvailable,cardOnFile,requiredCents,...}). It reads PREPAID
// available (never the combined balance) + card-on-file, so the launch UI can show the
// exact remedy (add a card / add prepaid) — it never 402s. The immediate charge
// (amountCents) and the 24h-minimum floor (minPrepaidCents) + currency pass through; the
// subject is pinned to the caller's OWN org (commerce keys the wallet under the bare org
// slug), so the gate reads exactly the wallet gpu-charge debits.
func (s *svc) gpuEligibility(c *zip.Ctx) error {
	return s.proxy(c, "/v1/billing/gpu-eligibility", "amountCents", "minPrepaidCents", "currency")
}

// paymentMethods → commerce GET /v1/billing/portal/payment-methods: the org's saved cards
// as the masked descriptor commerce returns (brand + last4 + expiry — never a PAN/CVV/
// token). The console requests the same-origin /v1/billing/payment-methods (mounted here);
// this proxies to commerce's admin-group PORTAL read, which filters CustomerId on the
// pinned subject (commerce 400s without a customerId — proxy always pins it), so a caller
// sees ONLY its OWN org's methods. Backs the launch gate's card-on-file check.
func (s *svc) paymentMethods(c *zip.Ctx) error {
	return s.proxy(c, "/v1/billing/portal/payment-methods")
}

// gpuCharge → commerce POST /v1/billing/gpu-charge: the prepay-only, card-required GPU
// debit. Commerce enforces BOTH gates + the gpu-tagged (credits-never-consulted)
// bucketing server-side, so this is a thin, org-scoped hop: the caller's charge params
// (amountCents/currency/requestId/tag) ride the body, but the billing SUBJECT is PINNED
// server-side to the caller's OWN org — a client can never charge another tenant. Commerce's
// status is forwarded VERBATIM (201 ok / 402 {card_required|insufficient_prepaid}), so the
// launch UI renders the exact remedy; a money verdict is never 500-masked.
func (s *svc) gpuCharge(c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		// A customer's OWN GPU charge — never admin-gate it; an absent identity is a
		// true "not signed in" (401), matching usage/balance.
		return zip.ErrUnauthorized("sign in to charge a GPU")
	}
	if !s.commerce.configured() {
		return zip.Errorf(http.StatusNotImplemented, "billing is not configured")
	}
	// Pin the billing subject to the caller's OWN org on the body — commerce's ChargeGPU
	// reads `user` from the JSON body, so a forged body subject must never widen scope.
	body := pinSubjectBody(c.Body(), org)
	respBody, status, err := s.commerce.post(c.Context(), "/v1/billing/gpu-charge", org, body)
	if err != nil {
		s.log.Warn("commerce gpu-charge failed", "org", org, "err", err)
		return zip.Errorf(http.StatusBadGateway, "billing upstream unreachable")
	}
	c.SetHeader("Content-Type", "application/json")
	// A money write's result must never be cached by the browser or an intermediary.
	c.SetHeader("Cache-Control", "no-store")
	return c.Bytes(status, respBody)
}

// pinSubjectBody overwrites every commerce billing-subject key on a top-level JSON object
// with subject (the caller's OWN org), so a POST body can NEVER act on another tenant's
// wallet. The keys mirror commerce's edge-auth billing-subject set {user,userId,customerId}
// (kept identical to clients/account's scopedBillingBody), so whichever param commerce reads
// is the caller's. A non-object/empty body starts fresh with only the pinned subject (commerce
// then 400s on the missing amount — honest), so the subject can never be omitted. Non-subject
// fields (amountCents/currency/requestId/tag) are preserved verbatim.
func pinSubjectBody(raw []byte, subject string) []byte {
	obj := map[string]json.RawMessage{}
	if len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, &obj); err != nil {
			obj = map[string]json.RawMessage{} // non-object body — keep only the pinned subject
		}
	}
	s, err := json.Marshal(subject)
	if err != nil {
		return raw
	}
	for _, k := range billingSubjectKeys {
		obj[k] = s
	}
	out, err := json.Marshal(obj)
	if err != nil {
		return raw
	}
	return out
}
