// Copyright © 2026 Hanzo AI. MIT License.

package cloud

// Tests for the funding rung — the step that lets the paywall be switched on without
// dead-ending the signup it is supposed to charge.
//
// THEY RUN AGAINST THE REAL LEDGER. finance.New(t.TempDir()) is the shipped
// double-entry store, so the idempotency under test is finance's own (kind, ref)
// dedup inside the insert transaction — not a fake's re-statement of it. The credit
// seam is the same two calls the co-resident adapter makes (apps/commerce/ledger.go:
// Deposit on the address, then Balance read back on it), so a grant lands where the
// gate looks and the balance the test asserts is the balance the gate reads.
//
// The amount is never written as a number in an assertion that could drift with the
// code: it comes from credit.StarterCreditCents, and a separate canary binds THAT to
// the catalog the plans endpoint publishes.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/apps/finance"
	"github.com/hanzoai/cloud/money"
	"github.com/hanzoai/cloud/types"
	commercebilling "github.com/hanzoai/commerce/api/billing"
	"github.com/hanzoai/commerce/billing/credit"
	"github.com/hanzoai/commerce/billing/creditledger"
)

// ── harness ─────────────────────────────────────────────────────────────────────

// founder is a validated, non-admin principal whose HOME org is their own customer
// org — the shape a real self-serve org has once IAM's first-run provision has moved
// them into it, and the only shape the rung funds. account.Payer pools a customer
// org, so the wallet is {acme, acme}.
var founder = map[string]string{
	"X-User-Id":    "u_9",
	"X-User-Name":  "founder",
	"X-Org-Id":     "acme",
	"X-User-Owner": "acme",
}

// books is the credit seam the rung writes through, delegating to the REAL finance
// ledger with the same mapping the co-resident adapter uses. It records what it was
// asked to post so a test can assert the amount, the bucket tag and the ref, and it
// counts calls so "posted twice, credited once" is visible as such.
//
// posted counts ATTEMPTS, not grants. Under concurrency several callers can reach the
// post before any of them lands, so the number that matters is the BALANCE — what the
// ledger actually accepted. mu guards the recording, not the dedup: the dedup is
// finance's own, inside the insert transaction.
type books struct {
	mu     sync.Mutex
	fin    types.FinanceClient
	posted []creditledger.CreditInput
	err    error
	// gate, when set, holds every caller inside Credit until all of them have
	// arrived — see rendezvous.
	gate *rendezvous
}

var _ creditledger.CreditLedger = (*books)(nil)

func (b *books) Credit(ctx context.Context, in creditledger.CreditInput) (string, int64, error) {
	b.mu.Lock()
	b.posted = append(b.posted, in)
	failWith, gate := b.err, b.gate
	b.mu.Unlock()
	if failWith != nil {
		return "", 0, failWith
	}
	if gate != nil {
		gate.wait()
	}
	id, err := b.fin.Deposit(ctx, types.DepositInput{
		Org:      in.Org,
		Subject:  in.Subject,
		Amount:   money.FromCents(in.AmountCents),
		Currency: in.Currency,
		Notes:    in.Reason,
		Tags:     in.Tag,
		Ref:      in.IdempotencyKey,
	})
	if err != nil {
		return "", 0, err
	}
	bal, err := b.fin.Balance(ctx, in.Org, in.Subject, in.Currency, false)
	if err != nil {
		return id, 0, err
	}
	return id, bal.Cents(), nil
}

// Balance mirrors apps/commerce.ledger's own: the WHOLE address, because an empty
// subject is the org's pool and a named one is the member's wallet, and a fake that
// collapses the two would let a read answer about a different account without
// erroring — the shape of a balance bug nobody notices.
func (b *books) Balance(ctx context.Context, org, subject, currency string, test bool) (int64, error) {
	if subject == "" {
		subject = org // pooled org: the slug IS the pool account
	}
	bal, err := b.fin.Balance(ctx, org, subject, currency, test)
	if err != nil {
		return 0, err
	}
	return bal.Cents(), nil
}

