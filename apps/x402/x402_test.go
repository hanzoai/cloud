package x402

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/finance"
	"github.com/hanzoai/cloud/apps/kms"
	"github.com/hanzoai/cloud/apps/metering"
	"github.com/hanzoai/cloud/apps/wallets"
	"github.com/hanzoai/cloud/money"
	"github.com/hanzoai/cloud/types"
	"github.com/luxfi/crypto"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"

	// devmaster keys this test binary: cek opens nothing without a master and a
	// test process has no KMS.
	_ "github.com/hanzoai/cloud/internal/devmaster"
)

// ── harness ───────────────────────────────────────────────────────────────────

// commerceDoer answers every commerce call 200. It exists only so metering.New has
// a transport and reports Enabled; with a co-resident finance ledger published, the
// metering spine posts the payer debit NATIVELY and this transport is never used —
// which is itself worth asserting (calls() stays 0).
type commerceDoer struct {
	mu sync.Mutex
	n  int
}

func (d *commerceDoer) calls() int { d.mu.Lock(); defer d.mu.Unlock(); return d.n }

func (d *commerceDoer) Do(*http.Request) (*http.Response, error) {
	d.mu.Lock()
	d.n++
	d.mu.Unlock()
	body := `{"transactionId":"t_1","user":"payer","amount":0,"currency":"usd","type":"withdraw"}`
	return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader([]byte(body))), Header: make(http.Header)}, nil
}

// halfDown is a ledger whose CREDIT side is broken and whose DEBIT side works —
// the exact partial failure a settlement has to survive, and the one no fake that
// simply removes the ledger can produce. The payer's half goes through the metering
// spine (RecordUsage) and the payee's is a Deposit, so failing only Deposit lands
// the debit and drops the credit.
type halfDown struct {
	finance.Client
	mu       sync.Mutex
	broken   bool
	deposits []types.DepositInput
}

func (h *halfDown) breaks(v bool) { h.mu.Lock(); h.broken = v; h.mu.Unlock() }

// credits returns the deposits tagged x402 — the settlement's payee side, captured
// at the ledger. It is how a test asserts WHERE money landed: the ledger's Balance
// aggregates at the org, so reading a balance cannot tell a credit to the payout
// wallet's subject from one to the org itself, and a settlement that paid the wrong
// subject would read as correct.
func (h *halfDown) credits() []types.DepositInput {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []types.DepositInput
	for _, d := range h.deposits {
		if d.Tags == "x402" {
			out = append(out, d)
		}
	}
	return out
}

func (h *halfDown) Deposit(ctx context.Context, in types.DepositInput) (string, error) {
	h.mu.Lock()
	broken := h.broken
	h.mu.Unlock()
	if broken {
		return "", errors.New("ledger: credit side is down")
	}
	id, err := h.Client.Deposit(ctx, in)
	if err == nil {
		h.mu.Lock()
		h.deposits = append(h.deposits, in)
		h.mu.Unlock()
	}
	return id, err
}

// funded is what the payer org starts with, so every debit has balance to draw down.
var funded = money.FromCents(1000)

type harness struct {
	t         *testing.T
	app       *zip.App
	doer      *commerceDoer
	fin       types.FinanceClient
	ledger    *halfDown
	payerKey  *ecdsa.PrivateKey
	payerOrg  string
	payeeOrg  string
	recipient string // recipient wallet id
}

// debited is what has left the payer's ledger — the only question that matters
// about a payment, asked of the ledger rather than of HTTP traffic.
func (h *harness) debited() money.Amount { return funded.Sub(h.balance(h.payerOrg, h.payerOrg)) }

// credited is what has reached the recipient wallet's ledger.
func (h *harness) credited() money.Amount { return h.balance(h.payeeOrg, h.recipient) }

func (h *harness) balance(org, subject string) money.Amount {
	h.t.Helper()
	amt, err := h.fin.Balance(context.Background(), org, subject, "usd", false)
	if err != nil {
		h.t.Fatalf("balance %s/%s: %v", org, subject, err)
	}
	return amt
}

