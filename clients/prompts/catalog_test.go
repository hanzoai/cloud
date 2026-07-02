package prompts

import "testing"

// TestStarterCatalog proves the embedded starter library is non-empty and that
// every entry it offers could be imported through the create handler (its name,
// reserved status, and size all satisfy the same guards), so the browse→import
// path can never surface a starter the API would reject.
func TestStarterCatalog(t *testing.T) {
	entries, err := starterCatalog()
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("embedded starter catalog is empty")
	}
	seen := map[string]bool{}
	for _, e := range entries {
		if e.Name == "" || reserved[e.Name] || !nameRE.MatchString(e.Name) {
			t.Fatalf("catalog entry %q would be rejected by create handler", e.Name)
		}
		if len(e.Prompt) == 0 || len(e.Prompt) > maxContent {
			t.Fatalf("catalog entry %q has invalid content length %d", e.Name, len(e.Prompt))
		}
		if seen[e.Name] {
			t.Fatalf("duplicate catalog name %q", e.Name)
		}
		seen[e.Name] = true
	}
}
