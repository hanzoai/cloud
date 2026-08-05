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
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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
	prior := teach
	teach = func(context.Context, *plane.RiskObserveIn) (*plane.RiskObserved, error) {
		return &plane.RiskObserved{Learned: 1}, nil
	}
	t.Cleanup(func() {
		if !released(3 * time.Second) {
			t.Errorf("%d teaching slot(s) never came back — the detached record leaked", len(teaching))
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

// settling is the credit door's screen with the whole money path behind it: a
// throwaway ledger published for the test, and a receipt read stating the $42 the
// door's own body asks for. It is what every door fixture in this package resolves
// its screen through.
func settling(t *testing.T) screen {
	t.Helper()
	funded(t)
	return stating(riskGate(luxlog.New("settletest")), settlement{cents: gateCents, currency: "usd"})
}

// payerWallet is the address the SPEND GATE will debit for the fixtures' caller —
// principal.WalletOf's answer for those headers, spelled through the one rule
// (account.Payer) rather than through a literal, so a change to the payer rule moves
// this test with it instead of leaving it green against a stale key.
func payerWallet() (org, subject string) {
	return gateOrg, account.Payer(account.Credential{Owner: gateOrg, Name: gateUser}).Subject()
}

// spendable reads the balance the AI gate reads: build.go's balanceReader is
// fin.Balance(ctx, org, subject, currency, false) and apps/metering's fetchAvailable
// is the same call, so this IS the gate's read and not a paraphrase of it.
func spendable(t *testing.T, fin finance.Client, test bool) int64 {
	t.Helper()
	org, subject := payerWallet()
	bal, err := fin.Balance(context.Background(), org, subject, "usd", test)
	if err != nil {
		t.Fatalf("read the spendable balance: %v", err)
	}
	return bal.Cents()
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
	org, subject := payerWallet()
	if err := fin.RecordUsage(context.Background(), types.UsageInput{
		Org: org, Subject: subject, Amount: money.FromCents(gateCents),
		Currency: "usd", Model: "zen", RequestID: "spend-1",
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
	const settled = `{"transactionId":"` + settledReceipt + `","status":"ok","processorRef":"` + settledRef + `"}`
	for _, tc := range []struct {
		name  string
		body  string
		screw func(screen) screen
		// ledger says whether a finance ledger is published at all.
		ledger bool
	}{
		{
			"the receipt cannot be read, so the amount is unknown",
			settled,
			func(s screen) screen { return refusing(s, errors.New("payment not found")) },
			true,
		},
		{
			"the charge settled in a currency this ledger does not hold",
			settled,
			func(s screen) screen {
				return stating(s, settlement{cents: 500000, currency: "jpy"})
			},
			true,
		},
		{
			"the receipt states no money",
			settled,
			func(s screen) screen { return stating(s, settlement{cents: 0, currency: "usd"}) },
			true,
		},
		{
			"the answer names no settlement to key the credit on",
			`{"status":"ok"}`,
			func(s screen) screen { return stating(s, settlement{cents: gateCents, currency: "usd"}) },
			true,
		},
		{
			"there is no ledger in the process that owns it",
			settled,
			func(s screen) screen { return stating(s, settlement{cents: gateCents, currency: "usd"}) },
			false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var fin finance.Client
			if tc.ledger {
				fin = funded(t)
			} else {
				finance.Publish(nil)
			}
			app := creditDoorBody(t, tc.screw(riskGate(luxlog.New("settletest"))), tc.body)

			code, body := topup(t, app)
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
	shortRuntimeDir(t)
	plane.Unbind()
	t.Cleanup(plane.Unbind)
	t.Cleanup(func() { cloud.SetRiskScorer(nil) })
	cloud.SetRiskScorer(allowAll)
	quiet(t)
	app := zip.New(zip.Config{Logger: luxlog.New("settletest"), DisableStartupMessage: true})
	app.Post("/v1/billing/topup/token", s.route(settledBody(http.StatusOK, body)))
	return app
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
	if _, subject := payerWallet(); out.Account != subject {
		t.Errorf("/v1/billing/balance reports the account %q, want %q — the customer's page and the "+
			"credit are naming different wallets", out.Account, subject)
	}
	return out.Available
}
