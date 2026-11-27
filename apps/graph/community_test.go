package graph

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/hanzoai/cloud/internal/sqltest"
)

// clique is every pair among keys, as links.
func clique(keys ...string) []link {
	var out []link
	for i := range keys {
		for j := i + 1; j < len(keys); j++ {
			out = append(out, link{keys[i], keys[j], 1})
		}
	}
	return out
}

// groups is a level of a partition as sets of keys, which is what a reader
// compares — ids are numbering, membership is the answer.
func groups(p partition, level int) [][]string {
	var out [][]string
	for _, ms := range p.members(level) {
		var g []string
		for _, m := range ms {
			g = append(g, p.keys[m])
		}
		out = append(out, g)
	}
	return out
}

// TestTwoClustersAreTwoCommunities is the whole contract in its smallest form:
// two groups with no edge between them are never one community, and each group
// is not cut in two.
func TestTwoClustersAreTwoCommunities(t *testing.T) {
	links := append(clique("a1", "a2", "a3", "a4", "a5"), clique("b1", "b2", "b3", "b4")...)
	p := detect(links)

	got := groups(p, 0)
	want := [][]string{{"a1", "a2", "a3", "a4", "a5"}, {"b1", "b2", "b3", "b4"}}
	if !slices.EqualFunc(got, want, slices.Equal) {
		t.Fatalf("communities = %v, want %v", got, want)
	}
	if p.modularity[0] <= 0.3 {
		t.Errorf("two disconnected cliques scored modularity %.3f; that is real structure", p.modularity[0])
	}
}

// TestABridgeDoesNotMergeTwoGroups: one edge between two dense groups is weaker
// evidence than the groups themselves, so they stay two.
func TestABridgeDoesNotMergeTwoGroups(t *testing.T) {
	links := append(clique("a1", "a2", "a3", "a4", "a5"), clique("b1", "b2", "b3", "b4", "b5")...)
	links = append(links, link{"a5", "b1", 1})
	got := groups(detect(links), 0)
	want := [][]string{{"a1", "a2", "a3", "a4", "a5"}, {"b1", "b2", "b3", "b4", "b5"}}
	if !slices.EqualFunc(got, want, slices.Equal) {
		t.Fatalf("communities = %v, want %v", got, want)
	}
}

// TestTheSameGraphIsTheSamePartition is what lets an answer cite a community by
// number: reading the same edges in any order partitions and numbers them the
// same way, every level of it.
func TestTheSameGraphIsTheSamePartition(t *testing.T) {
	links := planted(2000, 10000, 40, 7)
	want := detect(links)
	r := rand.New(rand.NewPCG(1, 2))
	for range 5 {
		shuffled := slices.Clone(links)
		r.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
		// Reversing an edge is the same edge.
		for i := range shuffled {
			if r.IntN(2) == 0 {
				shuffled[i].a, shuffled[i].b = shuffled[i].b, shuffled[i].a
			}
		}
		got := detect(shuffled)
		if !slices.EqualFunc(got.levels, want.levels, slices.Equal) {
			t.Fatal("the same edges read in another order partitioned differently")
		}
	}
	if len(want.levels) < 2 {
		t.Fatalf("a planted graph of %d communities found %d levels; this test no longer checks nesting", 50, len(want.levels))
	}
}

// TestLevelsNest: every community of a finer level lies inside one community of
// the coarser, which is what makes them levels of one hierarchy rather than
// unrelated partitions.
func TestLevelsNest(t *testing.T) {
	p := detect(planted(2000, 10000, 40, 7))
	for l := 1; l < len(p.levels); l++ {
		parent := map[int32]int32{}
		for i, c := range p.levels[l] {
			up := p.levels[l-1][i]
			if was, ok := parent[c]; ok && was != up {
				t.Fatalf("level %d community %d spans level %d communities %d and %d", l, c, l-1, was, up)
			}
			parent[c] = up
		}
	}
}

