// Copyright © 2026 Hanzo AI. MIT License.

package commerce

// settle.go — A SETTLED CARD PAYMENT LANDS IN THE LEDGER THE SPEND GATE READS.
//
// # The money that was taken and never arrived
//
// commerce's card core credits ITS OWN transaction store (api/billing
// payment_core.go, transaction.Deposit). Nothing in this binary spends from that
// store. The prepaid balance every gate consults is apps/finance —
// apps/metering's Balance is fin.Balance and GET /v1/billing/balance is
// fin.Balance — and the two are different files on
// disk. So a customer's card was charged, commerce wrote a row, and the balance
// that decides whether an inference request is served never moved. The one
// endpoint that reaches finance, POST /v1/billing/credit, is not mounted at all.
//
// This is the other half of the settlement: at the exact point the endpoints
// already notice a charge cleared, the amount that cleared is deposited into
// the ONE spendable ledger, at the address the spend gate will debit.
//
// # Where it is issued, and why there
//
// Both credit endpoints compose one value onto their HANDLER (risk.go
// screen.route, screen.op), and that is the only place in this process where all
// four projections of the typed op AND the browser's raw route converge on "the
// charge cleared". A credit issued anywhere else would be a credit one endpoint
// could be wired without.
//
// It runs BEFORE the risk record and, unlike it, it is NOT detached and NOT
// best-effort. Teaching the model is telemetry and a lost row costs nothing;
// funding the balance IS the payment. So a credit that cannot be posted refuses
// the request — a 500 in the customer's words and a RECONCILE line in ours —
// rather than answering 200 to a customer whose money went nowhere. That is the
// same posture commerce's own core takes when its ledger write fails, and it
// makes the retry a RECOVERY: the caller replays the idempotency key, the money
// core replays the receipt verbatim, and this deposit — keyed on the settlement —
// completes the half that was missing.
//
// # The address: the wallet the gate will debit, never the org pool by default
//
// (Ledger, Subject) IS the money's address. Both are taken from the [payment] the
// endpoint already resolved ONCE for the screen — payerOrg then principal.Subject —
// which is byte-for-byte the pair principal.WalletOf hands the spend gate and
// apps/billing hands GET /v1/billing/balance. Credit and spend therefore name one
// wallet by construction rather than by two call sites that happen to agree.
//
// # And the RECEIPT is read from the org the charge was WRITTEN in, which is not it
//
// Those are two different organisations for one caller — a platform SuperAdmin acting
// inside a customer's org — and holding one value for both was a card taken with no
// way to credit it. commerce writes the receipt under the EFFECTIVE org (the org being
// acted in: iammiddleware.IAMTokenRequired resolves it for the browser route,
// [orgOf] for the typed op), while principal.BillingOrg funds the SuperAdmin's own
// books, because platform sudo is not a statement about who pays. Reading the receipt
// out of the payer's namespace therefore looked in the admin's books for a row written
// in the customer's, found nothing, and refused — permanently, since the retry replays
// the same receipt into the same absent namespace and a fresh idempotency key charges
// the card again. So the read is keyed on [payment.org] (where the money core wrote)
// and the deposit on [payment.ledger] (whose balance it funds): one value was doing two
// jobs, and they are two values now.
//
// AND WHERE THOSE TWO NAMES DIVERGE, THE MINT REFUSES. Two names is the right shape for
// a READ and the wrong shape for a CREDIT. The card cleared on ONE org's merchant
// account, so a deposit into the OTHER's wallet is money crossing between two customers'
// books on nothing but a masqueraded session's say-so. Splitting the value fixed the read
// and left the mint quietly landing at the wrong address behind a 200; a payment whose
// two names are not one name is refused. There is no correct wallet to pick there — see
// the guard's own note — and a SuperAdmin funding a customer has the admin grant, which
// is a credit that states whose it is.
//
// THAT REFUSAL IS THE SCREEN'S, AND THIS ONE IS THE BACKSTOP. Asked here it is asked too
// late: this runs after the money core has charged the card, so the refusal it produced
// was a 500 over money that had really moved. [screen.decide] asks [payment.diverged]
// before the handler runs — same rule, one method, no card charged — and the check below
// stays for the mints this screen does not compose. Defence in depth, in the order that
// costs a customer nothing.
//
// This is also what closes the divergence payments.go records at exposePayments:
// commerce's typed op credits its store under the ORG POOL (org.Name), while for
// a member of a shared signup org — or a credential carrying a signed `person:`
// billing_account claim — account.Payer resolves a PERSON, and the person's wallet
// is the one the gate reads. Money in the pool of such an org is neither lost nor
// spendable. The credit that matters now lands on the payer.
//
// # The key: the settlement's own reference, so every path credits ONCE
//
// finance dedups a deposit on its Ref inside the insert transaction, and the Ref
// here is [firstRef]'s answer — the PROCESSOR's reference for the charge, falling
// back to the ledger receipt only where the processor stated none. That is the one
// identifier that is the same across every path that can credit one payment, so
// the browser route, the agent's typed op, any of its four projections and a
// replayed webhook all converge on a single deposit. It is deliberately the SAME
// key the risk record is filed under: one payment, one identity, everywhere.
//
// # The amount comes off the RECEIPT, never off the request
//
// The request's amountCents is what a caller ASKED to charge. It is the settled
// amount on a fresh charge and it is NOT on a replay: commerce's guard replays the
// first receipt for a repeated idempotency key whatever amount the repeat carried.
// Reading the request would therefore mean that a first attempt whose deposit
// failed, followed by a replay naming a larger amount, credits the larger one —
// a settled $5 charge minting $5,000. The receipt cannot be steered that way: it
// is the row the money core wrote when the card actually cleared, it is identical
// on a replay, and its currency and its books are the ones the charge really used.
//
// # The books: a sandbox charge credits the sandbox ledger
//
// organization.TestMode decides both the Square environment and the bucket
// commerce credits, and finance keeps sandbox money in a separate file per org
// (finance-test.db). The receipt states which bucket it was, so the deposit lands
// in the same one — a sandbox charge can never fund live inference, and a live
// charge is never parked in books no gate reads.
//
// The credit is posted through finance's own Deposit rather than through commerce's
// injected creditledger adapter (ledger.go) because of the ADDRESS, not the books:
// the client carries the test bit now, but it is commerce's entry point onto its
// own credit, and the address this file deposits at is (p.ledger, p.subject) —
// the payer the SCREEN resolved from the request's principal, which is a value
// commerce cannot compute for itself. Going through the adapter would mean handing
// it back the answer it exists to ask for. Same ledger, same idempotency, one
// address resolved where the identity is.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	financeclient "github.com/hanzoai/cloud/apps/finance"
	"github.com/hanzoai/cloud/money"
	"github.com/hanzoai/cloud/types"
	commercebilling "github.com/hanzoai/commerce/api/billing"
	commerceorg "github.com/hanzoai/commerce/pkg/org"
	"github.com/zap-proto/zip"
)

