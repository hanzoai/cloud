package cloud

// The prepaid loop over a REAL SOCKET, driven by a REAL SIGNED BEARER.
//
// prepaid_test.go proves the arithmetic closes with the identity handed in as
// headers. That leaves the most valuable question unasked: those headers are the
// GATEWAY's output, and a paying stranger does not send them — they send a token.
// Between the token and the debit sit the two steps that decide WHOSE money
// moves, and a test that starts after them cannot see either:
//
//	signature -> claims -> minted identity headers   (SanitizeIdentity)
//	minted identity      -> wallet address           (principal.WalletOf)
//
// So this test starts one step earlier and one layer lower. Nothing is handed in:
// there is an RSA key, a JWKS endpoint the validator really fetches, an RS256
// token really verified against it, a real TCP listener, a real net/http client
// that opens real connections, and the real per-org SQLite double-entry ledger.
// The only thing the test asserts about the address is that it is the one it
// pinned — everything else is arithmetic the binary does on its own.
//
// What it pins, in order:
//
//	the ADDRESS       — a signed token resolves to exactly one wallet, written out.
//	the SPEND         — four calls, four debits of the DECLARED price, over TCP.
//	the REFUSAL       — the fifth is 402 insufficient_balance and does NOT run.
//	the BOOKS         — five movements, five entries, each balanced to zero.
//	the CONTROLS      — an unpriced surface and a READ both move nothing.
//	the ISOLATION     — another org's valid token cannot spend this org's balance.
//
// The controls are not decoration. Every "the meter works" claim in this repo's
// history was true of the priced path and false of everything beside it; a debit
// that fires on paths it should not is the same defect as one that never fires,
// and only a control catches it.

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanzoai/cloud/finance"
	"github.com/hanzoai/cloud/money"
	"github.com/hanzoai/cloud/sdk/geo"
	"github.com/hanzoai/cloud/sdk/meter"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/account"
	"github.com/hanzoai/authz"
	"github.com/valyala/fasthttp"
	"github.com/zap-proto/zip"
)

// The two surfaces under test. One is priced, one is not, and the second exists
// only so the first can be shown to be the reason money moved.
const (
	e2ePricedSurface = "probe"
	e2ePricedPath    = "/v1/probe/run"
	e2ePricedRead    = "/v1/probe/list"
	e2eFreeSurface   = "audit"
	e2eFreePath      = "/v1/audit/write"
	e2ePrice         = 25
	e2eTopUp         = 100
)

// payer is one way a signed token becomes an address, and the address it must
// become. Both halves are WRITTEN OUT, not derived, because the address IS the
// fact under test: a gate that reads one address while the debit writes another
// is the bug this codebase has shipped three times (sdk/meter/wallet.go
// names them), and computing the expectation with the same helper the gate uses
// would hide precisely that.
type payer struct {
	name    string   // the subtest's name
	owner   string   // the token's home org
	user    string   // the token's username claim
	orgs    []string // the signed membership set, when not just the home
	ledger  string   // which org's books hold the wallet
	account string   // which wallet within them
}

// The two addressing modes the fleet actually has, both exercised. They are not
// variants of one rule — account.Payer branches on whether the home org is THE
// shared signup org — and the difference decides whose money a request spends:
//
//	a real tenant org POOLS: every member spends the company's one balance.
//	the shared signup org does NOT: a stranger who self-serves has their own
//	wallet, because a pooled balance there would be every other stranger's.
//
// Pinning only one of them is how this bug came back twice: each time, the layer
// that was tested agreed with itself and the other addressing mode was wrong.
var e2ePayers = []payer{
	{name: "tenant org pools", owner: "acme", user: "stranger", ledger: "acme", account: "acme"},
	// A brand's customer lives in its directory and pays from the org it acts in,
	// its own; a plain member of the brand org itself acts in nothing, so no
	// stranger's spend can land on the platform's books.
	{name: "directory customer pays from its own org", owner: "hanzo-id", user: "stranger", orgs: []string{"hanzo-id", "stranger"}, ledger: "stranger", account: "stranger"},
}

// e2eServer serves app on a real loopback TCP listener and returns its base URL.
// fasthttp, not httptest: this is the same handler the binary serves, reached the
// same way, so nothing about the transport is simulated.
func e2eServer(t *testing.T, app *zip.App) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = fasthttp.Serve(ln, app.Fiber().Handler()) }()
	t.Cleanup(func() { _ = ln.Close() })
	return "http://" + ln.Addr().String()
}

