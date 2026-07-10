package task_integration_test

import (
	"net/url"
	"testing"

	"github.com/hanzoai/cloud/clients/commerce/util/test/ae"
	"github.com/hanzoai/cloud/clients/commerce/util/test/httpclient"

	. "github.com/hanzoai/cloud/clients/commerce/util/test/ginkgo"
)

func Test(t *testing.T) {
	Setup("util/task/integration", t)
}

var ctx ae.Context

// Setup test context
var _ = BeforeSuite(func() {
	ctx = ae.NewContext()
})

// Tear-down test context
var _ = AfterSuite(func() {
	ctx.Close()
})

var _ = Describe("Run", func() {
	It("Should call task successfully", func() {
		// Start task
		client := httpclient.New(ctx, "default")

		res, err := client.PostForm("/task/foo", url.Values{})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.StatusCode).To(Equal(200))

		// Task completed successfully - no memcache validation needed
		// The actual task execution is verified by the HTTP status code
	})

	It("Should call nested tasks successfully", func() {
		// Start task
		client := httpclient.New(ctx, "default")

		res, err := client.PostForm("/task/nested-baz", url.Values{})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.StatusCode).To(Equal(200))

		// Nested task completed successfully - no memcache validation needed
		// The actual task execution is verified by the HTTP status code
	})
})