// tie asserts both sides of the settlement moved by exactly want — a payment is two
// entries that agree, never one.
func (h *harness) tie(want money.Amount, when string) {
	h.t.Helper()
	if got := h.debited(); got.Cmp(want) != 0 {
		h.t.Fatalf("%s: payer debited %s, want exactly %s", when, got.String(), want.String())
	}
	if got := h.credited(); got.Cmp(want) != 0 {
		h.t.Fatalf("%s: payee credited %s, want exactly %s", when, got.String(), want.String())
	}
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	// A settlement is a ledger entry on BOTH sides, so every test gets a real
	// co-resident ledger — there is no "settled" to assert without one.
	ledger := &halfDown{Client: finance.New(t.TempDir())}
	finance.Publish(ledger)
	t.Cleanup(func() { finance.Publish(nil); _ = wallets.Shutdown(); _ = Shutdown() })

	log := luxlog.New("test")
	dir := t.TempDir()

	kraw := make([]byte, 32)
	_, _ = rand.Read(kraw)
	kmsClient, err := kms.New(kms.Config{DataDir: dir, MasterKeyB64: base64.StdEncoding.EncodeToString(kraw)}, log)
	if err != nil {
		t.Fatalf("kms.New: %v", err)
	}
	t.Cleanup(func() { _ = kmsClient.Close() })

	app := zip.New(zip.Config{Logger: log})
	deps := cloud.Deps{Logger: log, KMS: kmsClient, DataDir: dir}

	if err := wallets.Mount(app, deps); err != nil {
		t.Fatalf("wallets.Mount: %v", err)
	}

	doer := &commerceDoer{}
	meter, err := metering.New(metering.Config{BaseURL: "http://commerce.test", HTTPClient: doer})
	if err != nil {
		t.Fatalf("metering.New: %v", err)
	}
	depsX := deps
	depsX.Metering = meter
	if err := Mount(app, depsX); err != nil {
		t.Fatalf("x402.Mount: %v", err)
	}

	// Priced routes behind the Enforce middleware. /paid/free stays unpriced.
	paid := app.Group("/paid", Enforce())
	served := func(c *zip.Ctx) error { return c.JSON(200, map[string]any{"served": true}) }
	paid.Get("/tool", served)
	paid.Get("/other", served)
	paid.Get("/free", served)

	h := &harness{t: t, app: app, doer: doer, fin: ledger, ledger: ledger,
		payerOrg: "payerorg", payeeOrg: "payeeorg"}
	h.payerKey, _ = crypto.GenerateKey()
	h.recipient = h.createRecipient(h.payeeOrg)
	if _, err := ledger.Client.Deposit(context.Background(), depositUSD(h.payerOrg, h.payerOrg, funded, "fund")); err != nil {
		t.Fatalf("fund payer: %v", err)
	}
	return h
}

// createRecipient provisions a KMS wallet in org and returns its id — the payout
// wallet a marketplace listing would name.
func (h *harness) createRecipient(org string) string {
	h.t.Helper()
	_, b, _ := h.req(http.MethodPost, "/v1/wallets/accounts", org, "", `{"name":"payee"}`)
	var acct struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(b, &acct); err != nil || acct.ID == "" {
		h.t.Fatalf("create account: %v (%s)", err, b)
	}
	body := `{"accountId":"` + acct.ID + `","name":"earnings","custody":"kms","tier":"hot","chain":"eip155:36963"}`
	code, b, _ := h.req(http.MethodPost, "/v1/wallets", org, "", body)
	if code != 200 {
		h.t.Fatalf("create wallet = %d (%s)", code, b)
	}
	var w struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(b, &w); err != nil || w.ID == "" {
		h.t.Fatalf("decode wallet: %v (%s)", err, b)
	}
	return w.ID
}