// rendezvous holds n callers at a point until all n have reached it, then releases
// them together. It makes a race a FACT of the test rather than a hope about the
// scheduler: without it, "run 8 goroutines" is satisfied by the runtime finishing the
// first request before the second starts, and the property under test is never
// exercised — most of the time on a busy machine, which is the worst kind of flake
// because the test still passes.
type rendezvous struct {
	mu      sync.Mutex
	n       int
	arrived int
	open    chan struct{}
}

func newRendezvous(n int) *rendezvous { return &rendezvous{n: n, open: make(chan struct{})} }

// wait blocks until the nth caller arrives. It gives up after a bounded delay so a
// mechanism that stops N callers from reaching this point fails the test instead of
// hanging it.
func (r *rendezvous) wait() {
	r.mu.Lock()
	r.arrived++
	if r.arrived == r.n {
		close(r.open)
	}
	r.mu.Unlock()
	select {
	case <-r.open:
	case <-time.After(10 * time.Second):
	}
}

// openBooks publishes a real finance ledger on the process-wide money seam and
// installs the credit seam over it, restoring both on cleanup.
func openBooks(t *testing.T) *books {
	t.Helper()
	fin := finance.New(t.TempDir())
	b := &books{fin: fin}

	prevFin, prevLed := finance.Current(), creditledger.Get()
	finance.Publish(fin)
	creditledger.Set(b)
	t.Cleanup(func() {
		finance.Publish(prevFin)
		creditledger.Set(prevLed)
		_ = fin.Close()
	})
	return b
}

// scorer installs a risk scorer for the duration of a test. Absent by default, which
// is Decide's "no judge is deployed" exemption; a test that wants the screen to have
// an opinion states one.
func scorer(t *testing.T, fn RiskScorer) {
	t.Helper()
	SetRiskScorer(fn)
	t.Cleanup(func() { SetRiskScorer(nil) })
}

// answers is a scorer that always returns the same action.
func answers(action string) RiskScorer {
	return func(context.Context, string, RiskQuery) (RiskVerdict, error) {
		return RiskVerdict{Action: action, Score: 0.9, Cause: "test"}, nil
	}
}

// balance reads the wallet the gate reads: (org, subject) resolved exactly as
// principal.WalletOf resolves it for a pooled customer org.
func balance(t *testing.T, b *books, org string) int64 {
	t.Helper()
	bal, err := b.fin.Balance(context.Background(), org, org, "usd", false)
	if err != nil {
		t.Fatalf("balance(%s): %v", org, err)
	}
	return bal.Cents()
}

// spend drives one billable request through the gate under enforcement.
func spend(t *testing.T, hdr map[string]string) int {
	t.Helper()
	code, _ := call(t, spendProbe(&planStub{}), http.MethodPost, "/v1/chat/completions", hdr)
	return code
}

// ── the rung ────────────────────────────────────────────────────────────────────

