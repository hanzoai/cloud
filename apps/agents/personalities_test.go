package agents

import (
	"context"
	"maps"
	"slices"
	"testing"

	luxlog "github.com/luxfi/log"

	"github.com/hanzoai/cloud"
)

// mountSeedTest wires the `mounted` singleton to a fresh store, so
// SeedPersonalities has a store to write. It took a default model until that
// knob was deleted; the seed model is cloud.DefaultModel and cannot vary.
func mountSeedTest(t *testing.T) {
	t.Helper()
	prev := mounted
	mounted = &cloud.Service[state]{
		Base:  cloud.Base{Log: luxlog.New("test")},
		State: state{stores: testStores(t)},
	}
	t.Cleanup(func() { mounted = prev })
}

// TestSeedPersonalities proves the built-in crew is created once, is idempotent,
// projects the exact @-handles a human mentions in Team, and seeds every persona
// on cloud.DefaultModel — the full contract of the one-way seed.
func TestSeedPersonalities(t *testing.T) {
	mountSeedTest(t)
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
			t.Fatalf("@%s not seeded; have %v", want, slices.Collect(maps.Keys(got)))
		}
		if a.Model != cloud.DefaultModel || a.Status != "ready" || a.Instructions == "" {
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

	// A fresh org seeds its full crew, every persona on cloud.DefaultModel. This
	// asserted the opposite — that an empty default model made the seed a no-op —
	// which was reachable only by hand-building the state with an empty field. A
	// deployment always had a default, so the no-op never happened in production
	// and now cannot be expressed at all.
	mountSeedTest(t)
	n3, err := SeedPersonalities(ctx, "globex")
	if err != nil || n3 != len(personalities) {
		t.Fatalf("fresh-org seed = (%d,%v), want (%d,nil)", n3, err, len(personalities))
	}
	for _, a := range mustList(t, ctx, "globex") {
		if a.Model != cloud.DefaultModel {
			t.Fatalf("seeded %q on model %q, want %q", a.Name, a.Model, cloud.DefaultModel)
		}
	}
}

// mustList is ListForOrg with the error folded into a fatal.
func mustList(t *testing.T, ctx context.Context, org string) []Agent {
	t.Helper()
	list, err := ListForOrg(ctx, org)
	if err != nil {
		t.Fatalf("ListForOrg(%s): %v", org, err)
	}
	return list
}
