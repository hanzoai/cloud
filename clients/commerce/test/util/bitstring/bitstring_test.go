package test

import (
	"testing"

	bs "github.com/hanzoai/cloud/clients/commerce/util/bitstring"

	. "github.com/hanzoai/cloud/clients/commerce/util/test/ginkgo"
)

func Test(t *testing.T) {
	Setup("util/bitstring", t)
}

var b = bs.New(128)
var bShort = bs.New(99)

var _ = Describe("BitString", func() {
	It("Should be able to construct", func() {
		Expect(b.Length).To(Equal(128))
		Expect(b.Segments).To(Equal(uint(2)))

		Expect(bShort.Length).To(Equal(99))
		Expect(bShort.Segments).To(Equal(uint(2)))
	})

	It("Should be able to SetBit/GetBit immutably", func() {
		Expect(b.GetBit(0)).To(BeFalse())
		bTest := b.SetBit(0)
		Expect(b.GetBit(0)).To(BeFalse())
		Expect(bTest.GetBit(0)).To(BeTrue())
	})

	It("Should be able to determine Equality", func() {
		bTest := b.SetBit(0).SetBit(120).SetBit(30)
		Expect(bTest.Equal(b)).To(BeFalse())
		bTest2 := b.SetBit(0).SetBit(120).SetBit(30)
		bTest3 := bShort.SetBit(0).SetBit(120).SetBit(30)
		Expect(bTest.Equal(bTest)).To(BeTrue())
		Expect(bTest.Equal(bTest2)).To(BeTrue())

		// Different length means false
		Expect(bTest.Equal(bTest3)).To(BeFalse())
	})

	It("Should be able to AND", func() {
		bTest := b.SetBit(0).SetBit(120).SetBit(100)
		Expect(bTest.And(b).Equal(b)).To(BeTrue())

		// Should truncate to shorter length
		bTestShort := bShort.SetBit(0).SetBit(120).SetBit(100)
		Expect(bTestShort.And(b).Equal(bShort)).To(BeTrue())
	})

	It("Should be able to Or", func() {
		bTest := b.SetBit(0).SetBit(120).SetBit(100)
		Expect(bTest.Or(b).Equal(bTest)).To(BeTrue())

		// Should truncate to shorter length
		bTestShort := bShort.SetBit(0).SetBit(120).SetBit(100)
		Expect(bTestShort.Or(b).Equal(bTestShort)).To(BeTrue())
	})

	It("Should be able to GetSegment", func() {
		bTest := b.SetBit(3)
		seg := bTest.GetSegment(0)
		Expect(int(seg)).To(Equal(8))
	})
})
