package x402

// x402.go owns the subsystem: Mount + the settlement store, the marketplace
// Registry CLIENT (Publish/Terms), the challenge→verify→settle→serve flow, the
// settle-once settlement, and the receipt lookup surface
// (/v1/x402/settlements/:id).
//
// The flow is written ONCE (run) and reached two ways, because a price attaches to
// two different kinds of thing:
//
//   - Enforce — a zip.Handler a priced ROUTE group applies (app.Use). The resource
//     is the request path, which is all a middleware knows.
//   - Settle — the same flow for an explicit resource id, reached from inside a
//     typed op where the priced thing is named in the BODY and no path identifies
//     it (the tool plane's per-tool price). It answers with an error instead of a
//     response, and the caller refuses.
//
// Both are inert until a Registry is Published: x402 links into a binary harmlessly
// and the marketplace subsystem plugs its price/recipient table in — the clean
// client: x402 enforces payment; the marketplace declares what is priced and who is
// paid.

//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

import (
	"cmp"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"

	protocol "github.com/hanzoai/commerce/payment/x402"
	luxlog "github.com/luxfi/log"

	"github.com/hanzoai/cloud/apps/wallet"
	"github.com/hanzoai/cloud/finance"
	"github.com/hanzoai/cloud/internal/lock"
	"github.com/hanzoai/cloud/money"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/hanzoai/cloud/sdk/env"
	"github.com/hanzoai/cloud/sdk/meter"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/account"
	"github.com/zap-proto/zip"
)

const (
	// DefaultNetwork pins the challenge's settlement network when neither the
	// resource's Terms nor the operator config names one. It is CAIP-2 and it names
	// the Hanzo L1 (whose EVM chain id IS its network id), matching wallets.
	DefaultNetwork = "eip155:36963"

	// DefaultAssetName / DefaultAssetVersion are the EIP-712 domain fields of the
	// settlement asset. USDC uses ("USD Coin", "2"); operators pin them per asset in
	// Config, and whatever they are, the CHALLENGE states them so the client signs
	// over exactly the domain this rail verifies against.
	DefaultAssetName    = "USD Coin"
	DefaultAssetVersion = "2"

	// DefaultAssetDecimals is USDC's precision (the atomic-unit scale the client
	// signs over). Operators override per asset via CLOUD_X402_ASSET_DECIMALS.
	DefaultAssetDecimals = 6

	// DefaultMaxTimeoutSeconds is how long a challenge gives the client to pay when
	// the operator config does not say.
	DefaultMaxTimeoutSeconds = 300

	// providerLabel is the commerce "provider"/"service" label x402 spend records
	// under, so pay-per-use appears in billing/usage attributable to this surface.
	providerLabel = "x402"
)

// Config is the operator-pinned x402 settlement config. Asset + its EIP-712 domain
// (name/version/decimals) MUST match the asset the client signs against, or every
// signature fails to recover. Values are read from env in Mount; tests inject
// Config directly.
type Config struct {
	Asset         string // EIP-3009 token contract (EIP-712 verifyingContract)
	AssetName     string // EIP-712 domain name (default "USD Coin")
	AssetVersion  string // EIP-712 domain version (default "2")
	AssetDecimals int    // asset atomic-unit scale (default 6)
	Network       string // CAIP-2 settlement network (default "eip155:36963")
	MaxTimeout    int64  // advertised maxTimeoutSeconds (default 300)
}

func (c Config) assetName() string {
	if c.AssetName != "" {
		return c.AssetName
	}
	return DefaultAssetName
}
func (c Config) assetVersion() string {
	if c.AssetVersion != "" {
		return c.AssetVersion
	}
	return DefaultAssetVersion
}
func (c Config) assetDecimals() int {
	if c.AssetDecimals > 0 {
		return c.AssetDecimals
	}
	return DefaultAssetDecimals
}

// Terms are a priced resource's payment terms: the price and the wallet that
// receives it. The marketplace registry OWNS the resource→Terms mapping; x402
// resolves the recipient wallet (via wallets) and enforces payment against these.
type Terms struct {
	Amount            money.Amount // exact 18-dp USD price
	RecipientOrg      string       // the recipient wallet's org
	RecipientWalletID string       // the wallet that receives payment
	Asset             string       // optional per-resource asset override
	Network           string       // optional per-resource CAIP-2 network override
	Category          string       // what a settlement pays for (client.Categories)
	// PayTo pins the payee address a payment agreed before it is made was signed
	// to — a job's, as this rail answered it at funding. The payee wallet's keys may
	// be rotated since; the credit still lands on the wallet the table named, and
	// the signature still names the address it had. Empty pays the address now.
	PayTo string
	// Once says one payment buys one act, so a settled authorization presented
	// again is a replay rather than the same purchase served again. A metered fee
	// is an act (metered.go): a function run, a resource created.
	Once bool
}

// Registry resolves a resource's payment Terms. ok=false means the resource is
// FREE — no enforcement. err is a real lookup failure (fail closed). The
// marketplace subsystem implements this and injects it via Publish.
type Registry interface {
	Price(ctx context.Context, resource string) (terms Terms, ok bool, err error)
}

// the published registry (nil until a marketplace Publishes one).
var (
	regMu sync.RWMutex
	reg   Registry
)

// Publish installs the marketplace registry x402 enforces against. Passing nil
// detaches it (Enforce reverts to passthrough). One registry, process-wide.
func Publish(r Registry) {
	regMu.Lock()
	reg = r
	regMu.Unlock()
}

func currentRegistry() Registry {
	regMu.RLock()
	defer regMu.RUnlock()
	return reg
}

// state is x402's own data; shared deps live in the embedded cloud.Base.
type state struct {
	store *store
	cfg   Config
	audit *audit.Recorder
	drain *drainer // states every settled payment on the event plane (economic.go)
	// settling serializes the work on one settlement id, on every replica that reaches
	// the store: a draw that is refused withdraws the claim (finish), and a void gives
	// the authorization up, and neither may fall between another replica's claim and
	// its mark.
	settling lock.Keyed
}

// live is the process singleton Enforce + Publish resolve. nil ⇒ not linked ⇒
// Enforce is a passthrough.
var live *cloud.Service[state]

