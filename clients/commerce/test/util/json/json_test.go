package test

import (
	"bytes"
	"testing"

	"github.com/hanzoai/cloud/clients/commerce/util/json"

	. "github.com/hanzoai/cloud/clients/commerce/util/test/ginkgo"
)

func Test(t *testing.T) {
	Setup("util/json", t)
}

type Cart struct {
	Codes []string
}

type ReadCloser struct {
	*bytes.Buffer
}

func (cb *ReadCloser) Close() error {
	return nil
}

var _ = Describe("json.EncodeBytes", func() {
	It("Should be able to decode JSON", func() {
		car := Cart{[]string{"A", "B", "C"}}
		b := json.EncodeBytes(car)
		json.DecodeBytes(b, &car)
		json.DecodeBytes(b, &car)
		json.DecodeBytes(b, &car)
		Expect(len(car.Codes)).To(Equal(3))
	})
})

var _ = Describe("json.Encode", func() {
	It("Should be able to decode JSON", func() {
		car := Cart{[]string{"A", "B", "C"}}
		s := json.Encode(car)
		cb := &ReadCloser{bytes.NewBufferString(s)}
		json.Decode(cb, &car)
		json.Decode(cb, &car)
		json.Decode(cb, &car)
		Expect(len(car.Codes)).To(Equal(3))
	})
})
