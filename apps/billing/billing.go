// Package billing is your org's balance, what it has spent, and the cards it pays with.
//
// It is the customer's own money surface, serving the org-scoped
// /v1/billing/{balance,usage,usage/accounts,ledger} reads. It does not own the
// prefix whole: commerce serves the merchant half of /v1/billing/* (invoices,
// subscriptions, alerts, methods, webhooks) from the store it keeps.
//
// It answered a second prefix once. /v1/finance was six more reads of this same
// wallet under another name — balance and usage were these reads respelled,
// credits, invoices and payment-methods were addresses commerce already serves —
// so HIP-0139 §7 closed it, and closing a shared address by fold means the
// duplicate is deleted rather than moved. What survived is the ledger read
// (ledger.go), at /v1/billing/ledger, where nobody else answers.
//
// WHY THIS EXISTS. On the console host (console.hanzo.ai) the ingress routes
// /v1/* straight to cloud-api:8000 — the console's Next BFF is reached only at
// "/". So the console's /v1/billing/usage + /v1/billing/balance calls land HERE,
// on cloud-api, NOT on the console's per-tenant commerce proxy. cloud-api
// previously wired commerce billing ONLY under the admin-gated aggregate
// (apps/admin, /v1/admin/*), so a normal org owner (e.g. davelorenzini /
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
// per-org READ twin of the admin god-view (apps/admin) — the SAME commerce S2S
// machinery, but scoped to the caller instead of all-orgs (which stays admin-only).
//
// SUBJECT. Prepaid balance is per-ORG: commerce keys the wallet under the BARE org
// slug as the `user` subject + the trusted `X-Org-Id` (admin.orgSubject /
// metering identityFromCtx — verified live: user=<org> + X-Org-Id=<org> returns
// the real wallet, "org/user" reads an empty one). The gateway debits this SAME
// key, so a read here shows exactly what the org is charged.
//
// TWO BACKENDS, ONE WIRE. Balance and usage read the co-resident native ledger
// DIRECTLY when finance is published (balance.go, usage_coresident.go) — which it always
// is in the unified binary — and fall back to the commerce S2S proxy only on a split
// deploy. Either way the wire is commerce's: the console's normalizeUsageRecords parses
// the RAW per-request ledger ({usage:[{transactionId,amount,metadata,createdAt}]}) and
// balance is the raw {balance,holds,available} cents object, so the proxy path forwards
// commerce's body + status VERBATIM and never rolls up (the rollup is the admin
// aggregate's job).
package billing

import (
	"github.com/hanzoai/cloud/internal/environ"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/account"
	"github.com/hanzoai/cloud/apps/commerce/transport"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/openapi"
	"github.com/zap-proto/zip"
)

// commerceProxy is a thin service-to-service reader for the commerce billing
// surface. It authenticates with the admin-scoped COMMERCE_SERVICE_TOKEN (a
// KMS-sourced secret already on the cloud env — never hard-coded) and scopes every
// read to ONE org via the trusted X-Org-Id S2S selector, which commerce's EdgeAuth
// honors only after it verifies the bearer is the service token. It is deliberately
// separate from the admin commerceClient (apps/admin/commerce/commerce.go): admin decodes
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
		http:  transport.Client(15 * time.Second),
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

// state is billing's own data; shared deps live in the embedded cloud.Base.
type state struct {
	commerce *commerceProxy
}

// Mount registers the customer-facing /v1/billing/* read surface on app.
//
// Every write below asks account.CSRF, which verifies a MAC the account process
// minted. Whether this process holds that key is not asked here: the catalog read
// at /v1/billing/plans is public and answers no token at all, so a key question
// gets between an anonymous caller and a price list it does not need.
func Use(app cloud.Router, deps cloud.Deps) error {
	return cloud.Use(app, deps, "billing", build, routes)
}