// Mount wires /v1/x402 and the settlement store. Direct construction (not
// cloud.Use) because it holds the package singleton the middleware reaches.
func Use(app cloud.Router, deps cloud.Deps) error {
	if luxlog.Default() == nil {
		return errUse("nil logger")
	}
	if cloud.DataDir() == "" {
		return errUse("empty DataDir")
	}
	st, err := openStore(cloud.DataDir())
	if err != nil {
		return errUse("open store: " + err.Error())
	}
	s := &cloud.Service[state]{Base: cloud.NewBase(deps, providerLabel), State: state{
		store: st,
		cfg:   configFromEnv(),
		audit: deps.Audit,
	}}
	live = s
	routes(app, s)
	expose(s)
	// Finish anything the last process left in doubt. A settlement claimed but not
	// completed is money owed in one direction or the other, and a restart is the
	// one moment we know no request is still holding it.
	if n, err := Reconcile(context.Background(), 0); err != nil {
		s.Log.Error("x402: startup reconcile failed", "err", err)
	} else if n > 0 {
		s.Log.Info("x402: startup reconcile completed settlements", "count", n)
	}
	// And state every settled payment the event plane has not landed, from now on.
	s.State.drain = drain(s)
	s.Log.Info("x402 live", "network", s.State.cfg.Network,
		"asset", s.State.cfg.Asset != "")
	return nil
}

// ops binds the subsystem to its typed handler: a TypedHandler has no parameter
// for the service, so it arrives as a RECEIVER and the op is a method value —
// the only bound form cmd/zipdoc can lift prose from.
type ops struct{ s *cloud.Service[state] }

// routes registers the receipt lookup and the metered price table.
//
// cloud.Bridge parks the request a typed op's signature drops, which is how the
// op below resolves the PAYER (see settlement). The composer owns that install,
// once at its root; the group is a bare path prefix.
func routes(app cloud.Router, s *cloud.Service[state]) {
	o := ops{s: s}
	g := app.Group("/v1/x402")
	g.Get("/settlements", o.settlements)
	g.Get("/settlements/:id", o.settlement)
	// The platform's flat fees, public (metered.go): the table, and one fee's 402.
	g.Get("/prices", o.prices)
	g.Get("/prices/:product", o.price, zip.WithStatus(http.StatusPaymentRequired))
}

// Sentinel outcomes of Settle — the typed half of the same flow Enforce renders as
// an HTTP response. BOTH are fail-closed: a caller that gets either must refuse the
// call, never serve it. A price that cannot be enforced is not a free price.
var (
	// ErrPaymentRequired — the caller has not paid: no payment, an authorization
	// that does not verify, or a replayed nonce. The CHALLENGE is on the response's
	// PAYMENT-REQUIRED header, so a client can pay and retry.
	ErrPaymentRequired = errors.New("x402: payment required")
	// ErrUnavailable — payment could not be enforced at all: x402 is not live in
	// this process, the price table or the recipient wallet did not resolve, the
	// caller has no billable identity, or settlement failed.
	ErrUnavailable = errors.New("x402: payment enforcement unavailable")
)

// Enforce is the pay-per-use middleware a priced route group applies, keyed on the
// request PATH.
//
// Applying it is a DECLARATION that the group is for sale, so anything that leaves
// x402 unable to answer what the group costs is a refusal, never a passthrough. It
// refuses when x402 is not live in this process and when no price table has been
// published — the two ways "I cannot enforce payment here" arises — because the
// alternative renders both as "free", permanently and silently.
//
// That is not hypothetical. The published Registry is a process-global installed by
// marketplace.Use, and the shipped topology is one binary per app (manifest/apps.go;
// Dockerfile builds a plugin per row; cmd/cloud loads each as a child process), so a
// process that mounts x402 does NOT have marketplace in it and the table is nil. The
// fleet has been bitten by exactly this once already — see resource_billing_peer.go:
// "Splitting apps into their own binaries turned every priced create free without
// changing a line of billing code."
//
// The TOOL path asks a different question — it offers EVERY dispatch to the client,
// free ones included, so it must be able to answer "this costs nothing" — but it
// reaches the same safe answer by asking the process that OWNS the table (peer.go)
// rather than by reading its own absence as free. A middleware is applied only to
// what is FOR SALE and can refuse on sight; the tool client is applied to everything
// and has to look. Neither ever renders "I cannot tell" as "free".
// IT PRICES BY PATH, so it covers a ROUTE and covers exactly that. An operation
// reached by name — over MCP, the call plane, the graph or the CLI — arrives with
// no path to look up, which is not a gap in this handler but the reason [Settle]
// exists: it prices a named resource and is asked from inside the operation, where
// every seam passes. Apply this to raw routes whose address IS the priced thing;
// price an operation with Settle.
func Enforce() zip.Handler {
	return func(c *zip.Ctx) error {
		s := live
		if s == nil {
			return refuse(c, unenforceable("x402 is not live in this process"))
		}
		if currentRegistry() == nil {
			return refuse(c, unenforceable("no price table is published in this process"))
		}
		terms, priced, err := priceOf(c.Context(), c.Path())
		if err != nil {
			return refuse(c, unavailable()) // price lookup blip → never serve a priced route free
		}
		if !priced {
			return c.Next() // the TABLE says this route is free — an answer, not a silence
		}
		// The operator the boundary attested rides the context run reads it from: a
		// raw route has no typed op to park it.
		ctx := principal.WithAct(c.Context(), c)
		g := run(s, ctx, meter.Payer(c), c.User(), c.Header(protocol.HeaderPaymentSignature), c.Path(), terms)
		if g.receipt != nil {
			served(s, ctx, c, g)
			return c.Next()
		}
		return refuse(c, g)
	}
}

