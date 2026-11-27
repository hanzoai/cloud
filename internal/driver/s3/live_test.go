package s3

import (
	"testing"

	"github.com/hanzoai/cloud/sdk/object"
	"github.com/hanzoai/cloud/sdk/object/objecttest"
)

// TestConformanceAgainstTheConfiguredStore runs the SDK's conformance suite against
// the object store the environment configures (S3_ADMIN_*, S3_PUBLIC_ENDPOINT), the
// same way production does. With none configured there is nothing to run against
// and the case is skipped; the fake in fake_test.go answers the gate.
func TestConformanceAgainstTheConfiguredStore(t *testing.T) {
	a := New()
	if !a.Configured() {
		t.Skip("no object store configured (S3_ADMIN_ACCESS_KEY)")
	}
	s, err := Open(a)
	if err != nil {
		t.Fatal(err)
	}
	var _ object.Delegator = s.(object.Delegator)
	objecttest.Run(t, s, objecttest.Options{Conditional: true, Links: a.PresignConfigured()})
}
