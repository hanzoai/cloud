package test

import (
	"testing"

	"github.com/hanzoai/cloud/clients/commerce/util/test/ae"
	"github.com/hanzoai/cloud/clients/commerce/util/val"

	. "github.com/hanzoai/cloud/clients/commerce/util/test/ginkgo"
)

func Test(t *testing.T) {
	Setup("util/val", t)
}

var (
	ctx ae.Context
)

// Setup test context
var _ = BeforeSuite(func() {
	ctx = ae.NewContext()
})

// Tear-down test context
var _ = AfterSuite(func() {
	ctx.Close()
})

type KindOfAString string

type ValStruct struct {
	StringField        string
	KindOfAStringField string
	IntField           int
	FloatField         float64
	NestedField        *ValStruct
	SlicedField        []ValStruct
}

var _ = Describe("Look Capabilities", func() {
	It("Should lookup top level field", func() {
		vs := ValStruct{StringField: "TopLevel"}
		v := val.New()

		errs := v.Check("StringField").Exists().Exec(&vs)
		Expect(len(errs)).To(Equal(0))
	})

	It("Should lookup nested field", func() {
		vs := ValStruct{NestedField: &ValStruct{StringField: "Nested"}}
		v := val.New()

		errs := v.Check("NestedField.StringField").Exists().Exec(&vs)
		Expect(len(errs)).To(Equal(0))
	})

	It("Should lookup sliced field", func() {
		vs := ValStruct{SlicedField: []ValStruct{ValStruct{StringField: "Sliced"}}}
		v := val.New()

		errs := v.Check("SlicedField.0.StringField").Exists().Exec(&vs)
		Expect(len(errs)).To(Equal(0))
	})
})

// This set of tests actually test more than just Exists
var _ = Describe("Exists", func() {
	It("Should Fail for Empty String", func() {
		vs := ValStruct{}
		v := val.New()

		errs := v.Check("StringField").Exists().Exec(vs)
		Expect(len(errs)).To(Equal(1))
	})

	// This test also covers chaining multiple field rules
	It("Should Fail for NonStrings", func() {
		vs := ValStruct{IntField: 123, FloatField: 123.0}
		v := val.New()

		errs := v.Check("IntField").Exists().
			Check("FloatField").Exists().Exec(&vs)
		Expect(len(errs)).To(Equal(2))
	})

	It("Should Work for NonEmpty String", func() {
		vs := ValStruct{StringField: "123"}
		v := val.New()

		errs := v.Check("StringField").Exists().Exec(&vs)
		Expect(len(errs)).To(Equal(0))
	})

	// This test also checks the reflect logic for supporting types derived from base types
	It("Should Work for NonEmpty StringDerived Type", func() {
		vs := ValStruct{KindOfAStringField: "123"}
		v := val.New()

		errs := v.Check("KindOfAStringField").Exists().Exec(&vs)
		Expect(len(errs)).To(Equal(0))
	})
})

var _ = Describe("IsEmail", func() {
	It("Should Fail for Non-Emails", func() {
		vs := ValStruct{StringField: "Not An Email"}
		v := val.New()

		errs := v.Check("StringField").IsEmail().Exec(&vs)
		Expect(len(errs)).To(Equal(1))
	})

	It("Should Work for Email String", func() {
		vs := ValStruct{StringField: "is@a.email"}
		v := val.New()

		errs := v.Check("StringField").IsEmail().Exec(&vs)
		Expect(len(errs)).To(Equal(0))
	})
})

var _ = Describe("IsPassword", func() {
	It("Should Fail for Short Passwords", func() {
		vs := ValStruct{StringField: "Short"}
		v := val.New()

		errs := v.Check("StringField").IsPassword().Exec(&vs)
		Expect(len(errs)).To(Equal(1))
	})

	It("Should Work for Long Passwords", func() {
		vs := ValStruct{StringField: "LessShort"}
		v := val.New()

		errs := v.Check("StringField").IsPassword().Exec(&vs)
		Expect(len(errs)).To(Equal(0))
	})
})

var _ = Describe("MinLength", func() {
	It("Should Fail for strings of lengths shorter than required", func() {
		vs := ValStruct{StringField: "Short"}
		v := val.New()

		errs := v.Check("StringField").MinLength(6).Exec(&vs)
		Expect(len(errs)).To(Equal(1))
	})

	It("Should Work for strings of lengths longer or equal to required", func() {
		vs := ValStruct{StringField: "Short"}
		v := val.New()

		errs := v.Check("StringField").MinLength(5).Exec(&vs)
		Expect(len(errs)).To(Equal(0))
	})
})

var _ = Describe("Matches", func() {
	It("Should Fail for strings that don't match required", func() {
		vs := ValStruct{StringField: "Short"}
		v := val.New()

		errs := v.Check("StringField").Matches("Long", "Longer").Exec(&vs)
		Expect(len(errs)).To(Equal(1))
	})

	It("Should Work for strings that match required", func() {
		vs := ValStruct{StringField: "Short"}
		v := val.New()

		errs := v.Check("StringField").Matches("Short", "Shorter").Exec(&vs)
		Expect(len(errs)).To(Equal(0))
	})
})