// Settle enforces payment for one resource against the REQUEST bound to ctx — the
// same flow Enforce runs, for a caller that identifies the priced thing itself
// rather than by path (the tool plane prices a TOOL, and every tool call arrives on
// the one /v1/tool/call route).
//
// Free resource → nil, and no request is needed: an unpriced call off the HTTP path
// (the CLI's LocalInvoke) still runs. A PRICED one always needs the request, because
// the payer is the attested principal on it and the proof rides its headers.
//
// Unpaid → the challenge is written to the response's PAYMENT-REQUIRED header and
// ErrPaymentRequired is returned, so the caller refuses 402 and the client can pay
// and retry. Paid → settled exactly once, the settlement is on PAYMENT-RESPONSE, nil.
func Settle(ctx context.Context, resource string) error {
	// FREE FIRST, before anything is required of the world. A caller that offers
	// every call to this client — which is the only way a gate and a settlement cannot
	// disagree — must not have its free calls broken by a payment rail that is
	// merely absent. Nothing unpriced needs x402 live, a request, or a payer.
	terms, priced, err := priceOf(ctx, resource)
	if err != nil {
		return fmt.Errorf("%w: price lookup failed: %v", ErrUnavailable, err)
	}
	if !priced {
		return nil
	}
	s := live
	if s == nil {
		return fmt.Errorf("%w: x402 is not live in this process", ErrUnavailable)
	}
	c, _ := cloud.Request(ctx) // nil off the HTTP path; run refuses a PRICED resource there
	var payer account.Account
	var agent, payment string
	if c != nil {
		payer, agent, payment = meter.Payer(c), c.User(), c.Header(protocol.HeaderPaymentSignature)
	}
	g := run(s, ctx, payer, agent, payment, resource, terms)
	if g.receipt != nil {
		served(s, ctx, c, g)
		return nil
	}
	if c != nil {
		writeRefusal(c, g)
	}
	if g.status == http.StatusPaymentRequired {
		return fmt.Errorf("%w: %s (%s)", ErrPaymentRequired, g.msg, g.code)
	}
	return fmt.Errorf("%w: %s (%s)", ErrUnavailable, g.msg, g.code)
}

// priceOf is THE free/priced decision, asked of the published price table. ok=false
// means the resource costs nothing — no table anywhere, no entry, or an entry whose
// price is not positive, which are the same fact and answer the same way.
//
// It is deliberately separate from run and takes no service: whether something is
// free must be answerable with nothing live and no request in hand, or a caller
// that offers EVERY call to the payment client cannot exist.
//
// TWO TRANSPORTS, ONE TABLE. The published Registry is a process-global installed
// by marketplace.Use, and the fleet runs one process per app — so in the x402
// binary it is nil, and reading that as "nothing is priced" was the fail-OPEN half
// of this defect. Nil means "the table is not HERE", and the process that owns it is
// asked. A table that cannot be asked leaves the price UNKNOWN, which is an error
// and never a zero (peer.go).
func priceOf(ctx context.Context, resource string) (Terms, bool, error) {
	// The platform's own fees are priced here, from their knobs, never asked of the
	// marketplace: no listing names them and no peer has to be up to price them.
	if terms, metered, priced := meteredTerms(resource); metered {
		return terms, priced, nil
	}
	r := currentRegistry()
	if r == nil {
		return pricePeer(ctx, resource)
	}
	terms, priced, err := r.Price(ctx, resource)
	if err != nil {
		return Terms{}, false, err
	}
	if !priced || terms.Amount.Sign() <= 0 {
		return Terms{}, false, nil
	}
	return terms, true, nil
}

// gate is the OUTCOME of one x402 enforcement: either settled (receipt) or refused
// (status + code + msg, carrying req when there is a challenge to advertise). It
// exists so the flow is decided once and rendered twice — as a response by Enforce,
// as an error by Settle — with no second copy of the policy.
//
// The wire renderings both hang off it: [gate.required] is the PaymentRequired a
// 402 advertises, [gate.settlement] is the SettlementResponse every answered
// request carries. Neither is stored, because a rendering that is stored is a
// rendering that can disagree with the outcome it renders.
type gate struct {
	receipt  *Receipt
	req      *protocol.PaymentRequirements
	resource string
	payer    string // payer ADDRESS, when a payload named one
	status   int
	code     string
	msg      string
}

// required renders the 402 challenge: the resource, and every way it may be paid
// for. The `accepts` array is where multi-asset pricing would arrive; this rail
// quotes exactly one way to pay, so it holds exactly one.
func (g gate) required() *protocol.PaymentRequired {
	if g.req == nil {
		return nil
	}
	return &protocol.PaymentRequired{
		X402Version: protocol.Version,
		Error:       g.msg,
		Resource:    resourceInfo(g.resource),
		Accepts:     []protocol.PaymentRequirements{*g.req},
	}
}

// settlement renders the SettlementResponse. The spec requires one on the FAILURE
// leg too, which is why this is defined for a refusal and not only for a receipt:
// a client that cannot tell "your signature was bad" from "our ledger is down"
// cannot decide whether retrying the same authorization is worth anything.
func (g gate) settlement() protocol.SettlementResponse {
	if g.receipt != nil {
		return protocol.SettlementResponse{
			Success: true, Payer: g.receipt.From, Transaction: g.receipt.settledTx(),
			Network: g.receipt.Network, Amount: g.receipt.Amount,
		}
	}
	s := protocol.SettlementResponse{Success: false, ErrorReason: g.code, Payer: g.payer}
	if g.req != nil {
		s.Network = g.req.Network
	}
	return s
}

// run is THE x402 enforcement for one PRICED resource: unpaid → challenge; paid →
// verify, settle once, receipt.
//
// It takes the payer, the agent acting for it and the proof as VALUES rather than a
// *zip.Ctx, because the process that holds the request is no longer necessarily the
// one that holds the rail. Three callers pass them from three places and the policy
// is written once:
// Enforce reads them off the request it is middleware on, Settle off the request
// parked on the context, and the plane op off the capability and the body of an
// internal call made by the process that does hold one. The payer is the ACCOUNT
// principal.Payer resolves — the books it is drawn in and the wallet within them —
// and never an org standing in for it: in the signup org that org's own account is
// the platform's pool. A zero payer is the same refusal in all three — nobody to
// charge — whether that is an anonymous request or no request at all.
func run(s *cloud.Service[state], ctx context.Context, payer account.Account, agent, paymentHdr, resource string, terms Terms) gate {
	if g, refused := supporting(ctx, resource); refused {
		return g
	}
	c, g := verify(s, ctx, payer, paymentHdr, resource, terms)
	if c.pay == nil {
		return g
	}
	req, pay, target := c.req, *c.pay, c.target

	// SETTLE once (replay-safe).
	receipt, err := settle(s, ctx, req, pay, terms, target, payer, agent, resource)
	if err != nil {
		if _, ok := errors.AsType[*replayed](err); ok {
			g.code, g.msg = reasonReplay, err.Error()
			return g
		}
		if inv, ok := errors.AsType[*protocol.Error](err); ok { // an expired authorization with nothing to resume
			g.code, g.msg = inv.Reason, inv.Message
			return g
		}
		s.Log.Error("x402: settlement failed", "resource", resource, "payer", payer.Subject(), "err", err)
		u := unavailable() // settlement backend down → never serve unpaid
		u.req, u.resource, u.payer = &req, resource, g.payer
		return u
	}
	return gate{receipt: receipt, resource: resource, payer: receipt.From}
}