// TestStarterFundsANewOrgOnItsFirstBillableRequest is the go-live property: with the
// paywall ON, a brand-new org's very first billable request is not a dead end. It is
// funded with the plan's included credit and it proceeds.
//
// MUTATION PROOF: delete the fund(c, w) call from standing()'s proven-unpaid branch —
//
//	default:
//	    c.Log().Info("spend: no subscription and no credit", ...)
//	    return ReasonUnpaid
//
// and the request is 402 with a $0 wallet: the signup dead-ends on request #1, which
// is exactly the state this rung exists to remove.
func TestStarterFundsANewOrgOnItsFirstBillableRequest(t *testing.T) {
	switches(t, map[string]bool{SwitchPaywallEnforced: true})
	b := openBooks(t)

	if code := spend(t, founder); code != http.StatusOK {
		t.Fatalf("a new org's first billable request = %d, want 200 — enforcement dead-ends the signup", code)
	}
	if got, want := balance(t, b, "acme"), int64(credit.StarterCreditCents); got != want {
		t.Fatalf("balance = %d cents, want %d — the wallet the gate reads must hold the plan's included credit", got, want)
	}
	if len(b.posted) != 1 {
		t.Fatalf("posted %d credits, want exactly 1", len(b.posted))
	}
	in := b.posted[0]
	if in.AmountCents != credit.StarterCreditCents {
		t.Errorf("AmountCents = %d, want %d (credit.StarterCreditCents, the one source)", in.AmountCents, credit.StarterCreditCents)
	}
	// PROMOTIONAL, NOT PREPAID. The tag is what billing/bucket.DepositKind reads to put
	// this in the non-cash Credit bucket; a bare or "prepaid" tag would make it real,
	// refundable, payout-able money.
	if in.Tag != credit.StarterCreditTag {
		t.Errorf("Tag = %q, want %q — an untagged grant is real money, not a promotional credit", in.Tag, credit.StarterCreditTag)
	}
	if in.IdempotencyKey != "starter:acme" {
		t.Errorf("IdempotencyKey = %q, want %q — the ref is the address, and it is the once-ever barrier", in.IdempotencyKey, "starter:acme")
	}
	if in.Org != "acme" || in.Subject != "acme" {
		t.Errorf("address = (%q,%q), want (acme,acme) — the credit must land where the gate reads", in.Org, in.Subject)
	}
}

// TestStarterGrantsOnceHoweverManyRequestsArrive is the idempotency property, proven
// against the REAL dedup rather than a fake's memory of it: the rung derives one
// deterministic ref, finance refuses the second posting of it inside the same
// transaction as the insert, and the org ends with ONE credit however many times the
// path is walked.
//
// It also proves the rung does not keep firing: the second request is admitted by the
// ordinary Funded leg, so the grant is a one-time rung and not a subsidy.
//
// MUTATION PROOF: drop the ref-dedup by making the key unique per attempt —
//
//	func starterRef(subject string) string { return "starter:" + subject + ":" + mint.ID("x") }
//
// and the balance climbs by the grant on every request (1500 after three), which is a
// wallet that refills itself forever.
func TestStarterGrantsOnceHoweverManyRequestsArrive(t *testing.T) {
	switches(t, map[string]bool{SwitchPaywallEnforced: true})
	b := openBooks(t)

	for i := 1; i <= 3; i++ {
		if code := spend(t, founder); code != http.StatusOK {
			t.Fatalf("request %d = %d, want 200", i, code)
		}
	}
	if got, want := balance(t, b, "acme"), int64(credit.StarterCreditCents); got != want {
		t.Fatalf("balance after 3 requests = %d cents, want %d — the grant must be once per org, ever", got, want)
	}
	// The rung is asked ONCE. Requests 2 and 3 never reach it: the wallet is funded, so
	// Stand admits at the credit leg and the proven-unpaid branch is not taken at all.
	if len(b.posted) != 1 {
		t.Fatalf("the credit seam was asked %d times, want 1 — a funded wallet must not re-enter the rung", len(b.posted))
	}
}