// build constructs the billing state: the commerce S2S proxy from its env
// (COMMERCE_SERVICE_TOKEN is a KMS-sourced secret already on the cloud env).
func build(b cloud.Base) (state, error) {
	cp := newCommerceProxy(transport.BaseURL(environ.Or("CLOUD_COMMERCE_HTTP_URL", "")), environ.Or("COMMERCE_SERVICE_TOKEN", ""))
	b.Log.Info("billing surface mounted", "prefix", "/v1/billing", "commerce", cp.configured())
	return state{commerce: cp}, nil
}

// routes registers the customer-facing /v1/billing/* read surface.
func routes(app cloud.Router, s *cloud.Service[state]) {
	o := ops{s: s}

	// Raw: serves bytes — the split-deploy leg forwards commerce's ledger status
	// and rows, which this handler then enriches (each row is stamped with its
	// canonical metadata.product, ?product= filters, ?groupBy=product rolls rows
	// up), and the co-resident leg writes the same envelope straight from the
	// finance ledger. Commerce's shape either way, never one declared here.
	app.Get("/v1/billing/usage", cloud.Handle(s, usage))
	// The per-account routed-usage breakdown the dashboard reads, in the billing
	// namespace beside /v1/billing/usage. The data is owned by clients/link (the
	// linked-account plane); this thin op asks it, scoped to the caller's OWN
	// (org, subject). Registered here — not from link — so it shadows the console
	// pkg's /v1/billing/* wildcard exactly like the other specific customer routes.
	zip.Get(cloud.ZipApp(app), "/v1/billing/usage/accounts", o.usageAccounts,
		zip.WithResponseHeader("Cache-Control"))
	// /v1/billing/methods is served CO-RESIDENT by the commerce app, not proxied
	// from here. These three forwarded over HTTP to commerce, and the proxy is
	// unconfigured on this deployment (CLOUD_COMMERCE_HTTP_URL unset), so every
	// saved-card call answered 401/501 while authenticated — a customer could
	// not list a card, and the checkout's prefill failed on every load. An
	// internal HTTP hop to a service compiled into the same binary is the wrong
	// shape regardless; the in-process registration has no hop to misconfigure.
	//
	// Raw: serves bytes — the split-deploy leg forwards commerce's body and
	// status, and the co-resident leg writes the same envelope from the ledger.
	app.Get("/v1/billing/balance", cloud.Handle(s, balance))
	// A GPU is a metered resource like any other, so it has no charge route here: a
	// machine is launched through /v1/visor/machines, which fronts the compute provider's
	// resell endpoint (apps/visor) where the balance gate and the per-hour meter both
	// live, keyed on the server-minted machine id. One meter bills every resource.
	//
	// Saving a card must be registered on the SAME router as the read: a specific
	// route shadows the console pkg's /v1/billing/* wildcard for its whole path, so
	// a GET-only registration made POST miss on METHOD (405) before the wildcard or
	// the co-resident commerce app could serve it — the console's save-card call
	// died there, and with it auto-recharge, which charges the vaulted card.
	// Removing a saved card, on the SAME router as the save for the same reason the
	// save is here: the host claims a prefix for ONE app across every method, so a
	// sub-resource this app does not register misses on METHOD (405) rather than
	// falling through to anyone else. It did — a customer could ADD a card and never
	// REMOVE one.

	// The org's own postings, signed — the widest read of the same wallet the two
	// above answer for. Typed, where they are raw (ledger.go says why).
	mountLedger(app, o)

	// The rest of /v1/billing. Each family is a relay onto the process that owns
	// the merchant store (peer.go), because the address is this capability's and
	// the rows are commerce's — one address, one owner, and no second copy of a
	// question that already has an answer.
	mountInvoices(app, o)
	mountStatement(app, o)
	mountCredits(app, o)
	mountAlerts(app, o)
	mountRails(app, o)
	// The four families this app took over when /v1/billing became one address:
	// the saved cards, the posture and catalog, the customer's own plan, and the
	// card endpoints that pay for it.
	mountMethods(app, o)
	mountPosture(app, o)
	mountSubscriptions(app, o)
	mountCards(app, o)
}

