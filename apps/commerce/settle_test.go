// Copyright © 2026 Hanzo AI. MIT License.

package commerce

// settle_test.go — THE MONEY HALF of a settled card payment: does the balance a
// customer can actually SPEND go up, exactly once, in the right books?
//
// The defect these tests exist for shipped silently, and it could: commerce's card
// core credits its own transaction store, every spend gate and GET
// /v1/billing/balance read apps/finance, and no test in the fleet had ever asserted
// that the two were the same money. A charge settled, a receipt came back, a row was
// written, and the number that decides whether an inference request is served never
// moved.
//
// So every assertion here is on a BALANCE READ THROUGH A REAL READER — the ledger the
// gate reads and the customer's own /v1/billing/balance route — never on the
// endpoint's answer, which was the thing that always looked fine.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/account"
	commercemod "github.com/hanzoai/commerce"
	"github.com/hanzoai/commerce/datastore"
	"github.com/hanzoai/commerce/models/transaction"
	"github.com/hanzoai/commerce/models/types/currency"
	commerceorg "github.com/hanzoai/commerce/pkg/org"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/finance"
	"github.com/hanzoai/cloud/money"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/plane"
)

// gateCents is what [gateBody] asks to charge, as a number. The body states it as
// JSON and the fixtures state it as the amount that SETTLED, and the two have to be
// the same figure or a test would prove the credit works while sizing it from the
// wrong place.
const gateCents int64 = 4200

// funded publishes a throwaway finance ledger — the ONE spendable ledger, in its own
// temp directory — for the duration of one test, and hands it back so the test can
// read the balance a credit produced. Every endpoint fixture in this package uses it,
// because a settled top-up now DEPOSITS, and an endpoint with no ledger behind it is
// an endpoint that refuses.
func funded(t *testing.T) money.FinanceClient {
	t.Helper()
	fin := finance.New(finance.Local(t.TempDir(), testMaster))
	finance.Publish(fin)
	t.Cleanup(func() { finance.Publish(nil) })
	return fin
}

// quiet substitutes the risk plane's teaching client and DRAINS it before the test tears
// its fixture down.
//
// The settlement's RECORD is detached by design — [screen.learn] spawns a goroutine
// that outlives the request — while every endpoint fixture in this package unbinds the
// plane in its own cleanup. Those two race: plane.Unbind on the test goroutine against
// plane.Bind inside the detached one. It is not a property of this change (main reports
// the same race from TestPayments_ANotDeployedScorerDoesNotCloseTheTypedEndpoint under
// -race, on a fixture that predates it), and it is not what these tests are about — the
// money is. So they take the plane out of the picture and WAIT for the goroutine.
//
// Call it AFTER the app fixture: cleanups run last-registered-first, so the drain has
// to be registered after the Unbind it must precede.
func quiet(t *testing.T) {
	t.Helper()
	mute(t)
	prior := teach
	teach = func(context.Context, *client.RiskObserveIn) (*client.RiskObserved, error) {
		return &client.RiskObserved{Learned: 1}, nil
	}
	t.Cleanup(func() {
		if !released(3 * time.Second) {
			t.Errorf("%d detached hand-off(s) never came back — a settlement's record leaked", inflight())
		}
		teach = prior
	})
}

// stating makes a screen read a settlement the TEST states rather than one a commerce
// datastore holds. It is the [teach] pattern applied to the other client: what a payment
// actually settled for is a fact about a row in another module's store, and an endpoint
// test has no business booting one to assert where the money went.
func stating(s screen, got settlement) screen {
	s.receipt = func(context.Context, string, string) (settlement, error) { return got, nil }
	return s
}

// refusing makes the receipt read FAIL, which is the state an endpoint must never
// credit from: it settled a charge and cannot learn what settled.
func refusing(s screen, err error) screen {
	s.receipt = func(context.Context, string, string) (settlement, error) { return settlement{}, err }
	return s
}

// asked is the (org, id) an endpoint LOOKED THE RECEIPT UP UNDER — the two arguments
// every other fixture in this file discards.
//
// Discarding them is what let an endpoint read the right amount out of the wrong
// namespace for a whole green suite: [stating] answers a settlement whatever it is
// asked, so a test driving a caller whose receipt lives in one org and whose balance
// lives in another still saw the credit land. What the read is KEYED ON is a
// property, and a client that cannot state it is a client that cannot hold it.
type asked struct {
	org string
	id  string
}