// settlement is what a payment that CLEARED actually was, as its own receipt
// records it: the amount that moved, the currency it moved in, and which books it
// credited. Every field is a fact about the PAST — none of them is read off the
// request that asked for the charge, which is the whole reason this type exists
// rather than the handler reusing the values it was sent.
type settlement struct {
	// cents is the amount that settled, in the currency's minor unit.
	cents int64
	// currency is the ISO code the charge settled in.
	currency string
	// test reports which books were credited: the sandbox ones (a Square sandbox
	// charge) or live money.
	test bool
	// memo is the receipt's own note, carrying the processor and its reference. It
	// is carried over so the customer's finance page names the same payment the
	// commerce receipt does.
	memo string
}

// receiptOf reads that record out of the books the CHARGE WAS WRITTEN IN.
//
// It is [commercebilling.ReadPayment] — the module's own published read of the row
// its money core wrote — resolved through commerce's own org resolver, which is the
// same binding the charge itself used. The org is [payment.org], the effective org the
// handler charged through, so the read doubles as a check that the money core really
// wrote this receipt in the namespace the endpoint was acting in: a receipt that is
// not there is not found, and a not-found receipt refuses rather than funding a
// wallet on an unverified amount. It is NOT the payer's org — see the package note;
// those are the same string for every caller but a masquerading SuperAdmin, and
// naming the payer here is what made that caller's top-up permanently uncreditable.
//
// It is held on the screen rather than called directly for the one thing that
// buys: an endpoint test can state what settled without standing up a commerce
// datastore, exactly as the risk plane's [teach] client lets one assert what leaves
// this process without a risk child to receive it. The production value is set
// once, by [riskGate], and never varies.
func receiptOf(ctx context.Context, org, id string) (settlement, error) {
	o, err := commerceorg.Resolve(ctx, org)
	if err != nil {
		return settlement{}, fmt.Errorf("resolve org %q: %w", org, err)
	}
	rec, fault := commercebilling.ReadPayment(ctx, o, id)
	if fault != nil {
		return settlement{}, fault
	}
	if rec == nil {
		return settlement{}, fmt.Errorf("the receipt read answered nothing")
	}
	return settlement{
		cents:    rec.AmountCents,
		currency: rec.Currency,
		test:     rec.Test,
		memo:     rec.Notes,
	}, nil
}

