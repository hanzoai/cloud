package x402

import (
	"bytes"
	"cmp"
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
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/apps/kms"
	"github.com/hanzoai/cloud/apps/wallet"
	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/finance"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/money"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/cloud/sdk/principal"
	protocol "github.com/hanzoai/commerce/payment/x402"
	"github.com/luxfi/crypto"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"

	// devmaster keys this test binary: cek opens nothing without a master and a
	// test process has no KMS.
	_ "github.com/hanzoai/cloud/sdk/kit/master"
)

// ── harness ───────────────────────────────────────────────────────────────────

// halfDown is a ledger whose CREDIT side can be broken while its DEBIT side works —
// the exact partial failure a settlement has to survive, and the one no fake that
// simply removes the ledger can produce. The payer's half is a Draw and the payee's
// a Deposit, so failing only Deposit lands the debit and drops the credit.
type halfDown struct {
	money.FinanceClient
	mu       sync.Mutex
	broken   bool
	deposits []money.DepositInput
	slow     bool               // a hold is kept on its way, unanswered, in late
	late     []money.UsageInput // holds still on their way to the ledger
}

// Draw is the payer's half, on the ledger underneath, which stays up.
func (h *halfDown) Draw(ctx context.Context, in money.UsageInput) (string, bool, error) {
	return h.FinanceClient.(finance.Drawer).Draw(ctx, in)
}

// Hold and Return set a payment agreed before it is made aside, and give it
// back, on the ledger underneath.
func (h *halfDown) Hold(ctx context.Context, in money.UsageInput) (string, bool, error) {
	h.mu.Lock()
	slow := h.slow
	if slow {
		h.late = append(h.late, in)
	}
	h.mu.Unlock()
	if slow {
		return "", false, errors.New("finance: the hold is on its way and its caller stopped waiting")
	}
	return h.FinanceClient.(finance.Holder).Hold(ctx, in)
}

// land delivers the holds still on their way to the ledger, and answers what the
// ledger said to each.
func (h *halfDown) land(ctx context.Context) []error {
	h.mu.Lock()
	late := h.late
	h.late, h.slow = nil, false
	h.mu.Unlock()
	var out []error
	for _, in := range late {
		_, _, err := h.FinanceClient.(finance.Holder).Hold(ctx, in)
		out = append(out, err)
	}
	return out
}

func (h *halfDown) Return(ctx context.Context, in money.UsageInput) (money.Amount, error) {
	return h.FinanceClient.(finance.Holder).Return(ctx, in)
}

func (h *halfDown) breaks(v bool) { h.mu.Lock(); h.broken = v; h.mu.Unlock() }

// credits returns the deposits tagged x402 — the settlement's payee side, captured
// at the ledger. It is how a test asserts WHERE money landed: the ledger's Balance
// aggregates at the org, so reading a balance cannot tell a credit to the payout
// wallet's subject from one to the org itself, and a settlement that paid the wrong
// subject would read as correct.
func (h *halfDown) credits() []money.DepositInput {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []money.DepositInput
	for _, d := range h.deposits {
		if d.Tags == "x402" {
			out = append(out, d)
		}
	}
	return out
}

func (h *halfDown) Deposit(ctx context.Context, in money.DepositInput) (string, error) {
	h.mu.Lock()
	broken := h.broken
	h.mu.Unlock()
	if broken {
		return "", errors.New("ledger: credit side is down")
	}
	id, err := h.FinanceClient.Deposit(ctx, in)
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
	fin       money.FinanceClient
	ledger    *halfDown
	plane     *plane
	payerKey  *ecdsa.PrivateKey
	payerOrg  string
	payeeOrg  string
	recipient string // recipient wallet id
}

// plane is the event plane as this rail meets it. Like the event app, it takes a
// notice and asks the RAIL what the proof records, and it keeps one event per
// (rail, proof). down makes it refuse, which is the plane being unreachable;
// unlanded makes it take the statement without the warehouse having landed it.
type plane struct {
	mu       sync.Mutex
	down     bool
	unlanded bool
	calls    int
	events   []client.EconomicEvent
	actors   []string
}

func (p *plane) state(ctx context.Context, in *client.EconomicNotice) (*client.EconomicStated, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	if p.down {
		return nil, errors.New("event plane: unreachable")
	}
	r := rail{live}
	e, err := r.planeEconomic(ctx, in)
	if err != nil {
		return nil, err
	}
	if !slices.ContainsFunc(p.events, func(have client.EconomicEvent) bool { return have.Proof == e.Proof }) {
		p.events = append(p.events, *e)
		p.actors = append(p.actors, cloud.Who(ctx).Org)
	}
	return &client.EconomicStated{ID: client.EconomicID(e.Rail, e.Proof), Landed: !p.unlanded}, nil
}