// supporting refuses a payment made in a support session — a SuperAdmin signed in
// AS the person (cloud impersonation.go). The ledger draws the person's wallet
// whoever signed the authorization, so a signature made in the window is no consent
// of the person's, and their money is theirs to spend signed in as themselves.
func supporting(ctx context.Context, resource string) (gate, bool) {
	if principal.ActFrom(ctx).Sub == "" {
		return gate{}, false
	}
	return gate{resource: resource, status: http.StatusForbidden, code: reasonSupport,
		msg: "a support session cannot pay: the person does that signed in as themselves"}, true
}

// checked is a payment that verified against the terms it was challenged with:
// the requirements stated, the payee they name, and the payload. pay is nil when
// the payment was refused or absent.
type checked struct {
	req    protocol.PaymentRequirements
	target wallet.PaymentTarget
	pay    *protocol.PaymentPayload
}

// verify is the half of the flow that moves nothing: the payee resolved, the terms
// stated, and a payment checked against them. Everything it checks is true or false
// forever; the TIME WINDOW is not checked here, because settle must tell a new
// authorization apart from one it already accepted (see settle). A refusal comes
// back as the gate the caller renders.
func verify(s *cloud.Service[state], ctx context.Context, payer account.Account, paymentHdr, resource string, terms Terms) (checked, gate) {
	// Ledger settlement draws a wallet, so a resolved payer is required.
	if payer.Zero() {
		return checked{}, gate{resource: resource, status: http.StatusForbidden, code: reasonUnbillable, msg: "sign in"}
	}

	// Resolve the recipient wallet the marketplace named (org-scoped in wallets, so
	// a listing can only ever be paid into a wallet of its OWN publisher org).
	target, ok := payeeOf(ctx, terms.RecipientOrg, terms.RecipientWalletID)
	if !ok {
		s.Log.Error("x402: recipient wallet unresolved",
			"resource", resource, "recipientOrg", terms.RecipientOrg, "wallet", terms.RecipientWalletID)
		return checked{}, gate{resource: resource, status: http.StatusServiceUnavailable, code: reasonPayee,
			msg: "resource recipient wallet is not resolvable"}
	}

	req, err := requirements(s.State.cfg, terms, cmp.Or(terms.PayTo, target.Address))
	if err != nil {
		// A challenge we cannot even STATE is a misconfigured rail, not a refusal the
		// client can answer: it would be quoting a network no signature can be made
		// against. Fail closed and name it.
		s.Log.Error("x402: cannot state payment requirements", "resource", resource, "err", err)
		return checked{}, gate{resource: resource, status: http.StatusServiceUnavailable,
			code: reasonOf(err), msg: err.Error()}
	}

	// CHALLENGE: no payment yet → 402 with the payment requirements.
	paymentHdr = strings.TrimSpace(paymentHdr)
	if paymentHdr == "" {
		return checked{req: req, target: target}, challenge(req, resource, reasonRequired, "payment required for "+resource)
	}

	pay, err := protocol.ParsePayment(paymentHdr)
	if err != nil {
		return checked{req: req, target: target}, challenge(req, resource, reasonOf(err), err.Error())
	}
	g := challenge(req, resource, "", "")
	g.payer = pay.Payload.Authorization.From
	if err := protocol.Verify(req, pay); err != nil {
		g.code, g.msg = reasonOf(err), err.Error()
		return checked{req: req, target: target}, g
	}
	return checked{req: req, target: target, pay: &pay}, g
}

// challenge is a 402 that tells the client exactly what to pay. Every 402 carries
// the requirements, not just the first: a client whose authorization expired or
// replayed can re-sign from the refusal alone.
func challenge(req protocol.PaymentRequirements, resource, code, msg string) gate {
	return gate{req: &req, resource: resource, status: http.StatusPaymentRequired, code: code, msg: msg}
}

func unavailable() gate {
	return gate{status: http.StatusServiceUnavailable, code: reasonUnavailable,
		msg: "payment settlement temporarily unavailable"}
}

// unenforceable is the refusal for a priced route x402 cannot price at all — a
// missing dependency in THIS process, not a transient fault. It carries no
// challenge, because there are no terms to offer: a client cannot pay its way past
// a payment rail that is not here. The reason is named so the failure reads as a
// deployment fact in the log rather than a mystery 503.
func unenforceable(why string) gate {
	return gate{status: http.StatusServiceUnavailable, code: reasonUnenforceable,
		msg: "payment cannot be enforced for this resource: " + why}
}

// served records a settled payment: the audit row and the PAYMENT-RESPONSE header
// the answer carries back. One place, so Enforce and Settle cannot drift on it.
func served(s *cloud.Service[state], ctx context.Context, c *zip.Ctx, g gate) {
	emitAudit(s, ctx, g.receipt)
	if c != nil {
		c.SetHeader(protocol.HeaderPaymentResponse, encode(g.settlement()))
	}
}

// writeRefusal puts the x402 refusal on the RESPONSE HEADERS: the challenge on
// PAYMENT-REQUIRED so a client can pay and retry, and the SettlementResponse on
// PAYMENT-RESPONSE, which the spec requires on the failure leg too.
//
// It is separate from [refuse] because the tool plane's caller writes the headers
// onto a response whose BODY is its own — the payment failed, but the thing being
// answered is a tool call, not an x402 request.
func writeRefusal(c *zip.Ctx, g gate) {
	if req := g.required(); req != nil {
		c.SetHeader(protocol.HeaderPaymentRequired, encode(req))
	}
	c.SetHeader(protocol.HeaderPaymentResponse, encode(g.settlement()))
}

// refuse writes the x402 refusal: the headers, AND the challenge in the body's
// `accepts`, so a client reading either can pay and retry. The body is a server
// implementation concern in x402 v2 (all protocol information is in the headers),
// so what is in it mirrors them rather than inventing a second contract.
func refuse(c *zip.Ctx, g gate) error {
	writeRefusal(c, g)
	body := payErr(g.code, g.msg)
	if req := g.required(); req != nil {
		body["accepts"] = req.Accepts
		body["x402Version"] = req.X402Version
	}
	return c.JSON(g.status, body)
}