// settle deposits a settled card payment into the one spendable ledger, exactly
// once per settlement.
//
// ref is the settlement's key ([firstRef]: the processor's reference, else the
// receipt) and id is the receipt itself — the ledger row the money core wrote,
// which is what the amount, the currency and the books are read back off.
//
// Every refusal below is the same fact — the card cleared and the balance did not
// move — and every one of them answers the caller rather than being swallowed,
// because a top-up that quietly credits nothing is the defect this file exists to
// end. There is no branch here that returns nil without a deposit.
//
// THEY ARE NOT ALL THE SAME REFUSAL, THOUGH, and the difference is the only thing an
// operator can act on: some clear themselves the moment the customer retries, and some
// refuse every retry there will ever be. Each branch names which it is by answering
// through [screen.uncredited] or [screen.stranded] — the second states a terminal bit to
// alert on and the endpoints that settle it.
func (s screen) settle(ctx context.Context, p payment, ref, id string) error {
	// The payment's three names, resolved once by [seen]. All are required: `org` says
	// which books hold the receipt this credit is sized from, `ledger` names the ledger
	// the deposit lands in and `subject` the wallet inside it — and a deposit that
	// guessed any of them would fund an address no gate reads off an amount nobody
	// wrote. None can be empty at an endpoint that settled a charge (the screen ahead of
	// this refuses a payment it cannot resolve a payer for, and a resolved payer means
	// a resolved namespace), so this is the boundary check saying so, not a fallback.
	if p.org == "" || p.ledger == "" || p.subject == "" {
		return s.stranded(p, ref, "the endpoint settled a payment whose payer this process could not resolve")
	}
	// No settlement identity, no idempotent deposit. Depositing under an invented
	// key credits the same money again on the very next retry, which is worse than
	// the credit this refuses.
	if ref == "" || id == "" {
		return s.stranded(p, ref, "the endpoint answered success and named no settlement to key the credit on")
	}

	got, err := s.receipt(ctx, p.org, id)
	if err != nil {
		// The one read whose failure this process cannot classify: a datastore that was
		// briefly unavailable and a row that is not in these books answer the same way.
		// Reported as RETRYABLE because a retry may genuinely clear it, and claiming to
		// know it is terminal would send an operator to reconcile a payment that is
		// about to credit itself.
		return s.uncredited(p, ref, "the settled receipt %q could not be read: %v", id, err)
	}
	if got.cents <= 0 {
		return s.stranded(p, ref, "the settled receipt %q states an amount of %d", id, got.cents)
	}
	// ONE ASSET. finance holds USD and its balance read ignores the currency
	// argument entirely, so a minor unit from another currency deposited here is
	// simply spent as though it were cents — ¥500,000 becoming $5,000. The endpoint
	// refuses instead, which is the same rule [paymentFacts] already applies to the
	// value it states to the model: an amount this process cannot express in USD is
	// not expressed at all.
	cur := strings.ToLower(strings.TrimSpace(got.currency))
	if cur == "" {
		cur = "usd"
	}
	if cur != "usd" {
		return s.stranded(p, ref, "the charge settled in %q and this ledger holds usd", cur)
	}

	// A MINT IS THE ONE PLACE THE TWO NAMES MUST BE ONE NAME — the BACKSTOP reading of it.
	//
	// Everything above this line is a READ, and the split is right for every one of them:
	// the receipt is read out of the books the charge was WRITTEN in, and the model judges
	// the org whose balance is at stake. A DEPOSIT is not a read. It takes money a CARD
	// cleared — on the effective org's merchant account, against a receipt commerce wrote
	// in [payment.org] — and turns it into spendable credit at (ledger, subject). While
	// those two organisations are one string that is one movement; when they are two, the
	// customer's card funded the SuperAdmin's own wallet and the endpoint answered 200.
	//
	// THERE IS NO ADDRESS HERE THAT IS NOT SURPRISING, which is why this refuses instead of
	// choosing. Crediting [payment.ledger] moves a customer's money into a platform admin's
	// balance; crediting [payment.org] has a masqueraded session top up the very org it is
	// only supposed to be inspecting. A platform operator who means to fund a customer has
	// an endpoint that SAYS SO — the admin grant, a credit whose whole subject is whose
	// it is — and a card taken inside someone else's org is not it.
	//
	// THE SCREEN HAS ALREADY REFUSED THIS, and that is why the refusal here is TERMINAL
	// rather than an endpoint's answer. [screen.decide] asks the same [payment.diverged]
	// before the handler charges the card, so no composed endpoint can reach this line —
	// anything that does is a mint standing outside the screen, and by the time it is here
	// the money has moved. Retrying cannot help it: the same identity resolves the same two
	// names every time. So it is refused, tagged terminal, and handed to an operator with
	// the two endpoints that CAN settle it.
	//
	// It cannot fire for the callers that pay for themselves — see [payment.diverged].
	if p.diverged() {
		return s.stranded(p, ref,
			"the card was charged in %q and this credit belongs to %q — a settled %d-cent top-up cannot "+
				"mint in books the charge was never taken against", p.org, p.ledger, got.cents)
	}

	fin, err := books("credit a settled top-up")
	if err != nil {
		return s.uncredited(p, ref, "%v", err)
	}
	entry, err := fin.Deposit(ctx, types.DepositInput{
		// THE LEDGER, not the namespace the receipt came out of. This half of the pair
		// is principal.WalletOf's answer and the spend gate's debit key.
		Org:      p.ledger,
		Subject:  p.subject,
		Amount:   money.FromCents(got.cents),
		Currency: cur,
		Notes:    got.memo,
		// The same tag commerce puts on its own row, so the two records of one
		// payment can be reconciled against each other by more than their amounts.
		Tags: "topup",
		Ref:  ref,
		Test: got.test,
	})
	if err != nil {
		// A REF THAT IS ANOTHER PAYMENT'S IS THE ONE DEPOSIT FAILURE THAT NEVER CLEARS.
		// finance refuses a ref already posted for a different (subject, amount) rather
		// than answering with the other payment's entry, and every replay of this
		// settlement carries the same ref, so the retry the customer is invited to make
		// refuses forever. Every other deposit failure is a write this process can lose
		// and win later.
		if errors.Is(err, financeclient.ErrRefTaken) {
			return s.stranded(p, ref, "the deposit failed: %v", err)
		}
		return s.uncredited(p, ref, "the deposit failed: %v", err)
	}
	s.lg.Info("a settled card payment credited the spendable ledger",
		"door", p.path, "via", p.via, "org", p.org,
		"ledger", p.ledger, "subject", p.subject,
		"cents", got.cents, "currency", cur, "test", got.test,
		"settlement", ref, "receipt", id, "entry", entry)
	return nil
}