// e2eCall opens a real connection and returns status + body.
func e2eCall(t *testing.T, base, method, path, tok string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, base+path, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	res, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	return res.StatusCode, body
}

// e2eToken mints a real RS256 bearer for one person in one org.
func e2eToken(t *testing.T, key *rsa.PrivateKey, owner, name string, orgs ...string) string {
	t.Helper()
	c := tokenClaims("hanzo-console", owner, name+"@"+owner+".io", false, time.Now().Add(time.Hour))
	c.Name = name
	if len(orgs) > 0 {
		// home first, as IAM signs it; every org after it the person founded.
		c.Orgs = []authz.Membership{{Org: orgs[0], Role: authz.Member}}
		for _, o := range orgs[1:] {
			c.Orgs = append(c.Orgs, authz.Membership{Org: o, Role: authz.Owner})
		}
	}
	return signWith(t, key, c)
}

// e2eLedger publishes a real finance ledger on the process-wide money client.
func e2eLedger(t *testing.T) *ledgerReader {
	t.Helper()
	fin := finance.New(finance.Local(t.TempDir(), testMaster))
	prev := finance.Current()
	finance.Publish(fin)
	t.Cleanup(func() { finance.Publish(prev) })
	return &ledgerReader{fin}
}

// e2eBalance reads the wallet under test.
func e2eBalance(t *testing.T, l *ledgerReader, p payer) int64 {
	t.Helper()
	bal, err := l.fin.Balance(context.Background(), p.ledger, p.account, "usd", false)
	if err != nil {
		t.Fatalf("read balance: %v", err)
	}
	return bal.Cents()
}

