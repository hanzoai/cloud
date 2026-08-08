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
// gate reads and the customer's own /v1/billing/balance route — never on the door's
// answer, which was the thing that always looked fine.

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

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/finance"
	"github.com/hanzoai/cloud/money"
	"github.com/hanzoai/cloud/plane"
	"github.com/hanzoai/cloud/types"
)

// gateCents is what [gateBody] asks to charge, as a number. The body states it as
// JSON and the fixtures state it as the amount that SETTLED, and the two have to be
// the same figure or a test would prove the credit works while sizing it from the
// wrong place.
const gateCents int64 = 4200

// funded publishes a throwaway finance ledger — the ONE spendable ledger, in its own
// temp directory — for the duration of one test, and hands it back so the test can
// read the balance a credit produced. Every door fixture in this package uses it,
// because a settled top-up now DEPOSITS, and a door with no ledger behind it is a
// door that refuses.
func funded(t *testing.T) finance.Client {
	t.Helper()
	fin := finance.New(t.TempDir())
	finance.Publish(fin)
	t.Cleanup(func() { finance.Publish(nil) })
	return fin
}

// quiet substitutes the risk plane's teaching seam and DRAINS it before the test tears
// its fixture down.
//
// The settlement's RECORD is detached by design — [screen.learn] spawns a goroutine
// that outlives the request — while every door fixture in this package unbinds the
// plane in its own cleanup. Those two race: plane.Unbind on the test goroutine against
// plane.Bind inside the detached one. It is not a property of this change (main reports
// the same race from TestPayments_ANotDeployedScorerDoesNotCloseTheTypedDoor under
// -race, on a fixture that predates it), and it is not what these tests are about — the
// money is. So they take the plane out of the picture and WAIT for the goroutine.
//
// Call it AFTER the app fixture: cleanups run last-registered-first, so the drain has
// to be registered after the Unbind it must precede.
func quiet(t *testing.T) {
	t.Helper()
	mute(t)
	prior := teach
	teach = func(context.Context, *plane.RiskObserveIn) (*plane.RiskObserved, error) {
		return &plane.RiskObserved{Learned: 1}, nil
	}
	t.Cleanup(func() {
		if !released(3 * time.Second) {
			t.Errorf("%d detached hand-off(s) never came back — a settlement's record leaked", inflight())
		}
		teach = prior
	})
}

// stating makes a screen read a settlement the TEST states rather than one a commerce
// datastore holds. It is the [teach] pattern applied to the other seam: what a payment
// actually settled for is a fact about a row in another module's store, and a door test
// has no business booting one to assert where the money went.
func stating(s screen, got settlement) screen {
	s.receipt = func(context.Context, string, string) (settlement, error) { return got, nil }
	return s
}

// refusing makes the receipt read FAIL, which is the state a door must never credit
// from: it settled a charge and cannot learn what settled.
func refusing(s screen, err error) screen {
	s.receipt = func(context.Context, string, string) (settlement, error) { return settlement{}, err }
	return s
}

// asked is the (org, id) a door LOOKED THE RECEIPT UP UNDER — the two arguments every
// other fixture in this file discards.
//
// Discarding them is what let a door read the right amount out of the wrong namespace
// for a whole green suite: [stating] answers a settlement whatever it is asked, so a
// test driving a caller whose receipt lives in one org and whose balance lives in
// another still saw the credit land. What the read is KEYED ON is a property, and a
// seam that cannot state it is a seam that cannot hold it.
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

// settling is the credit door's screen with the whole money path behind it: a
// throwaway ledger published for the test, and a receipt read stating the $42 the
// door's own body asks for. It is what every door fixture in this package resolves
// its screen through.
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

// held reads the balance the AI gate reads, for ONE org's own wallet: build.go's
// balanceReader is fin.Balance(ctx, org, subject, currency, test) and apps/metering's
// fetchAvailable is the same call, so this IS the gate's read and not a paraphrase of it.
func held(t *testing.T, fin finance.Client, org string, test bool) int64 {
	t.Helper()
	bal, err := fin.Balance(context.Background(), org, wallet(org), "usd", test)
	if err != nil {
		t.Fatalf("read the wallet %s/%s: %v", org, wallet(org), err)
	}
	return bal.Cents()
}

