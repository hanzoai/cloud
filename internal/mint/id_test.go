package mint_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/internal/mint"
)

var idRE = regexp.MustCompile(`^run_[0-9a-f]{32}$`)

// TestIDIsPrefixedAndRandom pins the shape twenty-six packages minted by hand and
// the only property that matters about the tail: it differs every time.
func TestIDIsPrefixedAndRandom(t *testing.T) {
	seen := make(map[string]bool, 1000)
	for range 1000 {
		id := mint.ID("run")
		if !idRE.MatchString(id) {
			t.Fatalf("mint.ID(%q) = %q, want run_ + 32 hex", "run", id)
		}
		if seen[id] {
			t.Fatalf("mint.ID repeated %q — the tail is not random", id)
		}
		seen[id] = true
	}
}

// TestIDKeepsThePrefixWhole guards the one thing a caller reads back off an id:
// the prefix it asked for, up to the first separator.
func TestIDKeepsThePrefixWhole(t *testing.T) {
	for _, prefix := range []string{"run", "sync", "wh", "a"} {
		id := mint.ID(prefix)
		got, _, ok := strings.Cut(id, "_")
		if !ok || got != prefix {
			t.Errorf("mint.ID(%q) = %q; prefix reads back as %q", prefix, id, got)
		}
	}
}