// uncredited is "the card was charged and the balance was not" for the refusals a
// RETRY CAN STILL CLEAR: a receipt read that failed, a deposit whose write was lost,
// a ledger this process did not have.
//
// Retrying with the same idempotency key replays the receipt and re-runs the deposit,
// so for these the customer's own retry IS the recovery path, and saying so is true.
// The ones it is not true of are [screen.stranded]'s.
func (s screen) uncredited(p payment, ref, why string, args ...any) error {
	return s.reconcile(p, ref, false, fmt.Sprintf(why, args...))
}

// stranded is that same fact for the refusals NO RETRY CAN EVER CLEAR — money taken
// and money that will still be uncredited after any number of attempts.
//
// They are a distinct class because the RECOVERY is distinct, and telling a customer
// to retry a payment that refuses forever is worse than telling them nothing: they
// retry, the idempotency key replays the same receipt into the same refusal, and a
// fresh key takes the card again. Every one of them is a fact about the settlement
// itself rather than about this process's luck — the two names that cannot be one
// name, a receipt that settled in another currency or for nothing, a settlement the
// endpoint could not identify, a reference that is already another payment's. Nothing
// downstream changes any of those.
//
// So this states the terminal bit for an operator to ALERT on, and names the two
// endpoints that actually settle it: the admin grant credits the balance and the
// processor refunds the charge. It is loud rather than silent for the reason the
// whole file is: a settled charge with no credit and nobody told is the defect.
func (s screen) stranded(p payment, ref, why string, args ...any) error {
	return s.reconcile(p, ref, true, fmt.Sprintf(why, args...))
}