// e2eSettled waits for the detached debit to land. BillingGate records on its own
// goroutine on purpose — a client disconnect must not cancel a debit — so waiting
// is the only honest way to observe the new balance.
func e2eSettled(t *testing.T, l *ledgerReader, p payer, want int64) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if got := e2eBalance(t, l, p); got == want {
			return
		} else if time.Now().After(deadline) {
			t.Fatalf("balance settled at %d¢, want %d¢", got, want)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// TestMeterOverTheWire proves the client the fleet's money really moves
// through. BillingGate (above) charges what a surface DECLARES at the edge, and
// no surface in the fleet declares a positive price — so today it charges nobody.
// Every product that CAN be billed bills through Meter instead: gate
// before the work, debit after it, inside the handler. That is 21 surfaces, and
// until this test the pair had only ever been proven against a FAKE commerce over
// HTTP (resource_billing_test.go) — which in the unified binary is not the path
// taken at all, because Record short-circuits to the co-resident ledger before it
// considers a socket.
//
// So: real bearer, real socket, real Meter, real double-entry ledger, and
// the canonical call pair copied from apps/functions — the shape every metered
// handler in the fleet uses.
func TestMeterOverTheWire(t *testing.T) {
	for _, p := range e2ePayers {
		t.Run(p.name, func(t *testing.T) { resourceMeterOverTheWire(t, p) })
	}
}

func resourceMeterOverTheWire(t *testing.T, p payer) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	jwks := jwksServer(t, &key.PublicKey)
	led := e2eLedger(t)

	const kind = "run"
	// Price this unit explicitly rather than inheriting the platform default, so
	// the arithmetic below is on a number this test states.
	t.Setenv("CLOUD_E2E_FEE_CENTS", "25")
	fee := ResourceFeeCents("CLOUD_E2E_FEE_CENTS", kind)
	if fee != e2ePrice {
		t.Fatalf("fee knob resolves to %d¢, want %d¢", fee, e2ePrice)
	}

	mc := mustClient(t, forbiddenCommerce(t))
	bill := NewMeter(Deps{Metering: mc}, "e2e")
	if !mc.Enabled() {
		t.Fatal("the client reports no ledger in this process — it would ask a peer, and " +
			"this test would prove nothing about the co-resident path")
	}

	var served atomic.Int64
	app := zip.New(zip.Config{})
	app.Use(SanitizeIdentity(newIdentityValidator(testIssuer, jwks.URL, 0)))
	// The canonical metered handler, verbatim in shape from apps/functions/invoke.go.
	app.Raw(http.MethodPost, e2ePricedPath, func(c *zip.Ctx) error {
		project, validated := principal.ValidatedProject(c)
		if err := bill.Authorize(c.Context(), meter.Payer(c), project, validated, kind, fee); err != nil {
			return DenyResource(c, err)
		}
		served.Add(1)
		bill.Record(meter.Payer(c), kind, meter.Usage{
			Model:       kind,
			AmountCents: fee,
			Project:     principal.Project(c),
			RequestID:   c.RequestID(),
			ClientIP:    geo.IP(c),
		})
		return c.JSON(http.StatusOK, map[string]string{"ok": "served"})
	})
	base := e2eServer(t, app)
	tok := e2eToken(t, key, p.owner, p.user, p.orgs...)

	// ── the address ─────────────────────────────────────────────────────────────
	// Meter now addresses money by the resolved ACCOUNT — Usage.User is its
	// Subject() and Usage.Org its Org() — so a handler still cannot bill someone else
	// and the two halves can no longer disagree. Fund BOTH candidate wallets and let
	// the binary say which one it moved: an assertion that only watched the one this
	// test expects would be blind to exactly the disagreement that has shipped three
	// times.
	pool := payer{ledger: p.ledger, account: p.ledger}
	for _, w := range []payer{pool, p} {
		if _, err := led.fin.Deposit(context.Background(), money.DepositInput{
			Org: w.ledger, Subject: w.account, Amount: money.FromCents(e2eTopUp), Ref: "topup-" + w.account,
		}); err != nil {
			t.Fatalf("top up %s: %v", w.account, err)
		}
	}

	code, body := e2eCall(t, base, http.MethodPost, e2ePricedPath, tok)
	if code != http.StatusOK {
		t.Fatalf("metered call: status = %d, want 200 (body=%s)", code, body)
	}
	e2eSettled(t, led, p, e2eTopUp-e2ePrice)

	// The gate and the debit must name ONE wallet, and it is the PAYER'S — the same
	// address principal.Payer resolves for the edge gate and the paywall. Where the
	// two coincide (a pooled tenant org) there is nothing to see; where they DIVERGE
	// (the shared signup org, where the payer is <org>/<user> and the pool is the bare
	// <org>) this says the platform's own books were NOT touched. It asserted the
	// opposite until the meter took an address instead of one overloaded string: a
	// self-serve customer's usage landed on Hanzo's pool while their own top-up sat
	// unspendable beside it.
	if p.account != pool.account {
		if got := e2eBalance(t, led, pool); got != e2eTopUp {
			t.Fatalf("the org pool moved to %d¢ — a self-serve caller's usage landed on the "+
				"platform's own books instead of their wallet %q", got, p.account)
		}
	}

	// ── spend it down, then refuse ──────────────────────────────────────────────
	for i := 2; i <= e2eTopUp/e2ePrice; i++ {
		if code, body := e2eCall(t, base, http.MethodPost, e2ePricedPath, tok); code != http.StatusOK {
			t.Fatalf("call %d: status = %d, want 200 (body=%s)", i, code, body)
		}
		e2eSettled(t, led, p, int64(e2eTopUp-i*e2ePrice))
	}

	ran := served.Load()
	code, body = e2eCall(t, base, http.MethodPost, e2ePricedPath, tok)
	if code != http.StatusPaymentRequired {
		t.Fatalf("exhausted call: status = %d, want 402 (body=%s)", code, body)
	}
	if served.Load() != ran {
		t.Fatal("the handler ran on an empty wallet — the gate is downstream of the work")
	}
	var refusal struct {
		Error struct{ Code, Message string } `json:"error"`
	}
	if err := json.Unmarshal(body, &refusal); err != nil {
		t.Fatalf("refusal body is not the money wire's shape: %v (body=%s)", err, body)
	}
	if refusal.Error.Code != "insufficient_balance" {
		t.Fatalf("refusal code = %q, want %q", refusal.Error.Code, "insufficient_balance")
	}

	// ── the books ───────────────────────────────────────────────────────────────
	entries, err := led.fin.ListEntries(context.Background(), pool.ledger, "", false, 0)
	if err != nil {
		t.Fatalf("read entries: %v", err)
	}
	deposits, debits := 0, 0
	for _, e := range entries {
		switch e.Kind {
		case finance.KindDeposit:
			deposits++
		case finance.KindUsage:
			debits++
			if e.Amount.Cents() != e2ePrice {
				t.Errorf("usage entry %s = %d¢, want the priced %d¢", e.ID, e.Amount.Cents(), e2ePrice)
			}
		default:
			t.Errorf("entry %s has kind %q — an entry nobody can classify must not be in the books", e.ID, e.Kind)
		}
	}
	wantDeposits := 1
	if p.account != pool.account {
		wantDeposits = 2 // both wallets were funded; both entries live in this org's file.
	}
	if want := e2eTopUp / e2ePrice; debits != want || deposits != wantDeposits {
		t.Fatalf("books hold %d deposits and %d debits, want %d and %d — one movement, one entry",
			deposits, debits, wantDeposits, want)
	}
}

