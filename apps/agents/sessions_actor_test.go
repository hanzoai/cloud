package agents

import (
	"context"
	"testing"
	"time"

	"github.com/hanzoai/cloud/internal/sqltest"
)

// A session lists for the actor who opened it and for nobody else in the org;
// one opened before actors were recorded names nobody and lists for no member.
// (Regression: it listed for everyone in the org.)
func TestListSessionsByActor(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
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
		open("s-nobody", "") // opened before sessions were attributed: nobody's
		mine, err := s.ListSessions(ctx, org, SessionFilter{Actor: "acme/bob", Attributed: true})
		if err != nil {
			t.Fatal(err)
		}
		if len(mine) != 1 || mine[0].ID != "s-bob" {
			t.Fatalf("bob sees %v; want s-bob alone", sessionIDs(mine))
		}
		admin, err := s.ListSessions(ctx, org, SessionFilter{Attributed: true})
		if err != nil {
			t.Fatal(err)
		}
		if len(admin) != 2 {
			t.Fatalf("an org admin lists %v; want the two attributed", sessionIDs(admin))
		}
		all, err := s.ListSessions(ctx, org, SessionFilter{})
		if err != nil {
			t.Fatal(err)
		}
		if len(all) != 3 {
			t.Fatalf("a SuperAdmin lists %d; want 3", len(all))
		}

	})
}

func sessionIDs(xs []Session) []string {
	out := make([]string, 0, len(xs))
	for _, x := range xs {
		out = append(out, x.ID)
	}
	return out
}