// capturing is [stating] that also RECORDS the lookup, so a test can assert the address
// the production read would have used.
func capturing(s screen, got settlement, seen *asked) screen {
	s.receipt = func(_ context.Context, org, id string) (settlement, error) {
		*seen = asked{org: org, id: id}
		return got, nil
	}
	return s
}

// settling is the credit endpoint's screen with the whole money path behind it: a
// throwaway ledger published for the test, and a receipt read stating the $42 the
// endpoint's own body asks for. It is what every endpoint fixture in this package
// resolves its screen through.
func settling(t *testing.T) screen {
	t.Helper()
	funded(t)
	return stating(riskGate(luxlog.New("settletest")), settlement{cents: gateCents, currency: "usd"})
}

// wallet is the address the SPEND GATE will debit for an org and the fixtures' user —
// principal.WalletOf's answer for those headers, spelled through the one rule
// (account.Payer) rather than through a literal, so a change to the payer rule moves
// these tests with it instead of leaving them green against a stale key.
func wallet(org string) string {
	return account.Payer(account.Credential{Owner: org, Name: gateUser}).Subject()
}

// held reads the balance the AI gate reads, for ONE org's own wallet: apps/metering's
// Balance is fin.Balance(ctx, org, subject, currency, test), so this IS the gate's read
// and not a paraphrase of it.
func held(t *testing.T, fin money.FinanceClient, org string, test bool) int64 {
	t.Helper()
	bal, err := fin.Balance(context.Background(), org, wallet(org), "usd", test)
	if err != nil {
		t.Fatalf("read the wallet %s/%s: %v", org, wallet(org), err)
	}
	return bal.Cents()
}

// spendable is [held] for the org every endpoint fixture in this file pays from.
func spendable(t *testing.T, fin money.FinanceClient, test bool) int64 {
	t.Helper()
	return held(t, fin, gateOrg, test)
}

// adminOrg is the reserved platform org — the `owner` claim a SuperAdmin carries while
// its X-Org-Id is whichever org it is acting in.
const adminOrg = "admin"

// callers are the three identities a credit endpoint can see, and the TWO organisations
// each one names. Two tests range over this one table because they are two halves of a
// single property — where the receipt is READ and where the credit may be MINTED — and a
// caller list that drifted between them would leave one half asserted on a population
// the other half never sees.
//
// `charged` and `ledger` are the same string for the first two and different for the
// third, which is the whole point: a masquerading SuperAdmin is the ONLY caller that
// separates them, so it is the only row either property can be wrong on.
var callers = []struct {
	name string
	hdr  map[string]string
	// charged is the namespace the money core WROTE the receipt in, and therefore the one
	// the read must be keyed on.
	charged string
	// ledger is the org whose spendable balance the credit belongs to.
	ledger string
}{
	{
		"an ordinary member charges and is credited in the one org it has",
		map[string]string{"X-Org-Id": gateOrg, "X-User-Id": gateUser},
		gateOrg, gateOrg,
	},
	{
		"a SuperAdmin at home is that same one org",
		map[string]string{
			"X-Org-Id": adminOrg, "X-User-Id": gateUser,
			"X-User-Owner": adminOrg, "X-User-IsAdmin": "true",
		},
		adminOrg, adminOrg,
	},
	{
		"a SuperAdmin masquerading charges where it is ACTING and pays from where it lives",
		map[string]string{
			"X-Org-Id": gateOrg, "X-User-Id": gateUser,
			"X-User-Owner": adminOrg, "X-User-IsAdmin": "true",
		},
		gateOrg, adminOrg,
	},
}

