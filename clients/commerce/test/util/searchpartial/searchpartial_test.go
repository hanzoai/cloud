package test

import (
	"testing"

	partial "github.com/hanzoai/cloud/clients/commerce/util/searchpartial"

	. "github.com/hanzoai/cloud/clients/commerce/util/test/ginkgo"
)

func Test(t *testing.T) {
	Setup("util/searchpartial", t)
}

var _ = Describe("SearchPartial", func() {
	It("Should be able to decompose a word", func() {
		str := partial.Partials("alongword")

		Expect(str).To(Equal("alongword alo lon ong ngw gwo wor ord alon long ongw ngwo gwor word along longw ongwo ngwor gword alongw longwo ongwor ngword alongwo longwor ongword alongwor longword"))
	})

	It("Should be able to decompose a unicode word", func() {
		str := partial.Partials("Gołębiewski")

		Expect(str).To(Equal("Gołębiewski Goł ołę łęb ębi bie iew ews wsk ski Gołę ołęb łębi ębie biew iews ewsk wski Gołęb ołębi łębie ębiew biews iewsk ewski Gołębi ołębie łębiew ębiews biewsk iewski Gołębie ołębiew łębiews ębiewsk biewski Gołębiew ołębiews łębiewsk ębiewski Gołębiews ołębiewsk łębiewski Gołębiewsk ołębiewski"))
	})
})
