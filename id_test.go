package cloud_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
)

var idRE = regexp.MustCompile(`^run_[0-9a-f]{32}$`)

// TestIDIsPrefixedAndRandom pins the shape twenty-six packages minted by hand and
// the only property that matters about the tail: it differs every time.
func TestIDIsPrefixedAndRandom(t *testing.T) {
	seen := make(map[string]bool, 1000)
	for range 1000 {
		id := cloud.ID("run")
		if !idRE.MatchString(id) {
			t.Fatalf("cloud.ID(%q) = %q, want run_ + 32 hex", "run", id)
		}
		if seen[id] {
			t.Fatalf("cloud.ID repeated %q — the tail is not random", id)
		}
		seen[id] = true
	}
}

// TestIDKeepsThePrefixWhole guards the one thing a caller reads back off an id:
// the prefix it asked for, up to the first separator.
func TestIDKeepsThePrefixWhole(t *testing.T) {
	for _, prefix := range []string{"run", "sync", "wh", "a"} {
		id := cloud.ID(prefix)
		got, _, ok := strings.Cut(id, "_")
		if !ok || got != prefix {
			t.Errorf("cloud.ID(%q) = %q; prefix reads back as %q", prefix, id, got)
		}
	}
}