// TestSettle_AMintThatCannotLandIsRefusedBeforeTheCardIsCharged — the money half, and
// the ORDER that makes it a control rather than a report.
//
// Naming the two organisations separately is right for every READ and wrong for the MINT.
// A card top-up is charged on the EFFECTIVE org's merchant account and its receipt is
// written in that org's books, while the credit is addressed to principal.WalletOf — and
// for a masquerading SuperAdmin those are two different organisations. The deposit went to
// the second of them: the customer's card cleared, spendable credit appeared in the
// platform admin's own wallet, and the endpoint answered 200. That is money moved between
// two customers' books on nothing but a masqueraded session.
//
// NEITHER ADDRESS IS RIGHT, so the mint refuses rather than choosing. Crediting the ledger
// is the bug above; crediting the charged org has an admin's card top up the customer it is
// only inspecting, which is a different surprise and equally unasked-for. A SuperAdmin who
// means to fund a customer has the admin grant, an endpoint whose whole subject is whose
// money it is — so the refusal names it.
//
// AND THE REFUSAL IS WORTH NOTHING BEHIND THE CHARGE, which is what this asserts. The first
// reading of the rule lived at the ledger boundary, which runs AFTER the handler: the
// masquerading admin's top-up cleared on the CUSTOMER's merchant account, then found there
// was nowhere to land it, and answered 500. Real money taken, permanently uncreditable, and
// takeable again on the next attempt because a fresh idempotency key is a fresh card
// authorisation. A status code cannot see the difference between that and a refusal that
// cost nobody anything — both are a non-2xx — so the assertion is the CHARGE COUNT.
//
// The two org == ledger callers must be untouched by it, and they are asserted here rather
// than assumed: the rule can only fire where the names differ, so a mistake in it shows up
// as an ordinary customer's top-up refused and their card never taken.
//
// Mutation proof: move the [payment.diverged] refusal out of [screen.decide] and leave
// only the boundary's copy — the shipped ordering — and the masquerade row fails on the
// COUNT (the handler runs once, the card is charged, the answer is a 500 over real money).
// Refuse when the names are EQUAL instead and both other rows fail on the count, the
// status and the balance, which is the no-regression half.
func TestSettle_AMintThatCannotLandIsRefusedBeforeTheCardIsCharged(t *testing.T) {
	for _, tc := range callers {
		t.Run(tc.name, func(t *testing.T) {
			fin := funded(t)
			var taken int
			app := creditEndpointHandler(t,
				stating(riskGate(luxlog.New("settletest")), settlement{cents: gateCents, currency: "usd"}),
				charging(settledBody(http.StatusOK,
					`{"transactionId":"`+settledReceipt+`","status":"ok","processorRef":"`+settledRef+`"}`), &taken))

			code, body := topupAs(t, app, tc.hdr)

			if tc.charged != tc.ledger {
				// THE CARD WAS NEVER TAKEN. Everything else in this branch is a
				// consequence; this is the property.
				if taken != 0 {
					t.Errorf("the charge handler ran %d time(s) for a payment that can never be credited, "+
						"want 0 — the customer's card was charged on %q's merchant account for a top-up "+
						"this process then refused, so the money is real, the credit is impossible, and "+
						"the next attempt charges it again", taken, tc.charged)
				}
				if code != http.StatusConflict {
					t.Errorf("a top-up charged in %q against a wallet in %q answered %d %s, want 409 — "+
						"a mint whose charge and credit are on different books cannot be silent, and it "+
						"is not a risk verdict either", tc.charged, tc.ledger, code, body)
				}
				if !strings.Contains(body, "/v1/admin/grants") {
					t.Errorf("the refusal (%s) does not name the endpoint that CAN fund another organisation — "+
						"an operator told only 'no' has nowhere to go", body)
				}
				// AND NO WALLET MOVED, in EITHER organisation. A refusal that still credited
				// somebody would be the defect wearing a 409.
				if got := held(t, fin, tc.ledger, false); got != 0 {
					t.Errorf("the SuperAdmin's own wallet holds %d cents, want 0 — a customer's card "+
						"funded a platform admin's balance", got)
				}
				if got := held(t, fin, tc.charged, false); got != 0 {
					t.Errorf("the org being acted in holds %d cents, want 0 — a masqueraded session "+
						"topped up the customer it was inspecting", got)
				}
				return
			}

			// org == ledger: the ordinary path, and it must be exactly as it was.
			if taken != 1 {
				t.Fatalf("the charge handler ran %d time(s) for a caller that pays for itself, want 1 — "+
					"the pre-charge refusal is shutting out ordinary customers", taken)
			}
			if code != http.StatusOK {
				t.Fatalf("a top-up whose charge and credit are the one org answered %d %s, want 200 — "+
					"the mint guard is refusing a caller that pays for itself", code, body)
			}
			// The credit itself is commerce's, posted before it answers (cutover_test.go).
		})
	}
}

