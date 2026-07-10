package test

import (
	"testing"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/models/referral"
	"github.com/hanzoai/cloud/clients/commerce/models/referralprogram"
	"github.com/hanzoai/cloud/clients/commerce/models/referrer"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/models/user"
	"github.com/hanzoai/cloud/clients/commerce/util/test/ae"

	. "github.com/hanzoai/cloud/clients/commerce/util/test/ginkgo"
)

func Test(t *testing.T) {
	Setup("models/referrer", t)
}

var (
	ctx  ae.Context
	db   *datastore.Datastore
	nsDb *datastore.Datastore
)

// Setup test context
var _ = BeforeSuite(func() {
	ctx = ae.NewContext()
	db = datastore.New(ctx)
})

// Tear-down test context
var _ = AfterSuite(func() {
	ctx.Close()
})

var _ = Describe("Referrer", func() {
	Context("SaveReferral", func() {
		var org *organization.Organization

		Before(func() {
			org = organization.New(db)
			org.MustCreate()

		})

		It("should work with referral triggers", func() {
			usr := user.Fake(db)
			usr.MustCreate()

			prog := referralprogram.New(db)
			prog.Actions = []referralprogram.Action{
				{
					Trigger: referralprogram.Trigger{
						Type: referralprogram.ReferralsGreaterThanOrEquals,
						ReferralsGreaterThanOrEqualsTrigger: referralprogram.ReferralsGreaterThanOrEqualsTrigger{
							ReferralsGreaterThanOrEquals: 1,
						},
					},
					Name: "test",
					Type: referralprogram.StoreCredit,
					CreditAction: referralprogram.CreditAction{
						Currency: currency.USD,
						Amount:   currency.Cents(7),
					},
				},
			}
			prog.MustCreate()

			rfr := referrer.Fake(db, usr.Id())
			rfr.ProgramId = prog.Id()
			rfr.MustCreate()

			rfl, err := rfr.SaveReferral(ctx, org.Id(), referral.NewOrder, usr, usr.Id(), true)
			Expect(err).ToNot(HaveOccurred())
			rfl.MustCreate()

			err = usr.LoadReferrals()
			Expect(err).ToNot(HaveOccurred())
			Expect(len(usr.Referrals)).To(Equal(1))

			err = usr.CalculateBalances(true)
			Expect(err).ToNot(HaveOccurred())
			Expect(usr.Transactions[currency.USD].Balance).To(Equal(currency.Cents(7)))

			rfl, err = rfr.SaveReferral(ctx, org.Id(), referral.NewOrder, usr, usr.Id(), true)
			Expect(err).ToNot(HaveOccurred())
			rfl.MustCreate()

			err = usr.LoadReferrals()
			Expect(err).ToNot(HaveOccurred())
			Expect(len(usr.Referrals)).To(Equal(2))

			err = usr.CalculateBalances(true)
			Expect(err).ToNot(HaveOccurred())
			Expect(usr.Transactions[currency.USD].Balance).To(Equal(currency.Cents(14)))
		})

		It("should work with multiple referral triggers", func() {
			usr := user.Fake(db)
			usr.MustCreate()

			prog := referralprogram.New(db)
			prog.Actions = []referralprogram.Action{
				{
					Trigger: referralprogram.Trigger{
						Type: referralprogram.Always,
					},
					Name: "test",
					Type: referralprogram.StoreCredit,
					CreditAction: referralprogram.CreditAction{
						Currency: currency.USD,
						Amount:   currency.Cents(7),
					},
				},
				{
					Trigger: referralprogram.Trigger{
						Type: referralprogram.Always,
					},
					Name: "test2",
					Type: referralprogram.StoreCredit,
					CreditAction: referralprogram.CreditAction{
						Currency: currency.USD,
						Amount:   currency.Cents(7),
					},
				},
			}
			prog.MustCreate()

			rfr := referrer.Fake(db, usr.Id())
			rfr.ProgramId = prog.Id()
			rfr.MustCreate()

			rfl, err := rfr.SaveReferral(ctx, org.Id(), referral.NewOrder, usr, usr.Id_, true)
			Expect(err).ToNot(HaveOccurred())
			rfl.MustCreate()

			err = usr.LoadReferrals()
			Expect(err).ToNot(HaveOccurred())
			Expect(len(usr.Referrals)).To(Equal(1))

			err = usr.CalculateBalances(true)
			Expect(err).ToNot(HaveOccurred())
			Expect(usr.Transactions[currency.USD].Balance).To(Equal(currency.Cents(14)))
		})

		It("should not fire everytime for referral triggers", func() {
			usr := user.Fake(db)
			usr.MustCreate()

			prog := referralprogram.New(db)
			prog.Actions = []referralprogram.Action{
				{
					Trigger: referralprogram.Trigger{
						Type: referralprogram.ReferralsGreaterThanOrEquals,
						ReferralsGreaterThanOrEqualsTrigger: referralprogram.ReferralsGreaterThanOrEqualsTrigger{
							ReferralsGreaterThanOrEquals: 2,
						},
					},
					Name: "test",
					Type: referralprogram.StoreCredit,
					CreditAction: referralprogram.CreditAction{
						Currency: currency.USD,
						Amount:   currency.Cents(7),
					},
				},
			}
			prog.MustCreate()

			rfr := referrer.Fake(db, usr.Id())
			rfr.ProgramId = prog.Id()
			rfr.MustCreate()

			rfl, err := rfr.SaveReferral(ctx, org.Id(), referral.NewOrder, usr, usr.Id_, true)
			Expect(err).ToNot(HaveOccurred())
			rfl.MustCreate()

			err = usr.LoadReferrals()
			Expect(err).ToNot(HaveOccurred())
			Expect(len(usr.Referrals)).To(Equal(1))

			err = usr.CalculateBalances(true)
			Expect(err).ToNot(HaveOccurred())
			Expect(usr.Transactions[currency.USD]).To(BeNil())

			rfl, err = rfr.SaveReferral(ctx, org.Id(), referral.NewOrder, usr, usr.Id_, true)
			Expect(err).ToNot(HaveOccurred())
			rfl.MustCreate()

			err = usr.LoadReferrals()
			Expect(err).ToNot(HaveOccurred())
			Expect(len(usr.Referrals)).To(Equal(2))

			err = usr.CalculateBalances(true)
			Expect(err).ToNot(HaveOccurred())
			Expect(usr.Transactions[currency.USD].Balance).To(Equal(currency.Cents(7)))
		})

		It("should work with referral triggers once", func() {
			usr := user.Fake(db)
			usr.MustCreate()

			prog := referralprogram.New(db)
			prog.Actions = []referralprogram.Action{
				{
					Trigger: referralprogram.Trigger{
						Type: referralprogram.ReferralsGreaterThanOrEquals,
						ReferralsGreaterThanOrEqualsTrigger: referralprogram.ReferralsGreaterThanOrEqualsTrigger{
							ReferralsGreaterThanOrEquals: 1,
						},
					},
					Name: "test",
					Type: referralprogram.StoreCredit,
					Once: true,
					CreditAction: referralprogram.CreditAction{
						Currency: currency.USD,
						Amount:   currency.Cents(7),
					},
				},
			}
			prog.MustCreate()

			rfr := referrer.Fake(db, usr.Id())
			rfr.ProgramId = prog.Id()
			rfr.MustCreate()

			rfl, err := rfr.SaveReferral(ctx, org.Id(), referral.NewOrder, usr, usr.Id_, true)
			Expect(err).ToNot(HaveOccurred())
			rfl.MustCreate()

			err = usr.LoadReferrals()
			Expect(err).ToNot(HaveOccurred())
			Expect(len(usr.Referrals)).To(Equal(1))

			err = usr.CalculateBalances(true)
			Expect(err).ToNot(HaveOccurred())
			Expect(usr.Transactions[currency.USD].Balance).To(Equal(currency.Cents(7)))

			rfl, err = rfr.SaveReferral(ctx, org.Id(), referral.NewOrder, usr, usr.Id_, true)
			Expect(err).ToNot(HaveOccurred())
			rfl.MustCreate()

			err = usr.LoadReferrals()
			Expect(err).ToNot(HaveOccurred())
			Expect(len(usr.Referrals)).To(Equal(2))

			Expect(usr.Referrers[0].State["test_done"].(bool)).To(Equal(true))

			err = usr.CalculateBalances(true)
			Expect(err).ToNot(HaveOccurred())
			Expect(usr.Transactions[currency.USD].Balance).To(Equal(currency.Cents(7)))
		})

		It("should work with balance triggers", func() {
			usr := user.Fake(db)
			usr.MustCreate()

			prog := referralprogram.New(db)
			prog.Actions = []referralprogram.Action{
				{
					Trigger: referralprogram.Trigger{
						Type: referralprogram.CreditGreaterThanOrEquals,
						CreditGreaterThanOrEqualsTrigger: referralprogram.CreditGreaterThanOrEqualsTrigger{
							CreditGreaterThanOrEquals: 1,
							Currency:                  currency.USD,
						},
					},
					Name: "test",
					Type: referralprogram.StoreCredit,
					CreditAction: referralprogram.CreditAction{
						Currency: currency.USD,
						Amount:   currency.Cents(7),
					},
				},
			}
			prog.MustCreate()

			rfr := referrer.Fake(db, usr.Id())
			rfr.ProgramId = prog.Id()
			rfr.MustCreate()

			rfl, err := rfr.SaveReferral(ctx, org.Id(), referral.NewOrder, usr, usr.Id_, true)
			Expect(err).ToNot(HaveOccurred())
			rfl.MustCreate()

			err = usr.LoadReferrals()
			Expect(err).ToNot(HaveOccurred())
			Expect(len(usr.Referrals)).To(Equal(1))

			err = usr.CalculateBalances(true)
			Expect(err).ToNot(HaveOccurred())
			Expect(usr.Transactions[currency.USD].Balance).To(Equal(currency.Cents(7)))

			rfl, err = rfr.SaveReferral(ctx, org.Id(), referral.NewOrder, usr, usr.Id_, true)
			Expect(err).ToNot(HaveOccurred())
			rfl.MustCreate()

			err = usr.LoadReferrals()
			Expect(err).ToNot(HaveOccurred())
			Expect(len(usr.Referrals)).To(Equal(2))

			err = usr.CalculateBalances(true)
			Expect(err).ToNot(HaveOccurred())
			Expect(usr.Transactions[currency.USD].Balance).To(Equal(currency.Cents(14)))
		})

		It("should not fire everytime for balance triggers", func() {
			usr := user.Fake(db)
			usr.MustCreate()

			prog := referralprogram.New(db)
			prog.Actions = []referralprogram.Action{
				{
					Trigger: referralprogram.Trigger{
						Type: referralprogram.CreditGreaterThanOrEquals,
						CreditGreaterThanOrEqualsTrigger: referralprogram.CreditGreaterThanOrEqualsTrigger{
							CreditGreaterThanOrEquals: 8,
							Currency:                  currency.USD,
						},
					},
					Name: "test",
					Type: referralprogram.StoreCredit,
					CreditAction: referralprogram.CreditAction{
						Currency: currency.USD,
						Amount:   currency.Cents(7),
					},
				},
			}
			prog.MustCreate()

			rfr := referrer.Fake(db, usr.Id())
			rfr.ProgramId = prog.Id()
			rfr.MustCreate()

			rfl, err := rfr.SaveReferral(ctx, org.Id(), referral.NewOrder, usr, usr.Id_, true)
			Expect(err).ToNot(HaveOccurred())
			rfl.MustCreate()

			err = usr.LoadReferrals()
			Expect(err).ToNot(HaveOccurred())
			Expect(len(usr.Referrals)).To(Equal(1))

			err = usr.CalculateBalances(true)
			Expect(err).ToNot(HaveOccurred())
			Expect(usr.Transactions[currency.USD]).To(BeNil())

			rfl, err = rfr.SaveReferral(ctx, org.Id(), referral.NewOrder, usr, usr.Id_, true)
			Expect(err).ToNot(HaveOccurred())
			rfl.MustCreate()

			err = usr.LoadReferrals()
			Expect(err).ToNot(HaveOccurred())
			Expect(len(usr.Referrals)).To(Equal(2))

			err = usr.CalculateBalances(true)
			Expect(err).ToNot(HaveOccurred())
			Expect(usr.Transactions[currency.USD]).To(BeNil())
		})

		It("should work with balance triggers once", func() {
			usr := user.Fake(db)
			usr.MustCreate()

			prog := referralprogram.New(db)
			prog.Actions = []referralprogram.Action{
				{
					Trigger: referralprogram.Trigger{
						Type: referralprogram.ReferralsGreaterThanOrEquals,
						CreditGreaterThanOrEqualsTrigger: referralprogram.CreditGreaterThanOrEqualsTrigger{
							CreditGreaterThanOrEquals: 7,
							Currency:                  currency.USD,
						},
					},
					Name: "test",
					Type: referralprogram.StoreCredit,
					Once: true,
					CreditAction: referralprogram.CreditAction{
						Currency: currency.USD,
						Amount:   currency.Cents(7),
					},
				},
			}
			prog.MustCreate()

			rfr := referrer.Fake(db, usr.Id())
			rfr.ProgramId = prog.Id()
			rfr.MustCreate()

			rfl, err := rfr.SaveReferral(ctx, org.Id(), referral.NewOrder, usr, usr.Id_, true)
			Expect(err).ToNot(HaveOccurred())
			rfl.MustCreate()

			err = usr.LoadReferrals()
			Expect(err).ToNot(HaveOccurred())
			Expect(len(usr.Referrals)).To(Equal(1))

			err = usr.CalculateBalances(true)
			Expect(err).ToNot(HaveOccurred())
			Expect(usr.Transactions[currency.USD].Balance).To(Equal(currency.Cents(7)))

			rfl, err = rfr.SaveReferral(ctx, org.Id(), referral.NewOrder, usr, usr.Id_, true)
			Expect(err).ToNot(HaveOccurred())
			rfl.MustCreate()

			err = usr.LoadReferrals()
			Expect(err).ToNot(HaveOccurred())
			Expect(len(usr.Referrals)).To(Equal(2))

			Expect(usr.Referrers[0].State["test_done"].(bool)).To(Equal(true))

			err = usr.CalculateBalances(true)
			Expect(err).ToNot(HaveOccurred())
			Expect(usr.Transactions[currency.USD].Balance).To(Equal(currency.Cents(7)))
		})

		// Deprecate soon
		It("should work with old balance triggers", func() {
			usr := user.Fake(db)
			usr.MustCreate()

			prog := referralprogram.New(db)
			prog.Triggers = []int{0}
			prog.Actions = []referralprogram.Action{
				{
					Name: "test",
					Type: referralprogram.StoreCredit,
					CreditAction: referralprogram.CreditAction{
						Currency: currency.USD,
						Amount:   currency.Cents(7),
					},
				},
			}
			prog.MustCreate()

			rfr := referrer.Fake(db, usr.Id())
			rfr.ProgramId = prog.Id()
			rfr.MustCreate()

			rfl, err := rfr.SaveReferral(ctx, org.Id(), referral.NewOrder, usr, usr.Id_, true)
			Expect(err).ToNot(HaveOccurred())
			rfl.MustCreate()

			err = usr.LoadReferrals()
			Expect(err).ToNot(HaveOccurred())
			Expect(len(usr.Referrals)).To(Equal(1))

			err = usr.CalculateBalances(true)
			Expect(err).ToNot(HaveOccurred())
			Expect(usr.Transactions[currency.USD].Balance).To(Equal(currency.Cents(7)))

			rfl, err = rfr.SaveReferral(ctx, org.Id(), referral.NewOrder, usr, usr.Id_, true)
			Expect(err).ToNot(HaveOccurred())
			rfl.MustCreate()

			err = usr.LoadReferrals()
			Expect(err).ToNot(HaveOccurred())
			Expect(len(usr.Referrals)).To(Equal(2))

			err = usr.CalculateBalances(true)
			Expect(err).ToNot(HaveOccurred())
			Expect(usr.Transactions[currency.USD].Balance).To(Equal(currency.Cents(14)))
		})

	})
})
