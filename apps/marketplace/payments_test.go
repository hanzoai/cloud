package marketplace

// payments_test.go proves the pay-per-use seam END TO END, over HTTP, with money
// actually moving: publish a monetized listing → a buyer installs it → calling the
// tool is refused with a real x402 challenge → the buyer signs an ERC-3009
// authorization over exactly those terms → the call runs and the ledger ties.
//
// Nothing here is faked below the seam. The registry is the marketplace's own, the
// charger is the marketplace's own, the settlement is x402's, the payee wallet is a
// real KMS wallet in the wallets subsystem, and the ledger is a real finance
// ledger. The only stand-in is the TOOL, which does not participate in payment.

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
	"testing"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/finance"
	"github.com/hanzoai/cloud/apps/kms"
	"github.com/hanzoai/cloud/apps/metering"
	"github.com/hanzoai/cloud/apps/tools"
	"github.com/hanzoai/cloud/apps/wallets"
	"github.com/hanzoai/cloud/apps/x402"
	"github.com/hanzoai/cloud/money"
	"github.com/hanzoai/cloud/plane"
	"github.com/hanzoai/cloud/types"
	"github.com/luxfi/crypto"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// refusingDoer fails every outbound commerce call. Nothing here may leave the
// process: with a co-resident ledger the metering spine posts the payer debit
// natively, so any HTTP attempt means the money took a path this test is not
// asserting on, and it should fail loudly rather than pass quietly.
type refusingDoer struct{}

func (refusingDoer) Do(*http.Request) (*http.Response, error) {
	return nil, errors.New("no commerce call may leave this process")
}

// ── harness ───────────────────────────────────────────────────────────────────

// market is the four subsystems that must share one process for a price to be
// payable: wallets (who is paid), x402 (enforcement + settlement), tools (where a
// call is dispatched) and marketplace (what is priced). Plus a co-resident finance
// ledger, so a settlement is an entry and not an HTTP hope.
type market struct {
	t     *testing.T
	app   *zip.App
	fin   types.FinanceClient
	key   *ecdsa.PrivateKey // the buyer's payment key
	payee string            // the seller's wallet id
	// resource is what the last challenge said it was FOR. In x402 v2 the resource
	// lives on the PaymentRequired envelope (resource.url), not on the individual
	// PaymentRequirements, so challengeOf parks it here for the assertions below.
	resource string
}

