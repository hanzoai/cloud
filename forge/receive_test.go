package forge

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
)

// A HISTORY IS MOVED IN BEFORE THE REPOSITORY IS SEALED: the repository is made
// empty and private, the push runs while nothing protects its default branch,
// and only then is the default set, Actions turned off and the branch protected.
func TestReceiveMovesAHistoryInBeforeItSeals(t *testing.T) {
	y := newYard(t, "acme")
	var pushedAt int
	r, err := y.machine(t).Receive(t.Context(), "acme", "probe", "", "trunk", func(context.Context) error {
		y.mu.Lock()
		pushedAt = len(y.writes)
		y.mu.Unlock()
		return nil
	})
	if err != nil {
		t.Fatalf("Receive: %v", err)
	}
	if !r.Private || r.Actions == nil || *r.Actions {
		t.Fatalf("received %+v, want private with Actions off", r)
	}
	y.mu.Lock()
	defer y.mu.Unlock()
	if pushedAt != 1 || y.writes[0] != "POST /orgs/acme/repos" {
		t.Fatalf("the push ran after %d writes %v, want right after the create", pushedAt, y.writes)
	}
	protect := slices.Index(y.writes, "POST /repos/acme/probe/branch_protections")
	if protect < pushedAt {
		t.Fatalf("the branch was protected before the push: %v", y.writes)
	}
}

// A PUSH THAT FAILS TAKES THE REPOSITORY BACK, and a taken name is never adopted.
func TestReceiveLeavesNothingHalfMade(t *testing.T) {
	y := newYard(t, "acme")
	m := y.machine(t)
	_, err := m.Receive(t.Context(), "acme", "probe", "", "main", func(context.Context) error { return errors.New("refused") })
	if err == nil {
		t.Fatal("a failed push was not an error")
	}
	if _, ok := y.repos["acme/probe"]; ok {
		t.Fatal("a failed move left its repository")
	}
	y.repos["acme/taken"] = &plot{branch: "main"}
	called := false
	if _, err := m.Receive(t.Context(), "acme", "taken", "", "main", func(context.Context) error { called = true; return nil }); !errors.Is(err, ErrExists) || called {
		t.Fatalf("Receive over a taken name = %v (pushed %v), want ErrExists and no push", err, called)
	}
}

// A WORKSPACE IS BOUNDED, AN ESTATE NAMESPACE IS NOT: a tenant's workspace at
// capacity refuses a move, and hanzoai, which holds far more than a workspace may,
// takes one.
func TestReceiveBoundsAWorkspaceAndNotTheEstate(t *testing.T) {
	y := newYard(t, "acme", "hanzoai")
	for i := range capacity {
		y.repos[fmt.Sprintf("acme/r%d", i)] = &plot{branch: "main"}
		y.repos[fmt.Sprintf("hanzoai/r%d", i)] = &plot{branch: "main"}
	}
	m := y.machine(t)
	push := func(context.Context) error { return nil }
	if _, err := m.Receive(t.Context(), "acme", "probe", "", "main", push); !errors.Is(err, ErrFull) {
		t.Fatalf("a full workspace took a move: %v", err)
	}
	if _, err := m.Receive(t.Context(), "hanzoai", "probe", "", "main", push); err != nil {
		t.Fatalf("the estate's namespace refused a move: %v", err)
	}
}