// replayed is a nonce reused for terms it was not signed for.
type replayed struct{ id string }

func (e *replayed) Error() string { return "payment nonce already used: " + e.id }

// settle enforces settle-once and performs the ledger settlement.
//
// Dedup + settle-once: the settlement id is DETERMINISTIC in (From, Nonce), so a
// re-submitted authorization maps to the same row. A found row with MATCHING terms
// is the same settlement; a found row with DIFFERENT terms is nonce reuse across
// resources → replay.
//
// CLAIM BEFORE THE MONEY MOVES. The row is written FIRST, unsettled, and only then
// is money moved against it. That ordering is the whole recovery story:
//
//   - A crash or a failure at ANY point after the claim leaves a durable record of
//     a settlement in flight, keyed on the same id both money writes are idempotent
//     on. Nothing is ever moved that nothing names.
//   - The old order — move, then record — could lose the record after the money
//     moved, and then a retry re-derived the same id, found no row, and moved it
//     again. Idempotency at commerce is what stopped that from double-charging,
//     which means the invariant was being held one layer away from where it was
//     stated.
//   - Marking the row settled is also what makes its statement on the event plane
//     OWED (economic.go): one write, so a settled payment is never one the plane
//     is not owed. The statement is drained off this path — the client is served
//     the moment its money has moved, whether or not the plane is up.
//
// AND THE WINDOW GATES ACCEPTANCE, NOT COMPLETION. [InWindow] is checked only when
// there is no claim yet. Once a claim exists, this authorization WAS valid when we
// took it, and validBefore has no further say: EIP-3009's window bounds when a
// transfer may be submitted, not how long the submission takes to finish. That one
// distinction is what removes the money-loss the previous implementation documented
// and left in place — a payer whose debit landed and whose credit did not could
// only recover by re-presenting a signature that expired 300 seconds later, so a
// client that gave up for five minutes was permanently debited with nothing served.
// Now the same client is served whenever it comes back, and [Reconcile] finishes
// the settlement even if it never does.
func settle(s *cloud.Service[state], ctx context.Context, req protocol.PaymentRequirements, pay protocol.PaymentPayload,
	terms Terms, target wallet.PaymentTarget, payer account.Account, agent, resource string) (*Receipt, error) {

	a := pay.Payload.Authorization
	id := settlementID(a.From, a.Nonce)
	st := &Settlement{
		ID: id, PayerOrg: payer.Org(), PayerSubject: payer.Subject(), From: a.From, Nonce: a.Nonce,
		Resource: resource, Payee: req.PayTo, PayeeOrg: target.Org,
		PayeeSubject: target.Subject, Agent: agent, Category: terms.Category, Amount: terms.Amount.AttoString(),
		Network: req.Network, SettledVia: "ledger", CreatedAt: nowUnix(),
	}

	// One settlement of one authorization at a time, on every replica: a draw the
	// wallet refuses withdraws the claim (finish), and that must not race the same
	// claim being completed by another replica's request or by its Reconcile.
	release, err := s.State.settling.Across(ctx, s.State.store.db, id)
	if err != nil {
		return nil, err
	}
	defer release()

	// An authorization held for another resource pays that one or nothing, and one
	// given up pays nothing.
	if held, ok, err := s.State.store.held(ctx, id); err != nil {
		return nil, err
	} else if ok && (held.resource != resource || held.given) {
		return nil, &replayed{id: id}
	}
	claim, err := claimOf(ctx, s.State.store, st)
	if err != nil {
		return nil, err
	}
	if claim == nil {
		// No claim yet, so this authorization is being ACCEPTED now — and that is the
		// one moment its time window has anything to say.
		if err := protocol.InWindow(a, atTime()); err != nil {
			return nil, err
		}
		claimed, existing, err := s.State.store.claim(ctx, st)
		if err != nil {
			return nil, err
		}
		if !claimed { // lost a concurrent race — the winner's row is the claim
			if existing == nil || !sameTerms(existing, st) {
				return nil, &replayed{id: id}
			}
			claim = existing
		} else {
			claim = st
		}
	}
	if claim.Settled {
		if terms.Once {
			return nil, &replayed{id: id} // it bought its one act already
		}
		return receiptOf(claim), nil // already paid for — serve it again, charge nothing
	}
	if err := finish(s, ctx, claim, terms.Amount); err != nil {
		return nil, err
	}
	claim.Settled = true
	s.State.drain.nudge()
	return receiptOf(claim), nil
}

// finish moves the money a claim names and marks it settled, under the claim's
// lock.
//
// A payer whose wallet cannot cover the payment is refused with nothing moved —
// the draw is all or nothing — and the claim is withdrawn, because it names money
// that will not move: left in place, Reconcile would take the payment the moment
// the payer next funded its wallet, for a call or a job that was refused. The
// payer may present the same authorization again once the wallet covers it.
func finish(s *cloud.Service[state], ctx context.Context, claim *Settlement, amount money.Amount) error {
	err := settleLedger(s, ctx, claim, amount)
	if errors.Is(err, errShort) {
		if uerr := s.State.store.unclaim(ctx, claim.ID); uerr != nil {
			return uerr // the claim is no longer this call's to withdraw: say nothing of the wallet
		}
		return &protocol.Error{Reason: protocol.ReasonInsufficientFunds, Message: "the payer's wallet does not cover " + amount.String()}
	}
	if err != nil {
		return err
	}
	if err := s.State.store.markSettled(ctx, claim.ID, claim.TxHash); err != nil {
		// The money moved and the row would not take the mark. Nothing is served, and
		// that is recoverable rather than lost: both money writes are idempotent on
		// the claim's id, so the next attempt — the client's, or Reconcile's — re-runs
		// them for free and marks it, or finds it marked.
		s.Log.Error("x402: settled but not marked — reconcile by id", "id", claim.ID, "err", err)
		return err
	}
	return nil
}

// claimOf reads the existing claim for a settlement, or nil when there is none.
// A row whose terms differ is not this settlement at all — it is the SAME nonce
// spent on something else, which is a replay and can never become valid.
func claimOf(ctx context.Context, st *store, want *Settlement) (*Settlement, error) {
	ex, found, err := st.get(ctx, want.ID)
	if err != nil || !found {
		return nil, err
	}
	if !sameTerms(ex, want) {
		return nil, &replayed{id: want.ID}
	}
	return ex, nil
}