func newMarket(t *testing.T, sellerOrg string, offered ...string) *market {
	t.Helper()
	raw := make([]byte, 32)
	_, _ = rand.Read(raw)
	t.Setenv("CLOUD_KMS_MASTER_KEY_REF", base64.StdEncoding.EncodeToString(raw))

	// The tool plane bills its OWN orchestration unit on every successful call —
	// cloud.DefaultResourceFeeCents, $1.00, unless a deployment prices it — and that
	// debit is fire-and-forget on a background context. Left at its default it would
	// land in the same ledger these tests read, asynchronously, so "the payer was
	// debited exactly the price" would be measuring two charges and racing one of
	// them. Priced at zero, the only thing that can move money here is x402.
	t.Setenv("CLOUD_TOOLS_FEE_CENTS", "0")

	log := luxlog.New("test")
	dir := t.TempDir()

	kraw := make([]byte, 32)
	_, _ = rand.Read(kraw)
	kmsClient, kerr := kms.New(kms.Config{DataDir: dir, MasterKeyB64: base64.StdEncoding.EncodeToString(kraw)}, log)
	if kerr != nil {
		t.Fatalf("kms.New: %v", kerr)
	}
	t.Cleanup(func() { _ = kmsClient.Close() })

	// A settlement is two ledger entries that agree, so the ledger is co-resident
	// and the metering spine (which is how the PAYER is debited) posts into it
	// natively. Both halves present, or x402 refuses to settle at all.
	fin := finance.New(t.TempDir())
	finance.Publish(fin)
	meter, err := metering.New(metering.Config{BaseURL: "http://commerce.test", HTTPClient: refusingDoer{}})
	if err != nil {
		t.Fatalf("metering.New: %v", err)
	}

	app := zip.New(zip.Config{Logger: log})
	deps := cloud.Deps{Logger: log, KMS: kmsClient, DataDir: dir, Metering: meter}

	// Mount order is the composition root's: marketplace installs cloud.Bridge
	// app-wide and fiber runs middleware in REGISTRATION order, so it must be
	// registered before the tool plane's leaves or a dispatch reaches no parked
	// request — no attested payer, and every priced tool 424s.
	if err := wallets.Mount(app, deps); err != nil {
		t.Fatalf("wallets.Mount: %v", err)
	}
	if err := x402.Mount(app, deps); err != nil {
		t.Fatalf("x402.Mount: %v", err)
	}
	if err := Mount(app, deps); err != nil {
		t.Fatalf("marketplace.Mount: %v", err)
	}

	// The REAL tool plane, not a stand-in for it. A payment seam proved through a
	// hand-rolled route proves the seam and not the product: the door that has to
	// carry a payer, map a 402 and let the challenge header out is tools' own
	// callTool, so that is the door every test here knocks on.
	if err := tools.Mount(app, deps); err != nil {
		t.Fatalf("tools.Mount: %v", err)
	}
	var offer []tools.Tool
	for _, name := range offered {
		offer = append(offer, tools.Tool{Name: name, Source: tools.SourceConnector, Dispatchable: true})
	}
	tools.Default().Register(&fakeProvider{tools: offer})

	t.Cleanup(func() {
		_ = Shutdown(context.Background())
		_ = tools.Shutdown(context.Background())
		_ = x402.Shutdown()
		_ = wallets.Shutdown()
		finance.Publish(nil)
		tools.Default().SetActivation(nil)
	})

	m := &market{t: t, app: app, fin: fin}
	m.key, _ = crypto.GenerateKey()
	m.payee = m.createWallet(sellerOrg)
	return m
}

// createWallet provisions a real KMS wallet in org and returns its id — the payout
// wallet a monetized listing names.
func (m *market) createWallet(org string) string {
	m.t.Helper()
	_, b, _ := m.req(http.MethodPost, "/v1/wallets/accounts", org, "", `{"name":"payee"}`)
	var acct struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(b, &acct); err != nil || acct.ID == "" {
		m.t.Fatalf("create account: %v (%s)", err, b)
	}
	code, b, _ := m.req(http.MethodPost, "/v1/wallets", org, "",
		`{"accountId":"`+acct.ID+`","name":"earnings","custody":"kms","tier":"hot","chain":"eip155:36963"}`)
	if code != 200 {
		m.t.Fatalf("create wallet = %d (%s)", code, b)
	}
	var w struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(b, &w); err != nil || w.ID == "" {
		m.t.Fatalf("decode wallet: %v (%s)", err, b)
	}
	return w.ID
}