// TestTheAddressCarriesBothHalves pins what the meter's address must answer,
// rather than leaving it as prose. There used to be a `booksOf` parse here that
// recovered the ledger from a payer STRING, and it existed so that adopting the
// person-scoped address could be a per-surface decision rather than a flag day.
// The meter takes an [account.Account] now, so the two halves travel together and
// the parse only happens at a genuine string boundary — a stored row, a wire field.
//
// Both claims that parse made are still load-bearing and are checked here: a bare
// org slug is that org's OWN account (an unmigrated caller is unchanged, byte for
// byte), and a person key names the org whose books hold it — without which a
// migrated caller would open a ledger file called "hanzo/stranger".
func TestTheAddressCarriesBothHalves(t *testing.T) {
	for _, tc := range []struct{ payer, books, subject string }{
		// A bare slug: the org's own pooled account.
		{"hanzo", "hanzo", "hanzo"},
		{"acme", "acme", "acme"},
		{"", "", ""},
		// A person key: the org holds the books, the person names the wallet.
		{"hanzo/stranger", "hanzo", "hanzo/stranger"},
		// A person key in a POOLED org collapses to the pool, and that is the rule
		// rather than a rounding: outside the signup org there is no member wallet to
		// hold money, so an amount credited to one could never be spent.
		{"acme/bob", "acme", "acme"},
	} {
		got := account.PayerOf("", tc.payer)
		if got.Org() != tc.books {
			t.Errorf("PayerOf(%q).Org() = %q, want %q", tc.payer, got.Org(), tc.books)
		}
		if got.Subject() != tc.subject {
			t.Errorf("PayerOf(%q).Subject() = %q, want %q", tc.payer, got.Subject(), tc.subject)
		}
	}
}

// TestASignupOrgStrangerSpendsNothing is where a stranger's spend used to land on
// the platform's books. A plain member of the signup org — a customer who landed in
// hanzo before the directory — is no tenant of it (authz.IsPlatformOrg), so a
// metered call carries no org: it is refused, nothing is served, and neither the
// platform's pool nor the person's old wallet moves.
func TestASignupOrgStrangerSpendsNothing(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	jwks := jwksServer(t, &key.PublicKey)
	led := e2eLedger(t)
	person := payer{ledger: "hanzo", account: "hanzo/stranger"}
	pool := payer{ledger: "hanzo", account: "hanzo"}

	t.Setenv("CLOUD_E2E_FEE_CENTS", "25")
	const kind = "run"
	fee := ResourceFeeCents("CLOUD_E2E_FEE_CENTS", kind)
	bill := NewMeter(Deps{
		Metering: mustClient(t, forbiddenCommerce(t)),
	}, "e2e")

	var served atomic.Int64
	app := zip.New(zip.Config{})
	app.Use(SanitizeIdentity(newIdentityValidator(testIssuer, jwks.URL, 0)))
	app.Raw(http.MethodPost, e2ePricedPath, func(c *zip.Ctx) error {
		project, validated := principal.ValidatedProject(c)
		if err := bill.Authorize(c.Context(), meter.Payer(c), project, validated, kind, fee); err != nil {
			return DenyResource(c, err)
		}
		served.Add(1)
		bill.Record(meter.Payer(c), kind, meter.Usage{Model: kind, AmountCents: fee, RequestID: c.RequestID()})
		return c.JSON(http.StatusOK, map[string]string{"ok": "served"})
	})
	base := e2eServer(t, app)

	if _, err := led.fin.Deposit(context.Background(), money.DepositInput{
		Org: person.ledger, Subject: person.account, Amount: money.FromCents(e2eTopUp), Ref: "topup-person",
	}); err != nil {
		t.Fatalf("top up: %v", err)
	}
	code, body := e2eCall(t, base, http.MethodPost, e2ePricedPath, e2eToken(t, key, "hanzo", "stranger"))
	if code == http.StatusOK || served.Load() != 0 {
		t.Fatalf("a plain member of the signup org was served: status = %d (body=%s)", code, body)
	}
	if got := e2eBalance(t, led, pool); got != 0 {
		t.Fatalf("the platform pool moved to %d¢", got)
	}
	if got := e2eBalance(t, led, person); got != e2eTopUp {
		t.Fatalf("the stranger's wallet moved to %d¢, want %d¢ untouched", got, e2eTopUp)
	}
}
