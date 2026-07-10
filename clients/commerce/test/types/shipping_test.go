package test

import (
	"github.com/hanzoai/cloud/clients/commerce/models/product"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"

	. "github.com/hanzoai/cloud/clients/commerce/models/types/productcachedvalues"
	. "github.com/hanzoai/cloud/clients/commerce/models/types/shipping"
	. "github.com/hanzoai/cloud/clients/commerce/models/types/weight"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("models/types/weight", func() {
	Context("GetCost", func() {
		It("should sort formulas by MinWeight", func() {
			rates := Rates{
				Formulas: []Formula{
					Formula{
						MinWeight: Mass(1000),
					},
					Formula{
						MinWeight: Mass(10),
					},
					Formula{
						MinWeight: Mass(100),
					},
				},
			}

			rates.GetPrice(product.New(nil))
			Expect(len(rates.Formulas)).To(Equal(3))
			Expect(rates.Formulas[0].MinWeight).To(Equal(Mass(10)))
			Expect(rates.Formulas[1].MinWeight).To(Equal(Mass(100)))
			Expect(rates.Formulas[2].MinWeight).To(Equal(Mass(1000)))
		})

		It("should calculate using the correct BaseRate", func() {
			rates := Rates{
				Formulas: []Formula{
					Formula{
						MinWeight: Mass(1000),
					},
					Formula{
						MinWeight: Mass(10),
						Price:     currency.Cents(1),
					},
					Formula{
						MinWeight: Mass(100),
					},
				},
				WeightUnit:   Pound,
				Currency:     currency.USD,
				BaseRateType: Flat,
				BasePrice:    currency.Cents(42),
			}

			p := product.Product{
				ProductCachedValues: ProductCachedValues{
					Weight:     1,
					WeightUnit: Pound,
				},
			}

			price, c := rates.GetPrice(&p)

			Expect(price).To(Equal(currency.Cents(42)))
			Expect(c).To(Equal(currency.USD))
		})

		It("should calculate using the correct MinWeight", func() {
			rates := Rates{
				Formulas: []Formula{
					Formula{
						MinWeight: Mass(1000),
						RateType:  Flat,
						Price:     currency.Cents(3),
					},
					Formula{
						MinWeight: Mass(10),
						RateType:  Flat,
						Price:     currency.Cents(1),
					},
					Formula{
						MinWeight: Mass(100),
						RateType:  Flat,
						Price:     currency.Cents(2),
					},
				},
				WeightUnit: Pound,
				Currency:   currency.USD,
			}

			p := product.Product{
				ProductCachedValues: ProductCachedValues{
					Weight:     100,
					WeightUnit: Pound,
				},
			}

			price, c := rates.GetPrice(&p)

			Expect(price).To(Equal(currency.Cents(2)))
			Expect(c).To(Equal(currency.USD))
		})

		It("should calculate using the Variable Rate", func() {
			rates := Rates{
				Formulas: []Formula{
					Formula{
						MinWeight: Mass(1000),
						RateType:  Variable,
						Price:     currency.Cents(3),
					},
					Formula{
						MinWeight: Mass(10),
						RateType:  Flat,
						Price:     currency.Cents(1),
					},
					Formula{
						MinWeight: Mass(100),
						RateType:  Flat,
						Price:     currency.Cents(2),
					},
				},
				WeightUnit: Pound,
				Currency:   currency.USD,
			}

			p := product.Product{
				ProductCachedValues: ProductCachedValues{
					Weight:     10000,
					WeightUnit: Pound,
				},
			}

			price, c := rates.GetPrice(&p)

			Expect(price).To(Equal(currency.Cents(30000)))
			Expect(c).To(Equal(currency.USD))
		})

		It("should be able to convert the Weight", func() {
			rates := Rates{
				Formulas: []Formula{
					Formula{
						MinWeight: Mass(1000),
						RateType:  Flat,
						Price:     currency.Cents(3),
					},
					Formula{
						MinWeight: Mass(10),
						RateType:  Flat,
						Price:     currency.Cents(1),
					},
					Formula{
						MinWeight: Mass(100),
						RateType:  Flat,
						Price:     currency.Cents(2),
					},
				},
				WeightUnit: Kilogram,
				Currency:   currency.USD,
			}

			p := product.Product{
				ProductCachedValues: ProductCachedValues{
					Weight:     10000,
					WeightUnit: Gram,
				},
			}

			price, c := rates.GetPrice(&p)

			Expect(price).To(Equal(currency.Cents(1)))
			Expect(c).To(Equal(currency.USD))
		})
	})
})