func (p *plane) stated() []client.EconomicEvent {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.events)
}

func (p *plane) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

func (p *plane) set(down, unlanded bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.down, p.unlanded = down, unlanded
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

// compose installs what a HOST installs. A subsystem never installs cloud.Bridge
// (routes() says why): the program's composer installs it once at the root. In a
// test the test IS the composer, so it owes the same thing — a test that skips it
// tests a program where the receipt lookup answers 403 for a reason that would
// never exist in production. Same helper apps/integrations uses.
func compose(app *zip.App) { app.Use(cloud.Bridge()) }

func newHarness(t *testing.T) *harness {
	t.Helper()
	// The event plane is bound FIRST and restored LAST: the drainer Use starts reads
	// it until Shutdown stops the drainer.
	pl := &plane{}
	prev := emit
	emit = pl.state
	t.Cleanup(func() { emit = prev })

	// A settlement is a ledger entry on BOTH sides, so every test gets a real
	// co-resident ledger — there is no "settled" to assert without one.
	ledger := &halfDown{FinanceClient: finance.New(finance.Local(t.TempDir(), testMaster))}
	finance.Publish(ledger)
	t.Cleanup(func() { finance.Publish(nil); _ = wallet.Shutdown(); _ = Shutdown() })

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
	compose(app)
	t.Setenv("CLOUD_DATA_DIR", dir)
	deps := cloud.Deps{KMS: kmsClient, MasterKey: bytes.Repeat([]byte{0x5a}, 32)}

	// The rail's store is the one this suite puts on the leg's engine, so it alone is
	// mounted with the DSN the leg set; the wallet's KMS store is ported by its own commit
	// and opens as a file until then.
	dsn := os.Getenv(locus.Env)
	t.Setenv(locus.Env, "")
	if err := wallet.Use(app, deps); err != nil {
		t.Fatalf("wallet.Use:  %v", err)
	}

	// The token the challenge names: a payer signs over its address, so an unset
	// asset is a rail no payer can pay.
	t.Setenv("CLOUD_X402_ASSET", cmp.Or(os.Getenv("CLOUD_X402_ASSET"), testAsset))
	t.Setenv(locus.Env, dsn)
	err = Use(app, deps)
	t.Setenv(locus.Env, "")
	if err != nil {
		t.Fatalf("x402.Use:  %v", err)
	}

	// Priced routes behind the Enforce middleware. /paid/free stays unpriced.
	paid := app.Group("/paid", Enforce())
	served := func(c *zip.Ctx) error { return c.JSON(200, map[string]any{"served": true}) }
	paid.Raw(http.MethodGet, "/tool", served)
	paid.Raw(http.MethodGet, "/other", served)
	paid.Raw(http.MethodGet, "/free", served)

	h := &harness{t: t, app: app, fin: ledger, ledger: ledger, plane: pl,
		payerOrg: "payerorg", payeeOrg: "payeeorg"}
	h.payerKey, _ = crypto.GenerateKey()
	h.recipient = h.createRecipient(h.payeeOrg)
	if _, err := ledger.FinanceClient.Deposit(context.Background(), depositUSD(h.payerOrg, h.payerOrg, funded, "fund")); err != nil {
		t.Fatalf("fund payer: %v", err)
	}
	return h
}

// createRecipient provisions a KMS wallet in org and returns its id — the payout
// wallet a marketplace listing would name.
func (h *harness) createRecipient(org string) string {
	h.t.Helper()
	_, b, _ := h.req(http.MethodPost, "/v1/wallet/accounts", org, "", `{"name":"payee"}`)
	var acct struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(b, &acct); err != nil || acct.ID == "" {
		h.t.Fatalf("create account: %v (%s)", err, b)
	}
	body := `{"accountId":"` + acct.ID + `","name":"earnings","custody":"kms","tier":"hot","chain":"eip155:36963"}`
	code, b, _ := h.req(http.MethodPost, "/v1/wallet", org, "", body)
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
		hr.Header.Set(protocol.HeaderPaymentSignature, payment)
	}
	resp, err := h.app.Test(hr, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
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
func (h *harness) challenge(path, org string) protocol.PaymentRequirements {
	h.t.Helper()
	code, b, hdr := h.req(http.MethodGet, path, org, "", "")
	if code != http.StatusPaymentRequired {
		h.t.Fatalf("challenge %s = %d (%s), want 402", path, code, b)
	}
	raw := hdr.Get(protocol.HeaderPaymentRequired)
	if raw == "" {
		h.t.Fatalf("402 carried no %s header — a challenge a client cannot read is not a challenge",
			protocol.HeaderPaymentRequired)
	}
	var req protocol.PaymentRequired
	if err := protocol.DecodeHeader(raw, &req); err != nil {
		h.t.Fatalf("decode %s: %v", protocol.HeaderPaymentRequired, err)
	}
	if req.X402Version != protocol.Version {
		h.t.Fatalf("challenge x402Version = %d, want %d", req.X402Version, protocol.Version)
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
func (h *harness) pay(req protocol.PaymentRequirements, nonce string, validAfter, validBefore int64) string {
	h.t.Helper()
	return encode(signPayment(h.t, h.payerKey, req, nonce, validAfter, validBefore))
}

// payFor signs for req with the given nonce over a live window and retries.
func (h *harness) payFor(path, org string, req protocol.PaymentRequirements, nonce string) (int, []byte, http.Header) {
	h.t.Helper()
	now := nowUnix()
	return h.req(http.MethodGet, path, org, h.pay(req, nonce, now-60, now+300), "")
}

// settlement decodes the PAYMENT-RESPONSE header an answered request carries.
func (h *harness) settlement(hdr http.Header) protocol.SettlementResponse {
	h.t.Helper()
	raw := hdr.Get(protocol.HeaderPaymentResponse)
	if raw == "" {
		h.t.Fatalf("no %s header on the response", protocol.HeaderPaymentResponse)
	}
	var s protocol.SettlementResponse
	if err := protocol.DecodeHeader(raw, &s); err != nil {
		h.t.Fatalf("decode %s: %v", protocol.HeaderPaymentResponse, err)
	}
	return s
}

func randNonce() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return "0x" + hex.EncodeToString(b)
}

func pubRegistry(m map[string]Terms) { Publish(fakeReg{m: m}) }

func depositUSD(org, subject string, amount money.Amount, ref string) money.DepositInput {
	return money.DepositInput{Org: org, Subject: subject, Amount: amount, Currency: "usd", Ref: ref}
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
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		h := newHarness(t)
		pubRegistry(map[string]Terms{
			"/paid/tool": {Amount: money.FromCents(100), RecipientOrg: h.payeeOrg, RecipientWalletID: h.recipient},
		})

		req := h.challenge("/paid/tool", h.payerOrg)
		if req.Amount != "1000000" { // $1.00 → USDC 6-dp atomic units
			t.Fatalf("challenge amount = %q, want 1000000", req.Amount)
		}
		if req.Scheme != protocol.SchemeExact {
			t.Fatalf("challenge scheme = %q, want %q", req.Scheme, protocol.SchemeExact)
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
	})
}

// A settled nonce is spent: reusing it for DIFFERENT terms is a replay and is
// refused, and the metering spine is NOT hit a second time.
func TestNonceReplayRejected(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
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
	})
}

