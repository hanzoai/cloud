package platform

import "testing"

// The tag is the version, so the two can never disagree. A registry port must
// not be mistaken for one, and a digest pin carries no version at all.
func TestImageVersion(t *testing.T) {
	for _, c := range []struct{ image, want string }{
		{"ghcr.io/hanzoai/git:v1.26.25", "1.26.25"},
		{"ghcr.io/hanzoai/console:v8.5.29", "8.5.29"},
		{"ghcr.io/hanzoai/git:1.26.25", "1.26.25"},
		{"registry.example.com:5000/team/app:v2.0.0", "2.0.0"},
		{"registry.example.com:5000/team/app", ""},
		{"ghcr.io/hanzoai/git", ""},
	} {
		if got := imageVersion(c.image); got != c.want {
			t.Errorf("imageVersion(%q) = %q, want %q", c.image, got, c.want)
		}
	}
}
