package x402

// x402.go owns the subsystem: Mount + the settlement store, the marketplace
// Registry SEAM (Publish/Terms), the challenge→verify→settle→serve flow, the
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
// seam: x402 enforces payment; the marketplace declares what is priced and who is
// paid.

//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/finance"
	"github.com/hanzoai/cloud/apps/metering"
	"github.com/hanzoai/cloud/apps/money"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/apps/wallets"
	"github.com/hanzoai/cloud/audit"
	"github.com/hanzoai/cloud/types"
	"github.com/zap-proto/zip"
)

const (
	// DefaultNetwork / DefaultChainID pin the challenge's settlement network when
	// neither the resource's Terms nor the operator config names one. Default to
	// the Hanzo L1 (its EVM chainID == its network id), matching wallets.
	DefaultNetwork = "hanzo"
	DefaultChainID = 36963

	// DefaultTokenDecimals is USDC's precision (the smallest-unit scale the client
	// signs over). Operators override per token via CLOUD_X402_TOKEN_DECIMALS.
	DefaultTokenDecimals = 6

	// providerLabel is the commerce "provider"/"service" label x402 spend records
	// under, so pay-per-use appears in billing/usage attributable to this surface.
	providerLabel = "x402"
)

// Config is the operator-pinned x402 settlement config. Token + its EIP-712 domain
// (name/version/decimals) MUST match the token the client signs against, or every
// signature fails to recover. Values are read from env in Mount; tests inject
// Config directly.
type Config struct {
	Token         string // ERC-3009 token contract (EIP-712 verifyingContract)
	TokenName     string // EIP-712 domain name (default "USD Coin")
	TokenVersion  string // EIP-712 domain version (default "2")
	TokenDecimals int    // token smallest-unit scale (default 6)
	Network       string // settlement network label (default "hanzo")
	ChainID       int64  // settlement chain id (default 36963)
	ValidFor      int64  // authorization validity window, seconds (default 300)
}

func (c Config) tokenName() string {
	if c.TokenName != "" {
		return c.TokenName
	}
	return DefaultTokenName
}
func (c Config) tokenVersion() string {
	if c.TokenVersion != "" {
		return c.TokenVersion
	}
	return DefaultTokenVersion
}
func (c Config) tokenDecimals() int {
	if c.TokenDecimals > 0 {
		return c.TokenDecimals
	}
	return DefaultTokenDecimals
}