// Re-submitting the SAME authorization (a client retry) is idempotent: served
// again, but settled — and metered — exactly once.
func TestSettleOnceOnRetry(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		h := newHarness(t)
		pubRegistry(map[string]Terms{
			"/paid/tool": {Amount: money.FromCents(100), RecipientOrg: h.payeeOrg, RecipientWalletID: h.recipient},
		})
		req := h.challenge("/paid/tool", h.payerOrg)
		now := nowUnix()
		payment := h.pay(req, randNonce(), now-60, now+300)

		var firstID string
		for i := range 3 {
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
	})
}

// An unpriced resource passes straight through — no 402, no charge.
func TestFreeResourcePassthrough(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		h := newHarness(t)
		pubRegistry(map[string]Terms{
			"/paid/tool": {Amount: money.FromCents(100), RecipientOrg: h.payeeOrg, RecipientWalletID: h.recipient},
		})
		code, b, _ := h.req(http.MethodGet, "/paid/free", h.payerOrg, "", "")
		if code != 200 || !bytes.Contains(b, []byte(`"served":true`)) {
			t.Fatalf("free resource = %d (%s), want 200 served", code, b)
		}
		h.tie(money.Zero(), "after a free request")
	})
}

// A priced resource requires a validated payer (ledger settlement debits an org).
func TestPricedResourceRequiresPayer(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		h := newHarness(t)
		pubRegistry(map[string]Terms{
			"/paid/tool": {Amount: money.FromCents(100), RecipientOrg: h.payeeOrg, RecipientWalletID: h.recipient},
		})
		code, _, _ := h.req(http.MethodGet, "/paid/tool", "", "", "") // no principal
		if code != http.StatusForbidden {
			t.Fatalf("unvalidated priced request = %d, want 403", code)
		}
	})
}

