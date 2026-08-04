package x402

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
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

// funded is what the payer org starts with, so every debit has balance to draw down.
var funded = money.FromCents(1000)

type harness struct {
	t         *testing.T
	app       *zip.App
	doer      *commerceDoer
	fin       types.FinanceClient
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
	fin := finance.New(t.TempDir())
	finance.Publish(fin)
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

	h := &harness{t: t, app: app, doer: doer, fin: fin, payerOrg: "payerorg", payeeOrg: "payeeorg"}
	h.payerKey, _ = crypto.GenerateKey()
	h.recipient = h.createRecipient(h.payeeOrg)
	if _, err := fin.Deposit(context.Background(), depositUSD(h.payerOrg, h.payerOrg, funded, "fund")); err != nil {
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

// req drives one request. org (non-empty) sets a validated principal; proof (non-
// empty) sets the X-Payment header; jsonBody (non-empty) is a JSON POST body.
func (h *harness) req(method, path, org, proof, jsonBody string) (int, []byte, http.Header) {
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
	if proof != "" {
		hr.Header.Set(HeaderProof, proof)
	}
	resp, err := h.app.Test(hr)
	if err != nil {
		h.t.Fatalf("Test %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b, resp.Header
}

// challenge does the unpaid GET and returns the PaymentRequirements from the 402.
func (h *harness) challenge(path, org string) PaymentRequirements {
	h.t.Helper()
	code, b, _ := h.req(http.MethodGet, path, org, "", "")
	if code != http.StatusPaymentRequired {
		h.t.Fatalf("challenge %s = %d (%s), want 402", path, code, b)
	}
	var body struct {
		Accepts PaymentRequirements `json:"accepts"`
	}
	if err := json.Unmarshal(b, &body); err != nil {
		h.t.Fatalf("decode 402: %v (%s)", err, b)
	}
	return body.Accepts
}

// payFor signs a proof for req with the given nonce and retries the request.
func (h *harness) payFor(path, org string, req PaymentRequirements, nonce string) (int, []byte, http.Header) {
	h.t.Helper()
	now := nowUnix()
	p := signProof(h.t, h.payerKey, req, nonce, now-60, now+300)
	pb, _ := json.Marshal(p)
	return h.req(http.MethodGet, path, org, string(pb), "")
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

// The full flow: unpaid → 402 + requirements; signed proof → verify, settle, serve
// (with a receipt); and the settlement is looked up scoped to the payer org.
func TestChallengeVerifyServe(t *testing.T) {
	h := newHarness(t)
	pubRegistry(map[string]Terms{
		"/paid/tool": {Amount: money.FromCents(100), RecipientOrg: h.payeeOrg, RecipientWalletID: h.recipient},
	})

	req := h.challenge("/paid/tool", h.payerOrg)
	if req.Amount != "1000000" { // $1.00 → USDC 6-dp
		t.Fatalf("challenge amount = %q, want 1000000", req.Amount)
	}
	if req.Payee == "" || req.Resource != "/paid/tool" {
		t.Fatalf("bad requirements: %+v", req)
	}

	code, b, hdr := h.payFor("/paid/tool", h.payerOrg, req, randNonce())
	if code != 200 {
		t.Fatalf("paid request = %d (%s), want 200", code, b)
	}
	if !bytes.Contains(b, []byte(`"served":true`)) {
		t.Fatalf("handler did not run: %s", b)
	}
	rcptHdr := hdr.Get(HeaderReceipt)
	if rcptHdr == "" {
		t.Fatal("no X-Payment-Receipt header on served response")
	}
	var rcpt Receipt
	if err := json.Unmarshal([]byte(rcptHdr), &rcpt); err != nil {
		t.Fatalf("decode receipt: %v", err)
	}
	if rcpt.Payer != h.payerOrg || rcpt.SettledVia != "ledger" || rcpt.Amount != "1" {
		t.Fatalf("bad receipt: %+v", rcpt)
	}
	h.tie(money.FromCents(100), "after one paid request")
	if h.doer.calls() != 0 {
		t.Fatalf("settlement went out over HTTP (%d calls) instead of the co-resident ledger", h.doer.calls())
	}

	// Receipt lookup is tenant-scoped: the payer sees it; another org gets 404.
	code, _, _ = h.req(http.MethodGet, "/v1/x402/settlements/"+rcpt.ID, h.payerOrg, "", "")
	if code != 200 {
		t.Fatalf("receipt lookup by payer = %d, want 200", code)
	}
	code, _, _ = h.req(http.MethodGet, "/v1/x402/settlements/"+rcpt.ID, "intruder", "", "")
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
	code, b, _ := h.payFor("/paid/other", h.payerOrg, reqOther, nonce)
	if code != http.StatusPaymentRequired {
		t.Fatalf("replayed nonce = %d (%s), want 402", code, b)
	}
	if !bytes.Contains(b, []byte("nonce_replayed")) {
		t.Fatalf("replay not flagged: %s", b)
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
	proof := signProof(t, h.payerKey, req, randNonce(), now-60, now+300)
	pb, _ := json.Marshal(proof)

	var firstID string
	for i := 0; i < 3; i++ {
		code, b, hdr := h.req(http.MethodGet, "/paid/tool", h.payerOrg, string(pb), "")
		if code != 200 {
			t.Fatalf("retry %d = %d (%s), want 200", i, code, b)
		}
		var rc Receipt
		_ = json.Unmarshal([]byte(hdr.Get(HeaderReceipt)), &rc)
		if firstID == "" {
			firstID = rc.ID
		} else if rc.ID != firstID {
			t.Fatalf("retry produced a new settlement id %s != %s", rc.ID, firstID)
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
	proof := signProof(t, h.payerKey, req, randNonce(), now-60, now+300)
	pb, _ := json.Marshal(proof)

	if code, b, _ := h.req(http.MethodGet, "/paid/tool", h.payerOrg, string(pb), ""); code != 200 {
		t.Fatalf("paid request = %d (%s)", code, b)
	}
	h.tie(money.FromCents(100), "after payment")
	if got := h.balance(h.payerOrg, h.payerOrg); got.Cmp(money.FromCents(900)) != 0 {
		t.Fatalf("payer balance = %s, want 9 (10 - 1)", got.String())
	}

	// Retry the same authorization: settle-once → balances unchanged.
	if code, _, _ := h.req(http.MethodGet, "/paid/tool", h.payerOrg, string(pb), ""); code != 200 {
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
	proof := signProof(t, h.payerKey, req, randNonce(), now-60, now+300)
	pb, _ := json.Marshal(proof)

	ledger := finance.Current()
	finance.Publish(nil) // the payee's ledger goes away mid-flight
	code, b, _ := h.req(http.MethodGet, "/paid/tool", h.payerOrg, string(pb), "")
	finance.Publish(ledger)

	if code != http.StatusServiceUnavailable {
		t.Fatalf("settlement with no payee ledger = %d (%s), want 503 — never serve a half-moved payment", code, b)
	}
	h.tie(money.Zero(), "after a refused settlement")
}