// The PROSE for the raw routes above. Each survivor is a raw *zip.Ctx handler
// because it SERVES BYTES: it forwards commerce's body and STATUS (a 402 decline
// keeps its reason), enriches it, or writes an envelope built to match
// commerce's shape — always commerce's contract, never a shape declared here,
// and a typed op owns its shape where these deliberately do not. zipdoc lifts
// prose from a typed op's doc comment, so these
// have nowhere else to state it, and without a Describe the document publishes an
// operationId and NOTHING else — an SDK method for a MONEY endpoint that cannot
// say whose ledger it reads, and a CLI command with no help.
//
// Declared through the same registry Register uses, so a description renders only while
// the router actually serves the route: prose is additive metadata on routes that
// exist, never an operation the registry invented.
func init() {
	openapi.Describe("/v1/billing/balance", http.MethodGet,
		"Prepaid credit the caller's org can still spend",
		"Answers the spendable prepaid balance of the wallet this caller bills from — the same "+
			"wallet the AI prepaid gate reads before admitting a paid request, the edge meter "+
			"debits, and a top-up credits.\n\n"+
			"The wallet is an ADDRESS, not an org: `account` echoes the key resolved within the "+
			"ledger — the org's shared pool for a tenant org, a personal account for a member of "+
			"the shared signup org. The echo is the point. A browser could only GUESS its own "+
			"payer by decoding its own token, and a guess that disagrees with the server is how "+
			"money lands in an account the gate never reads.\n\n"+
			"`balance`, `holds` and `available` are whole USD cents, ROUNDED from the ledger's "+
			"exact 18-decimal value. On the co-resident ledger `holds` is 0 and `available` "+
			"equals `balance`: the gate's reservations live in its own pod and are never posted, "+
			"so the settled balance IS the spendable one.\n\n"+
			"The ledger is the caller's own org, taken from the VALIDATED IAM owner claim and "+
			"never from a client header. No validated principal is 401 — with one exception, the "+
			"trusted in-process service token the AI gate itself presents, which reads the "+
			"gateway-pinned org and nothing it could name. A balance that cannot be READ is 502, "+
			"never 0: unknown is not broke.")

	openapi.Describe("/v1/billing/usage", http.MethodGet,
		"Every billed call the caller's org made, attributed to a product",
		"Answers one row per BILLED call against the caller's org — transaction id, amount, "+
			"timestamp and the metered unit. This is the raw charged ledger, not a rollup.\n\n"+
			"Each row is stamped with a canonical `metadata.product` derived from what the meter "+
			"persisted: `agent` becomes agents, `provisioning` becomes the provisioned kind, a "+
			"token-metered row becomes inference, anything else keeps its metering surface. The "+
			"ledger has no product field of its own, so this read is where that dimension is made "+
			"real — from the SAME charged rows, never a second meter. A row that already carries "+
			"its own product WINS, so the derivation stops the day the meter records one.\n\n"+
			"`product=<id>` filters to one product server-side. `groupBy=product` reduces to "+
			"`{product,requests,amountCents}` rollups instead of rows.\n\n"+
			"`amount` is whole USD cents, ROUNDED; `decimal` beside it is the SAME debit exact, "+
			"as an 18-decimal USD string. Sum `decimal`. A page of sub-cent token calls totals "+
			"correctly there and totals ZERO in `amount` — that difference is real money.\n\n"+
			"Scoped to the caller's own org's books, where the org's ledger file IS the tenant "+
			"boundary; no client-supplied subject is ever forwarded. 401 without a validated "+
			"principal. The co-resident read returns the 2000 most recent debits, newest first; "+
			"`start` and `end` narrow the window only on the split-deploy upstream.")

}