// req drives one request. org (non-empty) sets a validated principal; payment
// (non-empty) sets the PAYMENT-SIGNATURE header; jsonBody (non-empty) is a JSON
// POST body.
func (h *harness) req(method, path, org, payment, jsonBody string) (int, []byte, http.Header) {
	h.t.Helper()
	var body io.Reader
	if jsonBody != "" {
		body = bytes.NewReader([]byte(jsonBody))
	}
	hr := httptest.NewRequest(method, path, body)
	if jsonBody != "" {
		hr.Header.Set("Content-Type", "application/json")
	}
	if org != "" {
		hr.Header.Set("X-Org-Id", org)
		hr.Header.Set("X-User-Id", "u_"+org)
	}
	if payment != "" {
		hr.Header.Set(HeaderPaymentSignature, payment)
	}
	resp, err := h.app.Test(hr)
	if err != nil {
		h.t.Fatalf("Test %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b, resp.Header
}

// challenge does the unpaid GET and returns the PaymentRequirements the 402
// advertised — read off the PAYMENT-REQUIRED header, which is the canonical
// location the HTTP transport names, exactly as a compliant client reads it.
func (h *harness) challenge(path, org string) PaymentRequirements {
	h.t.Helper()
	code, b, hdr := h.req(http.MethodGet, path, org, "", "")
	if code != http.StatusPaymentRequired {
		h.t.Fatalf("challenge %s = %d (%s), want 402", path, code, b)
	}
	raw := hdr.Get(HeaderPaymentRequired)
	if raw == "" {
		h.t.Fatalf("402 carried no %s header — a challenge a client cannot read is not a challenge",
			HeaderPaymentRequired)
	}
	var req PaymentRequired
	if err := DecodeHeader(raw, &req); err != nil {
		h.t.Fatalf("decode %s: %v", HeaderPaymentRequired, err)
	}
	if req.X402Version != Version {
		h.t.Fatalf("challenge x402Version = %d, want %d", req.X402Version, Version)
	}
	if len(req.Accepts) != 1 {
		h.t.Fatalf("challenge offered %d ways to pay, want 1", len(req.Accepts))
	}
	if req.Resource.URL != path {
		h.t.Fatalf("challenge resource.url = %q, want %q", req.Resource.URL, path)
	}
	return req.Accepts[0]
}

// pay signs req with the given nonce and window and returns the PAYMENT-SIGNATURE
// header value — exactly what a compliant client sends.
func (h *harness) pay(req PaymentRequirements, nonce string, validAfter, validBefore int64) string {
	h.t.Helper()
	return EncodeHeader(signPayment(h.t, h.payerKey, req, nonce, validAfter, validBefore))
}

// payFor signs for req with the given nonce over a live window and retries.
func (h *harness) payFor(path, org string, req PaymentRequirements, nonce string) (int, []byte, http.Header) {
	h.t.Helper()
	now := nowUnix()
	return h.req(http.MethodGet, path, org, h.pay(req, nonce, now-60, now+300), "")
}

// settlement decodes the PAYMENT-RESPONSE header an answered request carries.
func (h *harness) settlement(hdr http.Header) SettlementResponse {
	h.t.Helper()
	raw := hdr.Get(HeaderPaymentResponse)
	if raw == "" {
		h.t.Fatalf("no %s header on the response", HeaderPaymentResponse)
	}
	var s SettlementResponse
	if err := DecodeHeader(raw, &s); err != nil {
		h.t.Fatalf("decode %s: %v", HeaderPaymentResponse, err)
	}
	return s
}

func randNonce() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return "0x" + hex.EncodeToString(b)
}

func pubRegistry(m map[string]Terms) { Publish(fakeReg{m: m}) }

func depositUSD(org, subject string, amount money.Amount, ref string) types.DepositInput {
	return types.DepositInput{Org: org, Subject: subject, Amount: amount, Currency: "usd", Ref: ref}
}

type fakeReg struct {
	m   map[string]Terms
	err error
}

func (r fakeReg) Price(_ context.Context, resource string) (Terms, bool, error) {
	if r.err != nil {
		return Terms{}, false, r.err
	}
	t, ok := r.m[resource]
	return t, ok, nil
}

// ── tests ─────────────────────────────────────────────────────────────────────

// The full exact-scheme flow: unpaid → 402 + requirements on PAYMENT-REQUIRED;
// signed payload on PAYMENT-SIGNATURE → verify, settle, serve, with the
// SettlementResponse on PAYMENT-RESPONSE; and the settlement is looked up scoped
// to the payer org.
func TestChallengeVerifyServe(t *testing.T) {
	h := newHarness(t)
	pubRegistry(map[string]Terms{
		"/paid/tool": {Amount: money.FromCents(100), RecipientOrg: h.payeeOrg, RecipientWalletID: h.recipient},
	})

	req := h.challenge("/paid/tool", h.payerOrg)
	if req.Amount != "1000000" { // $1.00 → USDC 6-dp atomic units
		t.Fatalf("challenge amount = %q, want 1000000", req.Amount)
	}
	if req.Scheme != SchemeExact {
		t.Fatalf("challenge scheme = %q, want %q", req.Scheme, SchemeExact)
	}
	if req.Network != DefaultNetwork {
		t.Fatalf("challenge network = %q, want the CAIP-2 %q", req.Network, DefaultNetwork)
	}
	if req.PayTo == "" || req.MaxTimeoutSeconds <= 0 || req.Extra == nil {
		t.Fatalf("bad requirements: %+v", req)
	}

	code, b, hdr := h.payFor("/paid/tool", h.payerOrg, req, randNonce())
	if code != 200 {
		t.Fatalf("paid request = %d (%s), want 200", code, b)
	}
	if !bytes.Contains(b, []byte(`"served":true`)) {
		t.Fatalf("handler did not run: %s", b)
	}
	settled := h.settlement(hdr)
	if !settled.Success || settled.Transaction == "" || settled.Network != req.Network {
		t.Fatalf("bad settlement response: %+v", settled)
	}
	if settled.Payer == "" || settled.Amount != "1" {
		t.Fatalf("settlement response amount/payer: %+v", settled)
	}
	h.tie(money.FromCents(100), "after one paid request")
	if h.doer.calls() != 0 {
		t.Fatalf("settlement went out over HTTP (%d calls) instead of the co-resident ledger", h.doer.calls())
	}

	// Receipt lookup is tenant-scoped: the payer sees it; another org gets 404. The
	// id is the settlement response's `transaction`, which is how a client finds it.
	code, _, _ = h.req(http.MethodGet, "/v1/x402/settlements/"+settled.Transaction, h.payerOrg, "", "")
	if code != 200 {
		t.Fatalf("receipt lookup by payer = %d, want 200", code)
	}
	code, _, _ = h.req(http.MethodGet, "/v1/x402/settlements/"+settled.Transaction, "intruder", "", "")
	if code != 404 {
		t.Fatalf("cross-tenant receipt lookup = %d, want 404", code)
	}
}

// A settled nonce is spent: reusing it for DIFFERENT terms is a replay and is
// refused, and the metering spine is NOT hit a second time.
func TestNonceReplayRejected(t *testing.T) {
	h := newHarness(t)
	pubRegistry(map[string]Terms{
		"/paid/tool":  {Amount: money.FromCents(100), RecipientOrg: h.payeeOrg, RecipientWalletID: h.recipient},
		"/paid/other": {Amount: money.FromCents(250), RecipientOrg: h.payeeOrg, RecipientWalletID: h.recipient},
	})

	nonce := randNonce()
	reqTool := h.challenge("/paid/tool", h.payerOrg)
	if code, b, _ := h.payFor("/paid/tool", h.payerOrg, reqTool, nonce); code != 200 {
		t.Fatalf("first payment = %d (%s)", code, b)
	}
	h.tie(money.FromCents(100), "after the first payment")

	// Reuse the SAME (from, nonce) to buy a DIFFERENT, more expensive resource.
	reqOther := h.challenge("/paid/other", h.payerOrg)
	code, b, hdr := h.payFor("/paid/other", h.payerOrg, reqOther, nonce)
	if code != http.StatusPaymentRequired {
		t.Fatalf("replayed nonce = %d (%s), want 402", code, b)
	}
	if !bytes.Contains(b, []byte(reasonReplay)) {
		t.Fatalf("replay not flagged: %s", b)
	}
	// The spec requires a SettlementResponse on the failure leg too.
	if failed := h.settlement(hdr); failed.Success || failed.ErrorReason != reasonReplay {
		t.Fatalf("failure leg settlement = %+v", failed)
	}
	h.tie(money.FromCents(100), "after the replay was refused")
}

// Re-submitting the SAME authorization (a client retry) is idempotent: served
// again, but settled — and metered — exactly once.
func TestSettleOnceOnRetry(t *testing.T) {
	h := newHarness(t)
	pubRegistry(map[string]Terms{
		"/paid/tool": {Amount: money.FromCents(100), RecipientOrg: h.payeeOrg, RecipientWalletID: h.recipient},
	})
	req := h.challenge("/paid/tool", h.payerOrg)
	now := nowUnix()
	payment := h.pay(req, randNonce(), now-60, now+300)

	var firstID string
	for i := 0; i < 3; i++ {
		code, b, hdr := h.req(http.MethodGet, "/paid/tool", h.payerOrg, payment, "")
		if code != 200 {
			t.Fatalf("retry %d = %d (%s), want 200", i, code, b)
		}
		id := h.settlement(hdr).Transaction
		if firstID == "" {
			firstID = id
		} else if id != firstID {
			t.Fatalf("retry produced a new settlement id %s != %s", id, firstID)
		}
	}
	h.tie(money.FromCents(100), "after 3 retries of one authorization")
}

// An unpriced resource passes straight through — no 402, no charge.
func TestFreeResourcePassthrough(t *testing.T) {
	h := newHarness(t)
	pubRegistry(map[string]Terms{
		"/paid/tool": {Amount: money.FromCents(100), RecipientOrg: h.payeeOrg, RecipientWalletID: h.recipient},
	})
	code, b, _ := h.req(http.MethodGet, "/paid/free", h.payerOrg, "", "")
	if code != 200 || !bytes.Contains(b, []byte(`"served":true`)) {
		t.Fatalf("free resource = %d (%s), want 200 served", code, b)
	}
	h.tie(money.Zero(), "after a free request")
}

// A priced resource requires a validated payer (ledger settlement debits an org).
func TestPricedResourceRequiresPayer(t *testing.T) {
	h := newHarness(t)
	pubRegistry(map[string]Terms{
		"/paid/tool": {Amount: money.FromCents(100), RecipientOrg: h.payeeOrg, RecipientWalletID: h.recipient},
	})
	code, _, _ := h.req(http.MethodGet, "/paid/tool", "", "", "") // no principal
	if code != http.StatusForbidden {
		t.Fatalf("unvalidated priced request = %d, want 403", code)
	}
}

// End-to-end LEDGER settlement, stated as balances: a paid request debits the payer's
// org through the metering spine AND credits the recipient wallet's ledger — once.
// A retry moves no more money.
func TestSettlesToRecipientLedger(t *testing.T) {
	h := newHarness(t)
	pubRegistry(map[string]Terms{
		"/paid/tool": {Amount: money.FromCents(100), RecipientOrg: h.payeeOrg, RecipientWalletID: h.recipient},
	})
	req := h.challenge("/paid/tool", h.payerOrg)
	now := nowUnix()
	payment := h.pay(req, randNonce(), now-60, now+300)

	if code, b, _ := h.req(http.MethodGet, "/paid/tool", h.payerOrg, payment, ""); code != 200 {
		t.Fatalf("paid request = %d (%s)", code, b)
	}
	h.tie(money.FromCents(100), "after payment")
	if got := h.balance(h.payerOrg, h.payerOrg); got.Cmp(money.FromCents(900)) != 0 {
		t.Fatalf("payer balance = %s, want 9 (10 - 1)", got.String())
	}

	// Retry the same authorization: settle-once → balances unchanged.
	if code, _, _ := h.req(http.MethodGet, "/paid/tool", h.payerOrg, payment, ""); code != 200 {
		t.Fatal("retry not served")
	}
	h.tie(money.FromCents(100), "after retrying the same authorization")
}

// A settlement moves BOTH sides or neither. With no ledger to credit the payee, the
// payer must not be charged and the resource must not be served — the alternative is
// money debited into nothing, or (with the credit alone) money minted from a missing
// dependency.
func TestSettlementRefusesHalfMove(t *testing.T) {
	h := newHarness(t)
	pubRegistry(map[string]Terms{
		"/paid/tool": {Amount: money.FromCents(100), RecipientOrg: h.payeeOrg, RecipientWalletID: h.recipient},
	})
	req := h.challenge("/paid/tool", h.payerOrg)
	now := nowUnix()
	payment := h.pay(req, randNonce(), now-60, now+300)

	ledger := finance.Current()
	finance.Publish(nil) // the payee's ledger goes away mid-flight
	code, b, _ := h.req(http.MethodGet, "/paid/tool", h.payerOrg, payment, "")
	finance.Publish(ledger)

	if code != http.StatusServiceUnavailable {
		t.Fatalf("settlement with no payee ledger = %d (%s), want 503 — never serve a half-moved payment", code, b)
	}
	h.tie(money.Zero(), "after a refused settlement")
}

// THE MONEY-LOSS REGRESSION. The payer's debit lands and the payee's credit fails —
// the one interleaving that actually moves money and then stops.
//
// The old flow left the payer permanently down: recovery ran only through the
// client re-presenting its authorization, and the authorization expires
// (maxTimeoutSeconds, 300s), so a client that gave up for five minutes was debited
// with nothing delivered and nothing to sweep it. This pins the two independent
// paths that now converge instead:
//
//  1. the CLIENT comes back — at any time, with the SAME authorization, long after
//     validBefore — and is served, because the window gates ACCEPTING a payment and
//     not COMPLETING one already accepted; and
//  2. the client never comes back and Reconcile finishes it from the durable claim,
//     with no signature involved at all.
//
// Either way the books end tied, which is the whole invariant: the payer is never
// left down money that no path can deliver.
func TestIncompleteSettlementNeverStrandsThePayer(t *testing.T) {
	for _, tc := range []struct {
		name    string
		recover func(t *testing.T, h *harness, path, payment string)
	}{
		{"the client returns after the authorization expired", func(t *testing.T, h *harness, path, payment string) {
			// Long past validBefore. The claim, not the signature, is what completes it.
			restore := nowUnix
			nowUnix = func() int64 { return restore() + 100_000 }
			defer func() { nowUnix = restore }()

			code, b, hdr := h.req(http.MethodGet, path, h.payerOrg, payment, "")
			if code != 200 {
				t.Fatalf("expired retry of an accepted payment = %d (%s), want 200 — "+
					"validBefore bounds acceptance, not completion", code, b)
			}
			if s := h.settlement(hdr); !s.Success {
				t.Fatalf("completed settlement reported failure: %+v", s)
			}
		}},
		{"the client never returns and the sweep finishes it", func(t *testing.T, h *harness, _, _ string) {
			n, err := Reconcile(context.Background(), 0)
			if err != nil {
				t.Fatalf("reconcile: %v", err)
			}
			if n != 1 {
				t.Fatalf("reconcile completed %d settlements, want 1", n)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			pubRegistry(map[string]Terms{
				"/paid/tool": {Amount: money.FromCents(100), RecipientOrg: h.payeeOrg, RecipientWalletID: h.recipient},
			})
			req := h.challenge("/paid/tool", h.payerOrg)
			now := nowUnix()
			payment := h.pay(req, randNonce(), now-60, now+300)

			// The credit side breaks AFTER the debit has landed.
			h.ledger.breaks(true)
			code, b, _ := h.req(http.MethodGet, "/paid/tool", h.payerOrg, payment, "")
			if code != http.StatusServiceUnavailable {
				t.Fatalf("settlement with a broken credit = %d (%s), want 503", code, b)
			}
			if got := h.debited(); got.Cmp(money.FromCents(100)) != 0 {
				t.Fatalf("precondition: payer debited %s, want the interleaving that "+
					"strands money (debit landed, credit did not)", got.String())
			}
			if got := h.credited(); got.Sign() != 0 {
				t.Fatalf("precondition: payee credited %s, want 0", got.String())
			}

			h.ledger.breaks(false) // the ledger comes back
			tc.recover(t, h, "/paid/tool", payment)

			// The books tie: the payer is down exactly the price and the payee is up
			// exactly the price. Nothing minted, nothing destroyed.
			h.tie(money.FromCents(100), "after the settlement converged")

			// And it landed in the RIGHT account — the payout wallet the listing
			// named, not the seller org. A balance read cannot tell those apart
			// (finance aggregates at the org), so the ledger is asked directly.
			credits := h.ledger.credits()
			if len(credits) != 1 {
				t.Fatalf("want exactly one x402 credit, got %d: %+v", len(credits), credits)
			}
			if credits[0].Subject != h.recipient {
				t.Fatalf("credited subject %q, want the payout wallet %q — completing a "+
					"settlement must pay the account the claim named", credits[0].Subject, h.recipient)
			}
			if credits[0].Org != h.payeeOrg {
				t.Fatalf("credited org %q, want %q", credits[0].Org, h.payeeOrg)
			}
		})
	}
}

// The claim is durable BEFORE the money moves, which is what makes an interrupted
// settlement recoverable at all — and it must not, on its own, look like a paid
// settlement. A claim that served the resource would be a free lunch.
func TestAClaimIsNotAReceipt(t *testing.T) {
	h := newHarness(t)
	pubRegistry(map[string]Terms{
		"/paid/tool": {Amount: money.FromCents(100), RecipientOrg: h.payeeOrg, RecipientWalletID: h.recipient},
	})
	req := h.challenge("/paid/tool", h.payerOrg)
	now := nowUnix()
	payment := h.pay(req, randNonce(), now-60, now+300)

	h.ledger.breaks(true)
	if code, _, _ := h.req(http.MethodGet, "/paid/tool", h.payerOrg, payment, ""); code != http.StatusServiceUnavailable {
		t.Fatalf("broken credit must refuse, got %d", code)
	}
	// The claim exists and is unsettled.
	pending, err := mounted.State.store.pending(context.Background(), nowUnix())
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if len(pending) != 1 || pending[0].Settled {
		t.Fatalf("want exactly one UNSETTLED claim, got %+v", pending)
	}
	// Retrying while the ledger is still broken must still refuse, never serve: a
	// claim is a record that we accepted a payment, not that we received one.
	if code, _, _ := h.req(http.MethodGet, "/paid/tool", h.payerOrg, payment, ""); code != http.StatusServiceUnavailable {
		t.Fatalf("a retry against a still-broken ledger = %d, want 503 — a claim is not a payment", code)
	}
	h.ledger.breaks(false)
	if code, _, _ := h.req(http.MethodGet, "/paid/tool", h.payerOrg, payment, ""); code != 200 {
		t.Fatalf("the settlement must complete once the ledger returns")
	}
	h.tie(money.FromCents(100), "after the claim completed")
}