// TestStarterGrantsOnceUnderConcurrentFirstRequests is what actually proves the ref
// dedup, and it exists because the sequential test above CANNOT.
//
// WHY THE SEQUENTIAL TEST IS NOT THE PROOF. After the first grant lands the wallet is
// funded, so Stand admits at the credit leg and requests 2 and 3 never reach the rung
// again. Walking the path three times therefore shows "one grant" whatever starterRef
// returns — a unique-per-attempt key passes it. The rung is only ever RE-ENTERED while
// the wallet is still empty, which is to say by requests that are already in flight.
//
// So this is the real case, and it is an ordinary one: a client that opens with a
// handful of parallel calls. Every one of them reads a $0 wallet, every one of them
// sees zero lifetime usage, every one of them passes the screen, and every one of them
// posts. Nothing in cloud serialises them. The ONLY thing standing between that and N
// grants is that all N derive the same ref and finance refuses the duplicate inside the
// same transaction as the insert.
//
// THE RACE IS FORCED, NOT HOPED FOR. A rendezvous in the credit seam holds all n
// callers at the post until every one of them has arrived, so they are provably
// concurrent and provably all past the "is this wallet empty?" reads. Spawning n
// goroutines and trusting the scheduler does NOT reproduce this: on a loaded machine
// the first request usually finishes before the second begins, only one caller reaches
// the rung, and the test passes without ever exercising the dedup.
//
// THE BALANCE IS THE ASSERTION, not the call count: posted counts attempts, and n
// attempts is the CORRECT behaviour here. What must be true is that the ledger accepted
// exactly one of them.
//
// MUTATION PROOF: make the key unique per attempt —
//
//	return "starter:" + subject + ":" + time.Now().Format(time.RFC3339Nano)
//
// and the balance is n grants instead of one.
func TestStarterGrantsOnceUnderConcurrentFirstRequests(t *testing.T) {
	switches(t, map[string]bool{SwitchPaywallEnforced: true})
	b := openBooks(t)

	const n = 8
	b.gate = newRendezvous(n)

	app := spendProbe(&planStub{})
	codes := make([]int, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			for k, v := range founder {
				req.Header.Set(k, v)
			}
			resp, err := app.Test(req)
			if err != nil {
				codes[i] = -1
				return
			}
			defer resp.Body.Close()
			codes[i] = resp.StatusCode
		}(i)
	}
	wg.Wait()

	// All n reached the post — that is what the rendezvous guarantees, and it is the
	// only state in which the ref is load-bearing.
	if len(b.posted) != n {
		t.Fatalf("%d callers reached the post, want %d — the race did not happen, so nothing was proven", len(b.posted), n)
	}
	if got, want := balance(t, b, "acme"), int64(credit.StarterCreditCents); got != want {
		t.Fatalf("balance after %d concurrent first requests = %d cents, want %d — the wallet was granted more than once",
			n, got, want)
	}
	// Every racer is served, not just the winner. finance's replay branch returns the
	// entry that DID post rather than an error, so a caller whose deposit was deduped
	// still reads a funded wallet and is admitted on the same request.
	for i, c := range codes {
		if c != http.StatusOK {
			t.Errorf("request %d = %d, want 200 — a deduped racer must still be served on the credit that landed", i, c)
		}
	}
	// And the org is funded from here on: the next request is admitted by the ordinary
	// Funded leg without re-entering the rung.
	b.gate = nil
	before := len(b.posted)
	if code := spend(t, founder); code != http.StatusOK {
		t.Fatalf("the request after the race = %d, want 200", code)
	}
	if len(b.posted) != before {
		t.Fatalf("the rung was re-entered after the wallet was funded (%d -> %d posts)", before, len(b.posted))
	}
}

// TestStarterIsWithheldFromAFlaggedSignup is the fraud control on free money. A
// signup the risk plane refuses receives nothing — and it is refused by the ordinary
// paywall, so it can still buy credit like anyone else. This is what bounds
// "$5 × as many fake signups as can be minted".
//
// MUTATION PROOF: skip the screen —
//
//	func admitted(c *zip.Ctx, w principal.Wallet) bool { return true }
//
// and the flagged org is funded and served: free credit to an account the platform's
// own model just judged.
func TestStarterIsWithheldFromAFlaggedSignup(t *testing.T) {
	for _, action := range []string{ActionBlock, ActionChallenge, ActionRestrict} {
		t.Run(action, func(t *testing.T) {
			switches(t, map[string]bool{SwitchPaywallEnforced: true})
			b := openBooks(t)
			scorer(t, answers(action))

			if code := spend(t, founder); code != http.StatusPaymentRequired {
				t.Fatalf("a %s signup = %d, want 402 — it must not be funded", action, code)
			}
			if got := balance(t, b, "acme"); got != 0 {
				t.Fatalf("balance = %d cents, want 0 — a flagged signup received free credit", got)
			}
			if len(b.posted) != 0 {
				t.Fatalf("the credit seam was asked %d times, want 0 — the screen must decide BEFORE the post", len(b.posted))
			}
		})
	}
}

