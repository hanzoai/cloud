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

	luxlog "github.com/luxfi/log"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/finance"
	"github.com/hanzoai/cloud/apps/metering"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/apps/wallet"
	"github.com/hanzoai/cloud/audit"
	"github.com/hanzoai/cloud/internal/environ"
	"github.com/hanzoai/cloud/money"
	"github.com/hanzoai/cloud/types"
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
	meter *metering.Client // the metering spine — synchronous, idempotent payer debit
	cfg   Config
	audit *audit.Recorder
}

// live is the process singleton Enforce + Publish resolve. nil ⇒ not linked ⇒
// Enforce is a passthrough.
var live *cloud.Service[state]

// Mount wires /v1/x402 and the settlement store. Direct construction (not
// cloud.Use) because it holds the package singleton the middleware reaches.
func Use(app cloud.Router, deps cloud.Deps) error {
	if app == nil || luxlog.Default() == nil {
		return errUse("nil app or logger")
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
		meter: deps.Metering,
		cfg:   configFromEnv(),
		audit: deps.Audit,
	}}
	live = s
	routes(app, s)
	exposeSettle(s)
	// Finish anything the last process left in doubt. A settlement claimed but not
	// completed is money owed in one direction or the other, and a restart is the
	// one moment we know no request is still holding it.
	if n, err := Reconcile(context.Background(), 0); err != nil {
		s.Log.Error("x402: startup reconcile failed", "err", err)
	} else if n > 0 {
		s.Log.Info("x402: startup reconcile completed settlements", "count", n)
	}
	s.Log.Info("x402 live", "network", s.State.cfg.Network,
		"asset", s.State.cfg.Asset != "", "meter", s.State.meter != nil && s.State.meter.Enabled())
	return nil
}

// ops binds the subsystem to its typed handler: a TypedHandler has no parameter
// for the service, so it arrives as a RECEIVER and the op is a method value —
// the only bound form cmd/zipdoc can lift prose from.
type ops struct{ s *cloud.Service[state] }

// routes registers the receipt lookup.
//
// cloud.Bridge parks the request a typed op's signature drops, which is how the
// op below resolves the PAYER (see settlement). The composer owns that install,
// once at its root; the group is a bare path prefix.
func routes(app cloud.Router, s *cloud.Service[state]) {
	g := app.Group("/v1/x402")
	o := ops{s: s}
	zip.Get(g, "/settlements/:id", o.settlement)
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
		g := run(s, c.Context(), principal.Ledger(c), c.Header(HeaderPaymentSignature), c.Path(), terms)
		if g.receipt != nil {
			served(s, c.Context(), c, g)
			return c.Next()
		}
		return refuse(c, g)
	}
}

// Settle enforces payment for one resource against the REQUEST bound to ctx — the
// same flow Enforce runs, for a caller that identifies the priced thing itself
// rather than by path (the tool plane prices a TOOL, and every tool call arrives on
// the one /v1/tools/call route).
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
	var payer, payment string
	if c != nil {
		payer, payment = principal.Ledger(c), c.Header(HeaderPaymentSignature)
	}
	g := run(s, ctx, payer, payment, resource, terms)
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
	req      *PaymentRequirements
	resource string
	payer    string // payer ADDRESS, when a payload named one
	status   int
	code     string
	msg      string
}

// required renders the 402 challenge: the resource, and every way it may be paid
// for. The `accepts` array is where multi-asset pricing would arrive; this rail
// quotes exactly one way to pay, so it holds exactly one.
func (g gate) required() *PaymentRequired {
	if g.req == nil {
		return nil
	}
	return &PaymentRequired{
		X402Version: Version,
		Error:       g.msg,
		Resource:    ResourceInfo{URL: g.resource},
		Accepts:     []PaymentRequirements{*g.req},
	}
}

// settlement renders the SettlementResponse. The spec requires one on the FAILURE
// leg too, which is why this is defined for a refusal and not only for a receipt:
// a client that cannot tell "your signature was bad" from "our ledger is down"
// cannot decide whether retrying the same authorization is worth anything.
func (g gate) settlement() SettlementResponse {
	if g.receipt != nil {
		return SettlementResponse{
			Success: true, Payer: g.receipt.From, Transaction: g.receipt.settledTx(),
			Network: g.receipt.Network, Amount: g.receipt.Amount,
		}
	}
	s := SettlementResponse{Success: false, ErrorReason: g.code, Payer: g.payer}
	if g.req != nil {
		s.Network = g.req.Network
	}
	return s
}

