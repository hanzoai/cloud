// Package billing mounts the CUSTOMER-facing, org-scoped billing READ surface
// (/v1/billing/usage, /v1/billing/balance) on the unified cloud binary.
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
// (principal.Tenant — the trusted X-Org-Id the identity middleware minted from the
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
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
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
		http:  &http.Client{Timeout: 15 * time.Second},
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
		commerce: newCommerceProxy(os.Getenv("CLOUD_COMMERCE_HTTP_URL"), os.Getenv("COMMERCE_SERVICE_TOKEN")),
		log:      log,
	}

	app.Get("/v1/billing/usage", s.usage)
	app.Get("/v1/billing/balance", s.balance)

	log.Info("billing surface mounted", "prefix", "/v1/billing", "commerce", s.commerce.configured())
	return nil
}

func init() {
	cloud.Register("billing", 121, func(app any, deps cloud.Deps) error {
		a, ok := app.(*zip.App)
		if !ok {
			return fmt.Errorf("billing.Mount: app is %T, want *zip.App", app)
		}
		return Mount(a, deps)
	})
}

// proxy resolves the caller's OWN org from the VALIDATED principal, pins the
// commerce billing subject to it (the client can NEVER widen scope — user/org are
// server-resolved, never read from the request), forwards ONLY the safe passthrough
// params, and returns commerce's raw body + status verbatim.
func (s *svc) proxy(c *zip.Ctx, commercePath string, passthrough ...string) error {
	org, ok := principal.Tenant(c)
	if !ok {
		// No validated principal / no org. This is a customer's OWN billing — never
		// admin-gate it — so an absent identity is a true "not signed in" (401),
		// not a 403 "not authorized for this surface".
		return zip.ErrUnauthorized("sign in to view billing")
	}
	if !s.commerce.configured() {
		return zip.Errorf(http.StatusNotImplemented, "billing is not configured")
	}

	// Pin the subject to the caller's OWN org (the bare org slug is cloud's canonical
	// per-org billing key — admin.orgSubject / metering identityFromCtx). Forward
	// ONLY the whitelisted non-subject params; a client-supplied user/userId/
	// customerId/org is never forwarded, so scope can never be widened.
	q := url.Values{"user": {org}}
	for _, k := range passthrough {
		if v := strings.TrimSpace(c.Query(k)); v != "" {
			q.Set(k, v)
		}
	}

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

// usage → commerce GET /v1/billing/usage: the RAW per-request ledger the console's
// normalizeUsageRecords parses (one row per billed call). Not time-filtered here —
// the console applies the 24h/7d/30d window client-side — but start/end pass through
// for callers that request a server window.
func (s *svc) usage(c *zip.Ctx) error {
	return s.proxy(c, "/v1/billing/usage", "start", "end")
}

// balance → commerce GET /v1/billing/balance: the org's prepaid credit balance
// ({balance,holds,available} in USD cents), the SAME wallet the gateway debits.
func (s *svc) balance(c *zip.Ctx) error {
	return s.proxy(c, "/v1/billing/balance", "currency")
}
