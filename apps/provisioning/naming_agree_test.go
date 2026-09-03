package provisioning

import (
	"testing"

	"github.com/hanzoai/cloud/s3admin"
)

// The lifted copy must answer identically, or two orgs can fold onto one bucket.
func TestLiftedNamingAgrees(t *testing.T) {
	cases := [][2]string{
		{"acme", "photos"}, {"acme", "my-db"}, {"acme-my", "db"},
		{"hanzo", "space"}, {"", "x"}, {"Zoo", "a-b-c"},
	}
	for _, c := range cases {
		if got, want := s3admin.BucketName(c[0], c[1]), BucketName(c[0], c[1]); got != want {
			t.Errorf("BucketName(%q,%q) = %q, provisioning says %q", c[0], c[1], got, want)
		}
	}
	for _, org := range []string{"acme", "hanzo", "", "Zoo"} {
		if got, want := s3admin.BucketPrefix(org), BucketPrefix(org); got != want {
			t.Errorf("BucketPrefix(%q) = %q, provisioning says %q", org, got, want)
		}
	}
}
