package test

import (
	"testing"

	"github.com/hanzoai/cloud/clients/commerce/util/bit"
	"github.com/hanzoai/cloud/clients/commerce/util/permission"

	. "github.com/hanzoai/cloud/clients/commerce/util/test/ginkgo"
)

func Test(t *testing.T) {
	Setup("util/bit", t)
}

var _ = Describe("Permission", func() {
	It("All should pass for any permission", func() {
		field := new(bit.Field)
		field.Set(permission.All)

		Expect(field.Has(permission.Admin)).To(Equal(true))
		Expect(field.Has(permission.Charge)).To(Equal(true))
		Expect(field.Has(permission.Capture)).To(Equal(true))
	})

	It("None should never pass", func() {
		field := new(bit.Field)
		field.Set(permission.None)

		Expect(field.Has(permission.Admin)).To(Equal(false))
		Expect(field.Has(permission.Charge)).To(Equal(false))
		Expect(field.Has(permission.Capture)).To(Equal(false))
	})

	It("Should be able to build set of permissions from None", func() {
		permissions := permission.None
		masks := []bit.Mask{permission.Charge, permission.Capture}
		for _, mask := range masks {
			permissions |= mask
		}

		field := new(bit.Field)
		field.Set(permissions)

		Expect(field.Has(permission.Admin)).To(Equal(false))
		Expect(field.Has(permission.Charge)).To(Equal(true))
		Expect(field.Has(permission.Capture)).To(Equal(true))
	})
})
