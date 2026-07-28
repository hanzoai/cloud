package agents

import (
	"context"
	"testing"
	"time"
)

// TestListControlAfterFiltersCursorsAndScopes proves the CLI drain query: it
// returns ONLY control events, oldest first, honours the ?after cursor so an
// applied command is never redelivered, and is org-scoped so no co-tenant can
// drain another org's steering queue.
func TestListControlAfterFiltersCursorsAndScopes(t *testing.T) {
	s := testSessionStore(t)
	ctx := context.Background()
	if err := s.CreateSession(ctx, mkSession("acme", "root", "", "root")); err != nil {
		t.Fatalf("create: %v", err)
	}
	now := time.Now().Unix()
	// Interleave non-control noise with two control commands.
	_, _ = s.AppendEvent(ctx, Event{ID: genIDMust(t), SessionID: "root", Org: "acme", Kind: KindLog, CreatedAt: now})
	c1, err := s.AppendEvent(ctx, Event{ID: genIDMust(t), SessionID: "root", Org: "acme", Kind: KindControl, Payload: `{"command":"pause"}`, CreatedAt: now})
	if err != nil {
		t.Fatalf("append c1: %v", err)
	}
	_, _ = s.AppendEvent(ctx, Event{ID: genIDMust(t), SessionID: "root", Org: "acme", Kind: KindMessage, CreatedAt: now})
	c2, err := s.AppendEvent(ctx, Event{ID: genIDMust(t), SessionID: "root", Org: "acme", Kind: KindControl, Payload: `{"command":"stop"}`, CreatedAt: now})
	if err != nil {
		t.Fatalf("append c2: %v", err)
	}

	// From the start: exactly the two control events, in seq order, no noise.
	got, err := s.ListControlAfter(ctx, "acme", "root", 0, 200)
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 control events, got %d", len(got))
	}
	if got[0].Seq != c1.Seq || got[1].Seq != c2.Seq {
		t.Fatalf("want ordered [%d,%d], got [%d,%d]", c1.Seq, c2.Seq, got[0].Seq, got[1].Seq)
	}
	for _, e := range got {
		if e.Kind != KindControl {
			t.Fatalf("non-control kind leaked into drain: %s", e.Kind)
		}
	}

	// Cursor past the first command yields only the second (no redelivery).
	got2, err := s.ListControlAfter(ctx, "acme", "root", c1.Seq, 200)
	if err != nil {
		t.Fatalf("drain after cursor: %v", err)
	}
	if len(got2) != 1 || got2[0].Seq != c2.Seq {
		t.Fatalf("cursor drain want [%d], got %+v", c2.Seq, got2)
	}

	// Tenant isolation: another org drains nothing from acme's session.
	evil, err := s.ListControlAfter(ctx, "evil", "root", 0, 200)
	if err != nil {
		t.Fatalf("evil drain: %v", err)
	}
	if len(evil) != 0 {
		t.Fatalf("cross-tenant control leak: %d events", len(evil))
	}
}