// run is THE x402 enforcement for one PRICED resource: unpaid → challenge; paid →
// verify, settle once, receipt.
//
// It takes the payer and the proof as VALUES rather than a *zip.Ctx, because the
// process that holds the request is no longer necessarily the one that holds the
// rail. Three callers pass them from three places and the policy is written once:
// Enforce reads them off the request it is middleware on, Settle off the request
// parked on the context, and the plane op off the capability and the body of an
// internal call made by the process that does hold one. An empty payer is the same
// refusal in all three — nobody to charge — whether that is an anonymous request or
// no request at all.
func run(s *cloud.Service[state], ctx context.Context, payer, paymentHdr, resource string, terms Terms) gate {
	// Ledger settlement debits an ORG ledger, so a validated payer is required.
	if payer == "" {
		return gate{resource: resource, status: http.StatusForbidden, code: reasonUnbillable, msg: "sign in"}
	}

	// Resolve the recipient wallet the marketplace named (org-scoped in wallets, so
	// a listing can only ever be paid into a wallet of its OWN publisher org).
	target, ok := payeeOf(ctx, terms.RecipientOrg, terms.RecipientWalletID)
	if !ok {
		s.Log.Error("x402: recipient wallet unresolved",
			"resource", resource, "recipientOrg", terms.RecipientOrg, "wallet", terms.RecipientWalletID)
		return gate{resource: resource, status: http.StatusServiceUnavailable, code: reasonPayee,
			msg: "resource recipient wallet is not resolvable"}
	}

	req, err := requirements(s.State.cfg, terms, target.Address)
	if err != nil {
		// A challenge we cannot even STATE is a misconfigured rail, not a refusal the
		// client can answer: it would be quoting a network no signature can be made
		// against. Fail closed and name it.
		s.Log.Error("x402: cannot state payment requirements", "resource", resource, "err", err)
		return gate{resource: resource, status: http.StatusServiceUnavailable,
			code: reasonOf(err), msg: err.Error()}
	}

	// CHALLENGE: no payment yet → 402 with the payment requirements.
	paymentHdr = strings.TrimSpace(paymentHdr)
	if paymentHdr == "" {
		return challenge(req, resource, reasonRequired, "payment required for "+resource)
	}

	// VERIFY the submitted payload against the requirements. Everything checked here
	// is true or false forever; the TIME WINDOW is checked in settle, where it can
	// be told apart from an authorization we already accepted (see settle).
	pay, err := ParsePayment(paymentHdr)
	if err != nil {
		return challenge(req, resource, reasonOf(err), err.Error())
	}
	g := challenge(req, resource, "", "")
	g.payer = pay.Payload.Authorization.From
	if err := Verify(req, *pay); err != nil {
		g.code, g.msg = reasonOf(err), err.Error()
		return g
	}

	// SETTLE once (replay-safe).
	receipt, err := settle(s, ctx, req, *pay, terms, target, payer, resource)
	if err != nil {
		if _, ok := errors.AsType[*replayed](err); ok {
			g.code, g.msg = reasonReplay, err.Error()
			return g
		}
		if inv, ok := errors.AsType[*Invalid](err); ok { // an expired authorization with nothing to resume
			g.code, g.msg = inv.Reason, inv.Detail
			return g
		}
		s.Log.Error("x402: settlement failed", "resource", resource, "payer", payer, "err", err)
		u := unavailable() // settlement backend down → never serve unpaid
		u.req, u.resource, u.payer = &req, resource, g.payer
		return u
	}
	return gate{receipt: receipt, resource: resource, payer: receipt.From}
}