// A support session — a SuperAdmin signed in AS the person (cloud
// impersonation.go) — pays for nothing. The ledger draws the payer ORG whoever
// signed, so an authorization signed in the window is no consent of the person's:
// refused at the priced route, at the plane's settle and at a hold, with nothing
// debited or set aside. The same authorization is then the person's own to spend.
func TestASupportSessionPaysForNothing(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		h := newHarness(t)
		terms := Terms{Amount: money.FromCents(100), RecipientOrg: h.payeeOrg, RecipientWalletID: h.recipient}
		pubRegistry(map[string]Terms{"/paid/tool": terms, client.JobResource("j1"): terms})
		req := h.challenge("/paid/tool", h.payerOrg)
		now := nowUnix()
		payment := h.pay(req, randNonce(), now-60, now+300)

		// The priced route behind Enforce, with the principal the boundary mints for a
		// support session: the person's org and user, the operator in Act.
		route := zip.New(zip.Config{})
		route.Use(zip.Handler(func(c *zip.Ctx) error {
			principal.Mint(c, principal.Principal{Org: h.payerOrg, User: "u_" + h.payerOrg, Act: principal.Act{Sub: "u-op", Owner: "admin", Name: "z"}})
			return c.Next()
		}))
		route.Group("/paid", Enforce()).Raw(http.MethodGet, "/tool", func(c *zip.Ctx) error { return c.JSON(http.StatusOK, map[string]bool{"served": true}) })
		hr := httptest.NewRequest(http.MethodGet, "/paid/tool", nil)
		hr.Header.Set("X-Org-Id", h.payerOrg)
		hr.Header.Set("X-User-Id", "u_"+h.payerOrg)
		hr.Header.Set(protocol.HeaderPaymentSignature, payment)
		resp, err := route.Test(hr, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden || !bytes.Contains(b, []byte(reasonSupport)) {
			t.Fatalf("support session on a priced route = %d %s, want 403 %s", resp.StatusCode, b, reasonSupport)
		}

		r := rail{s: live}
		support := zip.WithCaller(context.Background(), zip.Caller{Org: h.payerOrg, User: "u_" + h.payerOrg, ActedBy: "u-op"})
		if out, err := r.planeSettle(support, &client.SettleIn{Resource: "/paid/tool", Payment: payment}); err != nil || out.OK || out.Code != reasonSupport {
			t.Fatalf("support session settle = %+v %v, want refused", out, err)
		}
		if out, err := r.planeAuthorize(support, &client.AuthorizationIn{Resource: client.JobResource("j1"), Payment: payment, Hold: true}); err != nil || out.OK || out.Code != reasonSupport {
			t.Fatalf("support session hold = %+v %v, want refused", out, err)
		}
		if out, _ := r.planeSettle(support, &client.SettleIn{Resource: "/paid/free"}); !out.OK || !out.Free {
			t.Fatalf("support session free call = %+v, want served", out)
		}
		h.tie(money.Zero(), "after a support session tried to pay")
		h.aside(money.Zero(), "after a support session tried to hold")

		if code, b, _ := h.req(http.MethodGet, "/paid/tool", h.payerOrg, payment, ""); code != http.StatusOK {
			t.Fatalf("the person's own payment = %d %s, want served", code, b)
		}
		h.tie(money.FromCents(100), "after the person paid")
	})
}

// End-to-end LEDGER settlement, stated as balances: a paid request debits the payer's
// org through the metering spine AND credits the recipient wallet's ledger — once.
// A retry moves no more money.
func TestSettlesToRecipientLedger(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
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
	})
}

// A settlement moves BOTH sides or neither. With no ledger to credit the payee, the
// payer must not be charged and the resource must not be served — the alternative is
// money debited into nothing, or (with the credit alone) money minted from a missing
// dependency.
func TestSettlementRefusesHalfMove(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
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
	})
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
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
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
	})
}

// The claim is durable BEFORE the money moves, which is what makes an interrupted
// settlement recoverable at all — and it must not, on its own, look like a paid
// settlement. A claim that served the resource would be a free lunch.
func TestAClaimIsNotAReceipt(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
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
		pending, err := live.State.store.pending(context.Background(), nowUnix())
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
	})
}
