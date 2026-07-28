package agents

import (
	"context"
	"testing"

	luxlog "github.com/luxfi/log"

	"github.com/hanzoai/cloud"
)

// mountSeedTest wires the `mounted` singleton to a fresh store with a default
// model, so SeedPersonalities has both a store to write and a model to attach.
func mountSeedTest(t *testing.T, defaultModel string) {
	t.Helper()
	prev := mounted
	mounted = &cloud.Service[state]{
		Base:  cloud.Base{Log: luxlog.New("test")},
		State: state{store: testStore(t), defaultModel: defaultModel},
	}
	t.Cleanup(func() { mounted = prev })
}

// TestSeedPersonalities proves the built-in crew is created once, is idempotent,
// projects the exact @-handles a human mentions in Team, and no-ops without a
// model — the full contract of the one-way seed.
func TestSeedPersonalities(t *testing.T) {
	mountSeedTest(t, "zen-1")
	ctx := context.Background()
	const org = "acme"

	// First seed creates the whole crew.
	n, err := SeedPersonalities(ctx, org)
	if err != nil {
		t.Fatalf("SeedPersonalities: %v", err)
	}
	if n != len(personalities) {
		t.Fatalf("created %d, want %d (the full built-in crew)", n, len(personalities))
	}

	// They are ordinary registry rows — ListForOrg returns them with the @-handles.
	list, err := ListForOrg(ctx, org)
	if err != nil {
		t.Fatalf("ListForOrg: %v", err)
	}
	got := map[string]Agent{}
	for _, a := range list {
		got[a.Name] = a
	}
	for _, want := range []string{"dev", "des", "vi"} {
		a, ok := got[want]
		if !ok {
			t.Fatalf("@%s not seeded; have %v", want, keysOf(got))
		}
		if a.Model != "zen-1" || a.Status != "ready" || a.Instructions == "" {
			t.Fatalf("@%s malformed: model=%q status=%q instr=%dB", want, a.Model, a.Status, len(a.Instructions))
		}
	}

	// Idempotent: a re-seed creates nothing and never duplicates.
	n2, err := SeedPersonalities(ctx, org)
	if err != nil {
		t.Fatalf("re-seed: %v", err)
	}
	if n2 != 0 {
		t.Fatalf("re-seed created %d, want 0 (idempotent)", n2)
	}
	if list2, _ := ListForOrg(ctx, org); len(list2) != len(personalities) {
		t.Fatalf("after re-seed: %d agents, want %d (no dup)", len(list2), len(personalities))
	}

	// No default model → no-op, never a half-seeded org.
	mountSeedTest(t, "")
	if n3, err := SeedPersonalities(ctx, "globex"); err != nil || n3 != 0 {
		t.Fatalf("no-model seed = (%d,%v), want (0,nil)", n3, err)
	}
}

func keysOf(m map[string]Agent) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
