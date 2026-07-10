package test

import (
	"math/big"

	. "github.com/hanzoai/cloud/clients/commerce/models/types/currency"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("models/types/currency", func() {
	Context("Type.IsZero", func() {
		It("Should return true for certain currencies and false for others", func() {
			Expect(JPY.IsZeroDecimal()).To(BeTrue())
			Expect(USD.IsZeroDecimal()).To(BeFalse())
		})
	})

	Context("Type.ToString/Type.Symbol/Type.TopStringNoSymbol", func() {
		It("Should render positive currency cents correctly", func() {
			// Non Zero Decimal
			s1 := USD.ToString(Cents(10000))
			Expect(s1).To(Equal("$100.00"))
			sym1 := USD.Symbol()
			noSym1 := USD.ToStringNoSymbol(Cents(10000))
			Expect(s1).To(Equal(sym1 + noSym1))

			// Zero Decimal
			s2 := JPY.ToString(Cents(10000))
			Expect(s2).To(Equal("¥10000"))
			sym2 := JPY.Symbol()
			noSym2 := JPY.ToStringNoSymbol(Cents(10000))
			Expect(s2).To(Equal(sym2 + noSym2))
		})

		It("Should render negative currency cents correctly", func() {
			// Non Zero Decimal
			Expect(USD.ToString(Cents(-10000))).To(Equal("-$100.00"))
			Expect(USD.ToStringNoSymbol(Cents(-10000))).To(Equal("-100.00"))

			// Zero Decimal
			Expect(JPY.ToString(Cents(-10000))).To(Equal("-¥10000"))
			Expect(JPY.ToStringNoSymbol(Cents(-10000))).To(Equal("-10000"))
		})
	})

	Context("Type.ToFloat", func() {
		It("Should render positive currency cents correctly", func() {
			// Non Zero Decimal
			Expect(USD.ToFloat(Cents(10023))).To(Equal(100.23))

			// Zero Decimal
			Expect(JPY.ToFloat(Cents(10023))).To(Equal(10023.0))
		})

		It("Should render negative currency cents correctly", func() {
			// Non Zero Decimal
			Expect(USD.ToFloat(Cents(-10023))).To(Equal(-100.23))

			// Zero Decimal
			Expect(JPY.ToFloat(Cents(-10023))).To(Equal(-10023.0))
		})
	})

	Context("Type.Label/Type.Code", func() {
		It("Should render Label and Code correctly", func() {
			Expect(USD.Label()).To(Equal("$ USD"))
			Expect(USD.Code()).To(Equal("USD"))
		})
	})

	// Crypto-Land

	Context("Type.IsCrypto", func() {
		It("Should render detect crypto-ness correctly", func() {
			Expect(USD.IsCrypto()).To(BeFalse())
			Expect(BTC.IsCrypto()).To(BeTrue())
		})
	})

	Context("Type.MinimalUnitFactor", func() {
		It("Should get conversion factor correctly", func() {
			Expect(USD.MinimalUnitFactor().Cmp(big.NewInt(1)) == 0).To(BeTrue())
			Expect(ETH.MinimalUnitFactor().Cmp(big.NewInt(1e9)) == 0).To(BeTrue())
		})
	})

	Context("Type.ToMinimalUnits", func() {
		It("Should convert correctly", func() {
			Expect(USD.ToMinimalUnits(Cents(1)).Cmp(big.NewInt(1)) == 0).To(BeTrue())
			Expect(ETH.ToMinimalUnits(Cents(1)).Cmp(big.NewInt(1e9)) == 0).To(BeTrue())
		})
	})

	Context("Type.FromMinimalUnits", func() {
		It("Should convert correctly", func() {
			Expect(USD.FromMinimalUnits(big.NewInt(1))).To(Equal(Cents(1)))
			Expect(ETH.FromMinimalUnits(big.NewInt(1e9))).To(Equal(Cents(1)))
		})

		It("Should truncate really small values", func() {
			Expect(ETH.FromMinimalUnits(big.NewInt(1e6))).To(Equal(Cents(0)))
		})
	})
})
