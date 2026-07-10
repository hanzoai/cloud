package test

import (
	"testing"

	"github.com/hanzoai/cloud/clients/commerce/util/task"
	. "github.com/hanzoai/cloud/clients/commerce/util/test/ginkgo"
)

func Test(t *testing.T) {
	Setup("util/task", t)
}

var _ = Describe("Register", func() {
	It("Should add task to registry", func() {
		t := new(task.Task)
		t.Name = "foo"

		Expect(len(task.Registry["foo"])).To(Equal(0))
		task.Register("foo", t)
		Expect(len(task.Registry["foo"])).To(Equal(1))
	})

	It("Should append additional tasks when name is re-used", func() {
		t := new(task.Task)
		t.Name = "bar"

		task.Register("bar", t)
		Expect(len(task.Registry["bar"])).To(Equal(1))

		task.Register("bar", t)
		Expect(len(task.Registry["bar"])).To(Equal(2))

		for _, v := range task.Registry["bar"] {
			Expect(v.Name).To(Equal("bar"))
		}
	})

	It("Should append multiple tasks at once", func() {
		t := new(task.Task)
		t.Name = "baz"

		task.Register("baz", t, t, t)
		Expect(len(task.Registry["baz"])).To(Equal(3))

		for _, v := range task.Registry["baz"] {
			Expect(v.Name).To(Equal("baz"))
		}
	})
})