// TestSettle_APlanSaleAnswersItsReceiptAndDepositsNothing — a card sale buys a plan,
// not balance.
//
// The subscription commerce opened is what the buyer paid for, and a sale writes no
// top-up receipt: the read answers "payment not found" for a subscription id, as
// [receiptOf] does. So the sale composition answers its receipt and the spendable
// ledger does not move, while the same composition stated as a credit refuses.
//
// Mutation proof: drop the `credit` branch in [screened] and the first half answers
// the reconcile error over a charge that settled.
func TestSettle_APlanSaleAnswersItsReceiptAndDepositsNothing(t *testing.T) {
	fin := funded(t)
	quiet(t)
	t.Cleanup(func() { cloud.SetRiskScorer(nil) })
	cloud.SetRiskScorer(func(context.Context, string, cloud.RiskQuery) (cloud.RiskVerdict, error) {
		return cloud.RiskVerdict{Action: cloud.ActionAllow}, nil
	})
	s := refusing(riskGate(luxlog.New("saletest")), errors.New("payment not found"))
	sold := &client.Sold{Sale: &client.Sale{
		SubscriptionID: "sub_dev", PlanID: "dev", AmountCents: 1900, Currency: "usd", Status: "ok",
	}}
	core := func(context.Context, *client.SaleIn) (*client.Sold, error) { return sold, nil }
	worth := func(_ context.Context, in *client.SaleIn) (int64, string) { return 0, in.Currency }
	receipt := func(_ *client.SaleIn, out *client.Sold) cleared {
		if out.Sale == nil {
			return cleared{}
		}
		return cleared{ref: out.Sale.SubscriptionID, id: out.Sale.SubscriptionID, cents: out.Sale.AmountCents, currency: out.Sale.Currency}
	}

	subject := func(in *client.SaleIn) string { return in.Subject }
	// The buyer the billing endpoint forwards over the plane: a validated caller and the
	// wallet it resolved for them. A sale that names nobody is refused before the core.
	buyer := zip.WithCaller(context.Background(), zip.Caller{Org: gateOrg, User: gateUser})
	in := &client.SaleIn{PlanID: "dev", Subject: wallet(gateOrg)}

	out, err := screened(s, "/billing/subscribe", false, worth, subject, receipt, nil, core)(buyer, in)
	if err != nil {
		t.Fatalf("a settled plan sale answered %v — the card was charged and the buyer is told it failed", err)
	}
	if out == nil || out.Sale == nil || out.Sale.SubscriptionID != "sub_dev" {
		t.Fatalf("the sale answered %+v, want the receipt for sub_dev", out)
	}
	if got := spendable(t, fin, false); got != 0 {
		t.Errorf("a plan sale moved %d cents into the spendable ledger", got)
	}
}

// settled is the answer commerce's core gives for a charge that cleared.
const settled = `{"transactionId":"` + settledReceipt + `","status":"ok","processorRef":"` + settledRef + `"}`

// refusals is EVERY way a settled charge can fail to credit, in one table.
//
// It is one table because three properties are asserted over it — the endpoint never
// answers 200, no wallet moves, and the refusal says whether a retry can clear it — and
// a row list that drifted between them would leave one property asserted on a population
// another never sees. That is the same reason [callers] is shared.
var refusals = []refusal{
	{
		name:   "the receipt cannot be read, so the amount is unknown",
		body:   settled,
		screw:  func(s screen) screen { return refusing(s, errors.New("payment not found")) },
		ledger: true,
		// RETRYABLE: a read that failed is a read that may succeed. This process cannot
		// tell a datastore blip from a row that is not there, and claiming to know it is
		// terminal would page an operator for a payment about to credit itself.
		terminal: false,
	},
	{
		name:     "the charge settled in a currency this ledger does not hold",
		body:     settled,
		screw:    func(s screen) screen { return stating(s, settlement{cents: 500000, currency: "jpy"}) },
		ledger:   true,
		terminal: true, // the receipt is immutable: every retry reads the same yen.
	},
	{
		name:     "the receipt states no money",
		body:     settled,
		screw:    func(s screen) screen { return stating(s, settlement{cents: 0, currency: "usd"}) },
		ledger:   true,
		terminal: true,
	},
	{
		name:     "the answer names no settlement to key the credit on",
		body:     `{"status":"ok"}`,
		screw:    func(s screen) screen { return stating(s, settlement{cents: gateCents, currency: "usd"}) },
		ledger:   true,
		terminal: true, // the same core answers the same way, so there is never a key.
	},
	{
		name:   "the settlement's reference is already another payment's",
		body:   settled,
		screw:  func(s screen) screen { return stating(s, settlement{cents: gateCents, currency: "usd"}) },
		ledger: true,
		// finance refuses a ref posted for a different (subject, amount) rather than
		// answering with the other payment's entry — the one deposit failure that is a
		// fact about the books instead of about this attempt's luck.
		seed: func(t *testing.T, fin money.FinanceClient) {
			t.Helper()
			// Another wallet in the same books, so the conflict is the ref naming a
			// different payment and the pool this test reads is untouched by the seed.
			if _, err := fin.Deposit(context.Background(), money.DepositInput{
				Org: gateOrg, Subject: gateOrg + "/someone-else", Amount: money.FromCents(1),
				Currency: "usd", Ref: settledRef,
			}); err != nil {
				t.Fatalf("seed the conflicting posting: %v", err)
			}
		},
		terminal: true,
	},
	{
		name:   "there is no ledger in the process that owns it",
		body:   settled,
		screw:  func(s screen) screen { return stating(s, settlement{cents: gateCents, currency: "usd"}) },
		ledger: false,
		// RETRYABLE: an operator fixes the deployment and the customer's own retry lands.
		terminal: false,
	},
}

