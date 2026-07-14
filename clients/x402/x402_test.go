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
	"github.com/hanzoai/cloud/clients/commerce/metering"
	"github.com/hanzoai/cloud/clients/finance"
	"github.com/hanzoai/cloud/clients/kms"
	"github.com/hanzoai/cloud/clients/money"
	"github.com/hanzoai/cloud/clients/wallets"
	"github.com/hanzoai/cloud/types"
	"github.com/luxfi/crypto"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// ── harness ───────────────────────────────────────────────────────────────────

// countingDoer answers every commerce call 200 and counts usage POSTs, so a test
// can prove the metering spine is hit EXACTLY once per settled payment (settle-once)
// without standing up commerce.
type countingDoer struct {
	mu    sync.Mutex
	usage int
}

func (d *countingDoer) count() int { d.mu.Lock(); defer d.mu.Unlock(); return d.usage }

func (d *countingDoer) Do(req *http.Request) (*http.Response, error) {
	if req.Method == http.MethodPost && req.URL.Path == "/v1/billing/usage" {
		d.mu.Lock()
		d.usage++
		d.mu.Unlock()
	}
	body := `{"transactionId":"t_1","user":"payer","amount":0,"currency":"usd","type":"withdraw"}`
	return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader([]byte(body))), Header: make(http.Header)}, nil
}

type harness struct {
	t         *testing.T
	app       *zip.App
	doer      *countingDoer
	payerKey  *ecdsa.PrivateKey
	payerOrg  string
	payeeOrg  string
	recipient string // recipient wallet id
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	// The cek data plane (wallets.db + x402.db) is encryption-capable here; supply a
	// process master key so the stores open.
	raw := make([]byte, 32)
	_, _ = rand.Read(raw)
	t.Setenv("CLOUD_KMS_MASTER_KEY_REF", base64.StdEncoding.EncodeToString(raw))

	// Default: NO co-resident finance (counting tests exercise the HTTP metering
	// path). The ledger test publishes its own; both reset on cleanup.
	finance.Publish(nil)
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

	doer := &countingDoer{}
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

	h := &harness{t: t, app: app, doer: doer, payerOrg: "payerorg", payeeOrg: "payeeorg"}
	h.payerKey, _ = crypto.GenerateKey()
	h.recipient = h.createRecipient(h.payeeOrg)
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
	resp, err := h.app.Fiber().Test(hr)
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
	if h.doer.count() != 1 {
		t.Fatalf("metering usage recorded %d times, want 1", h.doer.count())
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
	if h.doer.count() != 1 {
		t.Fatalf("usage after first payment = %d, want 1", h.doer.count())
	}

	// Reuse the SAME (from, nonce) to buy a DIFFERENT, more expensive resource.
	reqOther := h.challenge("/paid/other", h.payerOrg)
	code, b, _ := h.payFor("/paid/other", h.payerOrg, reqOther, nonce)
	if code != http.StatusPaymentRequired {
		t.Fatalf("replayed nonce = %d (%s), want 402", code, b)
	}
	if !bytes.Contains(b, []byte("nonce_replayed")) {
		t.Fatalf("replay not flagged: %s", b)
	}
	if h.doer.count() != 1 {
		t.Fatalf("replay double-charged: usage = %d, want 1", h.doer.count())
	}
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
	if h.doer.count() != 1 {
		t.Fatalf("settle-once violated: metered %d times across 3 retries, want 1", h.doer.count())
	}
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
	if h.doer.count() != 0 {
		t.Fatalf("free resource charged: usage = %d", h.doer.count())
	}
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

// End-to-end LEDGER settlement: with a co-resident finance ledger, a paid request
// debits the payer's org through the metering spine AND credits the recipient
// wallet's ledger — once. A retry moves no more money.
func TestSettlesToRecipientLedger(t *testing.T) {
	h := newHarness(t)
	fin := finance.New(h.t.TempDir())
	finance.Publish(fin)
	ctx := context.Background()

	// Fund the payer so its metered debit has balance to draw down.
	if _, err := fin.Deposit(ctx, depositUSD(h.payerOrg, h.payerOrg, money.FromCents(1000), "fund")); err != nil {
		t.Fatalf("fund payer: %v", err)
	}
	balance := func(org, subject string) int64 {
		amt, err := fin.Balance(ctx, org, subject, "usd", false)
		if err != nil {
			t.Fatalf("balance: %v", err)
		}
		return amt.Cents()
	}

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
	if got := balance(h.payerOrg, h.payerOrg); got != 900 {
		t.Fatalf("payer balance = %d cents, want 900 (1000 - 100)", got)
	}
	if got := balance(h.payeeOrg, h.recipient); got != 100 {
		t.Fatalf("recipient balance = %d cents, want 100", got)
	}

	// Retry the same authorization: settle-once → balances unchanged.
	if code, _, _ := h.req(http.MethodGet, "/paid/tool", h.payerOrg, string(pb), ""); code != 200 {
		t.Fatal("retry not served")
	}
	if got := balance(h.payerOrg, h.payerOrg); got != 900 {
		t.Fatalf("payer balance after retry = %d cents, want still 900", got)
	}
	if got := balance(h.payeeOrg, h.recipient); got != 100 {
		t.Fatalf("recipient balance after retry = %d cents, want still 100", got)
	}
}