// spendable is [held] for the org every door fixture in this file pays from.
func spendable(t *testing.T, fin finance.Client, test bool) int64 {
	t.Helper()
	return held(t, fin, gateOrg, test)
}

// TestSettle_ASettledTopUpFundsTheWalletTheSpendGateReads is THE BUG, in one test.
//
// A card top-up settled and the spendable balance stayed at zero, because commerce
// credited its own transaction store while every gate reads apps/finance. Three reads
// are asserted here and they are deliberately three DIFFERENT readers of the same
// money — the gate's own ledger call, the customer's own HTTP route, and a real debit
// — because the defect was precisely that one writer and several readers disagreed.
//
// Mutation proof: delete the s.settle call from [screen.route] and the first
// assertion reads 0 against a 200 — which is the shipped defect, exactly.
func TestSettle_ASettledTopUpFundsTheWalletTheSpendGateReads(t *testing.T) {
	fin := funded(t)
	app := creditDoor(t, stating(riskGate(luxlog.New("settletest")),
		settlement{cents: gateCents, currency: "usd"}))

	if code, body := topup(t, app); code != http.StatusOK {
		t.Fatalf("the top-up answered %d %s, want 200", code, body)
	}

	// THE GATE'S OWN READ.
	if got := spendable(t, fin, false); got != gateCents {
		t.Fatalf("the wallet the spend gate reads holds %d cents after a settled $42 top-up, want %d — "+
			"this is the shipped defect: the customer was charged and the balance that admits an "+
			"inference request never moved", got, gateCents)
	}

	// THE CUSTOMER'S OWN READ, through the real GET /v1/billing/balance handler.
	if got := reportedBalance(t, mountReader(t)); got != gateCents {
		t.Errorf("/v1/billing/balance reports %d cents, want %d — the credit landed somewhere the "+
			"customer's own page cannot see", got, gateCents)
	}

	// AND IT IS REALLY SPENDABLE: the edge meter's debit, at the same address, takes it
	// back to zero. A balance that reads but cannot be spent is a different bug wearing
	// this one's answer.
	org, subject := gateOrg, wallet(gateOrg)
	if err := fin.RecordUsage(context.Background(), types.UsageInput{
		Org: org, Subject: subject, Amount: money.FromCents(gateCents),
		Currency: "usd", Model: "zen", Ref: "spend-1",
	}); err != nil {
		t.Fatalf("spend the credited balance: %v", err)
	}
	if got := spendable(t, fin, false); got != 0 {
		t.Errorf("after spending the whole credit the wallet holds %d cents, want 0", got)
	}
}