// TestACommunityIsConnected: a label shared by two groups with no path between
// them is two communities. Louvain alone can produce that; the split cannot.
func TestACommunityIsConnected(t *testing.T) {
	g := &wgraph{adj: [][]arc{{{1, 1}}, {{0, 1}}, {{3, 1}}, {{2, 1}}}, loop: make([]float64, 4)}
	got := number(split(g, []int32{0, 0, 0, 0}))
	if got[0] != got[1] || got[2] != got[3] || got[0] == got[2] {
		t.Fatalf("two disconnected pairs under one label split into %v", got)
	}
}

// TestNoEdgesIsNoCommunities: an empty graph answers one level of nothing rather
// than failing.
func TestNoEdgesIsNoCommunities(t *testing.T) {
	p := detect(nil)
	if len(p.levels) != 1 || len(p.members(0)) != 0 {
		t.Fatalf("an empty graph found %d levels and %d communities", len(p.levels), len(p.members(0)))
	}
}

// TestAPartitionIsPointInTime: an edge asserted later is not in an earlier
// partition, exactly as a walk at that instant would not follow it.
func TestAPartitionIsPointInTime(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s := newStore(t)
		ctx := context.Background()
		var early, late []Fact
		for _, l := range clique("a1", "a2", "a3") {
			early = append(early, mk(t, l.a, "depends", l.b, true, "deploy", t0))
		}
		for _, l := range clique("b1", "b2", "b3") {
			late = append(late, mk(t, l.a, "depends", l.b, true, "deploy", t0.Add(48*time.Hour)))
		}
		if _, err := s.record(ctx, append(early, late...)); err != nil {
			t.Fatalf("record: %v", err)
		}

		p, err := partitionAt(ctx, s, "", point(t0.Add(time.Hour)))
		if err != nil {
			t.Fatalf("partition: %v", err)
		}
		if got := groups(p, 0); !slices.EqualFunc(got, [][]string{{"a1", "a2", "a3"}}, slices.Equal) {
			t.Errorf("as of before the second group, communities = %v", got)
		}
		p, err = partitionAt(ctx, s, "", point(t0.Add(72*time.Hour)))
		if err != nil {
			t.Fatalf("partition: %v", err)
		}
		if got := groups(p, 0); len(got) != 2 {
			t.Errorf("as of after the second group, communities = %v, want two", got)
		}
	})
}

// TestPropertiesAndRedeliveriesAreNotEdges: a property is never a hop, and one
// edge said twice — by two sources — is one edge, not a heavier one.
func TestPropertiesAndRedeliveriesAreNotEdges(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s := newStore(t)
		ctx := context.Background()
		facts := []Fact{
			mk(t, "a", "depends", "b", true, "deploy", t0),
			mk(t, "a", "depends", "b", true, "registry", t0),
			mk(t, "a", "title", "b", false, "wiki", t0),
		}
		if _, err := s.record(ctx, facts); err != nil {
			t.Fatalf("record: %v", err)
		}
		links, ok, err := s.links(ctx, "", point(t0.Add(time.Hour)), edgeMax)
		if err != nil || !ok {
			t.Fatalf("links: ok=%v err=%v", ok, err)
		}
		if len(links) != 1 || links[0].w != 1 {
			t.Fatalf("links = %+v, want one edge of weight 1", links)
		}
	})
}

// TestTheCeilingRefusesRatherThanTruncates: a graph one edge over the ceiling is
// refused, and one exactly at it is read whole. Communities found in whatever
// part of the graph a truncated read kept are wrong about the part it dropped.
func TestTheCeilingRefusesRatherThanTruncates(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s := newStore(t)
		ctx := context.Background()
		var facts []Fact
		for i := range 4 {
			facts = append(facts, mk(t, fmt.Sprintf("n%d", i), "depends", fmt.Sprintf("n%d", i+1), true, "deploy", t0))
		}
		if _, err := s.record(ctx, facts); err != nil {
			t.Fatalf("record: %v", err)
		}
		asOf := t0.Add(time.Hour)
		if _, ok, err := s.links(ctx, "", point(asOf), 3); err != nil || ok {
			t.Fatalf("four edges under a ceiling of three: ok=%v err=%v, want a refusal", ok, err)
		}
		if links, ok, err := s.links(ctx, "", point(asOf), 4); err != nil || !ok || len(links) != 4 {
			t.Fatalf("four edges under a ceiling of four: %d edges ok=%v err=%v, want all four", len(links), ok, err)
		}
	})
}