// settleLedger is the LIVE settlement backend: it draws the payment from the
// payer's wallet — all or nothing, refused when the wallet cannot cover it —
// and credits the recipient wallet's ledger. Both writes are idempotent on the
// settlement id (the finance ref), so a retried settle never double-moves money.
// On-chain broadcast of the authorization is a SEPARATE client (not wired) — this
// path is ledger-only.
//
// BOTH SIDES ARE MANDATORY. Each write used to be skipped when its backend was
// absent, which is the one thing a settlement may never do: with no payer's side
// the buyer was served and never charged while the seller was credited — money
// minted out of a missing dependency — and with no payee's side the buyer paid
// into nothing. A settlement that cannot move both sides does not happen at all;
// the caller answers 503 and the resource is never served, so the worst case is an
// outage rather than a silent half-transfer nobody can reconcile.
//
// The prepaid ledger has ONE writer and it is commerce's process, so in the shipped
// fleet neither backend is here: both halves cross the plane, to the same peer, and
// a debit that lands means that peer is up for the credit that follows.
//
// A credit that fails ANYWAY is not compensated, and that is still the deliberate
// answer: the failure that makes compensation necessary is usually a TIMEOUT, and a
// timeout does not mean the credit did not commit — so a reversing entry would
// refund a payer who was correctly charged while the seller kept the money, minting
// the difference out of a slow socket.
//
// What HAS changed is that not-compensating is no longer the same as not-recovering.
// Both writes are idempotent on the settlement id and the CLAIM is already durable
// before either runs (settle), so an incomplete settlement is a row that names
// exactly what is owed and to whom. Retrying it is free and converges — from the
// client whenever it returns, with no expiry to beat, and from [Reconcile] when it
// never does. An unretried debit used to be a number the log named and nobody
// swept; it is now a row the sweep finishes.
func settleLedger(s *cloud.Service[state], ctx context.Context, st *Settlement, amount money.Amount) error {
	if st.PayeeOrg == "" {
		return errors.New("x402: no payee org to credit")
	}
	if err := debitPayer(ctx, st, amount); err != nil {
		return err
	}
	if err := creditPayee(ctx, st, amount); err != nil {
		s.Log.Error("x402: payer debited, payee credit failed — settlement incomplete, reconcilable by id",
			"id", st.ID, "payer", st.wallet(), "payeeOrg", st.PayeeOrg,
			"amount", amount.String(), "err", err)
		return err
	}
	return nil
}

// Reconcile finishes every settlement that was CLAIMED but never completed — the
// in-doubt window of any two-process money movement, made recoverable by the claim
// being written before the money moves.
//
// It is exactly the sweep the previous implementation described and did not have,
// and claiming first is what made it cheap: it reads THIS store's own unsettled
// rows rather than needing a cross-process reader of commerce's usage rows to
// discover which debits had no settlement. Every row carries the payer, the payee
// and the amount, and both money writes are idempotent on its id — so replaying
// them either completes the settlement or costs nothing, and no signature is
// involved, which is why an expired authorization cannot block it.
//
// olderThan skips rows still on a live request's path, so the sweep never races the
// flow that owns them. Mount runs it once at startup; it is safe to run at any time
// and any number of times.
func Reconcile(ctx context.Context, olderThan time.Duration) (completed int, err error) {
	s := live
	if s == nil {
		return 0, nil
	}
	pending, err := s.State.store.pending(ctx, nowUnix()-int64(olderThan.Seconds()))
	if err != nil {
		return 0, err
	}
	for _, st := range pending {
		amount, perr := money.ParseInt(st.Amount)
		if perr != nil {
			s.Log.Error("x402: reconcile: unreadable amount", "id", st.ID, "err", perr)
			continue
		}
		release, gerr := s.State.settling.Across(ctx, s.State.store.db, st.ID)
		if gerr != nil {
			s.Log.Error("x402: reconcile: not completed", "id", st.ID, "err", gerr)
			continue
		}
		cur, found, gerr := s.State.store.get(ctx, st.ID)
		if gerr == nil && found && !cur.Settled {
			gerr = finish(s, ctx, st, amount)
		}
		release()
		if gerr != nil || !found || cur.Settled {
			if gerr != nil {
				s.Log.Error("x402: reconcile: not completed", "id", st.ID, "err", gerr)
			}
			continue
		}
		completed++
		s.Log.Info("x402: reconcile: settlement completed", "id", st.ID, "payer", st.wallet())
	}
	if completed > 0 {
		s.State.drain.nudge()
	}
	return completed, nil
}

// errShort is a payer's wallet refusing a draw it cannot cover. Nothing moved.
var errShort = errors.New("x402: the payer's wallet does not cover the payment")

// debitPayer moves the buyer's half as a DRAW: a payment the wallet must cover in
// full, checked and posted in one transaction by the ledger that holds the wallet.
//
// It was a metered debit, and a metered debit lands whatever the wallet holds,
// because a metered call was admitted before it ran. Nothing admitted this one: an
// org with nothing on its books paid any sum into a colluding seller's ledger, the
// seller holding real balance and the payer's negative one reading as zero. A
// payment is decided before anything is sold, so the wallet is asked then, in the
// same transaction as the posting, where no concurrent spend can come between.
//
// The ledger in this process when there is one, else the ledger's own process over
// the plane, with the settlement id as the idempotency key on both. It draws the
// wallet the settlement was claimed for, inside its books.
func debitPayer(ctx context.Context, st *Settlement, amount money.Amount) error {
	if d, ok := finance.Current().(finance.Drawer); ok {
		_, _, err := d.Draw(ctx, money.UsageInput{
			Org: st.PayerOrg, Subject: st.wallet(), Amount: amount, Currency: "usd",
			Provider: providerLabel, Service: providerLabel, Model: st.Resource,
			// The SETTLEMENT is the act, and its id is the server's name for it — so a
			// reconcile that draws a settlement again moves the money once.
			Ref: st.ID,
		})
		if errors.Is(err, finance.ErrShortWallet) {
			return fmt.Errorf("%w: %v", errShort, err)
		}
		return err
	}
	return debitPeer(st, amount)
}

// errGivenUp is a hold asked for again under a settlement whose hold was given
// back, or that names another amount: that authorization pays nothing now, and
// the payer signs another.
var errGivenUp = errors.New("x402: this payment's hold was given up")