// billingSubjectKeys — every query/body param through which a commerce billing endpoint
// identifies its subject. Kept identical to commerce's edge-auth billingSubjectKeys
// {user,userId,customerId} AND clients/account's billingData: pinning ALL of them is what
// scopes EVERY endpoint no matter which one it filters on — usage and balance read
// `user`, portal/methods requires `customerId`. Change all three in lockstep.
var billingSubjectKeys = []string{"user", "userId", "customerId"}

// readerOrg is the ONE tenant resolution the billing READ surface shares: the org of
// the validated principal, else — for the trusted in-proc S2S caller — the
// gateway-pinned X-Org-Id.
//
// It exists because the rule was stated TWICE and the two copies disagreed. balance()
// held the S2S half privately and then, off the co-resident path, delegated to proxy()
// — which asked principal.Org a SECOND time, without it, and refused the very caller
// balance() had just admitted. In prod that second resolution answered
// 401 "sign in to view billing" to ai's prepaid gate, which is fail-CLOSED, so every
// paid completion fleet-wide returned 503 balance_unavailable while the first
// resolution was working perfectly (the request log carried org=hanzo AND the refusal).
// One rule, one place: a handler cannot admit a caller its own helper then denies.
//
// Scope is unchanged by this. The token is compared constant-time against the
// configured COMMERCE_SERVICE_TOKEN by the predicate apps/account already owns
// (account.IsServiceToken), and the org comes from X-Org-Id — which the gateway strips
// from every client request — never from a caller-supplied field. It grants no user, no
// admin and no roles, so it is a READ resolution only: the money WRITE
// (createPaymentMethod) and the user-scoped breakdown (usageAccounts, which needs
// c.User()) keep asking principal.Org and refuse it.
func readerOrg(c *zip.Ctx) (string, bool) { return account.ReaderOrg(c) }