// challenge is a 402 that tells the client exactly what to pay. Every 402 carries
// the requirements, not just the first: a client whose authorization expired or
// replayed can re-sign from the refusal alone.
func challenge(req PaymentRequirements, resource, code, msg string) gate {
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
		c.SetHeader(HeaderPaymentResponse, EncodeHeader(g.settlement()))
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
		c.SetHeader(HeaderPaymentRequired, EncodeHeader(req))
	}
	c.SetHeader(HeaderPaymentResponse, EncodeHeader(g.settlement()))
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
func settle(s *cloud.Service[state], ctx context.Context, req PaymentRequirements, pay PaymentPayload,
	terms Terms, target wallet.PaymentTarget, payerOrg, resource string) (*Receipt, error) {

	a := pay.Payload.Authorization
	id := settlementID(a.From, a.Nonce)
	st := &Settlement{
		ID: id, PayerOrg: payerOrg, From: a.From, Nonce: a.Nonce,
		Resource: resource, Payee: req.PayTo, PayeeOrg: target.Org,
		PayeeSubject: target.Subject, Amount: terms.Amount.AttoString(),
		Network: req.Network, SettledVia: "ledger", CreatedAt: nowUnix(),
	}

	claim, err := claimOf(ctx, s.State.store, st)
	if err != nil {
		return nil, err
	}
	if claim == nil {
		// No claim yet, so this authorization is being ACCEPTED now — and that is the
		// one moment its time window has anything to say.
		if err := InWindow(a, nowUnix()); err != nil {
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
		return receiptOf(claim), nil // already paid for — serve it again, charge nothing
	}
	if err := settleLedger(s, ctx, claim, terms.Amount); err != nil {
		return nil, err
	}
	if err := s.State.store.markSettled(ctx, claim.ID, claim.TxHash); err != nil {
		// The money moved and the row still says otherwise. Nothing is served, and
		// that is recoverable rather than lost: the claim is durable, both money
		// writes are idempotent on its id, and the next attempt — the client's, or
		// Reconcile's — re-runs them for free and marks it.
		s.Log.Error("x402: settled but not marked — reconcile by id", "id", claim.ID, "err", err)
		return nil, err
	}
	claim.Settled = true
	return receiptOf(claim), nil
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

// settleLedger is the LIVE settlement backend: it debits the payer's org through
// the metering spine (so paid usage appears in billing/usage like any metered
// spend) and credits the recipient wallet's ledger. Both writes are idempotent on
// the settlement id (metering RequestID / finance Ref), so a retried settle never
// double-moves money. On-chain broadcast of the authorization is a SEPARATE client
// (not wired) — this path is ledger-only.
//
// BOTH SIDES ARE MANDATORY. Each write used to be skipped when its backend was
// absent, which is the one thing a settlement may never do: with no meter the buyer
// was served and never charged while the seller was credited — money minted out of a
// missing dependency — and with no ledger the buyer paid into nothing. A settlement
// that cannot move both sides does not happen at all; the caller answers 503 and the
// resource is never served, so the worst case is an outage rather than a silent
// half-transfer nobody can reconcile.
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
	if err := debitPayer(s, ctx, st, amount); err != nil {
		return err
	}
	if err := creditPayee(ctx, st, amount); err != nil {
		s.Log.Error("x402: payer debited, payee credit failed — settlement incomplete, reconcilable by id",
			"id", st.ID, "payer", st.PayerOrg, "payeeOrg", st.PayeeOrg,
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
		if serr := settleLedger(s, ctx, st, amount); serr != nil {
			s.Log.Error("x402: reconcile: still incomplete", "id", st.ID, "err", serr)
			continue
		}
		if merr := s.State.store.markSettled(ctx, st.ID, st.TxHash); merr != nil {
			s.Log.Error("x402: reconcile: settled but not marked", "id", st.ID, "err", merr)
			continue
		}
		completed++
		s.Log.Info("x402: reconcile: settlement completed", "id", st.ID, "payer", st.PayerOrg)
	}
	return completed, nil
}

// debitPayer moves the buyer's half. The metering spine when it is configured here
// — which is also how paid usage reaches billing/usage — else the ledger's own
// process over the plane, with the settlement id as the idempotency key on both.
func debitPayer(s *cloud.Service[state], ctx context.Context, st *Settlement, amount money.Amount) error {
	if m := s.State.meter; m != nil && m.Enabled() {
		_, err := m.Record(ctx, metering.Usage{
			User: st.PayerOrg, Org: st.PayerOrg, Amount: amount, Currency: "usd",
			Provider: providerLabel, Service: providerLabel, Model: st.Resource,
			// The SETTLEMENT is the act, and its id is the server's name for it — so a
			// reconcile that re-debits a settlement moves the money once.
			Ref: st.ID, Status: "success",
		})
		return err
	}
	return debitPeer(st, amount)
}

// creditPayee moves the seller's half, into the ledger of the org that published
// the listing — never one the buyer named.
func creditPayee(ctx context.Context, st *Settlement, amount money.Amount) error {
	if fin := finance.Current(); fin != nil {
		_, err := fin.Deposit(ctx, types.DepositInput{
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
// It is scoped to the caller's PAYER org — the ledger that was debited — so one
// tenant can never read another's settlement, and an id that exists but belongs
// to somebody else is a 404 exactly like one that does not exist. A caller with
// no billable identity is refused outright.
func (o ops) settlement(ctx context.Context, in *settlementRef) (*Receipt, error) {
	payer := payerOf(ctx)
	if payer == "" {
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

// payerOf resolves the org whose LEDGER this caller spends from — the same value
// the Enforce middleware debits and the same one every settlement row is keyed on.
//
// It reaches the request cloud.Bridge parked rather than asking
// principal.OrgFrom, because the payer is not the org: principal.Ledger folds in
// the SuperAdmin masquerade rule (X-User-IsAdmin + the X-User-Owner home claim),
// so a platform admin inspecting another org still reads their OWN settlements.
// OrgFrom carries only X-Org-Id and would silently widen that read to the
// inspected tenant's receipts.
//
// Fails closed off the HTTP path (the CLI's LocalInvoke, where there is no
// request): no request, no attested payer, no receipt.
func payerOf(ctx context.Context) string {
	c, ok := cloud.Request(ctx)
	if !ok {
		return ""
	}
	return principal.Ledger(c)
}

// ── helpers ───────────────────────────────────────────────────────────────────

// requirements builds the ONE way this rail accepts payment for a resource, from
// config + the resource's terms. The network is validated here rather than at the
// point of signing, so a misconfigured chain is a mount-time-shaped failure a log
// names once instead of a signature mismatch every client hits forever.
func requirements(cfg Config, terms Terms, payee string) (PaymentRequirements, error) {
	network := cmp.Or(strings.TrimSpace(terms.Network), cfg.Network, DefaultNetwork)
	if _, err := ChainID(network); err != nil {
		return PaymentRequirements{}, err
	}
	maxTimeout := cfg.MaxTimeout
	if maxTimeout <= 0 {
		maxTimeout = DefaultMaxTimeoutSeconds
	}
	return PaymentRequirements{
		Scheme:            SchemeExact,
		Network:           network,
		Amount:            atomicUnits(terms.Amount, cfg.assetDecimals()),
		Asset:             cmp.Or(strings.TrimSpace(terms.Asset), cfg.Asset),
		PayTo:             payee,
		MaxTimeoutSeconds: maxTimeout,
		Extra: &Extra{
			AssetTransferMethod: TransferEIP3009,
			Name:                cfg.assetName(),
			Version:             cfg.assetVersion(),
		},
	}, nil
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
// PayerOrg is part of the identity and not an afterthought. Without it a captured
// PAYMENT-SIGNATURE header is a bearer token across tenants: org B replays org A's
// payload,
// the id matches, every other field matches because they describe the same purchase,
// and B is served the priced tool having paid nothing while A's receipt is handed
// back. The payer is who the settlement was FOR, so a different payer is a different
// settlement — and it can only ever be a replay, because the signature commits to
// A's address and B cannot produce one of its own for A's nonce.
func sameTerms(a, b *Settlement) bool {
	return a.Resource == b.Resource && a.Amount == b.Amount && a.PayerOrg == b.PayerOrg &&
		addressEqual(a.Payee, b.Payee) && addressEqual(a.From, b.From)
}

func receiptOf(st *Settlement) *Receipt {
	amt, _ := money.ParseInt(st.Amount)
	return &Receipt{
		ID: st.ID, Resource: st.Resource, Payer: st.PayerOrg, From: st.From,
		Payee: st.Payee, PayeeOrg: st.PayeeOrg, Amount: amt.String(), Nonce: st.Nonce,
		Network: st.Network, SettledVia: st.SettledVia, TxHash: st.TxHash,
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
		Asset:         environ.Or("CLOUD_X402_ASSET", ""),
		AssetName:     environ.Or("CLOUD_X402_ASSET_NAME", ""),
		AssetVersion:  environ.Or("CLOUD_X402_ASSET_VERSION", ""),
		AssetDecimals: environ.Int("CLOUD_X402_ASSET_DECIMALS", DefaultAssetDecimals),
		Network:       environ.Or("CLOUD_X402_NETWORK", DefaultNetwork),
		MaxTimeout:    int64(environ.Int("CLOUD_X402_MAX_TIMEOUT_SECONDS", DefaultMaxTimeoutSeconds)),
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

// Shutdown closes the settlement store. Idempotent.
func Shutdown() error {
	if live == nil || live.State.store == nil {
		return nil
	}
	err := live.State.store.Close()
	live = nil
	return err
}