// refusal is ONE way a settled charge fails to credit.
type refusal struct {
	name string
	// body is what the charge handler answers.
	body string
	// screw is what is wrong with the settlement.
	screw func(screen) screen
	// ledger says whether a finance ledger is published at all.
	ledger bool
	// seed is money already in the books before the endpoint runs — the only way to reach
	// the deposit's own refusals, which need a real conflicting posting.
	seed func(t *testing.T, fin money.FinanceClient)
	// terminal says whether NO retry can ever clear this one.
	terminal bool
}

// refused puts the world in the state one row describes: the ledger (or deliberately
// none) and whatever is already posted in it.
func refused(t *testing.T, tc refusal) (money.FinanceClient, bool) {
	t.Helper()
	if !tc.ledger {
		finance.Publish(nil)
		return nil, false
	}
	fin := funded(t)
	if tc.seed != nil {
		tc.seed(t, fin)
	}
	return fin, true
}

// refusalEndpoint is the credit endpoint for one row, with the screen the caller states — the
// production one, or one whose log a test can read.
func refusalEndpoint(t *testing.T, tc refusal, s screen) *zip.App {
	t.Helper()
	return creditEndpointBody(t, tc.screw(s), tc.body)
}

// journal is a logger's output a test can READ, and it is mutex-guarded because the
// screen's logger is also held by the detached teaching goroutine ([screen.learn]):
// an unguarded buffer is a data race the -race build would find, on a fixture rather
// than on the code under test.
type journal struct {
	mu    sync.Mutex
	lines bytes.Buffer
}

func (j *journal) Write(p []byte) (int, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.lines.Write(p)
}

func (j *journal) String() string {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.lines.String()
}

// TestReceiptOf_ReadsTheRowTheMoneyCoreWROTE — the client's DEFAULT, against a real
// commerce datastore.
//
// Every other test in this file states the settlement, which is what lets them run
// without booting another module's store — and it is also what would let the production
// read rot unnoticed. This one drives [receiptOf] itself over a real embedded commerce:
// a real transaction.Deposit row in a real org namespace, read back through commerce's
// own published reader, and asserted field by field against what a credit needs.
func TestReceiptOf_ReadsTheRowTheMoneyCoreWROTE(t *testing.T) {
	emb, err := commercemod.Embed(context.Background(), commercemod.EmbedConfig{DataDir: t.TempDir(), Dev: true, Ledger: ledger{}})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	PublishEmbedded(emb)
	t.Cleanup(func() {
		PublishEmbedded(nil)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = emb.Stop(ctx)
	})

	ctx := context.Background()
	const org = "receiptco"
	o, err := commerceorg.Resolve(ctx, org)
	if err != nil {
		t.Fatalf("resolve org: %v", err)
	}
	// The row commerce's card core writes on a settled charge, field for field
	// (api/billing payment_core.go).
	tr := transaction.New(datastore.New(o.Namespaced(ctx)))
	tr.Type = transaction.Deposit
	tr.DestinationId = org
	tr.DestinationKind = "iam-user"
	tr.Currency = currency.USD
	tr.Amount = currency.Cents(gateCents)
	tr.Notes = "Top-up via square (ref: sq_pay_real)"
	tr.Tags = "topup"
	tr.Test = true
	tr.MustCreate()

	got, err := receiptOf(ctx, org, tr.Id())
	if err != nil {
		t.Fatalf("receiptOf: %v — the production read cannot see the row the money core wrote", err)
	}
	if got.cents != gateCents {
		t.Errorf("cents = %d, want %d", got.cents, gateCents)
	}
	if strings.ToLower(got.currency) != "usd" {
		t.Errorf("currency = %q, want usd", got.currency)
	}
	if !got.test {
		t.Error("test = false for a row written in the sandbox books — a sandbox charge would credit live money")
	}
	if got.memo != tr.Notes {
		t.Errorf("memo = %q, want %q", got.memo, tr.Notes)
	}

	// AND A RECEIPT FROM ANOTHER TENANT IS NOT FOUND, so a credit can never be sized by
	// a row outside the payer's own books.
	if _, err := receiptOf(ctx, "someoneelse", tr.Id()); err == nil {
		t.Error("a foreign org read the receipt — the read is not scoped to the payer's namespace")
	}
}