// req drives one request as org, optionally carrying an x402 proof.
func (m *market) req(method, path, org, proof, body string) (int, []byte, http.Header) {
	m.t.Helper()
	var r io.Reader
	if body != "" {
		r = bytes.NewReader([]byte(body))
	}
	hr := httptest.NewRequest(method, path, r)
	if body != "" {
		hr.Header.Set("Content-Type", "application/json")
	}
	if org != "" {
		hr.Header.Set("X-Org-Id", org)
		hr.Header.Set("X-User-Id", "u_"+org)
	}
	if proof != "" {
		hr.Header.Set(x402.HeaderPaymentSignature, proof)
	}
	resp, err := m.app.Test(hr, zip.TestConfig{Timeout: 0})
	if err != nil {
		m.t.Fatalf("Test %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b, resp.Header
}

// list publishes a listing as org and returns the created row.
func (m *market) list(org string, in publishReq) Listing {
	m.t.Helper()
	body, _ := json.Marshal(in)
	code, b, _ := m.req(http.MethodPost, "/v1/marketplace/listings", org, "", string(body))
	if code != http.StatusCreated {
		m.t.Fatalf("publish = %d (%s), want 201", code, b)
	}
	var l Listing
	if err := json.Unmarshal(b, &l); err != nil {
		m.t.Fatalf("decode listing: %v (%s)", err, b)
	}
	return l
}

// install activates a tool for org, the way a buyer does.
func (m *market) install(org, tool string) {
	m.t.Helper()
	if code, b, _ := m.req(http.MethodPost, "/v1/marketplace/install", org, "", `{"tool":"`+tool+`"}`); code != 200 {
		m.t.Fatalf("install = %d (%s), want 200", code, b)
	}
}

// unpublish withdraws a listing as org.
func (m *market) unpublish(org, id string) {
	m.t.Helper()
	if code, b, _ := m.req(http.MethodDelete, "/v1/marketplace/listings/"+id, org, "", ""); code != 204 {
		m.t.Fatalf("unpublish = %d (%s), want 204", code, b)
	}
}

// call dispatches a tool as org, optionally paying.
func (m *market) call(org, tool, proof string) (int, []byte, http.Header) {
	m.t.Helper()
	return m.req(http.MethodPost, "/v1/tools/call", org, proof, `{"name":"`+tool+`"}`)
}

// pay signs an authorization over exactly the challenge's terms — what a compliant
// x402 client does — and returns the PAYMENT-SIGNATURE header value.
func (m *market) pay(req x402.PaymentRequirements) string {
	m.t.Helper()
	now := time.Now().Unix()
	nonce := make([]byte, 32)
	_, _ = rand.Read(nonce)
	p, err := x402.Sign(req, m.key, "0x"+hex.EncodeToString(nonce), now-60, now+300)
	if err != nil {
		m.t.Fatalf("sign: %v", err)
	}
	return x402.EncodeHeader(p)
}

// challengeOf reads the PaymentRequirements off a 402 refusal's header — the
// canonical carrier, and the only one a typed op's error body can leave room for.
func (m *market) challengeOf(h http.Header) x402.PaymentRequirements {
	m.t.Helper()
	raw := h.Get(x402.HeaderPaymentRequired)
	if raw == "" {
		m.t.Fatalf("402 carried no %s header — a challenge a client cannot read is not a challenge", x402.HeaderPaymentRequired)
	}
	var required x402.PaymentRequired
	if err := x402.DecodeHeader(raw, &required); err != nil {
		m.t.Fatalf("decode challenge %q: %v", raw, err)
	}
	if len(required.Accepts) != 1 {
		m.t.Fatalf("challenge offered %d ways to pay, want 1", len(required.Accepts))
	}
	m.resource = required.Resource.URL
	return required.Accepts[0]
}

// balance is the exact ledger balance of (org, subject) — never cents.
func (m *market) balance(org, subject string) money.Amount {
	m.t.Helper()
	amt, err := m.fin.Balance(context.Background(), org, subject, "usd", false)
	if err != nil {
		m.t.Fatalf("balance %s/%s: %v", org, subject, err)
	}
	return amt
}

func (m *market) fund(org string, amount money.Amount) {
	m.t.Helper()
	if _, err := m.fin.Deposit(context.Background(), types.DepositInput{
		Org: org, Subject: org, Amount: amount, Currency: "usd", Ref: "fund_" + org,
	}); err != nil {
		m.t.Fatalf("fund %s: %v", org, err)
	}
}

// ── the proofs ────────────────────────────────────────────────────────────────

// TestPricedToolChallengedThenSettles is the whole defect, closed:
//
//	(a) a priced tool called without payment is refused with the x402 CHALLENGE —
//	    not ErrChargerUnset, which was a permanently unpayable 402 with no terms in
//	    it and therefore no way for any client to proceed;
//	(b) the same call, paid, runs — and the ledger entries tie EXACTLY. The price
//	    is a quarter of a cent, which is zero in every cents-typed field this path
//	    used to run through, so "0.0025 charged" and "0.0025 credited" is the
//	    assertion that a sub-cent price is a real price.
func TestPricedToolChallengedThenSettles(t *testing.T) {
	const (
		seller = "sellerorg"
		buyer  = "buyerorg"
		tool   = "conn_premium"
	)
	m := newMarket(t, seller, tool)

	price, err := money.ParseUSD("0.0025")
	if err != nil {
		t.Fatalf("parse price: %v", err)
	}
	m.list(seller, publishReq{
		Tool: tool, Title: "Premium", Price: "0.0025", Currency: "USD",
		Recipient: m.payee, Public: true,
	})
	m.install(buyer, tool)
	m.fund(buyer, money.FromCents(100))

	before := m.balance(buyer, buyer)

	// (a) UNPAID → 402 carrying the terms to pay.
	code, body, hdr := m.call(buyer, tool, "")
	if code != http.StatusPaymentRequired {
		t.Fatalf("unpaid priced call = %d (%s), want 402", code, body)
	}
	if bytes.Contains(body, []byte("payment seam not configured")) {
		t.Fatalf("still fails closed on an unwired charger, not an x402 challenge: %s", body)
	}
	req := m.challengeOf(hdr)
	if m.resource != plane.ToolResource(tool) {
		t.Fatalf("challenge names %q, want the tool resource %q", m.resource, plane.ToolResource(tool))
	}
	if req.PayTo == "" {
		t.Fatal("challenge names no payee address — nothing to pay")
	}
	// $0.0025 at USDC's 6 decimals is 2500 smallest units. A cents-typed path would
	// have offered 0 here and the client would have signed away nothing.
	if req.Amount != "2500" {
		t.Fatalf("challenge amount = %q, want 2500 (0.0025 USD at 6dp)", req.Amount)
	}
	if got := m.balance(buyer, buyer); got.Cmp(before) != 0 {
		t.Fatalf("a refused call moved money: %s → %s", before.String(), got.String())
	}

	// (b) PAID → served, and both sides of the ledger tie exactly.
	code, body, hdr = m.call(buyer, tool, m.pay(req))
	if code != http.StatusOK {
		t.Fatalf("paid call = %d (%s), want 200", code, body)
	}
	if !bytes.Contains(body, []byte(`"ran":true`)) {
		t.Fatalf("tool did not run after payment: %s", body)
	}
	if hdr.Get(x402.HeaderPaymentResponse) == "" {
		t.Fatalf("paid call carried no %s receipt", x402.HeaderPaymentResponse)
	}

	debited := before.Sub(m.balance(buyer, buyer))
	if debited.Cmp(price) != 0 {
		t.Fatalf("payer debited %s, want exactly %s", debited.String(), price.String())
	}
	credited := m.balance(seller, m.payee)
	if credited.Cmp(price) != 0 {
		t.Fatalf("seller credited %s, want exactly %s", credited.String(), price.String())
	}
	if credited.IsZero() {
		t.Fatal("a sub-cent price rounded to zero — the money vanished")
	}
}

// TestUnpricedToolUnaffected: closing the seam must not put a toll on anything that
// was free. An unlisted tool, and a listed-but-free one, both dispatch with no
// challenge and no ledger movement — even though the charger is wired and consulted.
func TestUnpricedToolUnaffected(t *testing.T) {
	const (
		seller = "sellerorg"
		buyer  = "buyerorg"
	)
	m := newMarket(t, seller, "conn_free", "conn_unlisted")

	m.list(seller, publishReq{Tool: "conn_free", Title: "Free", Public: true})
	m.install(buyer, "conn_free")
	m.install(buyer, "conn_unlisted")
	m.fund(buyer, money.FromCents(100))
	before := m.balance(buyer, buyer)

	for _, tool := range []string{"conn_free", "conn_unlisted"} {
		code, body, hdr := m.call(buyer, tool, "")
		if code != http.StatusOK {
			t.Fatalf("%s = %d (%s), want 200 — an unpriced tool is not for sale", tool, code, body)
		}
		if hdr.Get(x402.HeaderPaymentRequired) != "" {
			t.Fatalf("%s was challenged for payment it does not cost", tool)
		}
	}
	if got := m.balance(buyer, buyer); got.Cmp(before) != 0 {
		t.Fatalf("free calls moved money: %s → %s", before.String(), got.String())
	}
}

// TestCrossOrgCreditImpossible: a buyer cannot redirect a credit, and a seller
// cannot be paid into a wallet outside its own org.
//
// Both fall out of the same fact rather than from a check: the payee org on the
// Terms is the LISTING ROW's publisher, never anything on the wire, and wallets
// resolves an id only within the org it is asked for. So naming a wallet that
// exists in another org resolves to nothing and the call is refused — the money
// does not go to the wrong place, and it does not go anywhere.
func TestCrossOrgCreditImpossible(t *testing.T) {
	const (
		seller  = "sellerorg"
		buyer   = "buyerorg"
		outside = "victimorg"
		tool    = "conn_premium"
	)
	m := newMarket(t, seller, tool)

	// A wallet that belongs to somebody else entirely.
	victimWallet := m.createWallet(outside)

	// The seller lists its tool but names the VICTIM's wallet as the payee.
	bad := m.list(seller, publishReq{
		Tool: tool, Title: "Premium", Price: "1.00", Currency: "USD",
		Recipient: victimWallet, Public: true,
	})
	m.install(buyer, tool)
	m.fund(buyer, money.FromCents(1000))
	before := m.balance(buyer, buyer)

	code, body, hdr := m.call(buyer, tool, "")
	if code == http.StatusOK {
		t.Fatalf("a listing naming another org's wallet was SERVED: %s", body)
	}
	if hdr.Get(x402.HeaderPaymentRequired) != "" {
		t.Fatalf("an unpayable listing issued a challenge a client could satisfy: %s", hdr.Get(x402.HeaderPaymentRequired))
	}
	if got := m.balance(outside, victimWallet); !got.IsZero() {
		t.Fatalf("credited %s to an org that never published anything", got.String())
	}
	if got := m.balance(buyer, buyer); got.Cmp(before) != 0 {
		t.Fatalf("buyer was debited for a call that never ran: %s → %s", before.String(), got.String())
	}

	// And the same listing pointed at the seller's OWN wallet is payable — proving
	// the refusal above is the org boundary and not a broken path.
	m.unpublish(seller, bad.ID)
	m.list(seller, publishReq{
		Tool: tool, Title: "Premium (own wallet)", Price: "1.00", Currency: "USD",
		Recipient: m.payee, Public: true,
	})
	code, body, hdr = m.call(buyer, tool, "")
	if code != http.StatusPaymentRequired {
		t.Fatalf("own-wallet listing = %d (%s), want a 402 challenge", code, body)
	}
	if code, body, _ = m.call(buyer, tool, m.pay(m.challengeOf(hdr))); code != http.StatusOK {
		t.Fatalf("paid own-wallet call = %d (%s), want 200", code, body)
	}
	if got := m.balance(seller, m.payee); got.Cmp(money.FromCents(100)) != 0 {
		t.Fatalf("seller credited %s, want exactly 1", got.String())
	}
	if got := m.balance(outside, victimWallet); !got.IsZero() {
		t.Fatalf("the outside org was credited after all: %s", got.String())
	}
}

// TestFreeToolSurvivesAnAbsentRail: the charger is consulted on EVERY dispatch, so
// an absent payment rail must not take the free tools down with it. With x402 not
// mounted, an unpriced call still runs — nothing free needs a rail — while a priced
// one is refused rather than served.
//
// This pins a real regression: written the obvious way, Settle refused before it
// asked whether the resource was free, and every free tool in the process 424'd the
// moment marketplace was mounted without x402.
func TestFreeToolSurvivesAnAbsentRail(t *testing.T) {
	const (
		seller = "sellerorg"
		buyer  = "buyerorg"
	)
	m := newMarket(t, seller, "conn_free", "conn_paid")

	m.list(seller, publishReq{
		Tool: "conn_paid", Title: "Paid", Price: "1.00", Currency: "USD",
		Recipient: m.payee, Public: true,
	})
	m.install(buyer, "conn_free")
	m.install(buyer, "conn_paid")

	// The rail goes away; the price table stays (marketplace is still mounted).
	if err := x402.Shutdown(); err != nil {
		t.Fatalf("x402.Shutdown: %v", err)
	}

	if code, body, _ := m.call(buyer, "conn_free", ""); code != http.StatusOK {
		t.Fatalf("free tool with no payment rail = %d (%s), want 200", code, body)
	}
	if code, body, _ := m.call(buyer, "conn_paid", ""); code == http.StatusOK {
		t.Fatalf("priced tool was served with no payment rail: %s", body)
	}
}