// TestCommunitiesOverHTTP reads the partition the way a caller does, and holds
// the endpoint to the bounds it states.
func TestCommunitiesOverHTTP(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountGraph(t)
		for _, l := range append(clique("acme/svc/a1", "acme/svc/a2", "acme/svc/a3"), clique("acme/svc/b1", "acme/svc/b2")...) {
			assertFact(t, app, "", l.a, "depends", l.b, true)
		}

		code, b := call(t, app, http.MethodPost, "/v1/graph/communities", "", map[string]any{})
		if code != http.StatusOK {
			t.Fatalf("communities: status %d: %s", code, b)
		}
		var out graphCommunitiesOut
		if err := json.Unmarshal(b, &out); err != nil {
			t.Fatalf("communities answered something that is not its shape: %v: %s", err, b)
		}
		if len(out.Communities) != 2 || out.Communities[0].Size != 3 || out.Communities[1].Size != 2 {
			t.Fatalf("communities = %+v, want sizes 3 and 2, largest first", out.Communities)
		}
		if !slices.Equal(out.Communities[1].Members, []string{"acme/svc/b1", "acme/svc/b2"}) {
			t.Errorf("community 1 members = %v", out.Communities[1].Members)
		}
		if out.Entities != 5 || out.Edges != 4 || out.Ceiling != edgeMax || out.Truncated {
			t.Errorf("entities=%d edges=%d ceiling=%d truncated=%v", out.Entities, out.Edges, out.Ceiling, out.Truncated)
		}

		code, b = call(t, app, http.MethodPost, "/v1/graph/communities", "", map[string]any{"level": out.Levels})
		if code != http.StatusBadRequest {
			t.Errorf("a level past the last: status %d, want 400: %s", code, b)
		}
	})
}

// planted is a graph of n nodes and e edges in groups of `size`, nine in ten
// edges inside a group: the shape community detection exists to find.
func planted(n, e, size int, seed uint64) []link {
	r := rand.New(rand.NewPCG(seed, seed))
	seen := map[[2]int]bool{}
	var out []link
	for len(out) < e {
		a := r.IntN(n)
		var b int
		if r.IntN(10) < 9 {
			b = a/size*size + r.IntN(size)
		} else {
			b = r.IntN(n)
		}
		if a == b || seen[[2]int{min(a, b), max(a, b)}] {
			continue
		}
		seen[[2]int{min(a, b), max(a, b)}] = true
		out = append(out, link{fmt.Sprintf("n%06d", a), fmt.Sprintf("n%06d", b), 1})
	}
	return out
}

// BenchmarkDetect is the cost of one partition at the ceiling: 100,000 edges
// over 20,000 entities. Run with -bench Detect -benchtime 10x.
func BenchmarkDetect(b *testing.B) {
	links := planted(20000, edgeMax, 50, 1)
	b.ResetTimer()
	var p partition
	for b.Loop() {
		p = detect(links)
	}
	b.ReportMetric(float64(len(p.members(0))), "communities")
	b.ReportMetric(p.modularity[0], "modularity")
}

// BenchmarkPartition is what a caller waits for: the ceiling's worth of edges
// read out of one tenant's file and partitioned.
func BenchmarkPartition(b *testing.B) {
	s := newStore(b)
	ctx := context.Background()
	links := planted(20000, edgeMax, 50, 1)
	facts := make([]Fact, 0, len(links))
	for _, l := range links {
		facts = append(facts, mk(b, l.a, "depends", l.b, true, "deploy", t0))
	}
	if _, err := s.record(ctx, facts); err != nil {
		b.Fatalf("record: %v", err)
	}
	asOf := t0.Add(time.Hour)
	b.ResetTimer()
	for b.Loop() {
		if _, err := partitionAt(ctx, s, "", point(asOf)); err != nil {
			b.Fatalf("partition: %v", err)
		}
	}
}
