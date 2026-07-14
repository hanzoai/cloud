package x402

// x402.go owns the subsystem: Mount + the settlement store, the marketplace
// Registry SEAM (Publish/Terms), the Enforce middleware that runs the
// challenge→verify→settle→serve flow, the settle-once settlement, and the receipt
// lookup surface (/v1/x402/settlements/:id).
//
// The middleware is a zip.Handler a priced route group applies (app.Use). It is a
// NO-OP passthrough until a Registry is Published, so x402 links into the binary
// harmlessly and the marketplace subsystem plugs its price/recipient table in
// later — the clean seam: x402 enforces payment; the marketplace declares what is
// priced and who is paid.

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/audit"
	"github.com/hanzoai/cloud/clients/commerce/metering"
	"github.com/hanzoai/cloud/clients/finance"
	"github.com/hanzoai/cloud/clients/money"
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/hanzoai/cloud/clients/wallets"
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
func Mount(app *zip.App, deps cloud.Deps) error {
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
	s.Log.Info("x402 mounted", "network", s.State.cfg.Network, "chainId", s.State.cfg.ChainID,
		"token", s.State.cfg.Token != "", "meter", s.State.meter != nil && s.State.meter.Enabled())
	return nil
}

func routes(app *zip.App, s *cloud.Service[state]) {
	app.Get("/v1/x402/settlements/:id", cloud.Handle(s, getSettlement))
}

// Enforce is the pay-per-use middleware a priced route group applies. It is a
// passthrough until Mount runs AND a Registry is Published — so linking x402 never
// gates traffic on its own.
func Enforce() zip.Handler {
	return func(c *zip.Ctx) error {
		s := mounted
		if s == nil {
			return c.Next()
		}
		return enforce(s, c)
	}
}

// enforce runs the x402 flow for one request: no registry / free resource →
// passthrough; unpaid → 402 + requirements; paid → verify, settle once, serve.
func enforce(s *cloud.Service[state], c *zip.Ctx) error {
	r := currentRegistry()
	if r == nil {
		return c.Next()
	}
	resource := c.Path()
	terms, priced, err := r.Price(c.Context(), resource)
	if err != nil {
		return denyUnavailable(c) // registry blip → fail closed, don't serve a priced resource free
	}
	if !priced || terms.Amount.IsZero() || terms.Amount.IsNeg() {
		return c.Next() // free
	}

	// Ledger settlement debits an ORG ledger, so a validated payer is required.
	payer := principal.Payer(c)
	if payer == "" {
		return zip.ErrForbidden("sign in")
	}

	// Resolve the recipient wallet the marketplace named (org-scoped in wallets).
	target, ok := wallets.ResolvePaymentTarget(c.Context(), terms.RecipientOrg, terms.RecipientWalletID)
	if !ok {
		s.Log.Error("x402: recipient wallet unresolved",
			"resource", resource, "recipientOrg", terms.RecipientOrg, "wallet", terms.RecipientWalletID)
		return c.JSON(http.StatusServiceUnavailable, payErr("payee_unavailable", "resource recipient wallet is not resolvable"))
	}

	req := requirements(s.State.cfg, resource, terms, target.Address)

	// CHALLENGE: no proof yet → 402 with the payment requirements.
	proofHdr := strings.TrimSpace(c.Header(HeaderProof))
	if proofHdr == "" {
		c.SetHeader(HeaderRequirements, marshalHeader(req))
		return c.JSON(http.StatusPaymentRequired, map[string]any{
			"error":   payErrBody("payment_required", "payment required for "+resource),
			"accepts": req,
		})
	}

	// VERIFY the submitted proof against the requirements.
	proof, err := ParseProof(proofHdr)
	if err != nil {
		return c.JSON(http.StatusPaymentRequired, payErr("invalid_payment", err.Error()))
	}
	if err := Verify(req, *proof, s.State.cfg.tokenName(), s.State.cfg.tokenVersion(), nowUnix()); err != nil {
		return c.JSON(http.StatusPaymentRequired, payErr("payment_invalid", err.Error()))
	}

	// SETTLE once (replay-safe), then SERVE.
	receipt, replay, err := settle(s, c.Context(), req, *proof, terms, target, payer)
	if err != nil {
		s.Log.Error("x402: settlement failed", "resource", resource, "payer", payer, "err", err)
		return denyUnavailable(c) // settlement backend down → 503, never serve unpaid
	}
	if replay {
		return c.JSON(http.StatusPaymentRequired, payErr("nonce_replayed", "payment nonce already used"))
	}
	emitAudit(s, c.Context(), receipt)
	c.SetHeader(HeaderReceipt, marshalHeader(receipt))
	return c.Next()
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
		Amount: terms.Amount.IntString(), SettledVia: "ledger", CreatedAt: nowUnix(),
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
func settleLedger(s *cloud.Service[state], ctx context.Context, st *Settlement, payeeSubject string, amount money.Amount) error {
	if m := s.State.meter; m != nil && m.Enabled() {
		if _, err := m.Record(ctx, metering.Usage{
			User: st.PayerOrg, Org: st.PayerOrg, Amount: amount, Currency: "usd",
			Provider: providerLabel, Service: providerLabel, Model: st.Resource,
			RequestID: st.ID, Status: "success",
		}); err != nil {
			return err
		}
	}
	if fin := finance.Current(); fin != nil && st.PayeeOrg != "" {
		if _, err := fin.Deposit(ctx, types.DepositInput{
			Org: st.PayeeOrg, Subject: payeeSubject, Amount: amount, Currency: "usd",
			Ref: st.ID, Notes: "x402:" + st.Resource, Tags: "x402",
		}); err != nil {
			return err
		}
	}
	return nil
}

// getSettlement is the receipt lookup: GET /v1/x402/settlements/:id, scoped to the
// caller's payer org so one tenant can never read another's settlement.
func getSettlement(s *cloud.Service[state], c *zip.Ctx) error {
	payer := principal.Payer(c)
	if payer == "" {
		return zip.ErrForbidden("sign in")
	}
	st, found, err := s.State.store.getScoped(c.Context(), payer, strings.TrimSpace(c.Param("id")))
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "get settlement: %v", err)
	}
	if !found {
		return zip.ErrNotFound("settlement not found")
	}
	return c.JSON(http.StatusOK, receiptOf(st))
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
	i := amount.Int() // 18-dp magnitude
	if decimals >= money.Decimals {
		return i.String()
	}
	div := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(money.Decimals-decimals)), nil)
	return new(big.Int).Quo(i, div).String()
}

func sameTerms(a, b *Settlement) bool {
	return a.Resource == b.Resource && a.Amount == b.Amount &&
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

func payErr(code, msg string) map[string]any     { return map[string]any{"error": payErrBody(code, msg)} }
func payErrBody(code, msg string) map[string]any { return map[string]any{"code": code, "message": msg} }
func denyUnavailable(c *zip.Ctx) error {
	return c.JSON(http.StatusServiceUnavailable, payErr("x402_unavailable", "payment settlement temporarily unavailable"))
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
