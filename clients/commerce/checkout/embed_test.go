// Pinning tests for the embedded pay UI bundle. These test the contract
// between hanzoai/pay (SPA source) and hanzoai/commerce (embedding host):
// the Dockerfile's pay-build stage MUST produce an index.html and at
// least one hashed asset, and the go:embed directive MUST resolve them.
//
// When run on a fresh clone with only .gitkeep in checkout/ui/dist, these
// tests are skipped — the build hasn't happened yet. Once the pay-build
// stage runs (Docker) or a developer overlays dist/ locally, the tests
// enforce the shape.
package checkout

import (
	"io/fs"
	"strings"
	"testing"
)

// TestUISub_Resolves confirms the go:embed fs.Sub on "ui/dist" returns a
// non-nil filesystem. Guards against future refactors breaking the embed
// directive.
func TestUISub_Resolves(t *testing.T) {
	f := UISub()
	if f == nil {
		t.Fatal("UISub() returned nil")
	}
}

// TestEmbeddedPayUI_HasIndex checks that the Vite build output is
// present. Skipped when only the .gitkeep placeholder exists (fresh
// clone, build not yet run).
func TestEmbeddedPayUI_HasIndex(t *testing.T) {
	f := UISub()
	file, err := f.Open("index.html")
	if err != nil {
		t.Skipf("no embedded index.html — run pay-build stage first: %v", err)
	}
	defer file.Close()
	st, err := file.Stat()
	if err != nil {
		t.Fatalf("stat index.html: %v", err)
	}
	if st.Size() < 50 {
		t.Errorf("index.html size=%d, too small to be a real bundle", st.Size())
	}
}

// TestEmbeddedPayUI_HasHashedAssets confirms Vite's hashed-asset output
// is present. Every deploy lands at least one JS chunk under assets/.
// Skipped if the build hasn't run.
func TestEmbeddedPayUI_HasHashedAssets(t *testing.T) {
	f := UISub()
	if _, err := f.Open("index.html"); err != nil {
		t.Skipf("no embedded bundle: %v", err)
	}
	var jsCount, cssCount int
	_ = fs.WalkDir(f, "assets", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		switch {
		case strings.HasSuffix(path, ".js"):
			jsCount++
		case strings.HasSuffix(path, ".css"):
			cssCount++
		}
		return nil
	})
	if jsCount == 0 {
		t.Error("expected at least one JS chunk under assets/, found none")
	}
	// CSS chunk is produced by Tailwind — not strictly required if a fork
	// drops styling, so just log.
	if cssCount == 0 {
		t.Log("no CSS chunk under assets/ — expected for style-less forks")
	}
}
