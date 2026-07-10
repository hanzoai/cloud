package test

import (
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/models/types/georate"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("models/types/georate", func() {
	Context("Georate.New", func() {
		It("Should Clean Input (City)", func() {
			gr := georate.New("us", "ks", "city", "postal codes", 0, 0, 0.1, 1)
			Expect(gr.Country).To(Equal("US"))
			Expect(gr.State).To(Equal("KS"))
			Expect(gr.City).To(Equal("CITY"))
			Expect(gr.PostalCodes).To(Equal(""))
			Expect(gr.Above).To(Equal(currency.Cents(0)))
			Expect(gr.Below).To(Equal(currency.Cents(0)))
			Expect(gr.Percent).To(Equal(0.1))
			Expect(gr.Cost).To(Equal(currency.Cents(1)))
		})

		It("Should Clean Input (Postal Code)", func() {
			gr := georate.New("us", "ks", "", ",,,postal codes,,,", 0, 0, 0.1, 1)
			Expect(gr.Country).To(Equal("US"))
			Expect(gr.State).To(Equal("KS"))
			Expect(gr.City).To(Equal(""))
			Expect(gr.PostalCodes).To(Equal("POSTALCODES"))
			Expect(gr.Above).To(Equal(currency.Cents(0)))
			Expect(gr.Below).To(Equal(currency.Cents(0)))
			Expect(gr.Percent).To(Equal(0.1))
			Expect(gr.Cost).To(Equal(currency.Cents(1)))
		})

		It("Should Only Accept Real Countries", func() {
			gr := georate.New("zzz", "asv", "city", "postal codes", 0, 0, 0.1, 1)
			Expect(gr.Country).To(Equal(""))
			Expect(gr.State).To(Equal(""))
			Expect(gr.City).To(Equal(""))
			Expect(gr.PostalCodes).To(Equal(""))
			Expect(gr.Above).To(Equal(currency.Cents(0)))
			Expect(gr.Below).To(Equal(currency.Cents(0)))
			Expect(gr.Percent).To(Equal(0.1))
			Expect(gr.Cost).To(Equal(currency.Cents(1)))
		})

		It("Should Only Accept Real Country/State Combos", func() {
			gr := georate.New("us", "asv", "city", "postal codes", 0, 0, 0.1, 1)
			Expect(gr.Country).To(Equal("US"))
			Expect(gr.State).To(Equal(""))
			Expect(gr.City).To(Equal(""))
			Expect(gr.PostalCodes).To(Equal(""))
			Expect(gr.Above).To(Equal(currency.Cents(0)))
			Expect(gr.Below).To(Equal(currency.Cents(0)))
			Expect(gr.Percent).To(Equal(0.1))
			Expect(gr.Cost).To(Equal(currency.Cents(1)))
		})
	})

	Context("Georate.Match", func() {
		It("L-1, L0 (Wild Card) Above Match", func() {
			gr := georate.New("", "", "", "", 50, 0, 0.1, 1)
			isMatch, level := gr.Match("us", "ks", "", "66212", 100)
			Expect(isMatch).To(BeTrue())
			Expect(level).To(Equal(0))
		})

		It("L-1 Not Above Match", func() {
			gr := georate.New("us", "ks", "", "66213", 50, 0, 0.1, 1)
			isMatch, level := gr.Match("gb", "bkm", "", "sl8", 10)
			Expect(isMatch).To(BeFalse())
			Expect(level).To(Equal(-1))
		})

		It("L-1, L0 (Wild Card) Below Match", func() {
			gr := georate.New("", "", "", "", 0, 50, 0.1, 1)
			isMatch, level := gr.Match("us", "ks", "", "66212", 10)
			Expect(isMatch).To(BeTrue())
			Expect(level).To(Equal(0))
		})

		It("L-1 Not Below Match", func() {
			gr := georate.New("us", "ks", "", "66213", 0, 50, 0.1, 1)
			isMatch, level := gr.Match("gb", "bkm", "", "sl8", 100)
			Expect(isMatch).To(BeFalse())
			Expect(level).To(Equal(-1))
		})

		It("L0 (Wild Card) Match", func() {
			gr := georate.New("", "", "", "", 0, 0, 0.1, 1)
			isMatch, level := gr.Match("us", "ks", "", "66212", 100)
			Expect(isMatch).To(BeTrue())
			Expect(level).To(Equal(0))
		})

		It("L0 No Match", func() {
			gr := georate.New("us", "ks", "", "66213", 0, 0, 0.1, 1)
			isMatch, level := gr.Match("gb", "bkm", "", "sl8", 100)
			Expect(isMatch).To(BeFalse())
			Expect(level).To(Equal(0))
		})

		It("L1 (Country) Match", func() {
			gr := georate.New("us", "", "", "", 0, 0, 0.1, 1)
			isMatch, level := gr.Match("us", "ks", "", "66212", 100)
			Expect(isMatch).To(BeTrue())
			Expect(level).To(Equal(1))
		})

		It("L1 (Country) Partial Match", func() {
			gr := georate.New("us", "mo", "", "", 0, 0, 0.1, 1)
			isMatch, level := gr.Match("us", "ks", "", "66212", 100)
			Expect(isMatch).To(BeFalse())
			Expect(level).To(Equal(1))
		})

		It("L2 (Country + State) Match", func() {
			gr := georate.New("us", "ks", "", "", 0, 0, 0.1, 1)
			isMatch, level := gr.Match("us", "ks", "", "66212", 100)
			Expect(isMatch).To(BeTrue())
			Expect(level).To(Equal(2))
		})

		It("L2 (Country + State) Partial Match", func() {
			gr := georate.New("us", "ks", "", "66213", 0, 0, 0.1, 1)
			isMatch, level := gr.Match("us", "ks", "", "66212", 100)
			Expect(isMatch).To(BeFalse())
			Expect(level).To(Equal(2))
		})

		It("L3 (Country + State + City) Match", func() {
			gr := georate.New("us", "ks", "Overland Park", "66212", 0, 0, 0.1, 1)
			isMatch, level := gr.Match("us", "ks", "overland park", "66212", 100)
			Expect(isMatch).To(BeTrue())
			Expect(level).To(Equal(3))
		})

		It("L3 (Country + State + Postal Code) Match", func() {
			gr := georate.New("us", "ks", "", "66212", 0, 0, 0.1, 1)
			isMatch, level := gr.Match("us", "ks", "", "66212", 100)
			Expect(isMatch).To(BeTrue())
			Expect(level).To(Equal(3))
		})

		It("L3 (Country + State + Postal Code List) Match", func() {
			gr := georate.New("us", "ks", "", ",,,66212,66213,66214,,,", 0, 0, 0.1, 1)
			isMatch, level := gr.Match("us", "ks", "", "66212", 100)
			Expect(isMatch).To(BeTrue())
			Expect(level).To(Equal(3))
		})
	})

	Context("Match", func() {
		It("Should Match Match with Highest Level", func() {
			grs := []georate.GeoRate{
				georate.New("us", "ks", "", "", 0, 0, 0.1, 1),
				georate.New("us", "mo", "", "", 0, 0, 0.1, 1),
				georate.New("us", "ks", "emporia", "", 0, 0, 0.1, 1),
				georate.New("us", "ks", "", "66212", 0, 0, 0.1, 1),
				georate.New("us", "", "", "", 0, 0, 0.1, 1),
				georate.New("", "", "", "", 0, 0, 0.1, 1),
			}

			gr, level, idx := georate.Match(grs, "us", "ks", "", "66212", 100)
			Expect(gr).To(Equal(&grs[3]))
			Expect(level).To(Equal(3))
			Expect(idx).To(Equal(3))
		})

		It("Should Return L0 Default Rates", func() {
			grs := []georate.GeoRate{
				georate.New("us", "ks", "", "", 0, 0, 0.1, 1),
				georate.New("us", "mo", "", "", 0, 0, 0.1, 1),
				georate.New("", "", "", "", 0, 0, 0.1, 1),
				georate.New("us", "ks", "emporia", "", 0, 0, 0.1, 1),
				georate.New("us", "ks", "", "66212", 0, 0, 0.1, 1),
				georate.New("us", "", "", "", 0, 0, 0.1, 1),
			}

			gr, level, idx := georate.Match(grs, "gb", "bkm", "", "sl8", 100)
			Expect(gr).To(Equal(&grs[2]))
			Expect(level).To(Equal(0))
			Expect(idx).To(Equal(2))
		})

		It("Should Return L1 Country Rates", func() {
			grs := []georate.GeoRate{
				georate.New("us", "ks", "", "", 0, 0, 0.1, 1),
				georate.New("us", "mo", "", "", 0, 0, 0.1, 1),
				georate.New("", "", "", "", 0, 0, 0.1, 1),
				georate.New("us", "ks", "emporia", "", 0, 0, 0.1, 1),
				georate.New("us", "ks", "", "66212", 0, 0, 0.1, 1),
				georate.New("us", "", "", "", 0, 0, 0.1, 1),
			}

			gr, level, idx := georate.Match(grs, "US", "ky", "", "12345", 100)
			Expect(gr).To(Equal(&grs[5]))
			Expect(level).To(Equal(1))
			Expect(idx).To(Equal(5))
		})

		It("Should Return L2 State Rates", func() {
			grs := []georate.GeoRate{
				georate.New("us", "ks", "", "", 0, 0, 0.1, 1),
				georate.New("us", "mo", "", "", 0, 0, 0.1, 1),
				georate.New("", "", "", "", 0, 0, 0.1, 1),
				georate.New("us", "ks", "emporia", "", 0, 0, 0.1, 1),
				georate.New("us", "ks", "", "66212", 0, 0, 0.1, 1),
				georate.New("us", "", "", "", 0, 0, 0.1, 1),
			}

			gr, level, idx := georate.Match(grs, "us", "kS", "", "12345", 100)
			Expect(gr).To(Equal(&grs[0]))
			Expect(level).To(Equal(2))
			Expect(idx).To(Equal(0))
		})

		It("Should Return L3 City Rates", func() {
			grs := []georate.GeoRate{
				georate.New("us", "ks", "", "", 0, 0, 0.1, 1),
				georate.New("us", "mo", "", "", 0, 0, 0.1, 1),
				georate.New("", "", "", "", 0, 0, 0.1, 1),
				georate.New("us", "ks", "emporia", "", 0, 0, 0.1, 1),
				georate.New("us", "ks", "", "66212", 0, 0, 0.1, 1),
				georate.New("us", "", "", "", 0, 0, 0.1, 1),
			}

			gr, level, idx := georate.Match(grs, "uS", "Ks", "Emporia", "", 100)
			Expect(gr).To(Equal(&grs[3]))
			Expect(level).To(Equal(3))
			Expect(idx).To(Equal(3))
		})

		It("Should Return L3 Postal Code Rates", func() {
			grs := []georate.GeoRate{
				georate.New("us", "ks", "", "", 0, 0, 0.1, 1),
				georate.New("us", "mo", "", "", 0, 0, 0.1, 1),
				georate.New("", "", "", "", 0, 0, 0.1, 1),
				georate.New("us", "ks", "emporia", "", 0, 0, 0.1, 1),
				georate.New("us", "ks", "", "66212", 0, 0, 0.1, 1),
				georate.New("us", "", "", "", 0, 0, 0.1, 1),
			}

			gr, level, idx := georate.Match(grs, "us", "ks", "", "66212", 100)
			Expect(gr).To(Equal(&grs[4]))
			Expect(level).To(Equal(3))
			Expect(idx).To(Equal(4))
		})

		It("Should Fail Without a Default Rate", func() {
			grs := []georate.GeoRate{
				georate.New("us", "ks", "", "", 0, 0, 0.1, 1),
				georate.New("us", "mo", "", "", 0, 0, 0.1, 1),
				georate.New("us", "ks", "emporia", "", 0, 0, 0.1, 1),
				georate.New("us", "ks", "", "66212", 0, 0, 0.1, 1),
				georate.New("us", "", "", "", 0, 0, 0.1, 1),
			}

			gr, level, idx := georate.Match(grs, "gb", "bkm", "", "sl8", 100)
			Expect(gr).To(BeNil())
			Expect(level).To(Equal(-2))
			Expect(idx).To(Equal(-1))
		})
	})
})
