package test

import (
	"sort"

	. "github.com/hanzoai/cloud/clients/commerce/models/types/country"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("models/types/country", func() {
	Context("Countries", func() {
		It("should be populated", func() {
			Expect(len(Countries)).To(Equal(249))
		})

		It("should be sorted", func() {
			cs := make([]string, 0)
			for _, c := range Countries {
				cs = append(cs, c.Name.Common)
			}

			Expect(sort.StringsAreSorted(cs)).To(BeTrue())
		})

		It("should be able to find a country", func() {
			c, err := FindByISO3166_2("US")

			Expect(err).ToNot(HaveOccurred())
			Expect(c.Name.Common).To(Equal("United States"))
		})

		It("should be able to find a country with oddcase", func() {
			c, err := FindByISO3166_2("Us")

			Expect(err).ToNot(HaveOccurred())
			Expect(c.Name.Common).To(Equal("United States"))
		})
	})

	Context("ByISOCodeISO3166_2", func() {
		It("should be able to find a subdivision by name", func() {
			c, err := FindByISO3166_2("US")

			Expect(err).ToNot(HaveOccurred())

			sd, err := c.FindSubDivision("Florida")

			Expect(err).ToNot(HaveOccurred())

			Expect(sd.Code).To(Equal("FL"))
			Expect(sd.Name).To(Equal("Florida"))
		})

		It("should be able to find a subdivision by code", func() {
			c, err := FindByISO3166_2("us")

			Expect(err).ToNot(HaveOccurred())

			sd, err := c.FindSubDivision("fl")

			Expect(err).ToNot(HaveOccurred())

			Expect(sd.Code).To(Equal("FL"))
			Expect(sd.Name).To(Equal("Florida"))
		})
	})
})
