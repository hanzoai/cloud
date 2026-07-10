package test

import (
	. "github.com/hanzoai/cloud/clients/commerce/models/types/weight"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("models/types/weight", func() {
	Context("Convert", func() {
		It("should convert between lb and oz", func() {
			lbs := Mass(1)

			ozs := Convert(lbs, Pound, Ounce)
			Expect(ozs).To(Equal(Mass(16)))

			lbs = 123214
			lbs = Convert(ozs, Ounce, Pound)
			Expect(lbs).To(Equal(Mass(1)))
		})

		It("should convert between kg and g", func() {
			kgs := Mass(1)

			gs := Convert(kgs, Kilogram, Gram)
			Expect(gs).To(Equal(Mass(1000)))

			kgs = 123214
			kgs = Convert(gs, Gram, Kilogram)
			Expect(kgs).To(Equal(Mass(1)))
		})

		It("should convert between metric and imperial", func() {
			kgs := Mass(1)

			lbs := Convert(kgs, Kilogram, Pound)
			Expect(lbs).To(Equal(Mass(2.2046244201837775)))

			kgs = 123214
			kgs = Convert(lbs, Pound, Kilogram)
			Expect(kgs).To(Equal(Mass(1)))
		})
	})
})
