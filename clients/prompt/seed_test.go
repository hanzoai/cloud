package prompt

import (
	"path/filepath"
	"testing"
)

// TestSeedIfEmpty proves the starter catalog seeds an empty org exactly once
// and is idempotent thereafter, and that a user prompt suppresses seeding.
func TestSeedIfEmpty(t *testing.T) {
	st, err := openStore(filepath.Join(t.TempDir(), "prompts.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	cat, err := catalog()
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	if len(cat) == 0 {
		t.Fatal("embedded catalog is empty")
	}

	n, err := st.SeedIfEmpty("acme")
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if n != len(cat) {
		t.Fatalf("seeded %d, want %d", n, len(cat))
	}

	metas, err := st.List("acme")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(metas) != len(cat) {
		t.Fatalf("listed %d prompts, want %d", len(metas), len(cat))
	}

	// Idempotent: a second seed is a no-op.
	if n2, err := st.SeedIfEmpty("acme"); err != nil || n2 != 0 {
		t.Fatalf("second seed: n=%d err=%v, want 0/nil", n2, err)
	}

	// A different org that already has a user prompt must NOT be seeded.
	if _, err := st.Create("beta", PromptVersion{Name: "mine", Prompt: "hi"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if n3, err := st.SeedIfEmpty("beta"); err != nil || n3 != 0 {
		t.Fatalf("seed non-empty org: n=%d err=%v, want 0/nil", n3, err)
	}
}