// reconcile is the ONE answer to "the card was charged and the balance was not".
//
// It is loud in both directions on purpose. The OPERATOR gets a RECONCILE line naming
// the settlement, BOTH orgs, why, whether any retry can clear it and what does clear it
// — the books the charge is in are where the receipt is found and the ledger is where
// the credit belongs, and a manual credit needs both — and a silent 200 would leave
// nobody anything to look for. The CUSTOMER gets a 500 and the settlement reference —
// their own payment's id, which the receipt already returns to them — because the honest
// answer to "did my top-up work" is no, and because quoting the reference is what makes
// support able to find the charge.
//
// A terminal refusal says so to the customer as well, in the only terms that are
// actionable to them: do not retry, this needs support. Inviting a retry there is
// inviting a second charge.
func (s screen) reconcile(p payment, ref string, terminal bool, why string) error {
	recovery := "the customer's own retry replays the receipt and completes the credit"
	if terminal {
		recovery = "no retry can credit this: issue the balance with POST /v1/admin/grants " +
			"and refund the charge with the processor"
	}
	s.lg.Error("RECONCILE: a card payment settled and the spendable balance was NOT credited",
		"door", p.path, "via", p.via, "org", p.org,
		"ledger", p.ledger, "subject", p.subject,
		"settlement", ref, "terminal", terminal, "why", why, "recovery", recovery)
	answer := "the charge settled but crediting your balance failed — contact support"
	if terminal {
		answer = "the charge settled and it cannot be credited to this balance — " +
			"retrying will not credit it, contact support"
	}
	if ref == "" {
		return zip.Errorf(http.StatusInternalServerError, "%s", answer)
	}
	return zip.Errorf(http.StatusInternalServerError, "%s with reference %s", answer, ref)
}