// holdPayer sets a payment agreed before it is made aside in the payer's wallet,
// under its settlement id — the ref the draw that makes it names, so the draw is
// paid from what was set aside. A wallet that cannot cover it is [errShort].
//
// The ledger in this process when there is one, else the ledger's own process
// over the plane, as for the draw. It sets the amount aside in the payer's wallet,
// the one the settlement that pays it will draw.
func holdPayer(ctx context.Context, payer account.Account, id, resource string, amount money.Amount) error {
	if h, ok := finance.Current().(finance.Holder); ok {
		_, _, err := h.Hold(ctx, money.UsageInput{
			Org: payer.Org(), Subject: payer.Subject(), Amount: amount, Currency: "usd",
			Provider: providerLabel, Service: providerLabel, Model: resource, Ref: id,
		})
		switch {
		case errors.Is(err, finance.ErrShortWallet):
			return fmt.Errorf("%w: %v", errShort, err)
		case errors.Is(err, finance.ErrReturned), errors.Is(err, finance.ErrRefReused):
			return fmt.Errorf("%w: %v", errGivenUp, err)
		}
		return err
	}
	return holdPeer(payer, id, resource, amount)
}

// returnPayer gives a payment that will never be made back to the payer's wallet,
// and answers what was still set aside.
func returnPayer(ctx context.Context, payer account.Account, id, resource string) (money.Amount, error) {
	if h, ok := finance.Current().(finance.Holder); ok {
		return h.Return(ctx, money.UsageInput{Org: payer.Org(), Subject: payer.Subject(), Model: resource, Ref: id})
	}
	return returnPeer(payer, id, resource)
}

// creditPayee moves the seller's half, into the ledger of the org that published
// the listing — never one the buyer named.
func creditPayee(ctx context.Context, st *Settlement, amount money.Amount) error {
	if fin := finance.Current(); fin != nil {
		_, err := fin.Deposit(ctx, money.DepositInput{
			Org: st.PayeeOrg, Subject: st.PayeeSubject, Amount: amount, Currency: "usd",
			Ref: st.ID, Notes: "x402:" + st.Resource, Tags: "x402",
		})
		return err
	}
	return creditPeer(st.PayeeOrg, st.PayeeSubject, amount, st.ID, "x402:"+st.Resource)
}

// settlementRef addresses one settlement by the id its receipt carries.
type settlementRef struct {
	// ID is the settlement id from the URL — the deterministic keccak(from|nonce)
	// key an x402 receipt is issued under (the `id` field of a Receipt, and the
	// `transaction` of the SettlementResponse on the PAYMENT-RESPONSE header a paid
	// request answers with).
	ID string `json:"id"`
}

// Settlement reads one x402 payment receipt by id.
//
// It is scoped to the caller's own account — the wallet that was drawn, in the
// books that hold it — so one tenant can never read another's settlement, nor one
// member of the signup org another's, and an id that exists but belongs to
// somebody else is a 404 exactly like one that does not exist. A caller with no
// billable identity is refused outright.
func (o ops) settlement(ctx context.Context, in *settlementRef) (*Receipt, error) {
	payer := payerOf(ctx)
	if payer.Zero() {
		return nil, zip.ErrForbidden("sign in")
	}
	st, found, err := o.s.State.store.getScoped(ctx, payer, strings.TrimSpace(in.ID))
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "get settlement: %v", err)
	}
	if !found {
		return nil, zip.ErrNotFound("settlement not found")
	}
	return receiptOf(st), nil
}

// settlementsQuery narrows the caller's receipts.
type settlementsQuery struct {
	// Role is payer — what the caller's org paid — or payee — what it was paid.
	// Payer when empty.
	Role string `json:"role"`
	// Year keeps the calendar year (UTC) the payments settled in. Zero keeps
	// every year.
	Year int `json:"year"`
}

// settlementList is a page of receipts, newest first. It is never null.
type settlementList struct {
	// Settlements is at most 1000 receipts.
	Settlements []*Receipt `json:"settlements"`
}

// maxReceipts bounds one read.
const maxReceipts = 1000

// Settlements lists the caller's x402 receipts, newest first: as payer, what its
// wallet paid; as payee, what its org was paid — each settled payment with what it
// bought, both parties, the exact amount and when. A payer is the caller's own
// account, which for a SuperAdmin inspecting another org is still its own and for
// a member of the signup org is their own wallet, never the org's; a payee is the
// caller's own org. An unsettled claim is not a receipt and is never listed.
//
// Example: {"role": "payee", "year": 2026}
func (o ops) settlements(ctx context.Context, in *settlementsQuery) (*settlementList, error) {
	var payer account.Account
	var org string
	switch in.Role {
	case "", "payer":
		payer = payerOf(ctx)
		org = payer.Org()
	case "payee":
		org, _ = principal.Acting(ctx)
	default:
		return nil, zip.ErrBadRequest("role is payer or payee")
	}
	if org == "" {
		return nil, zip.ErrForbidden("sign in")
	}
	from, to := int64(0), int64(1)<<62
	if in.Year != 0 {
		if in.Year < 2000 || in.Year > 9999 {
			return nil, zip.ErrBadRequest("year is a calendar year")
		}
		from = time.Date(in.Year, 1, 1, 0, 0, 0, 0, time.UTC).Unix()
		to = time.Date(in.Year+1, 1, 1, 0, 0, 0, 0, time.UTC).Unix()
	}
	var rows []*Settlement
	var err error
	if payer.Zero() {
		rows, err = o.s.State.store.paidTo(ctx, org, from, to, maxReceipts)
	} else {
		rows, err = o.s.State.store.paidBy(ctx, payer, from, to, maxReceipts)
	}
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list settlements: %v", err)
	}
	out := &settlementList{Settlements: make([]*Receipt, 0, len(rows))}
	for _, st := range rows {
		out.Settlements = append(out.Settlements, receiptOf(st))
	}
	return out, nil
}

