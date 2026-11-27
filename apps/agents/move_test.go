package agents

import (
	"context"
	"testing"
	"time"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/dialect"
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/cloud/sdk/principal"
)

// An org's agents leave their file for hanzo-sql with their runs, sessions, events and
// spend, and the next event of a session is numbered after the ones it had.
func TestAgentsMoveFromTheirFile(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	org := principal.MustOrgNamespace("acme", "")
	t.Setenv(locus.Env, "")
	stores := cloud.NewOrgStore[*Store](cloud.Base{DataDir: dir}, "agents", openStore)
	s, err := stores.For(org)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"dev", "ops", "ünï"} {
		if err := s.Create(ctx, mk("acme", name)); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().Unix()
	for i := range 5 {
		if err := s.InsertRun(ctx, Run{
			ID: "run" + string(rune('a'+i)), Org: "acme", AgentName: "dev", Status: "done", Model: "m",
			Input: "in", Output: "out ✓", DurationMs: int64(i * 7), CreatedAt: now + int64(i), MicroUSD: int64(i * 1000),
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.CreateSession(ctx, mkSession("acme", "root", "", "root")); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateSession(ctx, mkSession("acme", "kid", "root", "root")); err != nil {
		t.Fatal(err)
	}
	var last Event
	for i := range 4 {
		if last, err = s.AppendEvent(ctx, Event{ID: "ev" + string(rune('a'+i)), SessionID: "root", Org: "acme", Kind: "log", Actor: "u", Payload: `{"line": "ünï"}`, CreatedAt: now + int64(i)}); err != nil {
			t.Fatal(err)
		}
	}
	if ok, err := s.ClaimFiring(ctx, "dev", now); err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
	}
	if err := s.Consume(ctx, "acme", "dev", "runa", "root", "model", 4200); err != nil {
		t.Fatal(err)
	}
	want := sqltest.Dump(t, s.db)
	wantRuns, err := s.ListRuns(ctx, "acme", "dev", nobodys, 100)
	if err != nil {
		t.Fatal(err)
	}
	if err := stores.CloseAll(); err != nil {
		t.Fatal(err)
	}

	sqltest.Server(t)
	t.Setenv(locus.MoveEnv, "lazy")
	stores = cloud.NewOrgStore[*Store](cloud.Base{DataDir: dir}, "agents", openStore)
	t.Cleanup(func() { _ = stores.CloseAll() })
	m, err := stores.For(org)
	if err != nil {
		t.Fatal(err)
	}
	if !dialect.Of(m.db).Postgres() {
		t.Fatal("the agents store did not move")
	}
	sqltest.Same(t, want, sqltest.Dump(t, m.db))

	gotRuns, err := m.ListRuns(ctx, "acme", "dev", nobodys, 100)
	if err != nil || len(gotRuns) != len(wantRuns) {
		t.Fatalf("%d runs after the move, %v; want %d", len(gotRuns), err, len(wantRuns))
	}
	spend, err := m.SpendByComponent(ctx, "acme", "dev")
	if err != nil || spend["model"] != 4200 {
		t.Fatalf("spend after the move: %v, %v", spend, err)
	}
	if again, err := m.ClaimFiring(ctx, "dev", now); err != nil || again {
		t.Fatalf("a minute claimed before the move was claimed again: %v, %v", again, err)
	}
	next, err := m.AppendEvent(ctx, Event{ID: "evz", SessionID: "root", Org: "acme", Kind: "log", Actor: "u", Payload: `{}`, CreatedAt: now + 9})
	if err != nil || next.Seq != last.Seq+1 {
		t.Fatalf("the first event after the move is %+v, %v; want seq %d", next, err, last.Seq+1)
	}
}