// TestStarterIsGrantedOnAScoredAllow is the other half of the screen: the control
// refuses flagged signups without refusing ordinary ones. Without this, a screen that
// simply never granted would pass the test above and still be broken.
func TestStarterIsGrantedOnAScoredAllow(t *testing.T) {
	for _, action := range []string{ActionAllow, ActionReview} {
		t.Run(action, func(t *testing.T) {
			switches(t, map[string]bool{SwitchPaywallEnforced: true})
			b := openBooks(t)
			scorer(t, answers(action))

			if code := spend(t, founder); code != http.StatusOK {
				t.Fatalf("a %s signup = %d, want 200", action, code)
			}
			if got, want := balance(t, b, "acme"), int64(credit.StarterCreditCents); got != want {
				t.Fatalf("balance = %d cents, want %d", got, want)
			}
		})
	}
}

// TestStarterIsWithheldWhenTheScorerCannotAnswer pins the fail posture on the money
// side, and it is the reason the query is marked Privileged.
//
// Privileged is STATED here rather than derived: cloud.Privileged classifies the
// REQUEST's path, and the request is an inference call, not a grant surface. So an
// unset bit would leave Decide on its fail-OPEN branch and a scorer outage would mint
// the grant for every unfunded wallet that asked.
//
// MUTATION PROOF: set Privileged: false in admitted() and a scorer that errors funds
// the org anyway — an outage becomes a mint.
func TestStarterIsWithheldWhenTheScorerCannotAnswer(t *testing.T) {
	switches(t, map[string]bool{SwitchPaywallEnforced: true})
	b := openBooks(t)
	scorer(t, func(context.Context, string, RiskQuery) (RiskVerdict, error) {
		return RiskVerdict{}, errors.New("scorer is having a bad day")
	})

	if code := spend(t, founder); code != http.StatusPaymentRequired {
		t.Fatalf("status = %d, want 402 — a judge that is present and silent must not mint", code)
	}
	if got := balance(t, b, "acme"); got != 0 {
		t.Fatalf("balance = %d cents, want 0", got)
	}
}

// TestStarterIsGrantedWhenNoScorerIsDeployed is Decide's absent exemption, which this
// rung inherits rather than re-decides. Refusing to onboard customers because the risk
// app is not part of a deployment is not a defense; it is a product that cannot be
// operated. Every other refusal (error, timeout, busy, silent) still withholds.
func TestStarterIsGrantedWhenNoScorerIsDeployed(t *testing.T) {
	switches(t, map[string]bool{SwitchPaywallEnforced: true})
	b := openBooks(t)
	SetRiskScorer(nil)

	if code := spend(t, founder); code != http.StatusOK {
		t.Fatalf("status = %d, want 200 — a deployment with no risk app must still onboard", code)
	}
	if got, want := balance(t, b, "acme"), int64(credit.StarterCreditCents); got != want {
		t.Fatalf("balance = %d cents, want %d", got, want)
	}
}

