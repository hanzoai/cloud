package billing

import (
	"context"
	"net/http"
	"strings"

	"github.com/hanzoai/cloud/internal/host"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/cloud/sdk/spec"
	"github.com/zap-proto/zip"
)

// state is billing's own data; shared deps live in the embedded cloud.Base. Every
// read below is of the ledger the commerce plugin holds, over the plane: billing
// keeps no address of its own to reach commerce by. trail is the audit trail plan
// sales are put on (saleAudit); nil keeps none.
type state struct {
	trail *audit.Recorder
}

// Mount registers the customer-facing /v1/billing/* read surface on app.
//
// Every write below asks cloud.CSRF, which verifies a MAC the account process
// minted. Whether this process holds that key is not asked here: the catalog read
// at /v1/billing/plans is public and answers no token at all, so a key question
// gets between an anonymous caller and a price list it does not need.
func Use(app cloud.Router, deps cloud.Deps) error {
	return cloud.Use(app, deps, "billing", func(b cloud.Base) (state, error) { return build(b, deps.Audit) }, routes)
}

// build constructs the billing state.
func build(b cloud.Base, trail *audit.Recorder) (state, error) {
	b.Log.Info("billing surface mounted", "prefix", "/v1/billing")
	return state{trail: trail}, nil
}

// routes registers the customer-facing /v1/billing/* read surface.
func routes(app cloud.Router, s *cloud.Service[state]) {
	o := ops{s: s}

	g := app.Group("/v1/billing")
	// Raw: serves bytes — the split-deploy leg forwards commerce's ledger status
	// and rows, which this handler then enriches (each row is stamped with its
	// canonical metadata.product, ?product= filters, ?groupBy=product rolls rows
	// up), and the co-resident leg writes the same envelope straight from the
	// finance ledger. Commerce's shape either way, never one declared here.
	g.Raw(http.MethodGet, "/usage", cloud.Handle(s, usage))
	// The write beside it: an application reporting usage it metered, debited
	// through the same meter every in-process surface uses (record.go).
	g.Post("/usage", o.recordUsage)
	// The per-account routed-usage breakdown the dashboard reads, in the billing
	// namespace beside /v1/billing/usage. The data is owned by clients/link (the
	// linked-account plane); this thin op asks it, scoped to the caller's OWN
	// (org, subject). Registered here — not from link — so it shadows the console
	// pkg's /v1/billing/* wildcard exactly like the other specific customer routes.
	g.Get("/usage/accounts", o.usageAccounts,
		zip.WithResponseHeader("Cache-Control"))
	// /v1/billing/methods is served CO-RESIDENT by the commerce app, not proxied
	// from here. These three forwarded over HTTP to commerce, and the proxy is
	// unconfigured on this deployment (CLOUD_COMMERCE_HTTP_URL unset), so every
	// saved-card call answered 401/501 while authenticated — a customer could
	// not list a card, and the checkout's prefill failed on every load. An
	// internal HTTP hop to a service compiled into the same binary is the wrong
	// shape regardless; the in-process registration has no hop to misconfigure.
	//
	// Typed: owns the commerceBalance wire shape with Cache-Control: no-store, so
	// REST, OpenAPI, SDKs and the MCP connector (`get_billing_balance`) project from one op.
	g.Get("/balance", o.balance,
		zip.WithResponseHeader("Cache-Control"))
	// A GPU is a metered resource like any other, so it has no charge route here: a
	// machine is launched through /v1/compute/machines, which fronts the compute provider's
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
	mountRoute(app, o)
	mountAgent(app, o)
	mountOrders(app, o)
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
	spec.Describe("/v1/billing/usage", http.MethodGet,
		"Every billed call the caller's org made, attributed to a product",
		"Answers one row per BILLED call against the caller's org — transaction id, amount, "+
			"timestamp and the metered unit. This is the raw charged ledger, not a rollup.\n\n"+
			"Each row is stamped with a canonical `metadata.product` derived from what the meter "+
			"persisted: `agent` becomes agents, `provisioning` becomes the provisioned kind, a "+
			"token-metered row becomes inference, anything else keeps its metering surface. The "+
			"ledger has no product field of its own, so this read is where that dimension is made "+
			"real — from the SAME charged rows, never a second meter. A row that already carries "+
			"its own product WINS, so the derivation stops the day the meter records one.\n\n"+
			"Each row carries `name`: what its metered unit (`metadata.model`) reads as on a bill — "+
			"`Desktop sandbox` for `desktop/visor`, `Agent sessions` for `session`. An AI model is "+
			"its own name. Show `name`; `metadata.model` stays the unit the ledger keys on.\n\n"+
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
// Scope is unchanged by this. The org is the validated principal's — never a
// caller-supplied field — and readerOrg grants no user, no admin and no roles,
// so it is a READ resolution only: the money WRITE
// (createPaymentMethod) and the user-scoped breakdown (usageAccounts, which needs
// c.User()) keep asking principal.Org and refuse it.
func readerOrg(c *zip.Ctx) (string, bool) { return host.ReaderOrg(c) }

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

	// No commerce in the fleet: there is no ledger to read, which is an outage, never
	// an empty list.
	return errNoLedger
}

// errNoLedger answers a read of the ledger in a fleet that runs no commerce plugin.
var errNoLedger = zip.Errorf(http.StatusServiceUnavailable, "billing ledger is not available on this deployment")

// Answers the spendable prepaid balance of the wallet this caller bills from — the same
// wallet the AI prepaid gate reads before admitting a paid request, the edge meter
// debits, and a top-up or admin grant credits.
//
// The wallet is an ADDRESS, not an org: `account` echoes the key resolved within the
// ledger — the org's shared pool for a tenant org, a personal account for a member of
// the shared signup org.
//
// `balance`, `holds` and `available` are whole USD cents, floored from the ledger's
// exact 18-decimal value. `cash`, `line` and `credit` say where `available` came from,
// and add up to it; `prepaid` is `cash` plus `line`.
//
// Scoped to the caller's own org from the validated IAM principal (401 without one).
// A balance that cannot be read is 502, never 0: unknown is not broke.
func (o ops) balance(ctx context.Context, _ *cloud.Unit) (*commerceBalance, error) {
	org, subject, err := payer(ctx)
	if err != nil {
		return nil, err
	}
	f, coResident, err := fundsCents(ctx, org, subject)
	if err != nil {
		o.s.Log.Warn("finance balance read failed", "org", org, "err", err)
		return nil, zip.Errorf(http.StatusBadGateway, "billing upstream unreachable")
	}
	if !coResident {
		return nil, errNoLedger
	}
	return &commerceBalance{
		Balance:     f.available,
		Holds:       0,
		Available:   f.available,
		Account:     subject,
		Cash:        f.cash,
		Credit:      f.credit,
		Prepaid:     f.prepaid(),
		CreditLimit: f.limit,
		Due:         f.due,
		Line:        f.line,
	}, nil
}
