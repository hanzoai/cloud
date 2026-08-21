package graph

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/hanzoai/sqlite" // registers "sqlite" under both build tags

	"github.com/hanzoai/cloud/claim"
)

var t0 = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

func newStore(t *testing.T) *store {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "graph.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	s, err := openStore(db)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return s
}

// mk builds an assertion the way the write path does: admitted, then stamped
// with the derived instant and its content address.
func mk(t *testing.T, entity, relation, value string, names bool, src string, wrote time.Time) Fact {
	t.Helper()
	f := Fact{
		Entity: entity, Relation: relation, Value: value, Names: names,
		At: t0, Seen: t0, Source: src, Evidence: "ev-1", By: "tester", Confidence: 0.5,
	}
	f, err := admit(f, wrote)
	if err != nil {
		t.Fatalf("admit(%s %s %s): %v", entity, relation, value, err)
	}
	f.Wrote = wrote
	f.Knowable = claim.Knowable(f.Seen, f.Wrote)
	f.ID = digest(f)
	return f
}

// TestRedeliveryAppendsNothing is the property a retrying caller depends on.
func TestRedeliveryAppendsNothing(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	f := mk(t, "svc:api", "depends", "svc:db", true, "deploy", t0)

	n, err := s.record(ctx, []Fact{f})
	if err != nil || n != 1 {
		t.Fatalf("first write: n=%d err=%v", n, err)
	}
	n, err = s.record(ctx, []Fact{f})
	if err != nil || n != 0 {
		t.Fatalf("redelivery appended %d rows (want 0), err=%v", n, err)
	}
	got, err := s.read(ctx, filter{Entity: "svc:api"})
	if err != nil || len(got) != 1 {
		t.Fatalf("read: %d rows, err=%v", len(got), err)
	}
}

// TestContestedRelationStaysContested is what separates this from last-write-wins.
func TestContestedRelationStaysContested(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	owner := mk(t, "svc:api", "owner", "team:core", false, "registry", t0)
	other := mk(t, "svc:api", "owner", "team:web", false, "survey", t0.Add(time.Hour))
	if _, err := s.record(ctx, []Fact{owner, other}); err != nil {
		t.Fatalf("record: %v", err)
	}

	facts, err := s.read(ctx, filter{Entity: "svc:api", Relation: "owner"})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	win, conflicts, contested, ok := Resolve(facts, t0.Add(2*time.Hour))
	if !ok {
		t.Fatal("nothing resolved from two knowable assertions")
	}
	if win.Value != "team:web" {
		t.Errorf("winner = %q, want the later-knowable claim team:web", win.Value)
	}
	if len(conflicts) != 1 || conflicts[0].Value != "team:core" {
		t.Errorf("the dissenter was discarded: %v", conflicts)
	}
	if !contested {
		t.Error("two sources naming different owners did not report as contested")
	}

	// As of BEFORE the correction, the original is still in force. A correction
	// cannot retroactively change what a past reader knew.
	win, _, _, ok = Resolve(facts, t0.Add(30*time.Minute))
	if !ok || win.Value != "team:core" {
		t.Errorf("as-of before the correction = %q, want team:core", win.Value)
	}
}

// TestWalkIsPointInTime: an edge asserted later is invisible to an earlier walk.
func TestWalkIsPointInTime(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	early := mk(t, "a", "depends", "b", true, "deploy", t0)
	late := mk(t, "b", "depends", "c", true, "deploy", t0.Add(48*time.Hour))
	if _, err := s.record(ctx, []Fact{early, late}); err != nil {
		t.Fatalf("record: %v", err)
	}

	nodes, _, _, err := s.walk(ctx, []string{"a"}, "", "out", 5, t0.Add(time.Hour))
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if has(nodes, "c") {
		t.Errorf("a walk as-of before the second edge reached c: %v", nodes)
	}
	if !has(nodes, "b") {
		t.Errorf("the walk did not reach b: %v", nodes)
	}

	nodes, depth, _, err := s.walk(ctx, []string{"a"}, "", "out", 5, t0.Add(72*time.Hour))
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if !has(nodes, "c") {
		t.Errorf("a walk as-of after the second edge did not reach c: %v", nodes)
	}
	if depth != 2 {
		t.Errorf("depth = %d, want 2", depth)
	}
}

// TestWalkFollowsDirection pins that in, out and both are three different reads.
func TestWalkFollowsDirection(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	if _, err := s.record(ctx, []Fact{mk(t, "a", "depends", "b", true, "deploy", t0)}); err != nil {
		t.Fatalf("record: %v", err)
	}
	asOf := t0.Add(time.Hour)

	if n, _, _, _ := s.walk(ctx, []string{"a"}, "", "out", 3, asOf); !has(n, "b") {
		t.Errorf("out from a did not reach b: %v", n)
	}
	if n, _, _, _ := s.walk(ctx, []string{"a"}, "", "in", 3, asOf); has(n, "b") {
		t.Errorf("in from a reached b, but the edge points a->b: %v", n)
	}
	if n, _, _, _ := s.walk(ctx, []string{"b"}, "", "in", 3, asOf); !has(n, "a") {
		t.Errorf("in from b did not reach a: %v", n)
	}
	if n, _, _, _ := s.walk(ctx, []string{"b"}, "", "both", 3, asOf); !has(n, "a") {
		t.Errorf("both from b did not reach a: %v", n)
	}
	// A property is not an edge, so it is never traversed.
	if _, err := s.record(ctx, []Fact{mk(t, "a", "title", "the api", false, "wiki", t0)}); err != nil {
		t.Fatalf("record: %v", err)
	}
	if n, _, _, _ := s.walk(ctx, []string{"a"}, "", "out", 3, asOf); has(n, "the api") {
		t.Errorf("a property was traversed as an edge: %v", n)
	}
}

// TestAdmitRefusesAtTheDoor: every bound is asked once, at the door.
func TestAdmitRefusesAtTheDoor(t *testing.T) {
	long := make([]byte, entityMax+1)
	for i := range long {
		long[i] = 'x'
	}
	base := func() Fact {
		return Fact{Entity: "a", Relation: "r", Value: "b", Names: true,
			At: t0, Seen: t0, Source: "s", Confidence: 0.5}
	}
	for name, mut := range map[string]func(Fact) Fact{
		"an entity over the ceiling": func(f Fact) Fact { f.Entity = string(long); return f },
		"no source":                  func(f Fact) Fact { f.Source = "  "; return f },
		"no instant":                 func(f Fact) Fact { f.At = time.Time{}; return f },
		"confidence out of range":    func(f Fact) Fact { f.Confidence = 1.5; return f },
		"seen before at":             func(f Fact) Fact { f.Seen = f.At.Add(-time.Hour); return f },
		"a timestamp from the future": func(f Fact) Fact {
			f.At = t0.Add(time.Hour)
			f.Seen = f.At
			return f
		},
	} {
		if _, err := admit(mut(base()), t0); err == nil {
			t.Errorf("%s was admitted", name)
		}
	}
	if _, err := admit(base(), t0); err != nil {
		t.Errorf("a well-formed assertion was refused: %v", err)
	}
}

func has(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}
