package catalog_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/hanzoai/cloud/piece/catalog"
	"github.com/hanzoai/cloud/script"
)

// The generated files agree with each other: every piece marked runnable has a
// program, every program belongs to a runnable piece, and every logo the
// catalog names is there.
func TestCatalogAgreesWithItsFiles(t *testing.T) {
	c, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.Source == "" || c.Count < 700 {
		t.Fatalf("catalog source %q count %d", c.Source, c.Count)
	}
	runnable := map[string]bool{}
	for _, p := range c.Pieces {
		if p.Runnable {
			runnable[p.ID] = true
			if _, ok := catalog.Program(p.ID); !ok {
				t.Errorf("%s is runnable and has no program", p.ID)
			}
		}
		if p.Logo != "" {
			if _, _, ok := catalog.Logo(p.ID); !ok {
				t.Errorf("%s names logo %s, which is not there", p.ID, p.Logo)
			}
		}
		if p.Name == "" {
			t.Errorf("%s has no name", p.ID)
		}
	}
	// A generated program belongs to a runnable piece. A hand-written one
	// (<id>.js, not <id>.provider.js) serves a narrower need of its own — the
	// knowledge sync's Notion reads — and is never regenerated or removed.
	for _, id := range catalog.Programs() {
		if _, err := os.Stat(filepath.Join("program", id+".js")); err == nil {
			continue
		}
		if !runnable[id] {
			t.Errorf("program %s belongs to no runnable piece", id)
		}
	}
}

// Every vendored program loads under the runtime shim alone — the property
// that made it runnable — so a shim change that breaks one is caught here and
// not by the org whose flow it was.
func TestEveryProgramLoads(t *testing.T) {
	for _, id := range catalog.Programs() {
		b, _ := catalog.Program(id)
		if err := script.Loads(id, b); err != nil {
			t.Errorf("%s: %v", id, err)
		}
	}
}