// Terms are a priced resource's payment terms: the price and the wallet that
// receives it. The marketplace registry OWNS the resource→Terms mapping; x402
// resolves the recipient wallet (via wallets) and enforces payment against these.
type Terms struct {
	Amount            money.Amount // exact 18-dp USD price
	RecipientOrg      string       // the recipient wallet's org
	RecipientWalletID string       // the wallet that receives payment
	Token             string       // optional per-resource token override
	Network           string       // optional per-resource network override
	ChainID           int64        // optional per-resource chain override
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

// mounted is the process singleton Enforce + Publish resolve. nil ⇒ not linked ⇒
// Enforce is a passthrough.
var mounted *cloud.Service[state]

// Mount wires /v1/x402 and the settlement store. Direct construction (not
// cloud.Mount) because it holds the package singleton the middleware reaches.
func Mount(app cloud.Router, deps cloud.Deps) error {
	if app == nil || deps.Logger == nil {
		return errMount("nil app or logger")
	}
	if deps.DataDir == "" {
		return errMount("empty DataDir")
	}
	if err := os.MkdirAll(deps.DataDir, 0o755); err != nil {
		return errMount("data dir: " + err.Error())
	}
	st, err := openStore(filepath.Join(deps.DataDir, "x402.db"))
	if err != nil {
		return errMount("open store: " + err.Error())
	}
	s := &cloud.Service[state]{Base: cloud.NewBase(deps, providerLabel), State: state{
		store: st,
		meter: deps.Metering,
		cfg:   configFromEnv(),
		audit: deps.Audit,
	}}
	mounted = s
	routes(app, s)
	exposeSettle(s)
	s.Log.Info("x402 mounted", "network", s.State.cfg.Network, "chainId", s.State.cfg.ChainID,
		"token", s.State.cfg.Token != "", "meter", s.State.meter != nil && s.State.meter.Enabled())
	return nil
}

// ops binds the subsystem to its typed handler: a TypedHandler has no parameter
// for the service, so it arrives as a RECEIVER and the op is a method value —
// the only bound form cmd/zipdoc can lift prose from.
type ops struct{ s *cloud.Service[state] }

// routes registers the receipt lookup.
//
// Bridge FIRST, on the group and BEFORE the leaf: fiber runs middleware in
// registration order, so one installed after its route never runs. It parks the
// request a typed op's signature drops, which is how the op below resolves the
// PAYER (see settlement). Serve installs one app-wide too; nesting is harmless,
// and this package's own tests mount on a bare app with no Serve, so this install
// is what makes them work.
func routes(app cloud.Router, s *cloud.Service[state]) {
	g := app.Group("/v1/x402", cloud.Bridge())
	o := ops{s: s}
	zip.Get(g, "/settlements/:id", o.settlement)
}

// Sentinel outcomes of Settle — the typed half of the same flow Enforce renders as
// an HTTP response. BOTH are fail-closed: a caller that gets either must refuse the
// call, never serve it. A price that cannot be enforced is not a free price.
var (
	// ErrPaymentRequired — the caller has not paid: no proof, an authorization that
	// does not verify, or a replayed nonce. The CHALLENGE is on the response's
	// X-Payment-Required header, so a client can pay and retry.
	ErrPaymentRequired = errors.New("x402: payment required")
	// ErrUnavailable — payment could not be enforced at all: x402 is not mounted in
	// this process, the price table or the recipient wallet did not resolve, the
	// caller has no billable identity, or settlement failed.
	ErrUnavailable = errors.New("x402: payment enforcement unavailable")
)

// Enforce is the pay-per-use middleware a priced route group applies, keyed on the
// request PATH.
//
// Applying it is a DECLARATION that the group is for sale, so anything that leaves
// x402 unable to answer what the group costs is a refusal, never a passthrough. It
// refuses when x402 is not mounted in this process and when no price table has been
// published — the two ways "I cannot enforce payment here" arises — because the
// alternative renders both as "free", permanently and silently.
//
// That is not hypothetical. The published Registry is a process-global installed by
// marketplace.Mount, and the shipped topology is one binary per app (manifest/apps.go;
// Dockerfile builds a plugin per row; cmd/cloud loads each as a child process), so a
// process that mounts x402 does NOT have marketplace in it and the table is nil. The
// fleet has been bitten by exactly this once already — see resource_billing_peer.go:
// "Splitting apps into their own binaries turned every priced create free without
// changing a line of billing code."
//
// The TOOL path answers the opposite way on purpose (see Settle): it offers EVERY
// dispatch to the seam, so an absent table there means "nothing is priced" and free
// tools keep working. A middleware is applied only to what is FOR SALE; the tool seam
// is applied to everything. Different questions, so the safe answers differ, and both
// are stated rather than inherited from one shared default.
func Enforce() zip.Handler {
	return func(c *zip.Ctx) error {
		s := mounted
		if s == nil {
			return refuse(c, unenforceable("x402 is not mounted in this process"))
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
		g := run(s, c.Context(), principal.Ledger(c), c.Header(HeaderProof), c.Path(), terms)
		if g.receipt != nil {
			served(s, c.Context(), c, g.receipt)
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
// Unpaid → the challenge is written to the response's X-Payment-Required header and
// ErrPaymentRequired is returned, so the caller refuses 402 and the client can pay
// and retry. Paid → settled exactly once, the receipt is on X-Payment-Receipt, nil.
func Settle(ctx context.Context, resource string) error {
	// FREE FIRST, before anything is required of the world. A caller that offers
	// every call to this seam — which is the only way a gate and a settlement cannot
	// disagree — must not have its free calls broken by a payment rail that is
	// merely absent. Nothing unpriced needs x402 mounted, a request, or a payer.
	terms, priced, err := priceOf(ctx, resource)
	if err != nil {
		return fmt.Errorf("%w: price lookup failed: %v", ErrUnavailable, err)
	}
	if !priced {
		return nil
	}
	s := mounted
	if s == nil {
		return fmt.Errorf("%w: x402 is not mounted in this process", ErrUnavailable)
	}
	c, _ := cloud.Request(ctx) // nil off the HTTP path; run refuses a PRICED resource there
	var payer, proof string
	if c != nil {
		payer, proof = principal.Ledger(c), c.Header(HeaderProof)
	}
	g := run(s, ctx, payer, proof, resource, terms)
	if g.receipt != nil {
		served(s, ctx, c, g.receipt)
		return nil
	}
	if c != nil && g.req != nil {
		c.SetHeader(HeaderRequirements, marshalHeader(*g.req))
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
// free must be answerable with nothing mounted and no request in hand, or a caller
// that offers EVERY call to the payment seam cannot exist.
//
// TWO TRANSPORTS, ONE TABLE. The published Registry is a process-global installed
// by marketplace.Mount, and the fleet runs one process per app — so in the x402
// binary it is nil, and reading that as "nothing is priced" was the fail-OPEN half
// of this defect. Nil now means "the table is not HERE", and the process that owns
// it is asked. Only ErrNoPeer — a fleet with no marketplace in it at all — restores
// the old answer, because then there really is no table to disagree with.
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
type gate struct {
	receipt *Receipt
	req     *PaymentRequirements
	status  int
	code    string
	msg     string
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
func run(s *cloud.Service[state], ctx context.Context, payer, proofHdr, resource string, terms Terms) gate {
	// Ledger settlement debits an ORG ledger, so a validated payer is required.
	if payer == "" {
		return gate{status: http.StatusForbidden, code: "unbillable", msg: "sign in"}
	}

	// Resolve the recipient wallet the marketplace named (org-scoped in wallets, so
	// a listing can only ever be paid into a wallet of its OWN publisher org).
	target, ok := payeeOf(ctx, terms.RecipientOrg, terms.RecipientWalletID)
	if !ok {
		s.Log.Error("x402: recipient wallet unresolved",
			"resource", resource, "recipientOrg", terms.RecipientOrg, "wallet", terms.RecipientWalletID)
		return gate{status: http.StatusServiceUnavailable, code: "payee_unavailable",
			msg: "resource recipient wallet is not resolvable"}
	}

	req := requirements(s.State.cfg, resource, terms, target.Address)

	// CHALLENGE: no proof yet → 402 with the payment requirements.
	proofHdr = strings.TrimSpace(proofHdr)
	if proofHdr == "" {
		return challenge(req, "payment_required", "payment required for "+resource)
	}

	// VERIFY the submitted proof against the requirements.
	proof, err := ParseProof(proofHdr)
	if err != nil {
		return challenge(req, "invalid_payment", err.Error())
	}
	if err := Verify(req, *proof, s.State.cfg.tokenName(), s.State.cfg.tokenVersion(), nowUnix()); err != nil {
		return challenge(req, "payment_invalid", err.Error())
	}

	// SETTLE once (replay-safe).
	receipt, replay, err := settle(s, ctx, req, *proof, terms, target, payer)
	if err != nil {
		s.Log.Error("x402: settlement failed", "resource", resource, "payer", payer, "err", err)
		return unavailable() // settlement backend down → never serve unpaid
	}
	if replay {
		return challenge(req, "nonce_replayed", "payment nonce already used")
	}
	return gate{receipt: receipt}
}

// challenge is a 402 that tells the client exactly what to pay. Every 402 carries
// the requirements, not just the first: a client whose authorization expired or
// replayed can re-sign from the refusal alone.
func challenge(req PaymentRequirements, code, msg string) gate {
	return gate{req: &req, status: http.StatusPaymentRequired, code: code, msg: msg}
}

func unavailable() gate {
	return gate{status: http.StatusServiceUnavailable, code: "x402_unavailable",
		msg: "payment settlement temporarily unavailable"}
}

// unenforceable is the refusal for a priced route x402 cannot price at all — a
// missing dependency in THIS process, not a transient fault. It carries no
// challenge, because there are no terms to offer: a client cannot pay its way past
// a payment rail that is not here. The reason is named so the failure reads as a
// deployment fact in the log rather than a mystery 503.
func unenforceable(why string) gate {
	return gate{status: http.StatusServiceUnavailable, code: "x402_unenforceable",
		msg: "payment cannot be enforced for this resource: " + why}
}

// served records a settled payment: the audit row and the receipt header the
// response carries back. One place, so Enforce and Settle cannot drift on it.
func served(s *cloud.Service[state], ctx context.Context, c *zip.Ctx, r *Receipt) {
	emitAudit(s, ctx, r)
	if c != nil {
		c.SetHeader(HeaderReceipt, marshalHeader(r))
	}
}

// refuse writes the x402 refusal: the challenge on the X-Payment-Required header AND
// in the body's `accepts`, so a client reading either can pay and retry.
func refuse(c *zip.Ctx, g gate) error {
	body := payErr(g.code, g.msg)
	if g.req != nil {
		c.SetHeader(HeaderRequirements, marshalHeader(*g.req))
		body["accepts"] = *g.req
	}
	return c.JSON(g.status, body)
}

// settle enforces settle-once and performs the ledger settlement.
//
// Dedup + settle-once: the settlement id is DETERMINISTIC in (From, Nonce), so a
// re-submitted authorization maps to the same row. A found row with MATCHING terms
// is an idempotent retry (return the receipt, do NOT re-charge); a found row with
// DIFFERENT terms is nonce reuse across resources → replay. When absent, settle
// FIRST (idempotent on the id at the ledger) then record — so a settle failure
// never leaves a spent-but-unpaid nonce that would serve free on retry.
func settle(s *cloud.Service[state], ctx context.Context, req PaymentRequirements, proof Proof,
	terms Terms, target wallets.PaymentTarget, payerOrg string) (*Receipt, bool, error) {

	id := settlementID(proof.From, proof.Nonce)
	st := &Settlement{
		ID: id, PayerOrg: payerOrg, From: proof.From, Nonce: proof.Nonce,
		Resource: req.Resource, Payee: req.Payee, PayeeOrg: target.Org,
		Amount: terms.Amount.AttoString(), SettledVia: "ledger", CreatedAt: nowUnix(),
	}

	if ex, found, err := s.State.store.get(ctx, id); err != nil {
		return nil, false, err
	} else if found {
		if sameTerms(ex, st) {
			return receiptOf(ex), false, nil // idempotent retry
		}
		return nil, true, nil // nonce reused for different terms → replay
	}

	if err := settleLedger(s, ctx, st, target.Subject, terms.Amount); err != nil {
		return nil, false, err
	}
	recorded, existing, err := s.State.store.record(ctx, st)
	if err != nil {
		return nil, false, err
	}
	if !recorded { // lost a concurrent race; settlement was idempotent
		if existing != nil && sameTerms(existing, st) {
			return receiptOf(existing), false, nil
		}
		return nil, true, nil
	}
	return receiptOf(st), false, nil
}

// settleLedger is the LIVE settlement backend: it debits the payer's org through
// the metering spine (so paid usage appears in billing/usage like any metered
// spend) and credits the recipient wallet's ledger. Both writes are idempotent on
// the settlement id (metering RequestID / finance Ref), so a retried settle never
// double-moves money. On-chain broadcast of the authorization is a SEPARATE seam
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
// A credit that fails ANYWAY is not compensated, and that is the deliberate answer.
// Both writes are idempotent on the settlement id, and no row is recorded and no
// receipt issued unless both landed — so the resource is refused and the client's
// retry, carrying the same authorization, re-runs both and completes the settlement
// exactly once. A reversing entry would look safer and be worse: the failure that
// makes it necessary is usually a TIMEOUT, and a timeout does not mean the credit
// did not commit — so the compensation would refund a payer who was correctly
// charged while the seller kept the money, minting the difference out of a slow
// socket. An unretried debit is a number the log names with the id both sides are
// keyed on. Minted money is a number nobody can find.
func settleLedger(s *cloud.Service[state], ctx context.Context, st *Settlement, payeeSubject string, amount money.Amount) error {
	if st.PayeeOrg == "" {
		return errors.New("x402: no payee org to credit")
	}
	if err := debitPayer(s, ctx, st, amount); err != nil {
		return err
	}
	if err := creditPayee(ctx, st, payeeSubject, amount); err != nil {
		// The payer's side landed and the payee's did not. Nothing is served and no
		// receipt is issued, so the settlement has not happened — but the debit has,
		// and it stays until the same authorization is retried. Say so, with the id
		// both sides are keyed on, so it is reconcilable rather than merely lost.
		s.Log.Error("x402: payer debited, payee credit failed — settlement incomplete until retried",
			"id", st.ID, "payer", st.PayerOrg, "payeeOrg", st.PayeeOrg,
			"amount", amount.String(), "err", err)
		return err
	}
	return nil
}

// debitPayer moves the buyer's half. The metering spine when it is configured here
// — which is also how paid usage reaches billing/usage — else the ledger's own
// process over the plane, with the settlement id as the idempotency key on both.
func debitPayer(s *cloud.Service[state], ctx context.Context, st *Settlement, amount money.Amount) error {
	if m := s.State.meter; m != nil && m.Enabled() {
		_, err := m.Record(ctx, metering.Usage{
			User: st.PayerOrg, Org: st.PayerOrg, Amount: amount, Currency: "usd",
			Provider: providerLabel, Service: providerLabel, Model: st.Resource,
			RequestID: st.ID, Status: "success",
		})
		return err
	}
	return debitPeer(st, amount)
}

// creditPayee moves the seller's half, into the ledger of the org that published
// the listing — never one the buyer named.
func creditPayee(ctx context.Context, st *Settlement, payeeSubject string, amount money.Amount) error {
	if fin := finance.Current(); fin != nil {
		_, err := fin.Deposit(ctx, types.DepositInput{
			Org: st.PayeeOrg, Subject: payeeSubject, Amount: amount, Currency: "usd",
			Ref: st.ID, Notes: "x402:" + st.Resource, Tags: "x402",
		})
		return err
	}
	return creditPeer(st.PayeeOrg, payeeSubject, amount, st.ID, "x402:"+st.Resource)
}

// settlementRef addresses one settlement by the id its receipt carries.
type settlementRef struct {
	// ID is the settlement id from the URL — the deterministic keccak(from|nonce)
	// key an x402 receipt is issued under (the `id` field of a Receipt, and the
	// value of the X-Payment-Response header a paid request answers with).
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

// requirements builds the 402 challenge from config + the resource's terms.
func requirements(cfg Config, resource string, terms Terms, payee string) PaymentRequirements {
	network := firstNonEmpty(terms.Network, cfg.Network, DefaultNetwork)
	token := firstNonEmpty(terms.Token, cfg.Token)
	chainID := terms.ChainID
	if chainID == 0 {
		chainID = cfg.ChainID
	}
	if chainID == 0 {
		chainID = DefaultChainID
	}
	validFor := cfg.ValidFor
	if validFor <= 0 {
		validFor = DefaultValidFor
	}
	return PaymentRequirements{
		Version: Version, Scheme: Scheme, Resource: resource, Network: network,
		ChainID: chainID, Token: token, Payee: payee,
		Amount: tokenUnits(terms.Amount, cfg.tokenDecimals()), ValidFor: validFor,
	}
}

// settlementID is the deterministic settle-once key: keccak(from|nonce). Stable
// across retries (so metering + finance idempotency align) and unique per
// authorization.
func settlementID(from, nonce string) string {
	h := keccak([]byte(strings.ToLower(trim0x(from))), []byte("|"), []byte(strings.ToLower(trim0x(nonce))))
	return "x402_" + hex.EncodeToString(h[:])
}

// tokenUnits converts an exact 18-dp USD amount to the token's smallest unit
// (e.g. USDC 6-dp) for the challenge the client signs. The LEDGER settlement uses
// the exact money.Amount, so no precision is lost where money actually moves.
func tokenUnits(amount money.Amount, decimals int) string {
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
// X-Payment header is a bearer token across tenants: org B replays org A's proof,
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
		SettledVia: st.SettledVia, TxHash: st.TxHash, SettledAt: st.CreatedAt,
	}
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

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func configFromEnv() Config {
	return Config{
		Token:         strings.TrimSpace(os.Getenv("CLOUD_X402_TOKEN")),
		TokenName:     strings.TrimSpace(os.Getenv("CLOUD_X402_TOKEN_NAME")),
		TokenVersion:  strings.TrimSpace(os.Getenv("CLOUD_X402_TOKEN_VERSION")),
		TokenDecimals: envInt("CLOUD_X402_TOKEN_DECIMALS", DefaultTokenDecimals),
		Network:       firstNonEmpty(os.Getenv("CLOUD_X402_NETWORK"), DefaultNetwork),
		ChainID:       int64(envInt("CLOUD_X402_CHAIN_ID", DefaultChainID)),
		ValidFor:      int64(envInt("CLOUD_X402_VALID_FOR", DefaultValidFor)),
	}
}

func envInt(key string, def int) int {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}

func errMount(msg string) error { return &mountErr{msg} }

type mountErr struct{ s string }

func (e *mountErr) Error() string { return "x402.Mount: " + e.s }

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return b
}

// Shutdown closes the settlement store. Idempotent.
func Shutdown() error {
	if mounted == nil || mounted.State.store == nil {
		return nil
	}
	err := mounted.State.store.Close()
	mounted = nil
	return err
}