// proxy resolves the caller's OWN org (readerOrg), pins the commerce
// billing subject to it on EVERY subject key (the client can NEVER widen scope — the
// subject is server-resolved, never read from the request; a forged user/userId/
// customerId is overwritten and `org` is dropped), forwards ONLY the safe passthrough
// params, and returns commerce's raw body + status verbatim.
func proxy(s *cloud.Service[state], c *zip.Ctx, commercePath string, passthrough ...string) error {
	org, ok := readerOrg(c)
	if !ok {
		// No validated principal, no trusted service token, no org. This is a
		// customer's OWN billing — never admin-gate it — so an absent identity is a
		// true "not signed in" (401), not a 403 "not authorized for this surface".
		return zip.ErrUnauthorized("sign in to view billing")
	}
	if !s.State.commerce.configured() {
		return zip.Errorf(http.StatusNotImplemented, "billing is not configured")
	}

	q := scopedBillingQuery(c, org, passthrough...)

	body, status, err := s.State.commerce.get(c.Context(), commercePath, org, q)
	if err != nil {
		s.Log.Warn("commerce billing read failed", "org", org, "path", commercePath, "err", err)
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
func usage(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrUnauthorized("sign in to view billing")
	}

	// Co-resident, read the usage ledger DIRECTLY from cloud's own finance ledger
	// (usage_coresident.go explains why this is NOT a commerce proxy: proxying
	// "/v1/billing/usage" re-enters THIS handler — commerce's own /v1/billing/usage
	// route is behind //go:build cloud and never compiled here, so the only
	// registration of that path is this handler — and the in-proc S2S hop carries no
	// validated principal, so usage() self-answered "sign in to view billing"; that
	// self-dispatch is the 500 a valid caller saw). This is the exact move balance()
	// already makes. Off the co-resident path the commerce S2S proxy is unchanged.
	if body, coResident, err := coResidentUsage(c.Context(), org, strings.TrimSpace(c.Query("product")), strings.TrimSpace(c.Query("groupBy"))); err != nil {
		s.Log.Warn("finance usage read failed", "org", org, "err", err)
		return zip.Errorf(http.StatusBadGateway, "billing upstream unreachable")
	} else if coResident {
		c.SetHeader("Content-Type", "application/json")
		c.SetHeader("Cache-Control", "no-store")
		return c.Bytes(http.StatusOK, body)
	}

	if !s.State.commerce.configured() {
		return zip.Errorf(http.StatusNotImplemented, "billing is not configured")
	}

	q := scopedBillingQuery(c, org, "start", "end")
	body, status, err := s.State.commerce.get(c.Context(), "/v1/billing/usage", org, q)
	if err != nil {
		s.Log.Warn("commerce billing read failed", "org", org, "path", "/v1/billing/usage", "err", err)
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

// balance answers the caller's prepaid credit balance ({balance,holds,available} in USD
// cents) — the SAME wallet the ai prepaid gate reads, the edge meter debits, and an admin
// grant credits.
//
// Co-resident it reads cloud's own finance ledger DIRECTLY (balance.go explains why this
// is NOT a commerce proxy: proxying "/v1/billing/balance" re-enters THIS handler, because
// the commerce transport dispatches the shared app by path and commerce's own billing routes are
// never registered in this binary — the proxy called itself and answered "sign in to view
// billing"). Off the co-resident path the commerce S2S proxy is unchanged.
//
// Holds are the ai gate's in-pod reservations, not a persisted ledger position (see
// types.FinanceClient.Balance), so the settled balance IS the available balance here.
func balance(s *cloud.Service[state], c *zip.Ctx) error {
	// readerOrg admits the TRUSTED S2S caller as well as a session. ai's prepaid gate
	// reads this endpoint to decide whether to admit a paid request, and once ai became
	// its own plugin PROCESS it stopped seeing build.go's in-process balanceReader hook
	// — a process-local func var cannot cross a process boundary — so it falls back to
	// the HTTP path documented as the split-deploy fallback. That request carries
	// COMMERCE_SERVICE_TOKEN, not a user session, so principal.Org alone is empty.
	//
	// The gate is fail-CLOSED (a balance it cannot verify must never degrade to free
	// inference), so a 401 here denies EVERY paid call fleet-wide:
	//
	//   [billing] GET /v1/billing/balance  err="sign in to view billing"
	//   [ai] balance_gate: balance unverifiable for cold subject=hanzo:
	//        commerce returned 401 (fail-CLOSED, retryable)
	//
	// The rule lives in readerOrg because the proxy leg below re-resolves the tenant,
	// and a copy here is what let the two answers drift apart.
	org, ok := readerOrg(c)
	if !ok {
		// A customer's OWN billing — never admin-gate it; an absent identity is a
		// true "not signed in" (401), matching usage.
		return zip.ErrUnauthorized("sign in to view billing")
	}
	// ONE resolution, used twice: the wallet to READ, and the account to REPORT. Calling
	// subjectFor once and echoing that exact value is what makes the reported account
	// honest — resolving it a second time for display could drift from the one the
	// balance came from, which is the same class of split that once showed a funded org
	// while the gate refused the member.
	subject := subjectFor(c, org)
	cents, coResident, err := availableCents(c.Context(), org, subject)
	if err != nil {
		// A balance that cannot be READ is unknown — surface it as an upstream failure.
		// It must never render as a zero balance: unknown is not "broke".
		s.Log.Warn("finance balance read failed", "org", org, "err", err)
		return zip.Errorf(http.StatusBadGateway, "billing upstream unreachable")
	}
	if !coResident {
		return proxy(s, c, "/v1/billing/balance", "currency") // split deploy
	}
	c.SetHeader("Content-Type", "application/json")
	// Per-tenant money must never be cached by the browser or an intermediary.
	c.SetHeader("Cache-Control", "no-store")
	return c.JSON(http.StatusOK, commerceBalance{
		Balance:   cents,
		Holds:     0,
		Available: cents,
		Account:   subject,
	})
}