// payerOf resolves the account this caller spends from — the same value the
// Enforce middleware draws and the same one every settlement row is keyed on.
//
// It reaches the request cloud.Bridge parked rather than asking
// principal.OrgFrom, because the payer is not the org: principal.Payer folds in
// the SuperAdmin masquerade rule (X-User-IsAdmin + the X-User-Owner home claim),
// so a platform admin inspecting another org still reads their OWN settlements,
// and resolves the wallet within the books from the signed billing_account claim,
// so a member of the signup org reads their own and not every member's. OrgFrom
// carries only X-Org-Id and would silently widen that read to the inspected
// tenant's receipts.
//
// Fails closed off the HTTP path (the CLI's LocalInvoke, where there is no
// request): no request, no attested payer, no receipt.
func payerOf(ctx context.Context) account.Account {
	c, ok := cloud.Request(ctx)
	if !ok {
		return account.Account{}
	}
	return meter.Payer(c)
}

// ── helpers ───────────────────────────────────────────────────────────────────

// requirements builds the ONE way this rail accepts payment for a resource, from
// config + the resource's terms. The network is validated here rather than at the
// point of signing, so a misconfigured chain is a mount-time-shaped failure a log
// names once instead of a signature mismatch every client hits forever.
func requirements(cfg Config, terms Terms, payee string) (protocol.PaymentRequirements, error) {
	network := cmp.Or(strings.TrimSpace(terms.Network), cfg.Network, DefaultNetwork)
	if _, err := protocol.ChainID(network); err != nil {
		return protocol.PaymentRequirements{}, err
	}
	maxTimeout := cfg.MaxTimeout
	if maxTimeout <= 0 {
		maxTimeout = DefaultMaxTimeoutSeconds
	}
	return protocol.Asset{
		Network:           network,
		Address:           cmp.Or(strings.TrimSpace(terms.Asset), cfg.Asset),
		Name:              cfg.assetName(),
		Version:           cfg.assetVersion(),
		MaxTimeoutSeconds: maxTimeout,
	}.Requirements(atomicUnits(terms.Amount, cfg.assetDecimals()), payee), nil
}

// settlementID is the deterministic settle-once key: keccak(from|nonce). Stable
// across retries (so metering + finance idempotency align) and unique per
// authorization.
func settlementID(from, nonce string) string {
	h := keccak([]byte(strings.ToLower(trim0x(from))), []byte("|"), []byte(strings.ToLower(trim0x(nonce))))
	return "x402_" + hex.EncodeToString(h[:])
}

// atomicUnits converts an exact 18-dp USD amount to the asset's atomic unit
// (e.g. USDC 6-dp) for the challenge the client signs. The LEDGER settlement uses
// the exact money.Amount, so no precision is lost where money actually moves.
func atomicUnits(amount money.Amount, decimals int) string {
	i := amount.Atto() // 18-dp magnitude
	if decimals >= money.Decimals {
		return i.String()
	}
	div := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(money.Decimals-decimals)), nil)
	return new(big.Int).Quo(i, div).String()
}

// sameTerms decides whether a re-submitted authorization is the SAME settlement —
// an idempotent retry, answered with the original receipt — or a nonce reused for
// something else, which is a replay and is refused.
//
// The payer — its books and its wallet — is part of the identity and not an
// afterthought. Without it a captured PAYMENT-SIGNATURE header is a bearer token
// across tenants, and across the members of the signup org, who share one org's
// books: org B replays org A's payload,
// the id matches, every other field matches because they describe the same purchase,
// and B is served the priced tool having paid nothing while A's receipt is handed
// back. The payer is who the settlement was FOR, so a different payer is a different
// settlement — and it can only ever be a replay, because the signature commits to
// A's address and B cannot produce one of its own for A's nonce.
func sameTerms(a, b *Settlement) bool {
	return a.Resource == b.Resource && a.Amount == b.Amount &&
		a.PayerOrg == b.PayerOrg && a.wallet() == b.wallet() &&
		addressEqual(a.Payee, b.Payee) && addressEqual(a.From, b.From)
}

func receiptOf(st *Settlement) *Receipt {
	amt, _ := money.ParseInt(st.Amount)
	return &Receipt{
		ID: st.ID, Resource: st.Resource, Payer: st.PayerOrg, From: st.From,
		Payee: st.Payee, PayeeOrg: st.PayeeOrg, Amount: amt.String(), Nonce: st.Nonce,
		Network: st.Network, SettledVia: st.SettledVia, TxHash: st.TxHash, Category: st.Category,
		SettledAt: st.CreatedAt,
	}
}

// settledTx is what the SettlementResponse's `transaction` reports: the chain's
// hash when the authorization was broadcast, and the settlement id when it was
// settled on the ledger. One field, one meaning — the handle that finds this
// settlement again on whichever rail settled it.
func (r *Receipt) settledTx() string {
	if r.TxHash != "" {
		return r.TxHash
	}
	return r.ID
}

func emitAudit(s *cloud.Service[state], ctx context.Context, r *Receipt) {
	if s.State.audit == nil {
		return
	}
	rec := audit.Record{
		Actor:    audit.Actor{Org: r.Payer},
		Action:   "x402.settle",
		Resource: audit.Resource{Type: "x402.settlement", ID: r.ID},
		Auth:     audit.AuthContext{Method: "x402"},
		Outcome:  audit.Outcome{Result: "success", Status: 200},
		After:    audit.Redact(mustJSON(r)),
	}
	if _, err := s.State.audit.Append(ctx, rec); err != nil {
		s.Log.Error("x402: audit emit failed", "id", r.ID, "err", err)
	}
}

func payErr(code, msg string) map[string]any {
	return map[string]any{"error": map[string]any{"code": code, "message": msg}}
}

func configFromEnv() Config {
	return Config{
		Asset:         env.Or("CLOUD_X402_ASSET", ""),
		AssetName:     env.Or("CLOUD_X402_ASSET_NAME", ""),
		AssetVersion:  env.Or("CLOUD_X402_ASSET_VERSION", ""),
		AssetDecimals: env.Int("CLOUD_X402_ASSET_DECIMALS", DefaultAssetDecimals),
		Network:       env.Or("CLOUD_X402_NETWORK", DefaultNetwork),
		MaxTimeout:    int64(env.Int("CLOUD_X402_MAX_TIMEOUT_SECONDS", DefaultMaxTimeoutSeconds)),
	}
}

func errUse(msg string) error { return &useErr{msg} }

type useErr struct{ s string }

func (e *useErr) Error() string { return "x402.Use:  " + e.s }

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return b
}

// Shutdown stops the drainer and closes the settlement store. Idempotent.
func Shutdown() error {
	if live == nil || live.State.store == nil {
		return nil
	}
	live.State.drain.close()
	err := live.State.store.Close()
	live = nil
	return err
}
