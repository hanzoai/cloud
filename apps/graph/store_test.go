package graph

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"slices"
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

// TestAdmitRefusesAtTheEndpoint: every bound is asked once, at admission.
func TestAdmitRefusesAtTheEndpoint(t *testing.T) {
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

// TestReadKeepsTheNewestAtTheCeiling is the property a resolution depends on.
//
// The table only grows: a correction is a row, so one (entity, relation) pair
// accumulates assertions without bound, and a read of it is capped. Which end
// the cap drops decides the answer rather than merely trimming it — the rows
// that win are the last ones written, so an oldest-first read past the ceiling
// returns a confident and wrong winner.
func TestReadKeepsTheNewestAtTheCeiling(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	// One pair, more assertions than a single read returns. Each is a distinct
	// row because the value differs, which is how a correction is recorded.
	facts := make([]Fact, 0, walkBound+1)
	for i := range walkBound + 1 {
		facts = append(facts, mk(t, "svc:api", "version",
			fmt.Sprintf("v%d", i), false, "deploy", t0.Add(time.Duration(i)*time.Second)))
	}
	if _, err := s.record(ctx, facts); err != nil {
		t.Fatalf("record: %v", err)
	}

	last := facts[len(facts)-1]
	newest, err := s.read(ctx, filter{Entity: "svc:api", Relation: "version", Newest: true})
	if err != nil {
		t.Fatalf("read newest: %v", err)
	}
	if len(newest) != walkBound {
		t.Fatalf("newest read returned %d rows, want the ceiling %d", len(newest), walkBound)
	}
	if !slices.ContainsFunc(newest, func(f Fact) bool { return f.ID == last.ID }) {
		t.Fatal("the newest-first read dropped the most recent assertion, which is the one that wins")
	}

	// The default order is the opposite end, which is what makes the flag
	// load-bearing rather than decorative.
	oldest, err := s.read(ctx, filter{Entity: "svc:api", Relation: "version"})
	if err != nil {
		t.Fatalf("read oldest: %v", err)
	}
	if slices.ContainsFunc(oldest, func(f Fact) bool { return f.ID == last.ID }) {
		t.Fatal("the default read reached the newest assertion; this test no longer proves the ordering matters")
	}
}

// TestWalkRefusesUnboundedWork is why the two ceilings exist. The store holds one
// connection, so a walk a caller sizes is a hold that caller places on the
// organization's write path.
func TestWalkRefusesUnboundedWork(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	seeds := make([]string, seedMax+1)
	for i := range seeds {
		seeds[i] = fmt.Sprintf("svc:%d", i)
	}
	if _, _, _, err := s.walk(ctx, seeds, "", "out", 1, t0); err == nil {
		t.Fatalf("a walk from %d seeds was admitted; the ceiling is %d", len(seeds), seedMax)
	}
	if _, _, _, err := s.walk(ctx, []string{"svc:api"}, "", "out", depthMax+1, t0); err == nil {
		t.Fatalf("a walk of %d hops was admitted; the ceiling is %d", depthMax+1, depthMax)
	}
	// The ceilings themselves are admitted: a bound that refuses its own limit
	// is a bound nobody can use.
	if _, _, _, err := s.walk(ctx, seeds[:seedMax], "", "out", depthMax, t0); err != nil {
		t.Fatalf("a walk at exactly the ceilings was refused: %v", err)
	}
}

// TestEdgeNeedsAValue is the difference between the two kinds of assertion. A
// property may assert the empty string; an edge names another entity, and an
// edge naming nothing is a row a walk would follow to nowhere.
func TestEdgeNeedsAValue(t *testing.T) {
	edge := Fact{
		Entity: "svc:api", Relation: "depends", Value: "  ", Names: true,
		At: t0, Seen: t0, Source: "deploy", By: "tester",
	}
	if _, err := admit(edge, t0); err == nil {
		t.Fatal("an edge with an empty value was admitted; it names no entity")
	}
	prop := Fact{
		Entity: "svc:api", Relation: "note", Value: "", Names: false,
		At: t0, Seen: t0, Source: "deploy", By: "tester",
	}
	if _, err := admit(prop, t0); err != nil {
		t.Fatalf("a property asserting the empty string was refused: %v", err)
	}
}
