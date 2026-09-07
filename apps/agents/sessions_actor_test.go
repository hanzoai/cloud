package agents

import (
	"context"
	"testing"
	"time"
)

// A session lists for the actor who opened it and for nobody else in the org;
// one opened before actors were recorded lists for everyone in it.
func TestListSessionsByActor(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	const org = "acme"
	now := time.Now().UnixMilli()
	open := func(id, actor string) {
		t.Helper()
		if err := s.CreateSession(ctx, Session{ID: id, Org: org, Actor: actor, Agent: "enso", Title: actor + " asks",
			Status: "running", StartedAt: now, CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	open("s-alice", "acme/alice")
	open("s-bob", "acme/bob")
	open("s-nobody", "") // opened before sessions were attributed: the org's
	mine, err := s.ListSessions(ctx, org, SessionFilter{Actor: "acme/bob"})
	if err != nil {
		t.Fatal(err)
	}
	if len(mine) != 2 || mine[0].ID == "s-alice" || mine[1].ID == "s-alice" {
		t.Fatalf("bob sees %v; want s-bob and s-nobody", sessionIDs(mine))
	}
	all, err := s.ListSessions(ctx, org, SessionFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("the org lists %d; want 3", len(all))
	}
}

func sessionIDs(xs []Session) []string {
	out := make([]string, 0, len(xs))
	for _, x := range xs {
		out = append(out, x.ID)
	}
	return out
}