// ── fixtures ─────────────────────────────────────────────────────────────────

// allowAll is the scorer that judges nothing, for the tests whose subject is the money
// rather than the verdict.
func allowAll(context.Context, string, cloud.RiskQuery) (cloud.RiskVerdict, error) {
	return cloud.RiskVerdict{Action: cloud.ActionAllow}, nil
}

// creditEndpoint is the browser credit endpoint answering the way commerce's core answers a
// charge that cleared — the REAL TakePaymentOut field names, because the settlement is
// read out of exactly those.
func creditEndpoint(t *testing.T, s screen) *zip.App {
	t.Helper()
	return creditEndpointBody(t, s,
		`{"transactionId":"`+settledReceipt+`","balanceCents":4200,"status":"ok","processorRef":"`+settledRef+`"}`)
}

func creditEndpointBody(t *testing.T, s screen, body string) *zip.App {
	t.Helper()
	return creditEndpointHandler(t, s, settledBody(http.StatusOK, body))
}

// creditEndpointHandler is that same endpoint in front of a handler the TEST supplies — the
// form a test needs when what it asserts is not what the handler ANSWERED but whether it
// ran at all. [charging] is the handler it exists for.
func creditEndpointHandler(t *testing.T, s screen, h zip.Handler) *zip.App {
	t.Helper()
	shortRuntimeDir(t)
	plane.Unbind()
	t.Cleanup(plane.Unbind)
	t.Cleanup(func() { cloud.SetRiskScorer(nil) })
	cloud.SetRiskScorer(allowAll)
	quiet(t)
	app := zip.New(zip.Config{Logger: luxlog.New("settletest"), DisableStartupMessage: true})
	app.Raw(http.MethodPost, "/v1/billing/topup/token", s.route(h))
	return app
}

// charging is the charge handler with a COUNT of how many times the card was taken.
//
// The count is the only thing that can tell a refusal apart from a refusal that happened
// AFTER the money moved, and the two are the whole difference this change is about. At
// the wire they are identical: the shipped ordering answered 500 on a charge that had
// really cleared on the customer's merchant account, which reads exactly like a 500 on a
// charge that never happened. Reaching the handler IS the card being taken (the fixtures'
// handler answers commerce's own settled receipt), so counting the calls counts the
// charges.
func charging(h zip.Handler, taken *int) zip.Handler {
	return func(c *zip.Ctx) error {
		*taken++
		return h(c)
	}
}

// reportedBalance drives the REAL GET /v1/billing/balance handler as the fixtures'
// caller and returns the `available` cents it answers — the customer's own view of the
// same money, resolved through apps/billing's own subject rule rather than through
// this test's.
func reportedBalance(t *testing.T, app *zip.App) int64 {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/billing/balance", nil)
	req.Header.Set("X-Org-Id", gateOrg)
	req.Header.Set("X-User-Id", gateUser)
	resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("GET /v1/billing/balance: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /v1/billing/balance: %d (%s)", resp.StatusCode, b)
	}
	var out struct {
		Available int64  `json:"available"`
		Account   string `json:"account"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("decode balance: %v (%s)", err, b)
	}
	// The account the reader RESOLVED must be the wallet the credit landed in, or the
	// two agree on a number by luck.
	if subject := wallet(gateOrg); out.Account != subject {
		t.Errorf("/v1/billing/balance reports the account %q, want %q — the customer's page and the "+
			"credit are naming different wallets", out.Account, subject)
	}
	return out.Available
}