// TestStarterDoesNotRefundAnOrgThatSpentIt: an account that has burned through its
// credit reads exactly like one that never had any — a zero balance says nothing about
// history — so the rung asks lifetime usage as well. An org that has spent is refused
// and stays refused until it pays.
//
// MUTATION PROOF: drop the usage leg from opening() and this org is topped back up,
// which turns the credit into an unlimited subsidy for anyone willing to drain it.
func TestStarterDoesNotRefundAnOrgThatSpentIt(t *testing.T) {
	switches(t, map[string]bool{SwitchPaywallEnforced: true})
	b := openBooks(t)
	ctx := context.Background()

	// The org's life so far: it received its credit and spent every cent of it.
	if _, err := b.fin.Deposit(ctx, types.DepositInput{
		Org: "acme", Subject: "acme", Amount: money.FromCents(credit.StarterCreditCents),
		Currency: "usd", Tags: credit.StarterCreditTag, Ref: starterRef("acme"),
	}); err != nil {
		t.Fatalf("seed grant: %v", err)
	}
	if err := b.fin.RecordUsage(ctx, types.UsageInput{
		Org: "acme", Subject: "acme", Amount: money.FromCents(credit.StarterCreditCents),
		Currency: "usd", Model: "zen-1", Ref: "burned-it",
	}); err != nil {
		t.Fatalf("seed usage: %v", err)
	}
	if got := balance(t, b, "acme"); got != 0 {
		t.Fatalf("setup: balance = %d cents, want 0", got)
	}

	if code := spend(t, founder); code != http.StatusPaymentRequired {
		t.Fatalf("status = %d, want 402 — an exhausted org must pay, not be refilled", code)
	}
	if got := balance(t, b, "acme"); got != 0 {
		t.Fatalf("balance = %d cents, want 0 — the grant was re-issued", got)
	}
	if len(b.posted) != 0 {
		t.Fatalf("the credit seam was asked %d times, want 0", len(b.posted))
	}
}

// TestStarterNeverFundsTheSharedSignupOrg. Its members are strangers to each other, so
// account.Payer resolves each to a person wallet inside Hanzo's own brand books.
// Someone parked there holds a login, not an account: funding them would put consumer
// credit into the platform's own ledger and would pay the same human a second time
// when IAM's first-run provision moves them into an org of their own. It is also the
// cheapest identity on the platform to mint.
func TestStarterNeverFundsTheSharedSignupOrg(t *testing.T) {
	switches(t, map[string]bool{SwitchPaywallEnforced: true})
	b := openBooks(t)

	if code := spend(t, member); code != http.StatusPaymentRequired {
		t.Fatalf("status = %d, want 402 — a login in the shared signup org is not an account", code)
	}
	if len(b.posted) != 0 {
		t.Fatalf("the credit seam was asked %d times, want 0 — consumer credit landed in the brand books", len(b.posted))
	}
}

// TestStarterOnlyFundsTheCallersHomeOrg is the multiplication guard. IAM's signed
// `orgs` claim lets a person act in any org they belong to, and a founder is a member
// of every org they create — so funding "whichever wallet is paying" would mint one
// credit per org to anyone who can make them. Position, not membership, is what marks
// the one org that is theirs: orgs[0], which the identity boundary mints as
// X-User-Owner.
//
// MUTATION PROOF: drop the home check from opening() and the same person collects the
// grant once per org they can name.
func TestStarterOnlyFundsTheCallersHomeOrg(t *testing.T) {
	switches(t, map[string]bool{SwitchPaywallEnforced: true})
	b := openBooks(t)

	// Same person, acting in a SECOND org they belong to. The ledger is "second"; their
	// home is still "acme".
	away := map[string]string{
		"X-User-Id": "u_9", "X-User-Name": "founder",
		"X-Org-Id": "second", "X-User-Owner": "acme",
	}
	if code := spend(t, away); code != http.StatusPaymentRequired {
		t.Fatalf("status = %d, want 402 — an additional org must not draw a second grant", code)
	}
	if len(b.posted) != 0 {
		t.Fatalf("the credit seam was asked %d times, want 0 — one credit per person, not per org", len(b.posted))
	}
}

// TestStarterIsUnreachableWhileTheGateIsDark is the structural answer to the objection
// that killed the previous grant: "a disabled money-mint is one flag away from an
// enabled one". This rung has no flag of its own. It is reached from one call site
// PAST the enforcement check, so the mint and the refusal it cures are the same
// switch — there is no state in which money is being created and the paywall is off.
//
// MUTATION PROOF: move fund() ahead of the Switch read in standing() and the dark gate
// starts minting, which is the arrangement that was deleted.
func TestStarterIsUnreachableWhileTheGateIsDark(t *testing.T) {
	switches(t, map[string]bool{SwitchPaywallEnforced: false})
	b := openBooks(t)

	if code := spend(t, founder); code != http.StatusOK {
		t.Fatalf("dark gate must admit: status = %d", code)
	}
	if len(b.posted) != 0 {
		t.Fatalf("a dark gate minted %d credits — the mint must not be armed while the paywall is off", len(b.posted))
	}
}

