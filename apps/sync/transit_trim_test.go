package sync

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The cap evicts the least recently used repositories first, stops as soon as
// the rest fit, and never removes one an advance holds.
func TestTransitKeepsTheRecentWithinItsCap(t *testing.T) {
	parent := t.TempDir()
	put := func(org, repo string, size int, used time.Time) string {
		dir := filepath.Join(parent, "transit", org, repo+".git")
		if err := os.MkdirAll(filepath.Join(dir, "objects"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "objects", "pack"), make([]byte, size), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(dir, used, used); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	now := time.Now()
	oldest := put("hanzoai", "hanzoai_old", 400, now.Add(-3*time.Hour))
	busy := put("hanzoai", "hanzoai_busy", 400, now.Add(-2*time.Hour))
	middle := put("lux", "luxfi_mid", 400, now.Add(-time.Hour))
	newest := put("lux", "luxfi_new", 400, now)

	gate := gateFor("hanzoai/hanzoai_busy")
	gate <- struct{}{}
	removed, left := trimTransit(parent, 900)
	<-gate

	if removed != 2 || left != 800 {
		t.Fatalf("removed %d, left %d bytes; want 2 and 800", removed, left)
	}
	for dir, want := range map[string]bool{oldest: false, busy: true, middle: false, newest: true} {
		if _, err := os.Stat(dir); (err == nil) != want {
			t.Errorf("%s present = %v, want %v", filepath.Base(dir), err == nil, want)
		}
	}
	if removed, left := trimTransit(parent, 900); removed != 0 || left != 800 {
		t.Fatalf("a second trim under the cap removed %d (left %d)", removed, left)
	}
}
