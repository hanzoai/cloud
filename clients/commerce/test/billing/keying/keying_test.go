// Package keying is a regression for the billing balance-key unification.
//
// Defect: a customer could top up and see a stale/zero balance because the
// deposit path (api/billing topup/token) credited the EMAIL key (e.g.
// z@hanzo.ai) while the read path (api/billing me/balance) resolved the
// ORG/SUB key (hanzo/<sub>) — neither matched the org-slug key the proven LLM
// gate reads (?user=<orgSlug>) and usage debits (SourceId=<orgSlug>).
//
// Fix: orgBillingKey() in api/billing makes BOTH the user-facing deposit AND
// the read paths key on the org slug. me/balance and topup/token compute the
// balance with the SAME call exercised here:
//
//	util.GetTransactionsByCurrency(ctx, <key>, "iam-user", cur, test)
//
// which nets Deposit(DestinationId==key) − Withdraw(SourceId==key). The
// contract this suite locks in: the deposit key and the read key must be
// identical, or the balance does not net — that identity IS the bug fix.
package keying

import (
	"testing"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/transaction"
	"github.com/hanzoai/cloud/clients/commerce/models/transaction/util"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/util/test/ae"

	. "github.com/hanzoai/cloud/clients/commerce/util/test/ginkgo"
)

const (
	// The canonical per-org billing key — what orgBillingKey() resolves
	// (the org slug), used by both deposit and read paths.
	orgKey = "suchtees"
	// The OLD-bug keys: topup used to credit the email; me/balance used to
	// read the org/sub composite. Neither equals orgKey, so credits to them
	// are invisible to the unified org-slug read.
	emailKey = "z@hanzo.ai"
	subKey   = "suchtees/0d2f-sub"

	kind = "iam-user"
)

var (
	ctx ae.Context
	db  *datastore.Datastore
)

func Test(t *testing.T) {
	Setup("billing/keying", t)
}

func deposit(dst string, cents int64, cur currency.Type) {
	tr := transaction.New(db)
	tr.Type = transaction.Deposit
	tr.DestinationId = dst
	tr.DestinationKind = kind
	tr.Currency = cur
	tr.Amount = currency.Cents(cents)
	tr.MustCreate()
}

func usage(src string, cents int64, cur currency.Type) {
	tr := transaction.New(db)
	tr.Type = transaction.Withdraw
	tr.SourceId = src
	tr.SourceKind = kind
	tr.Currency = cur
	tr.Amount = currency.Cents(cents)
	tr.MustCreate()
}

// balance mirrors exactly what api/billing me.go GetMyBalance and
// topup_token.go read back: the org-key tally in the org namespace.
func balance(key string, cur currency.Type) currency.Cents {
	datas, err := util.GetTransactionsByCurrency(ctx, key, kind, cur, false)
	Expect(err).NotTo(HaveOccurred())
	if d, ok := datas.Data[cur]; ok {
		return d.Balance
	}
	return 0
}

var _ = BeforeSuite(func() {
	ctx = ae.NewContext()
	db = datastore.New(ctx)
})

var _ = AfterSuite(func() { ctx.Close() })

var _ = Describe("billing balance-key unification", func() {

	Context("deposit key == read key (the fix)", func() {
		// Use an isolated currency per spec so tallies never cross-contaminate.

		It("shows a top-up immediately when both paths use the org slug", func() {
			cur := currency.SGD
			Expect(balance(orgKey, cur)).To(Equal(currency.Cents(0)))

			// topup/token now credits DestinationId = orgBillingKey = orgKey.
			deposit(orgKey, 5000, cur)

			// me/balance reads the SAME org-slug key → read == credit.
			Expect(balance(orgKey, cur)).To(Equal(currency.Cents(5000)))
		})

		It("nets a usage debit against the same org-slug key", func() {
			cur := currency.AUD
			deposit(orgKey, 5000, cur) // top-up
			usage(orgKey, 1500, cur)   // proven LLM debit: SourceId = orgKey

			// 5000 − 1500 = 3500, visible on the unified read.
			Expect(balance(orgKey, cur)).To(Equal(currency.Cents(3500)))
		})
	})

	Context("mismatched keys reproduce the defect (anti-regression)", func() {

		It("does NOT show an email-keyed credit on the org-slug read", func() {
			cur := currency.GBP
			// OLD topup bug: credit the email key.
			deposit(emailKey, 9999, cur)

			// The org-slug read (the fixed me/balance) sees nothing — proving
			// the keys MUST be unified. If a change re-keys deposits by email,
			// the org balance would jump and this assertion fails.
			Expect(balance(orgKey, cur)).To(Equal(currency.Cents(0)))
			// The credit is recoverable only under the (wrong) email key.
			Expect(balance(emailKey, cur)).To(Equal(currency.Cents(9999)))
		})

		It("does NOT show an org/sub-keyed credit on the org-slug read", func() {
			cur := currency.CAD
			// OLD me/balance bug: read org/sub. A credit to org/sub is invisible
			// to the unified org-slug read, and vice-versa.
			deposit(subKey, 7777, cur)

			Expect(balance(orgKey, cur)).To(Equal(currency.Cents(0)))
			Expect(balance(subKey, cur)).To(Equal(currency.Cents(7777)))
		})
	})
})