// TestStarterFailureNeverFailsTheRequest. Funding is not an authorization decision: a
// grant that could not be posted leaves the account unfunded and the gate refuses it
// in its own words, with the actionable 402 body. It must never surface the ledger's
// error to the caller, and it must never admit on a post that did not land.
func TestStarterFailureNeverFailsTheRequest(t *testing.T) {
	switches(t, map[string]bool{SwitchPaywallEnforced: true})
	b := openBooks(t)
	b.err = errors.New("the books are on fire")

	code, body := call(t, spendProbe(&planStub{}), http.MethodPost, "/v1/chat/completions", founder)
	if code != http.StatusPaymentRequired {
		t.Fatalf("status = %d, want 402", code)
	}
	if got := balance(t, b, "acme"); got != 0 {
		t.Fatalf("balance = %d cents, want 0", got)
	}
	if len(body) == 0 {
		t.Fatal("the refusal must still carry its actionable body")
	}
}

// ── the amount ──────────────────────────────────────────────────────────────────

// TestStarterAmountIsTheCatalogs is the drift canary. The grant must be worth what the
// product advertises, and the two facts live in two repositories: the amount this code
// posts (credit.StarterCreditCents) and the amount /v1/billing/plans publishes for the
// entry plan. Nothing keeps them equal except this assertion.
//
// MUTATION PROOF: hardcode any other number in fund() —
//
//	AmountCents: 1000,
//
// and the first assertion fails on the constant; change the constant instead and the
// second fails against the catalog. Either way the grant can never quietly stop being
// the "$5 free credit" the plans endpoint promises.
func TestStarterAmountIsTheCatalogs(t *testing.T) {
	// The entry plan is the one that advertises the free credit (api/billing/plans/
	// subscription.json: "go", limits.includedCreditUsd 5 / freeCredit 5).
	const entryPlan = "go"
	advertised := commercebilling.IncludedMonthlyCents(entryPlan)
	if advertised <= 0 {
		t.Fatalf("the %q plan advertises no included credit — the catalog changed shape", entryPlan)
	}
	if int64(credit.StarterCreditCents) != advertised {
		t.Fatalf("the grant is %d cents but %q advertises %d — a new org gets less than the page promises",
			credit.StarterCreditCents, entryPlan, advertised)
	}

	// And the code posts THAT, not a literal of its own.
	switches(t, map[string]bool{SwitchPaywallEnforced: true})
	b := openBooks(t)
	if code := spend(t, founder); code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if len(b.posted) != 1 || b.posted[0].AmountCents != advertised {
		t.Fatalf("posted %+v, want one credit of %d cents", b.posted, advertised)
	}
}

// TestStarterRefIsTheAddress. finance dedups on (kind, ref) WITHIN one org's books, so
// an org-keyed ref would collide between two members of an org whose members hold
// their own wallets: the first would be credited and the second answered with
// ErrRefTaken — a grant refused because somebody else already had one. The key is the
// ADDRESS, which is the org itself exactly where the org IS the address.
func TestStarterRefIsTheAddress(t *testing.T) {
	if got := starterRef("acme"); got != "starter:acme" {
		t.Errorf("starterRef(acme) = %q, want starter:acme", got)
	}
	if got := starterRef("hanzo/alice"); got != "starter:hanzo/alice" {
		t.Errorf("starterRef(hanzo/alice) = %q, want starter:hanzo/alice", got)
	}
	if starterRef("hanzo/alice") == starterRef("hanzo/bob") {
		t.Error("two members of one org share a ref — one of them cannot be granted")
	}
	// Stable across every retry, restart and re-login: no time, no nonce, no request id.
	if starterRef("acme") != starterRef(" ACME ") {
		t.Error("the ref is not stable under the address's own spelling")
	}
}
