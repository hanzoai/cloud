package integrations

import "testing"

func TestOurs(t *testing.T) {
	for _, c := range []struct {
		labels []string
		ours   bool
	}{
		{labels: []string{"linux-amd64"}, ours: true},
		{labels: []string{"linux-arm64"}, ours: true},
		{labels: []string{"hanzo-build-linux-amd64"}, ours: true},
		{labels: []string{"self-hosted"}, ours: true},
		{labels: []string{"Linux-AMD64"}, ours: true},
		{labels: []string{"ubuntu-latest"}},
		{labels: []string{"ubuntu-24.04"}},
		{labels: []string{"windows-latest"}},
		{labels: []string{"macos-14"}},
		{labels: nil},
		// macOS and Windows hosts are real machines on the forge, not pods this
		// path can mint.
		{labels: []string{"macos-arm64"}},
		{labels: []string{"win-amd64"}},
	} {
		if got := ours(c.labels); got != c.ours {
			t.Errorf("ours(%v) = %v, want %v", c.labels, got, c.ours)
		}
	}
}
