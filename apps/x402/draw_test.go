package x402

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/money"
	protocol "github.com/hanzoai/commerce/payment/x402"
)

// A payment the payer's wallet cannot cover is refused with nothing moved: no
// debit, no credit, and no claim left for Reconcile to take the moment the payer
// next funds its wallet. The same authorization settles once the wallet covers it.
//
// It pins the finding: the payer's half was a metered debit, which lands whatever
// the wallet holds, so an org with nothing on its books paid any sum into a
// colluding payee's ledger.
func TestAPaymentTheWalletCannotCoverMovesNothing(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		h := newHarness(t)
		price := funded.Add(money.FromCents(1)) // a cent more than the payer holds
		pubRegistry(map[string]Terms{"/paid/tool": {Amount: price, RecipientOrg: h.payeeOrg, RecipientWalletID: h.recipient}})
		req := h.challenge("/paid/tool", h.payerOrg)
		now := nowUnix()
		payment := h.pay(req, randNonce(), now-60, now+300)

		code, b, _ := h.req(http.MethodGet, "/paid/tool", h.payerOrg, payment, "")
		if code != http.StatusPaymentRequired || !strings.Contains(string(b), protocol.ReasonInsufficientFunds) {
			t.Fatalf("a payment past the wallet = %d (%s), want 402 %s", code, b, protocol.ReasonInsufficientFunds)
		}
		if strings.Contains(string(b), `"served":true`) {
			t.Fatal("the resource was served unpaid")
		}
		if got := h.debited(); got.Sign() != 0 {
			t.Fatalf("the payer was debited %s for a refused payment", got)
		}
		if got := h.credited(); got.Sign() != 0 {
			t.Fatalf("the payee was credited %s by a payer who could not pay", got)
		}
		if n, err := Reconcile(context.Background(), 0); err != nil || n != 0 {
			t.Fatalf("reconcile completed %d (%v): a refused payment left a claim to finish", n, err)
		}
		if _, err := h.ledger.FinanceClient.Deposit(context.Background(), depositUSD(h.payerOrg, h.payerOrg, money.FromCents(1), "top-up")); err != nil {
			t.Fatal(err)
		}
		if n, err := Reconcile(context.Background(), 0); err != nil || n != 0 {
			t.Fatalf("a top-up let reconcile take the refused payment: %d (%v)", n, err)
		}
		if got := h.credited(); got.Sign() != 0 {
			t.Fatalf("the payee was credited %s for a payment that was refused", got)
		}
		// The payer covers it now, and presents the same authorization again.
		if code, b, _ := h.req(http.MethodGet, "/paid/tool", h.payerOrg, payment, ""); code != 200 {
			t.Fatalf("the same payment, covered = %d (%s), want 200", code, b)
		}
		if got := h.credited(); got.Cmp(price) != 0 {
			t.Fatalf("payee credited %s, want %s", got, price)
		}
		if got := h.balance(h.payerOrg, h.payerOrg); got.Sign() != 0 {
			t.Fatalf("payer holds %s, want nothing left", got)
		}
	})
}

// A payment agreed before it is made names the payee address it was signed to.
// The payee rotating its wallet's keys in between does not strand it: terms that
// pin that address settle to the same wallet, while a payment priced now names
// the address the wallet has now.
func TestAPinnedPayeeOutlivesARotation(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		h := newHarness(t)
		terms := Terms{Amount: money.FromCents(100), RecipientOrg: h.payeeOrg, RecipientWalletID: h.recipient}
		pubRegistry(map[string]Terms{"/paid/tool": terms, "/paid/other": terms})
		signed := h.challenge("/paid/tool", h.payerOrg) // what a job's buyer signs when the job opens
		unpinned := h.challenge("/paid/other", h.payerOrg)

		hr, b, _ := h.req(http.MethodPost, "/v1/wallet/"+h.recipient+"/keys", h.payeeOrg, "", "{}")
		var w struct {
			Address string `json:"address"`
		}
		if hr != 200 || json.Unmarshal(b, &w) != nil || strings.EqualFold(w.Address, signed.PayTo) {
			t.Fatalf("rotate = %d (%s), want a new address", hr, b)
		}
		pinned := terms
		pinned.PayTo = signed.PayTo
		pubRegistry(map[string]Terms{"/paid/tool": pinned, "/paid/other": terms})

		if code, b, _ := h.payFor("/paid/other", h.payerOrg, unpinned, randNonce()); code != http.StatusPaymentRequired ||
			!strings.Contains(string(b), protocol.ReasonRecipientMismatch) {
			t.Fatalf("a payment to the old address, priced now = %d (%s), want 402 %s", code, b, protocol.ReasonRecipientMismatch)
		}
		if code, b, _ := h.payFor("/paid/tool", h.payerOrg, signed, randNonce()); code != 200 {
			t.Fatalf("a payment to the pinned address = %d (%s), want 200", code, b)
		}
		h.tie(money.FromCents(100), "after the pinned payment")
	})
}

// A claim the binary before this one left in doubt — its payer's half posted as a
// metered debit under the settlement id, its credit never landed — is finished by
// this one's reconcile as the payment it already is: the payee is credited, and
// the payer is charged once, not drawn again under the same settlement.
func TestAClaimLeftAsUsageIsFinishedOnce(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		h := newHarness(t)
		price := money.FromCents(100)
		pubRegistry(map[string]Terms{"/paid/tool": {Amount: price, RecipientOrg: h.payeeOrg, RecipientWalletID: h.recipient}})
		req := h.challenge("/paid/tool", h.payerOrg)
		now := nowUnix()
		payment := h.pay(req, randNonce(), now-60, now+300)
		p, err := protocol.ParsePayment(payment)
		if err != nil {
			t.Fatal(err)
		}
		a := p.Payload.Authorization
		st := &Settlement{ID: settlementID(a.From, a.Nonce), PayerOrg: h.payerOrg, From: a.From, Nonce: a.Nonce,
			Resource: "/paid/tool", Payee: req.PayTo, PayeeOrg: h.payeeOrg, PayeeSubject: h.recipient,
			Amount: price.AttoString(), Network: req.Network, SettledVia: "ledger", CreatedAt: now}
		if ok, _, err := live.State.store.claim(context.Background(), st); err != nil || !ok {
			t.Fatalf("claim: %v %v", ok, err)
		}
		if err := h.ledger.FinanceClient.RecordUsage(context.Background(), money.UsageInput{Org: h.payerOrg, Subject: h.payerOrg,
			Amount: price, Currency: "usd", Provider: providerLabel, Service: providerLabel, Model: st.Resource, Ref: st.ID}); err != nil {
			t.Fatal(err)
		}
		if n, err := Reconcile(context.Background(), 0); err != nil || n != 1 {
			t.Fatalf("reconcile completed %d (%v), want the one claim", n, err)
		}
		h.tie(price, "after the claim left as usage was finished")
		if code, b, _ := h.req(http.MethodGet, "/paid/tool", h.payerOrg, payment, ""); code != http.StatusOK {
			t.Fatalf("the payer's retry = %d (%s), want the receipt", code, b)
		}
		h.tie(price, "after the payer's retry")
	})
}
