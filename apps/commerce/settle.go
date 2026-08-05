// Copyright © 2026 Hanzo AI. MIT License.

package commerce

// settle.go — A SETTLED CARD PAYMENT LANDS IN THE LEDGER THE SPEND GATE READS.
//
// # The money that was taken and never arrived
//
// commerce's card core credits ITS OWN transaction store (api/billing
// payment_core.go, transaction.Deposit). Nothing in this binary spends from that
// store. The prepaid balance every gate consults is apps/finance — build.go's
// balanceReader is fin.Balance, apps/metering's fetchAvailable is fin.Balance,
// GET /v1/billing/balance is fin.Balance — and the two are different files on
// disk. So a customer's card was charged, commerce wrote a row, and the balance
// that decides whether an inference request is served never moved. The one door
// that reaches finance, POST /v1/billing/credit, is not mounted at all.
//
// This is the other half of the settlement: at the exact point the doors already
// notice a charge cleared, the amount that cleared is deposited into the ONE
// spendable ledger, at the address the spend gate will debit.
//
// # Where it is issued, and why there
//
// Both credit doors compose one value onto their HANDLER (risk.go screen.route,
// screen.op), and that is the only place in this process where all four
// projections of the typed op AND the browser's raw route converge on "the charge
// cleared". A credit issued anywhere else would be a credit one door could be
// wired without.
//
// It runs BEFORE the risk record and, unlike it, it is NOT detached and NOT
// best-effort. Teaching the model is telemetry and a lost row costs nothing;
// funding the balance IS the payment. So a credit that cannot be posted refuses
// the door — a 500 in the customer's words and a RECONCILE line in ours — rather
// than answering 200 to a customer whose money went nowhere. That is the same
// posture commerce's own core takes when its ledger write fails, and it makes the
// retry a RECOVERY: the caller replays the idempotency key, the money core replays
// the receipt verbatim, and this deposit — keyed on the settlement — completes the
// half that was missing.
//
// # The address: the wallet the gate will debit, never the org pool by default
//
// (Ledger, Subject) IS the money's address. Both are taken from the [payment] the
// door already resolved ONCE for the screen — payerOrg then principal.Subject —
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
// [payingOrg] for the typed op), while principal.BillingOrg funds the SuperAdmin's own
// books, because platform sudo is not a statement about who pays. Reading the receipt
// out of the payer's namespace therefore looked in the admin's books for a row written
// in the customer's, found nothing, and refused — permanently, since the retry replays
// the same receipt into the same absent namespace and a fresh idempotency key charges
// the card again. So the read is keyed on [payment.org] (where the money core wrote)
// and the deposit on [payment.ledger] (whose balance it funds): one value was doing two
// jobs, and they are two values now.
//
// This is also what closes the divergence payments.go records at exposePayments:
// commerce's typed door credits its store under the ORG POOL (org.Name), while for
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
// the browser door, the agent's typed op, any of its four projections and a
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
// This is why the credit is posted through finance's own Deposit rather than
// through commerce's injected creditledger adapter (ledger.go): CreditInput has no
// test field, so that seam cannot express which books to write, and routing a
// sandbox settlement through it would put unspendable sandbox money in the live
// ledger. Same ledger, same idempotency, one field the seam cannot carry.