// TestSettle_TheTypedDoorFundsThePayerAndNotTheOrgPool — the AGENT's door, and the
// divergence it closes.
//
// commerce's typed core credits its own store under the ORG POOL (org.Name), while the
// wallet the spend gate reads is account.Payer's answer — the org's pool for a
// dedicated tenant, and a PERSON for a member of the shared signup org or a credential
// carrying a signed `person:` billing_account claim. For that population the agent's
// door funded a balance nobody could spend while the payer stayed at zero. The
// spendable credit is posted at the payer's own address, so both doors fund one wallet.
//
// Mutation proof: delete the s.settle call from [screen.op] and this reads 0 — the
// browser door would still credit, so the cross-door test alone cannot see it.
func TestSettle_TheTypedDoorFundsThePayerAndNotTheOrgPool(t *testing.T) {
	// A member of the SHARED SIGNUP ORG, which is the population where the pool and the
	// payer are different keys — account.Payer resolves a person there and the org's
	// slug everywhere else. A dedicated tenant would make the two the same string and
	// this test could not tell a pool credit from a payer credit at all.
	const org, user = account.SignupOrg, "dana"
	subject := account.Payer(account.Credential{Owner: org, Name: user}).Subject()
	if subject == org {
		t.Fatalf("the payer rule resolves %q/%q to the pool (%q) — this fixture can no longer tell "+
			"the two addresses apart and the assertion below would be vacuous", org, user, subject)
	}

	app := payApp(t)
	quiet(t)
	fin := finance.Current()
	cloud.SetRiskScorer(allowAll)
	typedDoor(t, app, riskGate(luxlog.New("settletest")), func() string { return settledRef })

	r := httptest.NewRequest(http.MethodPost, paymentsDoor, strings.NewReader(gateBody))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Org-Id", org)
	r.Header.Set("X-User-Id", user)
	resp, err := app.Test(r)
	if err != nil {
		t.Fatalf("post %s: %v", paymentsDoor, err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("the typed door answered %d %s, want 201", resp.StatusCode, body)
	}

	ctx := context.Background()
	paid, err := fin.Balance(ctx, org, subject, "usd", false)
	if err != nil {
		t.Fatalf("read the payer's wallet: %v", err)
	}
	if paid.Cents() != gateCents {
		t.Fatalf("the payer's wallet holds %d cents after an agent's settled payment, want %d — "+
			"the agent's door credits a wallet the spend gate does not read", paid.Cents(), gateCents)
	}
	pool, err := fin.Balance(ctx, org, org, "usd", false)
	if err != nil {
		t.Fatalf("read the pool balance: %v", err)
	}
	if pool.Cents() != 0 {
		t.Errorf("the org POOL holds %d cents, want 0 — money in a shared org's pool is neither "+
			"lost nor spendable by the member who paid for it", pool.Cents())
	}
}

// TestSettle_ASettledTopUpCreditsExactlyOncePerSettlement — the replay.
//
// Settlement is at-least-once by construction: the door retries, a webhook replays, an
// idempotency key replays the first receipt verbatim. The deposit is keyed on the
// settlement's own reference, so every replay of one payment converges on one credit.
//
// Mutation proof: drop Ref from the DepositInput in [screen.settle] — finance takes a
// fresh ref for an empty one, so grants stack — and the second post doubles the
// balance. That mutation mints free money on every retry, which is why the key is the
// first thing this file asserts after the credit itself.
func TestSettle_ASettledTopUpCreditsExactlyOncePerSettlement(t *testing.T) {
	fin := funded(t)
	app := creditDoor(t, stating(riskGate(luxlog.New("settletest")),
		settlement{cents: gateCents, currency: "usd"}))

	for i := 0; i < 3; i++ {
		if code, body := topup(t, app); code != http.StatusOK {
			t.Fatalf("post %d answered %d %s, want 200", i, code, body)
		}
	}
	if got := spendable(t, fin, false); got != gateCents {
		t.Fatalf("three posts of ONE settlement credited %d cents, want %d — a replayed settlement "+
			"credits again, so a retrying client mints balance", got, gateCents)
	}
}

// TestSettle_OnePaymentThroughBothDoorsIsOneCredit — the cross-door convergence, on
// the money rather than on the model.
//
// The browser's raw route and the agent's typed op are two doors onto ONE charge core,
// so one payment can be answered at both — a retry that changes client, an agent
// finishing what a browser started. Both answers carry the processor's own reference
// for that charge, and the deposit is keyed on it, so the two doors credit one wallet
// once.
//
// Mutation proof: key the deposit on the RECEIPT instead of [firstRef]'s answer (each
// door writes its own row, so the two receipts differ) and this credits twice.
func TestSettle_OnePaymentThroughBothDoorsIsOneCredit(t *testing.T) {
	const one = "sq_pay_the_same_charge"
	s := riskGate(luxlog.New("settletest"))

	// payApp publishes the ledger this reads back, so it is resolved AFTER the fixture
	// rather than beside it — two ledgers published in one test would leave the
	// assertions reading the one the doors did not credit.
	app := payApp(t)
	quiet(t)
	fin := finance.Current()
	cloud.SetRiskScorer(allowAll)
	browserDoor(app, s, func() string { return one })
	typedDoor(t, app, s, func() string { return one })

	if code, body := pay(t, app, "/v1/billing/topup/token"); code != http.StatusOK {
		t.Fatalf("the browser door answered %d %s", code, body)
	}
	if code, body := pay(t, app, paymentsDoor); code != http.StatusCreated {
		t.Fatalf("the typed door answered %d %s", code, body)
	}
	if got := spendable(t, fin, false); got != gateCents {
		t.Fatalf("one payment answered at BOTH doors credited %d cents, want %d — the two doors are "+
			"not keying the same settlement, so a payment taken once is funded twice", got, gateCents)
	}
}

// TestSettle_TheCreditIsSizedByTheReceiptAndNeverByTheRequest — the mint this closes.
//
// commerce's guard replays the FIRST receipt for a repeated idempotency key, whatever
// amount the repeat carried. So a door that sized its credit from the request would let
// a settled $5 charge be replayed as $5,000 — one HTTP call, no second card
// authorisation — whenever the first attempt's deposit had not landed. The receipt is
// the row the money core wrote when the card actually cleared; it is identical on a
// replay and it is not a value the caller sends.
//
// Mutation proof: size the deposit from the request (p.cents / in.AmountCents) instead
// of the receipt and this credits 500000.
func TestSettle_TheCreditIsSizedByTheReceiptAndNeverByTheRequest(t *testing.T) {
	fin := funded(t)
	app := creditDoor(t, stating(riskGate(luxlog.New("settletest")),
		settlement{cents: gateCents, currency: "usd"}))

	// The request asks for $5,000; the settlement was $42.
	r := httptest.NewRequest(http.MethodPost, "/v1/billing/topup/token",
		strings.NewReader(`{"sourceId":"cnon:card-nonce-ok","amountCents":500000,"currency":"USD"}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Org-Id", gateOrg)
	r.Header.Set("X-User-Id", gateUser)
	resp, err := app.Test(r)
	if err != nil {
		t.Fatalf("topup: %v", err)
	}
	_ = resp.Body.Close()

	if got := spendable(t, fin, false); got != gateCents {
		t.Fatalf("a $42 settlement replayed under a $5,000 request credited %d cents, want %d — "+
			"the door is sizing the credit from what was ASKED rather than from what SETTLED", got, gateCents)
	}
}

// TestSettle_ASandboxChargeCreditsTheSandboxBooks.
//
// organization.TestMode decides the Square environment AND the bucket commerce
// credits, and an org that has never been flipped to live transacts in SANDBOX. A
// sandbox charge takes no real money, so its credit must never be spendable against
// live inference — finance keeps the two in separate files per org and the receipt
// says which one this was.
//
// Mutation proof: drop Test from the DepositInput (the omission Red found in the
// sibling adapter) and sandbox money lands in the live books — free inference for the
// cost of a test nonce.
func TestSettle_ASandboxChargeCreditsTheSandboxBooks(t *testing.T) {
	fin := funded(t)
	app := creditDoor(t, stating(riskGate(luxlog.New("settletest")),
		settlement{cents: gateCents, currency: "usd", test: true}))

	if code, body := topup(t, app); code != http.StatusOK {
		t.Fatalf("the sandbox top-up answered %d %s, want 200", code, body)
	}
	if got := spendable(t, fin, true); got != gateCents {
		t.Errorf("the sandbox books hold %d cents, want %d — a test-mode top-up credited neither "+
			"ledger", got, gateCents)
	}
	if got := spendable(t, fin, false); got != 0 {
		t.Fatalf("the LIVE books hold %d cents after a SANDBOX charge, want 0 — a Square test nonce "+
			"just minted spendable balance", got)
	}
	if got := reportedBalance(t, mountReader(t)); got != 0 {
		t.Errorf("/v1/billing/balance reports %d cents of sandbox money as live, want 0", got)
	}
}

// adminOrg is the reserved platform org — the `owner` claim a SuperAdmin carries while
// its X-Org-Id is whichever org it is acting in.
const adminOrg = "admin"

// callers are the three identities a credit door can see, and the TWO organisations each
// one names. Two tests range over this one table because they are two halves of a single
// property — where the receipt is READ and where the credit may be MINTED — and a caller
// list that drifted between them would leave one half asserted on a population the other
// half never sees.
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

// TestSettle_TheReceiptIsReadFromTheOrgTheChargeWasWrittenIn — TWO ORGS, and the caller
// that separates them.
//
// A card top-up names two organisations. The RECEIPT is written by commerce under the
// EFFECTIVE org — the org the door is acting in, which is what iammiddleware resolves
// for the browser route and [payingOrg] for the typed op — and the BALANCE it funds is
// principal.WalletOf's, which for a platform SuperAdmin is its OWN books, because
// platform sudo is not a statement about who pays. For every other caller the two are
// one string, which is exactly why one value could do both jobs through a green suite.
//
// For a SuperAdmin masquerading into a customer's org they are not. The receipt read was
// keyed on the PAYER, so it looked in the admin's books for a row commerce had written in
// the customer's, found nothing, and refused a charge that had already cleared — and
// permanently: the retry replays the same receipt into the same absent namespace, while a
// fresh idempotency key charges the card again.
//
// It asserts the ARGUMENTS of the receipt read and nothing else, because the amount alone
// cannot see this: [stating] answers a settlement whatever it is asked, so a door reading
// the right figure out of the wrong namespace looks identical to a correct one. Where the
// money may then GO is the sibling property, and
// [TestSettle_AMintThatCannotLandIsRefusedBeforeTheCardIsCharged] holds it.
//
// THE MASQUERADE ROW IS DRIVEN AT THE BOUNDARY RATHER THAN AT THE DOOR, and that is the
// screen's refusal arriving BEFORE the charge rather than a weakening of this property.
// A door that no longer takes that caller's card also never reads its receipt, so
// [screen.settle] is where the read still exists and where it is asserted: the same value,
// the same two names, no card charged. The other two rows keep driving the real door,
// which is what holds [seen] to resolving both names off a request.
//
// The read is asserted for a payment whose credit is REFUSED, and that is the whole
// reason the boundary's own guard sits after the read rather than before it: the two
// names are one string for every other caller, so a boundary that refused before reading
// would leave nothing in the suite able to tell p.org from p.ledger, and the read this
// commit fixed could quietly go back to the payer's namespace under a green bar.
//
// Mutation proof: read the receipt from p.ledger (the shipped defect) and the masquerade
// row fails on the captured org — "admin", where the charge was never written — while
// both other rows still pass.
func TestSettle_TheReceiptIsReadFromTheOrgTheChargeWasWrittenIn(t *testing.T) {
	for _, tc := range callers {
		t.Run(tc.name, func(t *testing.T) {
			funded(t)
			var seen asked
			s := capturing(riskGate(luxlog.New("settletest")), settlement{cents: gateCents, currency: "usd"}, &seen)

			if tc.charged != tc.ledger {
				// No door takes this caller's card, so the boundary is called the way a
				// mint outside the screen would call it. The credit refuses; the read
				// under test has already happened by then.
				_ = s.settle(context.Background(), payment{
					org: tc.charged, ledger: tc.ledger, subject: wallet(tc.ledger),
					door: "/v1/billing/topup/token", via: "/v1/billing/topup/token",
				}, settledRef, settledReceipt)
			} else {
				topupAs(t, creditDoorBody(t, s,
					`{"transactionId":"`+settledReceipt+`","status":"ok","processorRef":"`+settledRef+`"}`), tc.hdr)
			}

			if seen.org != tc.charged {
				t.Errorf("the settled receipt was looked up in %q, want %q — the read is keyed on the "+
					"wrong organisation, so in production commerce answers not-found for a row it "+
					"wrote and the door 500s on a card that cleared", seen.org, tc.charged)
			}
			if seen.id != settledReceipt {
				t.Errorf("the receipt read named %q, want the settlement's own receipt %q", seen.id, settledReceipt)
			}
		})
	}
}

// TestSettle_AMintThatCannotLandIsRefusedBeforeTheCardIsCharged — the money half, and
// the ORDER that makes it a control rather than a report.
//
// Naming the two organisations separately is right for every READ and wrong for the MINT.
// A card top-up is charged on the EFFECTIVE org's merchant account and its receipt is
// written in that org's books, while the credit is addressed to principal.WalletOf — and
// for a masquerading SuperAdmin those are two different organisations. The deposit went to
// the second of them: the customer's card cleared, spendable credit appeared in the
// platform admin's own wallet, and the door answered 200. That is money moved between two
// customers' books on nothing but a masqueraded session.
//
// NEITHER ADDRESS IS RIGHT, so the mint refuses rather than choosing. Crediting the ledger
// is the bug above; crediting the charged org has an admin's card top up the customer it is
// only inspecting, which is a different surprise and equally unasked-for. A SuperAdmin who
// means to fund a customer has the admin grant, a door whose whole subject is whose money
// it is — so the refusal names it.
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
			app := creditDoorHandler(t,
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
					t.Errorf("the refusal (%s) does not name the door that CAN fund another organisation — "+
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
					"the pre-charge refusal is closing the door on ordinary customers", taken)
			}
			if code != http.StatusOK {
				t.Fatalf("a top-up whose charge and credit are the one org answered %d %s, want 200 — "+
					"the mint guard is refusing a caller that pays for itself", code, body)
			}
			if got := held(t, fin, tc.ledger, false); got != gateCents {
				t.Fatalf("the wallet the spend gate reads holds %d cents, want %d", got, gateCents)
			}
		})
	}
}

// TestSettle_TheLedgerBoundaryStillRefusesAMintReachedDirectly — the backstop, on its own.
//
// The refusal that matters now happens at the screen, before any card moves. The boundary
// keeps its own reading of the same rule ([payment.diverged], one method, two callers)
// because refusing at the screen is a property of the two doors the screen is composed
// onto, and NOT a property of the ledger: a third mint, a replayed webhook, a job calling
// [screen.settle] directly is one composition away, and the address a divergent payment
// would land at is exactly as wrong there.
//
// So this calls the boundary the way such a caller would — no door, no screen in front —
// and asserts it refuses and moves nothing. It is also the only place the boundary's own
// answer is still asserted at all, now that no door can reach it.
//
// Mutation proof: delete the [payment.diverged] guard from [screen.settle] and this
// credits $42 of a customer's money into the admin's wallet.
func TestSettle_TheLedgerBoundaryStillRefusesAMintReachedDirectly(t *testing.T) {
	fin := funded(t)
	s := stating(riskGate(luxlog.New("settletest")), settlement{cents: gateCents, currency: "usd"})

	err := s.settle(context.Background(), payment{
		org: gateOrg, ledger: adminOrg, subject: wallet(adminOrg),
		door: "/v1/billing/topup/token", via: "/v1/billing/topup/token",
	}, settledRef, settledReceipt)

	if err == nil {
		t.Fatal("the ledger boundary CREDITED a payment charged in one organisation against a wallet " +
			"in another — a mint reached outside the screen has no second refusal behind it")
	}
	// And it is TERMINAL: the two names are the same two names on every attempt, so the
	// answer must not send anybody round a retry that takes the card again.
	if !strings.Contains(err.Error(), "retrying will not credit it") {
		t.Errorf("the boundary refused with %q, which invites a retry that can never credit", err)
	}
	if got := held(t, fin, adminOrg, false); got != 0 {
		t.Errorf("the SuperAdmin's own wallet holds %d cents, want 0", got)
	}
	if got := held(t, fin, gateOrg, false); got != 0 {
		t.Errorf("the org the charge was written in holds %d cents, want 0", got)
	}
}

// TestSettle_ARefusedCreditStillTeachesTheModel — teaching is telemetry, and telemetry
// is never gated on the money path.
//
// [screen.settle] can refuse a charge that CLEARED: a currency this ledger does not hold,
// a receipt it could not read, no ledger in the process. The card was still charged in
// every one of them — the settlement is a fact about the past — so a payment dropped from
// the payer's velocity there is a real payment the screen will never see. A caller who
// can provoke the refusal could then pay all day and accrue nothing, which is the bound
// the screen exists to apply, switched off by the door's own error path.
//
// Mutation proof: return early on settle's error (the shipped ordering) and this reads
// nothing on the channel.
func TestSettle_ARefusedCreditStillTeachesTheModel(t *testing.T) {
	funded(t)
	// A charge that settled in yen: a real settlement this USD ledger must refuse.
	app := creditDoorBody(t,
		stating(riskGate(luxlog.New("settletest")), settlement{cents: 500000, currency: "jpy"}),
		`{"transactionId":"`+settledReceipt+`","status":"ok","processorRef":"`+settledRef+`"}`)
	// AFTER the fixture: creditDoorBody substitutes the same seam ([quiet]), and the
	// watcher has to be the one in place when the door runs.
	seen := watchTeaching(t)

	if code, _ := topup(t, app); code != http.StatusInternalServerError {
		t.Fatalf("a settled charge that credited nothing answered %d, want 500", code)
	}

	got := await(t, seen)
	if got.in.Settlement != settledRef {
		t.Errorf("the record keys on %q, want the settlement %q", got.in.Settlement, settledRef)
	}
	if got.org != gateOrg {
		t.Errorf("the record was stated for %q, want the payer's org %q", got.org, gateOrg)
	}
}

// TestSettle_ATopUpThatCannotBeCreditedRefusesTheDoor — the fail-loud half.
//
// Every row here is the same fact: the card cleared and the balance cannot be moved.
// None of them may answer 200. A settled charge answered with success and no credit is
// the defect this file exists to end, and it is worse than a 500 because nobody — not
// the customer, not an operator — is told anything happened.
//
// The refusal must also OVERWRITE the handler's own 200, which is the part that is not
// obvious: the money core has already written its receipt into the response by the time
// the credit is attempted.
//
// Mutation proof: return nil instead of the refusal from any branch of [screen.settle]
// and that row answers 200 with a zero balance.
func TestSettle_ATopUpThatCannotBeCreditedRefusesTheDoor(t *testing.T) {
	for _, tc := range refusals {
		t.Run(tc.name, func(t *testing.T) {
			fin, _ := refused(t, tc)

			code, body := topup(t, refusalDoor(t, tc, riskGate(luxlog.New("settletest"))))
			if code == http.StatusOK {
				t.Fatalf("a settled charge that credited NOTHING answered 200 (%s) — the customer is "+
					"charged and told it worked", body)
			}
			if code != http.StatusInternalServerError {
				t.Errorf("answered %d, want 500 — %s", code, body)
			}
			if fin != nil {
				if got := spendable(t, fin, false); got != 0 {
					t.Errorf("the refused credit still moved %d cents", got)
				}
			}
		})
	}
}

// TestSettle_ARefusalSaysWHETHERARetryCanClearIt — the other half of failing loud.
//
// Every row of [refusals] is the same fact to the customer — the card cleared and the
// balance did not — and they are NOT the same fact to whoever has to fix it. Some clear
// themselves the moment the top-up is retried: the idempotency key replays the receipt,
// the deposit runs again, the credit lands. Some can never clear, because what refused
// them is the settlement itself — a charge that settled in another currency or for
// nothing, a settlement the door could not identify, a reference that is already another
// payment's. The receipt is immutable, so every retry reads the same refusal.
//
// Telling a customer to retry one of those is worse than telling them nothing: they
// retry, the same key replays into the same refusal, and a FRESH key takes the card
// again. And an operator watching the door cannot page on a class that is indistinguish-
// able from the transient one. So the refusal states which it is — to the customer in
// the only terms they can act on, and on the RECONCILE line as a `terminal` field to
// alert on, beside the two doors that DO settle it.
//
// It reads the log the door actually wrote rather than trusting the wire alone, because
// the alertable field is the operator's half and it is not on the wire at all.
//
// Mutation proof: answer every refusal through [screen.uncredited] (the shipped state)
// and the four terminal rows fail; answer every one through [screen.stranded] and the
// two retryable rows fail.
func TestSettle_ARefusalSaysWhetherARetryCanClearIt(t *testing.T) {
	for _, tc := range refusals {
		t.Run(tc.name, func(t *testing.T) {
			refused(t, tc)
			var lines journal
			_, body := topup(t, refusalDoor(t, tc, riskGate(luxlog.NewWriter(&lines))))

			// THE CUSTOMER'S HALF: a terminal refusal must not invite the retry that
			// takes their card again, and a retryable one must not send them to support
			// for something their own retry completes.
			const never = "retrying will not credit it"
			if got := strings.Contains(body, never); got != tc.terminal {
				t.Errorf("the answer %q says the retry is pointless = %v, want %v — a customer told the "+
					"wrong thing here either retries into a second charge or gives up on a top-up that "+
					"was about to work", body, got, tc.terminal)
			}

			// THE OPERATOR'S HALF: the alertable field, and the recovery named.
			log := lines.String()
			if !strings.Contains(log, "RECONCILE") {
				t.Fatalf("no RECONCILE line was written for a settled charge that credited nothing: %s", log)
			}
			want := `"terminal":false`
			if tc.terminal {
				want = `"terminal":true`
			}
			if !strings.Contains(log, want) {
				t.Errorf("the RECONCILE line does not carry %s — an operator cannot page on the class "+
					"that never self-heals: %s", want, log)
			}
			if tc.terminal && !strings.Contains(log, "/v1/admin/grants") {
				t.Errorf("a terminal RECONCILE line does not name the door that credits the balance: %s", log)
			}
			if tc.terminal && !strings.Contains(log, "refund") {
				t.Errorf("a terminal RECONCILE line does not say the charge must be refunded — the "+
					"customer is out the money until somebody does: %s", log)
			}
		})
	}
}

// settled is the answer commerce's core gives for a charge that cleared.
const settled = `{"transactionId":"` + settledReceipt + `","status":"ok","processorRef":"` + settledRef + `"}`

// refusals is EVERY way a settled charge can fail to credit, in one table.
//
// It is one table because three properties are asserted over it — the door never answers
// 200, no wallet moves, and the refusal says whether a retry can clear it — and a row
// list that drifted between them would leave one property asserted on a population
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
		seed: func(t *testing.T, fin finance.Client) {
			t.Helper()
			// Another wallet in the same books, so the conflict is the ref naming a
			// different payment and the pool this test reads is untouched by the seed.
			if _, err := fin.Deposit(context.Background(), types.DepositInput{
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
	// seed is money already in the books before the door runs — the only way to reach
	// the deposit's own refusals, which need a real conflicting posting.
	seed func(t *testing.T, fin finance.Client)
	// terminal says whether NO retry can ever clear this one.
	terminal bool
}

// refused puts the world in the state one row describes: the ledger (or deliberately
// none) and whatever is already posted in it.
func refused(t *testing.T, tc refusal) (finance.Client, bool) {
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

// refusalDoor is the credit door for one row, with the screen the caller states — the
// production one, or one whose log a test can read.
func refusalDoor(t *testing.T, tc refusal, s screen) *zip.App {
	t.Helper()
	return creditDoorBody(t, tc.screw(s), tc.body)
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

// TestReceiptOf_ReadsTheRowTheMoneyCoreWROTE — the seam's DEFAULT, against a real
// commerce datastore.
//
// Every other test in this file states the settlement, which is what lets them run
// without booting another module's store — and it is also what would let the production
// read rot unnoticed. This one drives [receiptOf] itself over a real embedded commerce:
// a real transaction.Deposit row in a real org namespace, read back through commerce's
// own published reader, and asserted field by field against what a credit needs.
//
// It SKIPS rather than fails on a build whose SQLite has no math functions, because
// commerce cannot bootstrap there at all — the same artifact TestBalanceCents and
// TestInProcessClient hit under -race, and not a fact about this seam.
func TestReceiptOf_ReadsTheRowTheMoneyCoreWROTE(t *testing.T) {
	emb, err := commercemod.Embed(context.Background(), commercemod.EmbedConfig{DataDir: t.TempDir(), Dev: true})
	if err != nil {
		if strings.Contains(err.Error(), "no such function: acos") {
			t.Skipf("commerce cannot bootstrap on this build's SQLite: %v", err)
		}
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

// creditDoor is the browser credit door answering the way commerce's core answers a
// charge that cleared — the REAL TakePaymentOut field names, because the settlement is
// read out of exactly those.
func creditDoor(t *testing.T, s screen) *zip.App {
	t.Helper()
	return creditDoorBody(t, s,
		`{"transactionId":"`+settledReceipt+`","balanceCents":4200,"status":"ok","processorRef":"`+settledRef+`"}`)
}

func creditDoorBody(t *testing.T, s screen, body string) *zip.App {
	t.Helper()
	return creditDoorHandler(t, s, settledBody(http.StatusOK, body))
}

// creditDoorHandler is that same door in front of a handler the TEST supplies — the form
// a test needs when what it asserts is not what the handler ANSWERED but whether it ran
// at all. [charging] is the handler it exists for.
func creditDoorHandler(t *testing.T, s screen, h zip.Handler) *zip.App {
	t.Helper()
	shortRuntimeDir(t)
	plane.Unbind()
	t.Cleanup(plane.Unbind)
	t.Cleanup(func() { cloud.SetRiskScorer(nil) })
	cloud.SetRiskScorer(allowAll)
	quiet(t)
	app := zip.New(zip.Config{Logger: luxlog.New("settletest"), DisableStartupMessage: true})
	app.Post("/v1/billing/topup/token", s.route(h))
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
	resp, err := app.Test(req)
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
