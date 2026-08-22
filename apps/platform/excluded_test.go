package platform

import "testing"

// The rule this pins used to live in prose above runnerBuild and in nothing
// executable: ghcr.io/hanzoai/cloud is numbered by a compare-and-swap in its own
// release lane, and a second allocator reading the same registry cannot reserve
// anything the first one honours. A comment holds only while it is read.
func TestTheImageWithItsOwnAllocatorIsNotPublishedHere(t *testing.T) {
	for _, ref := range []string{
		"ghcr.io/hanzoai/cloud",
		"ghcr.io/hanzoai/cloud:v1.801.362",
		"ghcr.io/hanzoai/cloud@sha256:5282acc5cf6b45d978e3fbe4aada055b42586e3e94adaf0e56a93ff02b88259f",
		"ghcr.io/hanzoai/cloud:v1.801.362@sha256:5282acc5cf6b45d978e3fbe4aada055b42586e3e94adaf0e56a93ff02b88259f",
	} {
		if !imageExcluded(ref) {
			t.Errorf("%s was publishable — a version tag is not a different image", ref)
		}
	}

	// Neighbours in the same namespace are ordinary. The rule is about one
	// allocator, not about the namespace being special.
	for _, ref := range []string{
		"ghcr.io/hanzoai/cloudflare",
		"ghcr.io/hanzoai/cloud-docs:latest",
		"ghcr.io/hanzoai/console:v8.5.91",
		"ghcr.io/luxfi/cloud:v1.0.0",
	} {
		if imageExcluded(ref) {
			t.Errorf("%s was refused — only the one image with its own allocator is", ref)
		}
	}
}

// A tag colon and a host-port colon look alike and mean opposite things. Reading
// the wrong one truncates the ref to the host and the rule stops matching.
func TestRepositoryDropsTheVersionAndNothingElse(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"ghcr.io/hanzoai/cloud", "ghcr.io/hanzoai/cloud"},
		{"ghcr.io/hanzoai/cloud:v1", "ghcr.io/hanzoai/cloud"},
		{"ghcr.io/hanzoai/cloud@sha256:abc", "ghcr.io/hanzoai/cloud"},
		{"registry:5000/hanzoai/cloud", "registry:5000/hanzoai/cloud"},
		{"registry:5000/hanzoai/cloud:v1", "registry:5000/hanzoai/cloud"},
	} {
		if got := repository(tc.in); got != tc.want {
			t.Errorf("repository(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