import (
	"context"
	"fmt"
	"net/http"
	"strings"

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
// rather than the door reusing the values it was sent.
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
// wrote this receipt in the namespace the door was acting in: a receipt that is not
// there is not found, and a not-found receipt refuses rather than funding a wallet on
// an unverified amount. It is NOT the payer's org — see the package note; those are
// the same string for every caller but a masquerading SuperAdmin, and naming the payer
// here is what made that caller's top-up permanently uncreditable.
//
// It is held on the screen rather than called directly for the one thing that
// buys: a door test can state what settled without standing up a commerce
// datastore, exactly as the risk plane's [teach] seam lets one assert what leaves
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
// move — and every one of them answers the door rather than being swallowed,
// because a top-up that quietly credits nothing is the defect this file exists to
// end. There is no branch here that returns nil without a deposit.
func (s screen) settle(ctx context.Context, p payment, ref, id string) error {
	// The payment's three names, resolved once by [seen]. All are required: `org` says
	// which books hold the receipt this credit is sized from, `ledger` names the ledger
	// the deposit lands in and `subject` the wallet inside it — and a deposit that
	// guessed any of them would fund an address no gate reads off an amount nobody
	// wrote. None can be empty at a door that settled a charge (the screen ahead of
	// this refuses a payment it cannot resolve a payer for, and a resolved payer means
	// a resolved namespace), so this is the boundary check saying so, not a fallback.
	if p.org == "" || p.ledger == "" || p.subject == "" {
		return s.uncredited(p, ref, "the door settled a payment whose payer this process could not resolve")
	}
	// No settlement identity, no idempotent deposit. Depositing under an invented
	// key credits the same money again on the very next retry, which is worse than
	// the credit this refuses.
	if ref == "" || id == "" {
		return s.uncredited(p, ref, "the door answered success and named no settlement to key the credit on")
	}

	got, err := s.receipt(ctx, p.org, id)
	if err != nil {
		return s.uncredited(p, ref, "the settled receipt %q could not be read: %v", id, err)
	}
	if got.cents <= 0 {
		return s.uncredited(p, ref, "the settled receipt %q states an amount of %d", id, got.cents)
	}
	// ONE ASSET. finance holds USD and its balance read ignores the currency
	// argument entirely, so a minor unit from another currency deposited here is
	// simply spent as though it were cents — ¥500,000 becoming $5,000. The door
	// refuses instead, which is the same rule [paymentFacts] already applies to the
	// value it states to the model: an amount this process cannot express in USD is
	// not expressed at all.
	cur := strings.ToLower(strings.TrimSpace(got.currency))
	if cur == "" {
		cur = "usd"
	}
	if cur != "usd" {
		return s.uncredited(p, ref, "the charge settled in %q and this ledger holds usd", cur)
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
		return s.uncredited(p, ref, "the deposit failed: %v", err)
	}
	s.lg.Info("a settled card payment credited the spendable ledger",
		"door", p.door, "via", p.via, "org", p.org,
		"ledger", p.ledger, "subject", p.subject,
		"cents", got.cents, "currency", cur, "test", got.test,
		"settlement", ref, "receipt", id, "entry", entry)
	return nil
}

// uncredited is the ONE answer to "the card was charged and the balance was not".
//
// It is loud in both directions on purpose. The OPERATOR gets a RECONCILE line
// naming the settlement, BOTH orgs and why — the books the charge is in are where the
// receipt is found and the ledger is where the credit belongs, and a manual credit
// needs both — and a silent 200 would leave nobody anything to look for. The
// CUSTOMER gets a 500 and the settlement reference — their own payment's id, which
// the receipt already returns to them — because the honest answer to "did my
// top-up work" is no, and because quoting the reference is what makes support able
// to find the charge. Retrying with the same idempotency key replays the receipt
// and re-runs the deposit, so the customer's own retry is the recovery path.
func (s screen) uncredited(p payment, ref, why string, args ...any) error {
	s.lg.Error("RECONCILE: a card payment settled and the spendable balance was NOT credited",
		"door", p.door, "via", p.via, "org", p.org,
		"ledger", p.ledger, "subject", p.subject,
		"settlement", ref, "why", fmt.Sprintf(why, args...))
	if ref == "" {
		return zip.Errorf(http.StatusInternalServerError,
			"the charge settled but crediting your balance failed — contact support")
	}
	return zip.Errorf(http.StatusInternalServerError,
		"the charge settled but crediting your balance failed — contact support with reference %s", ref)
}
