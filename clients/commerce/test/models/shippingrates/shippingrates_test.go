package test

import (
	"testing"

	"github.com/hanzoai/cloud/clients/commerce/models/shippingrates"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/models/types/georate"
	"github.com/hanzoai/cloud/clients/commerce/util/fake"

	. "github.com/hanzoai/cloud/clients/commerce/util/test/ginkgo"
)

func Test(t *testing.T) {
	Setup("models/shippingrates", t)
}

var _ = Describe("models/shippingrates", func() {
	Context("Match", func() {
		It("Should Match Match with Highest Level", func() {
			grs := shippingrates.ShippingRates{
				GeoRates: []shippingrates.GeoRate{
					{
						GeoRate:      georate.New("us", "ks", "", "", currency.Cents(10), currency.Cents(100), 0.1, currency.Cents(1)),
						ShippingName: fake.Characters(16),
					},
					{
						GeoRate:      georate.New("us", "mo", "", "", currency.Cents(10), currency.Cents(100), 0.1, currency.Cents(1)),
						ShippingName: fake.Characters(16),
					},
					{
						GeoRate:      georate.New("us", "ks", "emporia", "", currency.Cents(10), currency.Cents(100), 0.1, currency.Cents(1)),
						ShippingName: fake.Characters(16),
					},
					{
						GeoRate:      georate.New("us", "ks", "", "66212", currency.Cents(10), currency.Cents(100), 0.1, currency.Cents(1)),
						ShippingName: fake.Characters(16),
					},
					{
						GeoRate:      georate.New("us", "", "", "", currency.Cents(10), currency.Cents(100), 0.1, currency.Cents(1)),
						ShippingName: fake.Characters(16),
					},
					{
						GeoRate:      georate.New("", "", "", "", currency.Cents(10), currency.Cents(100), 0.1, currency.Cents(1)),
						ShippingName: fake.Characters(16),
					},
				},
			}

			gr, level, idx := grs.Match("us", "ks", "", "66212", currency.Cents(0))
			Expect(gr).To(Equal(&grs.GeoRates[3]))
			Expect(level).To(Equal(3))
			Expect(idx).To(Equal(3))
		})
		It("Should Return L0 Default Rates", func() {
			grs := shippingrates.ShippingRates{
				GeoRates: []shippingrates.GeoRate{
					{
						GeoRate:      georate.New("us", "ks", "", "", currency.Cents(10), currency.Cents(100), 0.1, currency.Cents(1)),
						ShippingName: fake.Characters(16),
					},
					{
						GeoRate:      georate.New("us", "mo", "", "", currency.Cents(10), currency.Cents(100), 0.1, currency.Cents(1)),
						ShippingName: fake.Characters(16),
					},
					{
						GeoRate:      georate.New("", "", "", "", currency.Cents(10), currency.Cents(100), 0.1, currency.Cents(1)),
						ShippingName: fake.Characters(16),
					},
					{
						GeoRate:      georate.New("us", "ks", "emporia", "", currency.Cents(10), currency.Cents(100), 0.1, currency.Cents(1)),
						ShippingName: fake.Characters(16),
					},
					{
						GeoRate:      georate.New("us", "ks", "", "66212", currency.Cents(10), currency.Cents(100), 0.1, currency.Cents(1)),
						ShippingName: fake.Characters(16),
					},
					{
						GeoRate:      georate.New("us", "", "", "", currency.Cents(10), currency.Cents(100), 0.1, currency.Cents(1)),
						ShippingName: fake.Characters(16),
					},
				},
			}

			gr, level, idx := grs.Match("gb", "bkm", "", "sl8", currency.Cents(0))
			Expect(gr).To(Equal(&grs.GeoRates[2]))
			Expect(level).To(Equal(0))
			Expect(idx).To(Equal(2))
		})

		It("Should Return L1 Country Rates", func() {
			grs := shippingrates.ShippingRates{
				GeoRates: []shippingrates.GeoRate{
					{

						GeoRate:      georate.New("us", "ks", "", "", currency.Cents(10), currency.Cents(100), 0.1, currency.Cents(1)),
						ShippingName: fake.Characters(16),
					},
					{

						GeoRate:      georate.New("us", "mo", "", "", currency.Cents(10), currency.Cents(100), 0.1, currency.Cents(1)),
						ShippingName: fake.Characters(16),
					},
					{

						GeoRate:      georate.New("", "", "", "", currency.Cents(10), currency.Cents(100), 0.1, currency.Cents(1)),
						ShippingName: fake.Characters(16),
					},
					{

						GeoRate:      georate.New("us", "ks", "emporia", "", currency.Cents(10), currency.Cents(100), 0.1, currency.Cents(1)),
						ShippingName: fake.Characters(16),
					},
					{

						GeoRate:      georate.New("us", "ks", "", "66212", currency.Cents(10), currency.Cents(100), 0.1, currency.Cents(1)),
						ShippingName: fake.Characters(16),
					},
					{

						GeoRate:      georate.New("us", "", "", "", currency.Cents(10), currency.Cents(100), 0.1, currency.Cents(1)),
						ShippingName: fake.Characters(16),
					},
				},
			}

			gr, level, idx := grs.Match("US", "ky", "", "12345", currency.Cents(0))
			Expect(gr).To(Equal(&grs.GeoRates[5]))
			Expect(level).To(Equal(1))
			Expect(idx).To(Equal(5))

		})
		It("Should Return L2 State Rates", func() {
			grs := shippingrates.ShippingRates{
				GeoRates: []shippingrates.GeoRate{
					{
						GeoRate:      georate.New("us", "ks", "", "", currency.Cents(10), currency.Cents(100), 0.1, currency.Cents(1)),
						ShippingName: fake.Characters(16),
					},
					{
						GeoRate:      georate.New("us", "mo", "", "", currency.Cents(10), currency.Cents(100), 0.1, currency.Cents(1)),
						ShippingName: fake.Characters(16),
					},
					{
						GeoRate:      georate.New("", "", "", "", currency.Cents(10), currency.Cents(100), 0.1, currency.Cents(1)),
						ShippingName: fake.Characters(16),
					},
					{
						GeoRate:      georate.New("us", "ks", "emporia", "", currency.Cents(10), currency.Cents(100), 0.1, currency.Cents(1)),
						ShippingName: fake.Characters(16),
					},
					{
						GeoRate:      georate.New("us", "ks", "", "66212", currency.Cents(10), currency.Cents(100), 0.1, currency.Cents(1)),
						ShippingName: fake.Characters(16),
					},
					{
						GeoRate:      georate.New("us", "", "", "", currency.Cents(10), currency.Cents(100), 0.1, currency.Cents(1)),
						ShippingName: fake.Characters(16),
					},
				},
			}

			gr, level, idx := grs.Match("us", "kS", "", "12345", currency.Cents(0))
			Expect(gr).To(Equal(&grs.GeoRates[0]))
			Expect(level).To(Equal(2))
			Expect(idx).To(Equal(0))
		})
		It("Should Return L3 City Rates", func() {
			grs := shippingrates.ShippingRates{
				GeoRates: []shippingrates.GeoRate{
					{
						GeoRate:      georate.New("us", "ks", "", "", currency.Cents(10), currency.Cents(100), 0.1, currency.Cents(1)),
						ShippingName: fake.Characters(16),
					},
					{
						GeoRate:      georate.New("us", "mo", "", "", currency.Cents(10), currency.Cents(100), 0.1, currency.Cents(1)),
						ShippingName: fake.Characters(16),
					},
					{
						GeoRate:      georate.New("", "", "", "", currency.Cents(10), currency.Cents(100), 0.1, currency.Cents(1)),
						ShippingName: fake.Characters(16),
					},
					{
						GeoRate:      georate.New("us", "ks", "emporia", "", currency.Cents(10), currency.Cents(100), 0.1, currency.Cents(1)),
						ShippingName: fake.Characters(16),
					},
					{
						GeoRate:      georate.New("us", "ks", "", "66212", currency.Cents(10), currency.Cents(100), 0.1, currency.Cents(1)),
						ShippingName: fake.Characters(16),
					},
					{
						GeoRate:      georate.New("us", "", "", "", currency.Cents(10), currency.Cents(100), 0.1, currency.Cents(1)),
						ShippingName: fake.Characters(16),
					},
				},
			}

			gr, level, idx := grs.Match("uS", "Ks", "Emporia", "", currency.Cents(0))
			Expect(gr).To(Equal(&grs.GeoRates[3]))
			Expect(level).To(Equal(3))
			Expect(idx).To(Equal(3))
		})
		It("Should Return L3 Postal Code Rates", func() {
			grs := shippingrates.ShippingRates{
				GeoRates: []shippingrates.GeoRate{
					{
						GeoRate:      georate.New("us", "ks", "", "", currency.Cents(10), currency.Cents(100), 0.1, currency.Cents(1)),
						ShippingName: fake.Characters(16),
					},
					{
						GeoRate:      georate.New("us", "mo", "", "", currency.Cents(10), currency.Cents(100), 0.1, currency.Cents(1)),
						ShippingName: fake.Characters(16),
					},
					{
						GeoRate:      georate.New("", "", "", "", currency.Cents(10), currency.Cents(100), 0.1, currency.Cents(1)),
						ShippingName: fake.Characters(16),
					},
					{
						GeoRate:      georate.New("us", "ks", "emporia", "", currency.Cents(10), currency.Cents(100), 0.1, currency.Cents(1)),
						ShippingName: fake.Characters(16),
					},
					{
						GeoRate:      georate.New("us", "ks", "", "66212", currency.Cents(10), currency.Cents(100), 0.1, currency.Cents(1)),
						ShippingName: fake.Characters(16),
					},
					{
						GeoRate:      georate.New("us", "", "", "", currency.Cents(10), currency.Cents(100), 0.1, currency.Cents(1)),
						ShippingName: fake.Characters(16),
					},
				},
			}

			gr, level, idx := grs.Match("us", "ks", "", "66212", currency.Cents(0))
			Expect(gr).To(Equal(&grs.GeoRates[4]))
			Expect(level).To(Equal(3))
			Expect(idx).To(Equal(4))
		})
		It("Should Fail Without a Default Rate", func() {
			grs := shippingrates.ShippingRates{
				GeoRates: []shippingrates.GeoRate{
					{
						GeoRate:      georate.New("us", "ks", "", "", currency.Cents(10), currency.Cents(100), 0.1, currency.Cents(1)),
						ShippingName: fake.Characters(16),
					},
					{
						GeoRate:      georate.New("us", "mo", "", "", currency.Cents(10), currency.Cents(100), 0.1, currency.Cents(1)),
						ShippingName: fake.Characters(16),
					},
					{
						GeoRate:      georate.New("us", "ks", "emporia", "", currency.Cents(10), currency.Cents(100), 0.1, currency.Cents(1)),
						ShippingName: fake.Characters(16),
					},
					{
						GeoRate:      georate.New("us", "ks", "", "66212", currency.Cents(10), currency.Cents(100), 0.1, currency.Cents(1)),
						ShippingName: fake.Characters(16),
					},
					{
						GeoRate:      georate.New("us", "", "", "", currency.Cents(10), currency.Cents(100), 0.1, currency.Cents(1)),
						ShippingName: fake.Characters(16),
					},
				},
			}

			gr, level, idx := grs.Match("gb", "bkm", "", "sl8", currency.Cents(0))
			Expect(gr).To(BeNil())
			Expect(level).To(Equal(-1))
			Expect(idx).To(Equal(-1))
		})
	})
})
